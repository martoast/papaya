package harness

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// WSClient reads a session socket, keeping terminal bytes and control events
// separate so tests can assert on each.
//
// Every text frame is required to be valid JSON on arrival: interleaved writes
// from more than one server goroutine show up here as a parse failure, which is
// exactly the corruption the single-writer rule exists to prevent.
type WSClient struct {
	t    *testing.T
	conn *websocket.Conn

	mu        sync.Mutex
	output    bytes.Buffer
	events    []map[string]any
	binFrames int
	txtFrames int
	readErr   error
	closed    bool

	done chan struct{}
	once sync.Once
}

func newWSClient(t *testing.T, conn *websocket.Conn) *WSClient {
	w := &WSClient{t: t, conn: conn, done: make(chan struct{})}
	go w.readLoop()
	return w
}

func (w *WSClient) readLoop() {
	for {
		typ, data, err := w.conn.ReadMessage()
		if err != nil {
			w.mu.Lock()
			w.readErr = err
			w.mu.Unlock()
			close(w.done)
			return
		}

		w.mu.Lock()
		switch typ {
		case websocket.BinaryMessage:
			w.binFrames++
			w.output.Write(data)

		case websocket.TextMessage:
			w.txtFrames++
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				// A malformed text frame means frames were interleaved.
				w.t.Errorf("received malformed JSON text frame (%v): %q", err, data)
			} else {
				w.events = append(w.events, ev)
			}
		}
		w.mu.Unlock()
	}
}

// Send writes raw keystroke bytes as a binary frame.
func (w *WSClient) Send(b []byte) {
	w.t.Helper()
	require.NoError(w.t, w.conn.WriteMessage(websocket.BinaryMessage, b))
}

// SendLine writes a command followed by a carriage return.
func (w *WSClient) SendLine(line string) {
	w.t.Helper()
	w.Send([]byte(line + "\r"))
}

// SendJSON writes a control message as a text frame.
func (w *WSClient) SendJSON(v any) {
	w.t.Helper()
	b, err := json.Marshal(v)
	require.NoError(w.t, err)
	require.NoError(w.t, w.conn.WriteMessage(websocket.TextMessage, b))
}

// Resize sends a resize control message.
func (w *WSClient) Resize(cols, rows int) {
	w.SendJSON(map[string]any{"type": "resize", "cols": cols, "rows": rows})
}

// Output returns everything received on binary frames so far.
func (w *WSClient) Output() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

// FrameCounts returns the number of binary and text frames received.
func (w *WSClient) FrameCounts() (binary, text int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.binFrames, w.txtFrames
}

// Events returns a copy of the control events received so far.
func (w *WSClient) Events() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]map[string]any, len(w.events))
	copy(out, w.events)
	return out
}

// WaitForOutput blocks until the terminal output contains substr.
func (w *WSClient) WaitForOutput(substr string, timeout time.Duration) string {
	w.t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if out := w.Output(); strings.Contains(out, substr) {
			return out
		}
		time.Sleep(5 * time.Millisecond) // polling an async stream; no event to wait on
	}

	w.t.Fatalf("timed out after %s waiting for %q in output:\n%s", timeout, substr, tail(w.Output(), 2000))
	return ""
}

// WaitForEvent blocks until a control event of the given type arrives that
// satisfies match, and returns it.
func (w *WSClient) WaitForEvent(typ string, timeout time.Duration, match func(map[string]any) bool) map[string]any {
	w.t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ev := range w.Events() {
			if ev["type"] != typ {
				continue
			}
			if match == nil || match(ev) {
				return ev
			}
		}
		time.Sleep(5 * time.Millisecond) // polling an async stream
	}

	w.t.Fatalf("timed out after %s waiting for a %q event; received: %v", timeout, typ, w.Events())
	return nil
}

// WaitForState blocks until a status event reports the given state.
func (w *WSClient) WaitForState(state string, timeout time.Duration) {
	w.t.Helper()
	w.WaitForEvent("status", timeout, func(ev map[string]any) bool {
		return ev["state"] == state
	})
}

// WaitForClose blocks until the socket is closed by either side.
func (w *WSClient) WaitForClose(timeout time.Duration) bool {
	select {
	case <-w.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// KillTCP severs the underlying connection without a close handshake, which is
// what a laptop lid or a dropped wifi link actually looks like.
func (w *WSClient) KillTCP() {
	w.once.Do(func() {
		if nc, ok := w.conn.UnderlyingConn().(*net.TCPConn); ok {
			nc.SetLinger(0) // force RST rather than a graceful FIN
			nc.Close()
			return
		}
		w.conn.UnderlyingConn().Close()
	})
}

// Close shuts the socket down cleanly.
func (w *WSClient) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	w.conn.Close()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// ExpectPrompt waits for the test shell's prompt, confirming it is ready for
// the next command.
func (w *WSClient) ExpectPrompt(timeout time.Duration) {
	w.t.Helper()
	w.WaitForOutput("$ ", timeout)
}

// RunCommand sends a command and waits for a marker echoed after it, returning
// the output captured since the command was sent.
func (w *WSClient) RunCommand(cmd string, timeout time.Duration) string {
	w.t.Helper()

	before := len(w.Output())
	w.SendLine(cmd)

	const marker = "$ "
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := w.Output()
		if len(out) > before && strings.Contains(out[before:], marker) {
			return out[before:]
		}
		time.Sleep(5 * time.Millisecond) // polling an async stream
	}

	w.t.Fatalf("timed out running %q; output since:\n%s", cmd, tail(w.Output()[before:], 2000))
	return ""
}
