package test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

func uploadStack(t *testing.T) (*harness.Stack, string) {
	t.Helper()
	dir := t.TempDir()
	st, _ := newStack(t, harness.StackOptions{UploadDir: filepath.Join(dir, "uploads")})
	return st, filepath.Join(dir, "uploads")
}

func TestUploadSmall(t *testing.T) {
	st, dir := uploadStack(t)
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	content := bytes.Repeat([]byte("PNGDATA"), 600) // ~4 KB
	resp, body := c.Upload(sess.ID, "shot.png", content)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "upload failed: %s", body)

	var out struct{ Path, Name string }
	require.NoError(t, json.Unmarshal(body, &out))
	require.Equal(t, "shot.png", out.Name)
	require.True(t, strings.HasPrefix(out.Path, dir), "file must land in the upload dir")

	got, err := os.ReadFile(out.Path)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(content), sha256.Sum256(got), "content must arrive intact")

	info, err := os.Stat(out.Path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "uploads must be owner-only")
}

// TestUploadInjectsPath checks the defining behaviour: the path is placed at the
// cursor with a trailing space and no newline, so the user reviews before
// running anything.
func TestUploadInjectsPath(t *testing.T) {
	st, _ := uploadStack(t)
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)
	ws.RunCommand("echo ready", 5*time.Second)

	before := ws.Output()
	resp, body := c.Upload(sess.ID, "shot.png", []byte("data"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct{ Path string }
	require.NoError(t, json.Unmarshal(body, &out))

	ev := ws.WaitForEvent("upload", 5*time.Second, nil)
	require.Equal(t, out.Path, ev["path"])
	require.Equal(t, "shot.png", ev["name"])

	// The shell must not have executed anything: no prompt advanced, because no
	// newline was injected.
	time.Sleep(300 * time.Millisecond) // prove absence of a command execution
	after := ws.Output()
	delta := after[len(before):]
	require.NotContains(t, delta, "GOT:", "injecting a path must not run a command")
}

func TestUploadOversized(t *testing.T) {
	dir := t.TempDir()
	st, _ := newStack(t, harness.StackOptions{
		UploadDir:      filepath.Join(dir, "uploads"),
		MaxUploadBytes: 1 << 20, // 1 MB cap
	})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	resp, _ := c.UploadStream(sess.ID, "big.png", 5<<20)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)

	// Nothing must be left behind remotely.
	entries, err := os.ReadDir(filepath.Join(dir, "uploads"))
	if err == nil {
		for _, e := range entries {
			info, _ := e.Info()
			require.Lessf(t, info.Size(), int64(2<<20),
				"a partial oversize file was left on the remote: %s", e.Name())
		}
	}
}

// TestUploadTraversal is S5. The hostile filename must land inside the upload
// directory, and the file it targets must be untouched.
func TestUploadTraversal(t *testing.T) {
	base := t.TempDir()
	uploads := filepath.Join(base, "uploads")

	// A file the attacker would love to overwrite.
	sshDir := filepath.Join(base, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))
	authKeys := filepath.Join(sshDir, "authorized_keys")
	original := []byte("ssh-ed25519 AAAA-legit-key alice@laptop\n")
	require.NoError(t, os.WriteFile(authKeys, original, 0o600))
	beforeHash := sha256.Sum256(original)

	st, _ := newStack(t, harness.StackOptions{UploadDir: uploads})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	hostile := []string{
		"../../../.ssh/authorized_keys",
		"../.ssh/authorized_keys",
		"../../" + filepath.Base(base) + "/.ssh/authorized_keys",
		`..\..\.ssh\authorized_keys`,
		"/etc/passwd",
	}

	for _, name := range hostile {
		resp, body := c.Upload(sess.ID, name, []byte("PWNED"))
		require.Equalf(t, http.StatusOK, resp.StatusCode, "upload of %q: %s", name, body)

		var out struct{ Path string }
		require.NoError(t, json.Unmarshal(body, &out))
		require.Truef(t, strings.HasPrefix(out.Path, uploads),
			"hostile name %q escaped the upload dir: %s", name, out.Path)
	}

	after, err := os.ReadFile(authKeys)
	require.NoError(t, err)
	require.Equal(t, beforeHash, sha256.Sum256(after),
		"authorized_keys was modified by a traversal upload")
}

// TestUploadReusesConnection confirms SFTP opens a channel on the pooled
// connection rather than dialing again.
func TestUploadReusesConnection(t *testing.T) {
	st, _ := uploadStack(t)
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	dialsBefore := st.Dialer.Dials.Load()
	for i := 0; i < 5; i++ {
		resp, _ := c.Upload(sess.ID, fmt.Sprintf("f%d.png", i), []byte("data"))
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	require.Equal(t, dialsBefore, st.Dialer.Dials.Load(),
		"uploads must reuse the pooled SSH connection")
}

// TestUploadDuringHeavyOutput checks the injected path is not spliced into the
// middle of a line of streaming output.
func TestUploadDuringHeavyOutput(t *testing.T) {
	st, _ := uploadStack(t)
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			ws.SendLine("storm 50")
		}
	}()

	var paths []string
	var mu sync.Mutex
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			resp, body := c.Upload(sess.ID, fmt.Sprintf("shot%d.png", id), []byte("data"))
			if resp.StatusCode == http.StatusOK {
				var out struct{ Path string }
				if json.Unmarshal(body, &out) == nil {
					mu.Lock()
					paths = append(paths, out.Path)
					mu.Unlock()
				}
			}
		}(i)
	}
	wg.Wait()

	require.Len(t, paths, 5, "all uploads must succeed under load")

	// Wait for the stream to go quiet before asserting on it.
	var last int
	require.Eventually(t, func() bool {
		n := len(ws.Output())
		settled := n > 0 && n == last
		last = n
		return settled
	}, 30*time.Second, 250*time.Millisecond, "output never settled")

	out := ws.Output()

	// Integrity check: every SEQ marker must be a complete 8-digit token. If an
	// injected upload path were spliced into the middle of a line of streaming
	// output, a marker would start without matching the full form and the two
	// counts would diverge.
	started := strings.Count(out, "SEQ:")
	complete := len(seqRe.FindAllString(out, -1))
	require.Equalf(t, started, complete,
		"%d sequence markers were truncated or spliced by a concurrent upload",
		started-complete)
	require.Greater(t, complete, 0, "expected streaming output to assert on")

	// Storm count is not asserted exactly: an injected path with no trailing
	// newline deliberately prefixes whatever the user types next, so some storm
	// commands legitimately become a different command line.
	require.Contains(t, out, "STORM-DONE:50", "at least one storm must have completed")
}

// seqRe matches a fully formed sequence marker from the storm fixture.
var seqRe = regexp.MustCompile(`SEQ:\d{8}`)

// TestUploadStreamsWithoutBuffering is S7: memory must not scale with upload
// size, which is what MultipartReader buys over ParseMultipartForm.
func TestUploadStreamsWithoutBuffering(t *testing.T) {
	dir := t.TempDir()
	st, _ := newStack(t, harness.StackOptions{
		UploadDir:      filepath.Join(dir, "uploads"),
		MaxUploadBytes: 128 << 20,
	})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	const size = 48 << 20
	resp, body := c.UploadStream(sess.ID, "large.bin", size)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "upload failed: %s", body)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// TotalAlloc grows with churn; what matters is that the live heap does not
	// hold the whole file.
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	require.Lessf(t, delta, int64(size/2),
		"heap grew by %d bytes for a %d byte upload: the body is being buffered, not streamed",
		delta, size)

	var out struct{ Path string }
	require.NoError(t, json.Unmarshal(body, &out))
	info, err := os.Stat(out.Path)
	require.NoError(t, err)
	require.Equal(t, int64(size), info.Size(), "the whole file must arrive")
}

func TestUploadToClosedSessionFails(t *testing.T) {
	st, _ := uploadStack(t)
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	c.DeleteSession(sess.ID)

	resp, _ := c.Upload(sess.ID, "x.png", []byte("data"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestUploadRejectsNonMultipart(t *testing.T) {
	st, _ := uploadStack(t)
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	resp, _ := c.Get("/upload?session=" + sess.ID)
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
}
