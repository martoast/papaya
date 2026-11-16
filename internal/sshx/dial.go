package sshx

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config is the subset of application configuration the SSH layer needs.
type Config struct {
	UseTailscaleSSH     bool
	KeyPath             string
	KnownHostsPath      string
	InsecureSkipHostKey bool
	UploadDir           string
	DialTimeout         time.Duration

	// InteractiveTimeout bounds how long to wait once the server has actually
	// asked us something. Tailscale SSH in "check" mode answers the handshake
	// with a prompt containing an approval URL and then waits for the operator
	// to visit it, which takes far longer than a normal handshake. Defaults to
	// two minutes.
	InteractiveTimeout time.Duration

	// OnPrompt, if set, receives keyboard-interactive prompts as they arrive.
	// The check-mode approval URL comes through here, and is useless unless it
	// reaches the person who has to click it.
	OnPrompt func(instruction string, questions []string)
}

// Dialer opens raw connections to hosts.
//
// The method set matches *net.Dialer deliberately: production passes the tsnet
// node, tests pass &net.Dialer{}, and everything below this interface is
// exercised identically either way without a tailnet.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Dial establishes an SSH client connection to host over the tailnet.
//
// Auth chain, in order:
//
//  1. "none" — x/crypto/ssh always attempts this before any listed method. This
//     is the path Tailscale SSH takes in "accept" mode: the tailnet identity of
//     the calling node plus the ACL are the credential, so no key is involved.
//  2. keyboard-interactive answering every prompt with an empty string — the
//     path Tailscale SSH "check" mode takes, where sshd emits a browser
//     approval URL as a prompt rather than a real challenge.
//  3. public key from PAPAYA_KEY_PATH, for hosts not running Tailscale SSH.
func Dial(ctx context.Context, d Dialer, host, user string, cfg Config) (*ssh.Client, error) {
	addr := net.JoinHostPort(host, "22")

	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tailnet dial %s: %w", addr, err)
	}

	hkcb, err := hostKeyCallback(cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}

	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	interactive := cfg.InteractiveTimeout
	if interactive <= 0 {
		interactive = 2 * time.Minute
	}

	var auths []ssh.AuthMethod
	if cfg.UseTailscaleSSH {
		auths = append(auths, ssh.KeyboardInteractive(
			func(_, instruction string, questions []string, _ []bool) ([]string, error) {
				// The server is alive and talking to us, so the short handshake
				// deadline no longer applies: extend it to give the operator
				// time to complete an approval.
				conn.SetDeadline(time.Now().Add(interactive))

				if instruction != "" || len(questions) > 0 {
					log.Printf("ssh %s@%s: server prompt: %s %s",
						user, host, strings.TrimSpace(instruction),
						strings.TrimSpace(strings.Join(questions, " ")))
				}
				if cfg.OnPrompt != nil {
					cfg.OnPrompt(instruction, questions)
				}

				return make([]string, len(questions)), nil
			}))
	}
	if cfg.KeyPath != "" {
		signer, err := loadSigner(cfg.KeyPath)
		if err != nil {
			// Not fatal on its own: Tailscale SSH may still authorize us. Log
			// without echoing key material or its contents.
			conn.Close()
			return nil, fmt.Errorf("load key %s: %w", cfg.KeyPath, err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}

	cc := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: hkcb,
		Timeout:         timeout,
	}

	// NewClientConn has no context, so enforce the deadline on the conn and
	// clear it once the handshake completes.
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(timeout))
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cc)
	if err != nil {
		conn.Close()

		// A handshake that stalls rather than failing has a specific and common
		// cause worth naming: a Tailscale SSH policy whose action is "check"
		// grants access per node, only after that node completes a browser
		// approval. A headless service cannot complete one, and the server
		// neither accepts nor rejects — it simply goes quiet until the deadline.
		if isTimeout(err) {
			return nil, fmt.Errorf("ssh handshake %s@%s timed out with the server silent; "+
				"if the host runs Tailscale SSH, its policy is most likely action \"check\", "+
				"which cannot be satisfied by a headless node — give this node a tag and add an "+
				"ssh rule with action \"accept\" for that tag: %w", user, host, err)
		}
		return nil, fmt.Errorf("ssh handshake %s@%s: %w", user, host, err)
	}
	conn.SetDeadline(time.Time{})

	return ssh.NewClient(c, chans, reqs), nil
}

func loadSigner(path string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		// Deliberately does not wrap err verbatim into user-facing paths;
		// callers log this, and passphrase-protected keys are a common case.
		return nil, fmt.Errorf("parse private key (encrypted keys are not supported): %w", err)
	}
	return signer, nil
}

// isTimeout reports whether err is a network timeout, including one wrapped by
// the ssh package.
func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return strings.Contains(err.Error(), "i/o timeout")
}
