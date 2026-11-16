package sshx

import (
	"errors"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Terminal dimension bounds. The frontend can compute nonsense dimensions when
// a terminal is measured while hidden (see the hidden-terminal resize trap);
// clamping here means even a buggy or hostile client cannot destroy the remote
// PTY's geometry.
const (
	minDim      = 2
	maxDim      = 1000
	defaultCols = 80
	defaultRows = 24
)

// PTY is an interactive shell channel on a shared SSH connection.
type PTY struct {
	sess  *ssh.Session
	stdin io.WriteCloser

	// mu guards stdin, which is written from both the WebSocket read pump and
	// the upload handler.
	mu     sync.Mutex
	closed bool
}

// OpenPTY requests a PTY and starts a shell, streaming all output to sink.
//
// An empty command starts the user's login shell. A non-empty one is run
// instead, which is how a session is placed inside tmux so it survives papaya
// going away.
//
// sink must be safe for concurrent use: the ssh package copies stdout and
// stderr with independent goroutines.
func OpenPTY(client *ssh.Client, cols, rows int, sink io.Writer, command string) (*PTY, error) {
	cols, rows = ClampDims(cols, rows)

	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}

	sess.Stdout = sink
	// Without this, stderr is silently discarded on non-PTY channels. With a
	// PTY the remote merges stderr into the same stream, but set it anyway.
	sess.Stderr = sink

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		sess.Close()
		return nil, err
	}

	// Start(cmd) runs the command through the account's login shell, so the
	// fallback in ShellCommand works. Shell() is the plain-login path.
	start := sess.Shell
	if command != "" {
		start = func() error { return sess.Start(command) }
	}
	if err := start(); err != nil {
		sess.Close()
		return nil, err
	}

	return &PTY{sess: sess, stdin: stdin}, nil
}

// Write sends bytes to the remote shell's stdin.
func (p *PTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, errors.New("session closed")
	}
	return p.stdin.Write(b)
}

// Resize updates the remote terminal geometry.
func (p *PTY) Resize(cols, rows int) error {
	cols, rows = ClampDims(cols, rows)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("session closed")
	}
	return p.sess.WindowChange(rows, cols)
}

// Wait blocks until the remote shell exits and all output has been copied.
func (p *PTY) Wait() error { return p.sess.Wait() }

// Close tears down the channel. It is safe to call more than once.
func (p *PTY) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	p.stdin.Close()
	return p.sess.Close()
}

// ClampDims bounds terminal dimensions to a sane range, substituting defaults
// for values that are zero, negative, or absurd.
func ClampDims(cols, rows int) (int, int) {
	if cols < minDim || cols > maxDim {
		if cols > maxDim {
			cols = maxDim
		} else {
			cols = defaultCols
		}
	}
	if rows < minDim || rows > maxDim {
		if rows > maxDim {
			rows = maxDim
		} else {
			rows = defaultRows
		}
	}
	return cols, rows
}
