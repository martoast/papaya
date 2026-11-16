package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// UnknownHostError is returned when a host offers a key that nothing vouches
// for. It carries the key so a caller can show its fingerprint and, once a
// person has confirmed it, trust it (TrustHostKey).
type UnknownHostError struct {
	Host string
	Key  ssh.PublicKey
	Path string
}

func (e *UnknownHostError) Error() string {
	where := e.Path
	if where == "" {
		where = "any known_hosts file"
	}
	return fmt.Sprintf("unknown host %s: %s %s is not in %s. "+
		"Confirm it in papaya's setup screen (Settings, then Test), or verify the fingerprint "+
		"out-of-band and add it with: ssh-keyscan %s >> %s",
		e.Host, e.Key.Type(), ssh.FingerprintSHA256(e.Key), where, e.Host, where)
}

// Fingerprint is the SHA256 fingerprint people compare by eye.
func (e *UnknownHostError) Fingerprint() string { return ssh.FingerprintSHA256(e.Key) }

// hostKeyCallback builds the verification callback for outgoing SSH.
//
// Verification is on by default. The insecure escape hatch logs loudly on every
// connection so it cannot be enabled in production and forgotten.
//
// A key is accepted when any of these vouches for it, in order: the operator's
// known_hosts (PAPAYA_KNOWN_HOSTS), papaya's own (keys confirmed in the setup
// screen), the host keys the tailnet advertises for a Tailscale SSH machine,
// or TrustOnFirstUse. A key that contradicts a pinned one is always refused.
func hostKeyCallback(cfg Config) (ssh.HostKeyCallback, error) {
	if cfg.InsecureSkipHostKey {
		var once sync.Once
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			once.Do(func() {
				log.Printf("SECURITY WARNING: host key verification is DISABLED " +
					"(PAPAYA_INSECURE_SKIP_HOST_KEY=true). Connections are open to " +
					"machine-in-the-middle attacks. Do not use this outside development.")
			})
			log.Printf("insecure: accepting unverified host key for %s (%s)",
				hostname, ssh.FingerprintSHA256(key))
			return nil
		}, nil
	}

	var files []string
	for _, p := range []string{cfg.KnownHostsPath, cfg.TrustedHostsPath} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}

	path := cfg.KnownHostsPath
	if path == "" {
		path = cfg.TrustedHostsPath
	}
	if len(files) == 0 && cfg.TailnetHostKeys == nil && cfg.TrustOnFirstUse == nil {
		if path == "" {
			return nil, errors.New("known_hosts path is empty and host key checking is enabled")
		}
		_, err := os.Stat(path)
		return nil, fmt.Errorf("known_hosts %q unreadable: %w "+
			"(populate it, or set PAPAYA_INSECURE_SKIP_HOST_KEY=true for development)", path, err)
	}

	var inner ssh.HostKeyCallback
	if len(files) > 0 {
		var err error
		if inner, err = knownhosts.New(files...); err != nil {
			return nil, fmt.Errorf("parse known_hosts %v: %w", files, err)
		}
	}
	shown := strings.Join(files, ", ")
	if shown == "" {
		shown = path
	}

	// knownhosts' own errors are terse. Rewrite them into something that tells
	// the operator exactly which fingerprint to pin or investigate.
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if inner != nil {
			err := inner(hostname, remote, key)
			if err == nil {
				return nil
			}
			var ke *knownhosts.KeyError
			if !errors.As(err, &ke) {
				return fmt.Errorf("host key verification failed for %s (%s): %w",
					hostname, ssh.FingerprintSHA256(key), err)
			}
			if len(ke.Want) > 0 {
				return fmt.Errorf("host key mismatch for %s: offered %s %s, but %s pins a different key. "+
					"This is either a re-provisioned host or a machine-in-the-middle attack",
					hostname, key.Type(), ssh.FingerprintSHA256(key), shown)
			}
		}

		host := hostOf(hostname)
		if cfg.TailnetHostKeys != nil && tailnetVouches(cfg.TailnetHostKeys(host), key) {
			return nil
		}
		if cfg.TrustOnFirstUse != nil && cfg.TrustOnFirstUse(hostname, key) {
			return nil
		}
		return &UnknownHostError{Host: host, Key: key, Path: shown}
	}, nil
}

// tailnetVouches reports whether key is one the tailnet advertises.
func tailnetVouches(advertised []string, key ssh.PublicKey) bool {
	want := key.Marshal()
	for _, line := range advertised {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue
		}
		if bytes.Equal(pk.Marshal(), want) {
			return true
		}
	}
	return false
}

// hostOf strips the port ssh appends to the address it verifies.
func hostOf(hostname string) string {
	if h, _, err := net.SplitHostPort(hostname); err == nil {
		return h
	}
	return hostname
}

var trustMu sync.Mutex

// TrustHostKey records key as the host key for hostname in papaya's own
// known_hosts file, creating it if needed. Writing the same key twice is a
// no-op.
func TrustHostKey(path, hostname string, key ssh.PublicKey) error {
	if path == "" {
		return errors.New("no trusted-hosts path configured")
	}
	trustMu.Lock()
	defer trustMu.Unlock()

	addr := knownhosts.Normalize(hostname)
	line := knownhosts.Line([]string{addr}, key)

	if existing, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(l) == line {
				return nil
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}
