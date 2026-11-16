// Package harness provides real SSH infrastructure for tests.
//
// Nothing here mocks *ssh.Client. The server below is a genuine SSH server
// built on x/crypto/ssh: real handshakes, real channels, real pty-req and
// window-change requests, and a real SFTP subsystem from pkg/sftp. A mock
// client would pass while the real thing deadlocks, so the only thing faked is
// the shell on the far end.
package harness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// AuthMode selects which authentication the test server accepts.
type AuthMode int

const (
	// AuthNone accepts the "none" method, modelling Tailscale SSH in accept
	// mode where the tailnet identity is the credential.
	AuthNone AuthMode = iota
	// AuthKeyboardInteractive models Tailscale SSH check mode.
	AuthKeyboardInteractive
	// AuthPublicKey models a plain host with authorized_keys.
	AuthPublicKey
)

// SSHServer is an in-process SSH server.
type SSHServer struct {
	Addr    string
	Signer  ssh.Signer
	Mode    AuthMode
	RootDir string

	// Conns counts completed handshakes. Tests assert that ten tabs across two
	// hosts produce two connections, not ten.
	Conns atomic.Int32
	// Sessions counts opened session channels.
	Sessions atomic.Int32

	ln       net.Listener
	authKeys map[string]bool

	mu      sync.Mutex
	closed  bool
	dims    map[uint32]*Dims
	nextID  uint32
	conns   []net.Conn
	stopped chan struct{}
	wg      sync.WaitGroup

	// execs records every command run as a one-shot "exec" request, as opposed
	// to typed into an interactive shell. Cleanup work like ending a remote tmux
	// session arrives this way, and asserting on it is how a test tells "the
	// server was asked to do this" from "the server was never asked".
	execs      []string
	execHang   chan struct{}
	shellHang  chan struct{}
	shellHeld  chan struct{}
	workspaces []string
}

// SetWorkspaces sets the tmux sessions the fake host reports, so a test can
// stand in for work left running by an earlier papaya.
func (s *SSHServer) SetWorkspaces(names ...string) {
	s.mu.Lock()
	s.workspaces = append([]string(nil), names...)
	s.mu.Unlock()
}

// SetExecHang makes subsequent exec requests accept the command and then block
// until ch is closed, standing in for a host that has stopped answering.
func (s *SSHServer) SetExecHang(ch chan struct{}) {
	s.mu.Lock()
	s.execHang = ch
	s.mu.Unlock()
}

// SetShellHang makes subsequent shell requests (a plain shell, or a command
// asked for alongside a PTY, which is how papaya starts tmux) wait for ch to
// close before they are answered, so a test can be sure no shell has started
// yet when it acts. The returned channel receives once per request held, so
// the test knows the connection is up and waiting.
func (s *SSHServer) SetShellHang(ch chan struct{}) <-chan struct{} {
	held := make(chan struct{}, 16)
	s.mu.Lock()
	s.shellHang, s.shellHeld = ch, held
	s.mu.Unlock()
	return held
}

func (s *SSHServer) waitShellHang() {
	s.mu.Lock()
	hang, held := s.shellHang, s.shellHeld
	s.mu.Unlock()
	if hang != nil {
		select {
		case held <- struct{}{}:
		default:
		}
		<-hang
	}
}

// Dims records the geometry a channel's PTY was given.
type Dims struct {
	mu         sync.Mutex
	Cols, Rows int
}

func (d *Dims) get() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Cols, d.Rows
}

func (d *Dims) set(c, r int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Cols, d.Rows = c, r
}

// NewSSHServer starts a server on localhost and registers cleanup.
func NewSSHServer(t *testing.T, mode AuthMode) *SSHServer {
	t.Helper()

	s, err := StartSSHServer(mode, t.TempDir())
	if err != nil {
		t.Fatalf("start ssh server: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// StartSSHServer is the plain constructor, usable outside a test (the browser
// test dev server needs it).
func StartSSHServer(mode AuthMode, rootDir string) (*SSHServer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("signer: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	s := &SSHServer{
		Addr:     ln.Addr().String(),
		Signer:   signer,
		Mode:     mode,
		RootDir:  rootDir,
		ln:       ln,
		authKeys: map[string]bool{},
		dims:     map[uint32]*Dims{},
		stopped:  make(chan struct{}),
	}

	go s.serve()
	return s, nil
}

// Host returns the hostname portion of the listen address.
func (s *SSHServer) Host() string {
	h, _, _ := net.SplitHostPort(s.Addr)
	return h
}

// Port returns the port the server is listening on.
func (s *SSHServer) Port() string {
	_, p, _ := net.SplitHostPort(s.Addr)
	return p
}

// AuthorizeKey allows a public key when the server is in AuthPublicKey mode.
func (s *SSHServer) AuthorizeKey(pub ssh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authKeys[string(pub.Marshal())] = true
}

// KnownHostsFile writes a known_hosts naming the given hosts and returns its
// path, for exercising real host key verification.
func (s *SSHServer) KnownHostsFile(t *testing.T, hosts ...string) string {
	t.Helper()
	if len(hosts) == 0 {
		hosts = []string{s.Host()}
	}

	var b strings.Builder
	line := string(ssh.MarshalAuthorizedKey(s.Signer.PublicKey()))
	for _, h := range hosts {
		// papaya always dials port 22 in the address it hands to the host key
		// callback, so pin the bare hostname.
		b.WriteString(h + " " + strings.TrimSpace(line) + "\n")
	}

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// AuthorizedHostKey returns the server's host key in known_hosts form, without
// a host prefix.
func (s *SSHServer) AuthorizedHostKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.Signer.PublicKey())))
}

// Close stops the server and drops all connections.
func (s *SSHServer) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stopped)
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()

	s.ln.Close()
	for _, c := range conns {
		c.Close()
	}
	s.wg.Wait()
}

// DropAllConns severs every live connection without a clean close, simulating a
// host dying underneath its sessions.
func (s *SSHServer) DropAllConns() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (s *SSHServer) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{}
	switch s.Mode {
	case AuthNone:
		cfg.NoClientAuth = true
	case AuthKeyboardInteractive:
		cfg.KeyboardInteractiveCallback = func(ssh.ConnMetadata, ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		}
	case AuthPublicKey:
		cfg.PublicKeyCallback = func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.authKeys[string(key.Marshal())] {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized key")
		}
	}
	cfg.AddHostKey(s.Signer)
	return cfg
}

func (s *SSHServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns = append(s.conns, conn)
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

func (s *SSHServer) handleConn(nc net.Conn) {
	defer nc.Close()

	sc, chans, reqs, err := ssh.NewServerConn(nc, s.config())
	if err != nil {
		return
	}
	defer sc.Close()
	s.Conns.Add(1)

	go func() {
		for req := range reqs {
			// keepalive@openssh.com must be answered or the client tears the
			// connection down.
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}()

	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, err := nch.Accept()
		if err != nil {
			continue
		}
		s.Sessions.Add(1)

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleSession(ch, chReqs)
		}()
	}
}

type ptyReq struct {
	Term          string
	Cols, Rows    uint32
	WidthPx, HiPx uint32
	Modes         string
}

type winChange struct {
	Cols, Rows    uint32
	WidthPx, HiPx uint32
}

func (s *SSHServer) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()

	dims := &Dims{Cols: 0, Rows: 0}
	s.mu.Lock()
	s.nextID++
	s.dims[s.nextID] = dims
	s.mu.Unlock()

	shellStarted := make(chan struct{})
	var once sync.Once
	sawPTY := false

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p ptyReq
			if err := ssh.Unmarshal(req.Payload, &p); err == nil {
				dims.set(int(p.Cols), int(p.Rows))
			}
			sawPTY = true
			req.Reply(true, nil)

		case "window-change":
			var w winChange
			if err := ssh.Unmarshal(req.Payload, &w); err == nil {
				dims.set(int(w.Cols), int(w.Rows))
			}
			req.Reply(true, nil)

		case "shell":
			s.waitShellHang()
			req.Reply(true, nil)
			once.Do(func() {
				close(shellStarted)
				go s.runShell(ch, dims)
			})

		case "exec":
			// Payload is a single length-prefixed string: the command line.
			var e struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &e); err != nil {
				req.Reply(false, nil)
				continue
			}
			if sawPTY {
				s.waitShellHang()
			}
			req.Reply(true, nil)

			s.mu.Lock()
			s.execs = append(s.execs, e.Command)
			hang := s.execHang
			s.mu.Unlock()

			// A command asked for alongside a PTY is an interactive session —
			// papaya launches its tmux workspace that way — and must keep the
			// channel open. A command with no PTY is one-shot: it runs, reports
			// an exit status and ends. Real sshd draws the same distinction, and
			// collapsing the two would make every tmux session close instantly.
			if sawPTY {
				once.Do(func() {
					close(shellStarted)
					go s.runShell(ch, dims)
				})
				continue
			}

			go func(cmd string) {
				if hang != nil {
					<-hang // a host that accepts the command and never answers
				}
				s.execute(ch, cmd, dims)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				ch.Close()
			}(e.Command)

		case "subsystem":
			if strings.Contains(string(req.Payload), "sftp") {
				req.Reply(true, nil)
				go s.runSFTP(ch)
			} else {
				req.Reply(false, nil)
			}

		default:
			req.Reply(false, nil)
		}
	}
}

// Execs returns the commands run as one-shot exec requests, in order.
func (s *SSHServer) Execs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execs...)
}

// LastDims returns the most recently created channel's PTY geometry.
func (s *SSHServer) LastDims() (int, int) {
	s.mu.Lock()
	d := s.dims[s.nextID]
	s.mu.Unlock()
	if d == nil {
		return 0, 0
	}
	return d.get()
}

// AllDims returns geometry for every channel, in creation order.
func (s *SSHServer) AllDims() [][2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][2]int, 0, len(s.dims))
	for i := uint32(1); i <= s.nextID; i++ {
		if d, ok := s.dims[i]; ok {
			c, r := d.get()
			out = append(out, [2]int{c, r})
		}
	}
	return out
}

// runShell is a minimal line-oriented shell. It supports just enough to make
// terminal behaviour machine-observable: the remote reports its own dimensions,
// which is the only trustworthy way to assert resize without eyes.
func (s *SSHServer) runShell(ch ssh.Channel, dims *Dims) {
	defer ch.Close()

	fmt.Fprint(ch, "$ ")

	buf := make([]byte, 4096)
	var line strings.Builder

	for {
		n, err := ch.Read(buf)
		if n > 0 {
			for _, b := range buf[:n] {
				switch b {
				case '\r', '\n':
					cmd := strings.TrimSpace(line.String())
					line.Reset()
					fmt.Fprint(ch, "\r\n")
					if !s.execute(ch, cmd, dims) {
						return
					}
					fmt.Fprint(ch, "$ ")

				case 0x7f, '\b': // backspace
					cur := line.String()
					if len(cur) > 0 {
						line.Reset()
						line.WriteString(cur[:len(cur)-1])
						fmt.Fprint(ch, "\b \b")
					}

				default:
					line.WriteByte(b)
					// A real PTY has ECHO on, so typed input and injected paths
					// appear on screen. Without this the terminal looks dead to
					// anything reading the rendered buffer.
					ch.Write([]byte{b})
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// execute returns false when the shell should exit.
func (s *SSHServer) execute(ch ssh.Channel, cmd string, dims *Dims) bool {
	switch {
	case cmd == "":
		return true

	case cmd == "exit":
		return false

	case strings.Contains(cmd, "list-sessions"):
		// Same shape as the real tmux -F output: name, windows, created,
		// attached — comma separated, because tmux replaces control characters
		// in -F output and a tab would arrive as "_".
		s.mu.Lock()
		names := append([]string(nil), s.workspaces...)
		s.mu.Unlock()
		for i, n := range names {
			fmt.Fprintf(ch, "%s,1,%d,0\n", n, 1700000000+i)
		}

	case cmd == "stty size":
		// Same output shape as the real stty: "rows cols".
		c, r := dims.get()
		fmt.Fprintf(ch, "%d %d\r\n", r, c)

	case strings.HasPrefix(cmd, "echo "):
		fmt.Fprintf(ch, "%s\r\n", strings.TrimPrefix(cmd, "echo "))

	case strings.HasPrefix(cmd, "work "):
		// Behaves like a coding agent at work: a spinner line every 100 ms for
		// N seconds, then a result. Drives the tab's working/done state.
		var secs int
		fmt.Sscanf(cmd, "work %d", &secs)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		for i := 0; i < secs*10; i++ {
			fmt.Fprintf(ch, "\r%s thinking… %ds", frames[i%len(frames)], i/10)
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprint(ch, "\r\nWORK-DONE\r\n")

	case strings.HasPrefix(cmd, "storm "):
		// Emits a known number of sequence-numbered lines with ANSI colour, for
		// throughput and integrity assertions.
		var count int
		fmt.Sscanf(cmd, "storm %d", &count)
		for i := 0; i < count; i++ {
			fmt.Fprintf(ch, "\x1b[3%dmSEQ:%08d\x1b[0m padding-padding-padding-padding\r\n", i%8, i)
		}
		fmt.Fprintf(ch, "STORM-DONE:%d\r\n", count)

	default:
		fmt.Fprintf(ch, "GOT:%s\r\n", cmd)
	}
	return true
}

func (s *SSHServer) runSFTP(ch ssh.Channel) {
	defer ch.Close()
	srv, err := sftp.NewServer(ch)
	if err != nil {
		return
	}
	defer srv.Close()
	srv.Serve()
}

// CountingDialer counts dials so tests can assert that the pool opens one
// connection per host rather than one per tab.
//
// Rewrite maps every requested address to a fixed target, which lets tests use
// friendly host names like "devbox-1" while the real server listens on an
// ephemeral localhost port.
type CountingDialer struct {
	Dials   atomic.Int32
	Rewrite map[string]string
	Fail    func(addr string) error

	inner net.Dialer
}

// NewCountingDialer maps each logical host name to a real address.
func NewCountingDialer(rewrite map[string]string) *CountingDialer {
	return &CountingDialer{Rewrite: rewrite}
}

func (d *CountingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.Fail != nil {
		if err := d.Fail(addr); err != nil {
			return nil, err
		}
	}
	target := addr
	if t, ok := d.Rewrite[addr]; ok {
		target = t
	} else if host, _, err := net.SplitHostPort(addr); err == nil {
		if t, ok := d.Rewrite[host]; ok {
			target = t
		}
	}
	d.Dials.Add(1)
	return d.inner.DialContext(ctx, network, target)
}
