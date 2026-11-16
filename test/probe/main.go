// Command probe runs papaya's own SSH dial from a real tsnet node.
//
// It isolates the layer under test: same tsnet, same sshx.Dial, same
// known_hosts as the server, but nothing else. When a connection works from the
// shell but not from papaya, this narrows down which side is at fault.
//
//	TS_AUTHKEY=... go run ./test/probe -host 100.64.0.10 -user ubuntu \
//	  -known-hosts deploy/known_hosts
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"tailscale.com/tsnet"

	"github.com/martoast/papaya/internal/sshx"
)

func main() {
	host := flag.String("host", "", "target host")
	user := flag.String("user", "ubuntu", "remote user")
	kh := flag.String("known-hosts", "deploy/known_hosts", "known_hosts path")
	insecure := flag.Bool("insecure", false, "skip host key verification")
	bulkURL := flag.String("bulk", "", "optional URL to fetch over tsnet, to test large-packet flow")
	timeout := flag.Duration("timeout", 30*time.Second, "dial timeout")
	flag.Parse()

	if *host == "" {
		log.Fatal("-host is required")
	}

	dir, _ := os.MkdirTemp("", "probe-*")
	defer os.RemoveAll(dir)

	srv := &tsnet.Server{
		Dir:       dir,
		Hostname:  "papaya-probe",
		AuthKey:   os.Getenv("TS_AUTHKEY"),
		Ephemeral: true,
		UserLogf:  func(string, ...any) {},
		Logf:      func(string, ...any) {},
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Print("bringing the probe node up... ")
	if _, err := srv.Up(ctx); err != nil {
		log.Fatalf("up: %v", err)
	}
	ip4, _ := srv.TailscaleIPs()
	fmt.Printf("%s\n\n", ip4)

	// 0. Bulk transfer, to separate "the path works at all" from "the path works
	//    for packets larger than a banner". An MTU black hole passes the small
	//    banner and stalls the first full-size packet, which looks exactly like a
	//    hung SSH key exchange.
	if *bulkURL != "" {
		fmt.Println("0. bulk transfer over tsnet")
		client := srv.HTTPClient()
		client.Timeout = 60 * time.Second

		start := time.Now()
		resp, err := client.Get(*bulkURL)
		if err != nil {
			fmt.Printf("   FAILED after %s: %v\n", time.Since(start), err)
		} else {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				fmt.Printf("   read FAILED after %s (%d bytes in): %v\n",
					time.Since(start), len(body), err)
			} else {
				fmt.Printf("   %d bytes in %s, sha256 %x\n",
					len(body), time.Since(start), sha256.Sum256(body))
			}
		}
		fmt.Println()
	}

	// 0b. Manual version exchange. If the server sends its KEXINIT after we send
	//     ours, the transport is fine and any stall is in negotiation. If it goes
	//     quiet, the server is refusing to proceed for this source.
	fmt.Println("0b. manual version exchange")
	{
		start := time.Now()
		c, err := srv.Dial(ctx, "tcp", *host+":22")
		if err != nil {
			log.Fatalf("   dial: %v", err)
		}
		c.SetDeadline(time.Now().Add(25 * time.Second))

		b := make([]byte, 512)
		n, err := c.Read(b)
		if err != nil {
			fmt.Printf("   banner read failed: %v\n", err)
		} else {
			fmt.Printf("   server banner: %q\n", string(b[:n]))
		}

		if _, err := c.Write([]byte("SSH-2.0-Go\r\n")); err != nil {
			fmt.Printf("   writing our version failed: %v\n", err)
		} else {
			fmt.Println("   sent our version string")
		}

		n, err = c.Read(b)
		if err != nil {
			fmt.Printf("   NO KEXINIT after %s: %v\n", time.Since(start), err)
			fmt.Println("   -> the server accepted the connection and then went silent")
		} else {
			// A KEXINIT packet begins with a 4-byte length, 1-byte padding
			// length, then message type 20.
			kind := "unknown"
			if n > 5 {
				switch b[5] {
				case 20:
					kind = "SSH_MSG_KEXINIT"
				case 1:
					kind = "SSH_MSG_DISCONNECT"
				}
			}
			fmt.Printf("   got %d bytes after %s, type=%s\n", n, time.Since(start), kind)
			if n > 5 && b[5] == 1 {
				fmt.Printf("   disconnect payload: %q\n", sanitize(b[:n]))
			}
		}
		c.Close()
		fmt.Println()
	}

	// 0c. Bare x/crypto handshake over a tsnet conn: no host key file, no auth
	//     methods, no deadline. Isolates the SSH library plus tsnet from
	//     anything papaya configures on top.
	fmt.Println("0c. bare ssh.NewClientConn over tsnet (no deadline, no auth)")
	{
		start := time.Now()
		c, err := srv.Dial(ctx, "tcp", *host+":22")
		if err != nil {
			log.Fatalf("   dial: %v", err)
		}

		done := make(chan error, 1)
		go func() {
			cc := &ssh.ClientConfig{
				User:            *user,
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			}
			sc, chans, reqs, err := ssh.NewClientConn(c, *host+":22", cc)
			if err == nil {
				ssh.NewClient(sc, chans, reqs).Close()
			}
			done <- err
		}()

		select {
		case err := <-done:
			if err != nil {
				fmt.Printf("   handshake returned after %s: %v\n", time.Since(start), err)
			} else {
				fmt.Printf("   handshake SUCCEEDED in %s\n", time.Since(start))
			}
		case <-time.After(25 * time.Second):
			fmt.Printf("   STALLED: no result after 25s\n")
			fmt.Println("   -> x/crypto/ssh hangs over a tsnet conn even with no papaya config")
		}
		c.Close()
		fmt.Println()
	}

	// 1. Raw TCP and banner, to prove reachability independently of SSH.
	fmt.Println("1. raw TCP + banner")
	start := time.Now()
	conn, err := srv.Dial(ctx, "tcp", *host+":22")
	if err != nil {
		log.Fatalf("   dial: %v", err)
	}
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Printf("   banner read FAILED after %s: %v\n", time.Since(start), err)
	} else {
		fmt.Printf("   banner in %s: %q\n", time.Since(start), string(buf[:n]))
	}
	conn.Close()

	// 2. papaya's own dial, with the same configuration the server uses.
	fmt.Println("\n2. sshx.Dial (papaya's auth chain and host key checking)")
	start = time.Now()

	client, err := sshx.Dial(ctx, dialer{srv}, *host, *user, sshx.Config{
		UseTailscaleSSH:     true,
		KnownHostsPath:      *kh,
		InsecureSkipHostKey: *insecure,
		DialTimeout:         *timeout,
		OnPrompt: func(instruction string, questions []string) {
			fmt.Printf("   server prompt after %s: instruction=%q questions=%v\n",
				time.Since(start), instruction, questions)
		},
	})
	if err != nil {
		fmt.Printf("   FAILED after %s: %v\n", time.Since(start), err)
		os.Exit(1)
	}
	defer client.Close()
	fmt.Printf("   connected in %s\n", time.Since(start))

	sess, err := client.NewSession()
	if err != nil {
		log.Fatalf("   new session: %v", err)
	}
	defer sess.Close()

	out, err := sess.CombinedOutput("whoami; hostname")
	if err != nil {
		log.Fatalf("   run: %v", err)
	}
	fmt.Printf("   remote: %s", out)
	fmt.Println("\nOK")
}

type dialer struct{ srv *tsnet.Server }

func (d dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.srv.Dial(ctx, network, addr)
}

// sanitize renders a byte slice as printable text for diagnostics.
func sanitize(b []byte) string {
	out := make([]rune, 0, len(b))
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			out = append(out, rune(c))
		} else {
			out = append(out, '.')
		}
	}
	return string(out)
}
