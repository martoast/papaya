// Command smoke drives a running papaya server end to end over the tailnet.
//
// Unlike the test suites, this talks to the real deployed binary through its
// real listener: REST, WebSocket, and upload, exactly as the browser does. It is
// the last mile that no in-process test can cover.
//
//	go run ./test/smoke -base http://100.64.0.20 -host build-server
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type session struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Host  string `json:"host"`
	User  string `json:"user"`
	State string `json:"state"`
}

func main() {
	base := flag.String("base", "", "papaya base URL, e.g. http://100.64.0.20")
	host := flag.String("host", "", "remote host to connect to")
	image := flag.String("image", "test/fixtures/screenshot.png", "image to upload")
	flag.Parse()

	if *base == "" || *host == "" {
		log.Fatal("both -base and -host are required")
	}
	if err := run(*base, *host, *image); err != nil {
		log.Fatalf("FAILED: %v", err)
	}
	fmt.Println("\nALL CHECKS PASSED")
}

func run(base, host, imagePath string) error {
	step := 0
	ok := func(format string, a ...any) {
		step++
		fmt.Printf("  %2d. OK   %s\n", step, fmt.Sprintf(format, a...))
	}

	// --- health, unauthenticated ---
	if body, err := get(base + "/healthz"); err != nil || strings.TrimSpace(string(body)) != "ok" {
		return fmt.Errorf("healthz: %v (%s)", err, body)
	}
	ok("/healthz responds over the tailnet")

	// --- create a session ---
	sess, err := createSession(base, host)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	defer deleteSession(base, sess.ID)
	ok("session created: %s@%s state=%s", sess.User, sess.Host, sess.State)

	// --- attach the WebSocket ---
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/ws?session=" + sess.ID
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		return fmt.Errorf("websocket dial (status %d): %w", code, err)
	}
	defer conn.Close()

	term := newReader(conn)
	if err := term.waitFor("connected", 45*time.Second, func(s string) bool {
		return strings.Contains(s, "\"state\":\"connected\"")
	}); err != nil {
		return fmt.Errorf("session never connected: %w\nevents: %s", err, term.events())
	}
	ok("websocket attached, session connected")

	// --- the shell is real ---
	send(conn, "echo PAPAYA-SMOKE-$$\r")
	if err := term.waitForOutput("PAPAYA-SMOKE-", 25*time.Second); err != nil {
		return fmt.Errorf("shell did not respond: %w", err)
	}
	ok("remote shell responded to a command")

	// --- geometry reaches the remote PTY ---
	//
	// Resize is a control message the far end never acknowledges, so ask
	// repeatedly rather than sleeping once and hoping it landed.
	sendJSON(conn, map[string]any{"type": "resize", "cols": 133, "rows": 42})
	var got string
	err = term.poll(func() { send(conn, "stty size\r") }, 25*time.Second, func(s string) bool {
		got = lastGeometry(s)
		return got != ""
	})
	if err != nil {
		return fmt.Errorf("resize did not reach the remote PTY: %w", err)
	}
	ok("resize propagated: remote reports %s", got)

	// --- upload an image, exactly as a browser drop does ---
	local, err := os.ReadFile(imagePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", imagePath, err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(local))

	up, err := upload(base, sess.ID, "screenshot.png", local)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	ok("image uploaded (%d bytes) to %s", len(local), up.Path)

	// --- the path lands at the cursor, unquoted ---
	if err := term.waitForOutput(up.Path, 25*time.Second); err != nil {
		return fmt.Errorf("injected path never appeared at the prompt: %w", err)
	}
	if strings.Contains(term.output(), "'"+up.Path+"'") {
		return fmt.Errorf("path was injected quoted; a stdin-reading agent could not open it")
	}
	ok("path injected at the cursor, unquoted, with no newline")

	// --- and the bytes are intact on the far side ---
	send(conn, "\x15") // Ctrl+U, clear the pending line
	send(conn, fmt.Sprintf("sha256sum %s | cut -c1-64\r", up.Path))
	if err := term.waitForOutput(sum, 30*time.Second); err != nil {
		return fmt.Errorf("remote checksum mismatch: %w", err)
	}
	ok("remote sha256 matches the local file")

	send(conn, fmt.Sprintf("stat -c '%%a %%s' %s\r", up.Path))
	if err := term.waitForOutput(fmt.Sprintf("600 %d", len(local)), 25*time.Second); err != nil {
		return fmt.Errorf("unexpected mode or size: %w", err)
	}
	ok("remote file is mode 0600, %d bytes", len(local))

	// --- tidy up after ourselves ---
	send(conn, "\x15")
	send(conn, fmt.Sprintf("rm -f %s && echo CLEANED\r", up.Path))
	if err := term.waitForOutput("CLEANED", 20*time.Second); err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	ok("uploaded file removed from the remote")

	return nil
}

// --- helpers ---

type reader struct {
	mu   sync.Mutex
	out  strings.Builder
	evs  strings.Builder
	done chan struct{}
}

func newReader(c *websocket.Conn) *reader {
	r := &reader{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for {
			typ, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			r.mu.Lock()
			if typ == websocket.BinaryMessage {
				r.out.Write(data)
			} else {
				r.evs.Write(data)
				r.evs.WriteString("\n")
			}
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *reader) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

func (r *reader) events() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evs.String()
}

func (r *reader) waitForOutput(substr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(r.output(), substr) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %q", substr)
}

func (r *reader) waitFor(what string, d time.Duration, match func(string) bool) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if match(r.events()) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

// poll re-issues a command until the accumulated output satisfies match. Use it
// for anything the far end does not acknowledge, where a single ask can race.
func (r *reader) poll(ask func(), d time.Duration, match func(string) bool) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ask()
		for i := 0; i < 25 && time.Now().Before(deadline); i++ {
			if match(r.output()) {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return fmt.Errorf("timed out; output so far: %s", tail(r.output(), 200))
}

// geometryRe matches the "<rows> <cols>" that stty size prints. Anchoring on
// the requested width is the real check: a wrong width is what corrupts
// wrapping. The row count is allowed to come up one short, because tmux spends
// a row on its status line — the pane genuinely is one row shorter than the
// terminal, and asserting otherwise fails a correct deployment.
var geometryRe = regexp.MustCompile(`(?m)^(4[12]) 133\s*$`)

func lastGeometry(out string) string {
	m := geometryRe.FindAllString(out, -1)
	if len(m) == 0 {
		return ""
	}
	return strings.TrimSpace(m[len(m)-1])
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func send(c *websocket.Conn, s string) {
	c.WriteMessage(websocket.BinaryMessage, []byte(s))
}

func sendJSON(c *websocket.Conn, v any) {
	b, _ := json.Marshal(v)
	c.WriteMessage(websocket.TextMessage, b)
}

func get(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func createSession(base, host string) (*session, error) {
	body, _ := json.Marshal(map[string]any{
		"host": host, "name": "smoke", "cols": 120, "rows": 40,
	})
	resp, err := http.Post(base+"/api/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, data)
	}

	var s session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func deleteSession(base, id string) {
	req, _ := http.NewRequest(http.MethodDelete, base+"/api/sessions/"+id, nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

type uploadResult struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func upload(base, sessionID, name string, content []byte) (*uploadResult, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}
	fw.Write(content)
	mw.Close()

	resp, err := http.Post(base+"/upload?session="+sessionID, mw.FormDataContentType(), &buf)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, data)
	}

	var r uploadResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
