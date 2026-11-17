package hub

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 1 << 20 // generous for paste; images go through /upload
	sendQueueDepth = 256

	// compressMinBytes is the smallest frame worth deflating.
	compressMinBytes = 512
)

// pongEvent answers a client's {"type":"ping"}. The browser cannot see
// WebSocket-level pongs, so this is how a page that has just come back from
// the background finds out whether the socket it still holds is alive.
var pongEvent = []byte(`{"type":"pong"}`)

type wsMessage struct {
	binary bool
	data   []byte
}

// wsConn wraps a WebSocket with the single-writer discipline gorilla requires.
//
// Every frame goes through send and is written by exactly one goroutine.
// Calling WriteMessage from two places is the classic source of interleaved,
// corrupt frames, and it is why nothing else in this package touches conn.
type wsConn struct {
	conn      *websocket.Conn
	send      chan wsMessage
	done      chan struct{}
	closeOnce sync.Once
}

func newWSConn(c *websocket.Conn) *wsConn {
	return &wsConn{
		conn: c,
		send: make(chan wsMessage, sendQueueDepth),
		done: make(chan struct{}),
	}
}

// enqueue queues a frame. If the client is too slow to drain the queue we drop
// the socket rather than the bytes: a reconnect replays the full ring buffer,
// whereas dropped frames would corrupt the terminal silently.
func (c *wsConn) enqueue(m wsMessage) {
	select {
	case <-c.done:
	case c.send <- m:
	default:
		log.Printf("ws: send queue full, closing slow client")
		c.close()
	}
}

func (c *wsConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

// writePump is the sole writer for this socket, pings included.
func (c *wsConn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	for {
		select {
		case <-c.done:
			return

		case m := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			typ := websocket.TextMessage
			if m.binary {
				typ = websocket.BinaryMessage
			}
			// Compress only what is worth it. A reattach replays the whole ring
			// (256 KB of escape sequences, ~10x smaller deflated) and that replay,
			// times every tab, is most of what a phone waits for when it reopens
			// the app. A keystroke's echo is a few bytes and deflate would only
			// add latency and framing to it. A no-op when the browser did not
			// negotiate permessage-deflate.
			c.conn.EnableWriteCompression(len(m.data) >= compressMinBytes)
			if err := c.conn.WriteMessage(typ, m.data); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// control is a client-to-server JSON message.
type control struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	// presence: is this device's page visible, and is the tab in use there.
	Visible bool `json:"visible"`
	Active  bool `json:"active"`
}

// readPump feeds client input to the shell until the socket dies.
func (c *wsConn) readPump(s *Session) {
	defer c.close()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		// Pongs are what distinguish a quiet client from a dead one; without
		// this the socket leaks until the process exits.
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		s.mu.Lock()
		s.lastSeen = time.Now()
		s.mu.Unlock()
		return nil
	})

	for {
		typ, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}

		switch typ {
		case websocket.BinaryMessage:
			// Hot path: raw keystrokes, straight to stdin, no parsing.
			if err := s.WriteInput(data); err != nil {
				return
			}
			// Typing is using: this device's size wins until another is used.
			s.markUsed(c)

		case websocket.TextMessage:
			var msg control
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "resize":
				s.resizeFrom(c, msg.Cols, msg.Rows)
			case "presence":
				s.presence(c, msg.Visible, msg.Active)
			case "ping":
				// The read deadline was already extended by the read itself; the
				// reply lets the client tell a live socket from one that a phone's
				// suspend left looking open but dead.
				c.enqueue(wsMessage{data: pongEvent})
			}
		}
	}
}

// Upgrader builds the WebSocket upgrader with an origin check pinned to the
// serving host, so a page on another origin cannot open a socket using the
// browser's tailnet access.
func Upgrader(allowedHosts []string) *websocket.Upgrader {
	allowed := make(map[string]bool, len(allowedHosts))
	for _, h := range allowedHosts {
		if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
			allowed[h] = true
		}
	}

	return &websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// permessage-deflate, negotiated only if the browser offers it (every
		// current one does). What is compressed is decided per frame in
		// writePump.
		EnableCompression: true,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				// Non-browser client. It already passed tailnet identity checks.
				return true
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			oh := strings.ToLower(u.Hostname())
			if oh == strings.ToLower(hostOnly(r.Host)) {
				return true
			}
			return allowed[oh]
		},
	}
}

func hostOnly(h string) string {
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		return h[:i]
	}
	return h
}

// ServeWS upgrades the request and runs the session's pumps until the socket
// closes. The caller has already verified ownership.
func ServeWS(up *websocket.Upgrader, w http.ResponseWriter, r *http.Request, s *Session) {
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response
	}

	c := newWSConn(conn)
	go c.writePump()

	s.attach(c)
	c.readPump(s)
	s.detach(c)
}
