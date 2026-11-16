//go:build integration

// Package integration exercises papaya against a real OpenSSH server.
//
// Everything here needs a genuine sshd: real PTY semantics, a real shell, a real
// SFTP subsystem, and real connection failure modes. Run with:
//
//	PAPAYA_INTEGRATION=1 go test -race -tags=integration ./test/integration/...
package integration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/test/harness"
)

const owner = "alice@example.com"

// realStack wires the full server to the containerized sshd.
func realStack(t *testing.T, opts harness.StackOptions) (*harness.Stack, *harness.SSHD) {
	t.Helper()

	sshd := harness.StartSSHD(t)

	dialer := harness.NewCountingDialer(map[string]string{sshd.LogicalHost: sshd.Addr})
	pool := sshx.NewPool(dialer, sshx.Config{
		UseTailscaleSSH: true,
		KeyPath:         sshd.KeyPath,
		KnownHostsPath:  sshd.KnownHosts,
	})
	t.Cleanup(pool.CloseAll)

	if opts.UploadDir == "" {
		opts.UploadDir = ".papaya-uploads"
	}

	st := harness.NewStackWithPool(t, pool, dialer, opts)
	return st, sshd
}

func TestRealSSHEchoRoundTrip(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	ws.SendLine("echo HELLO-$$")
	ws.WaitForOutput("HELLO-", 15*time.Second)
}

// TestRealPTYDimensions asserts against what the remote reports about itself,
// which is the only trustworthy way to check geometry without seeing pixels.
func TestRealPTYDimensions(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	// A session created at 120x40 must never report 80x24.
	ws.SendLine("stty size")
	out := ws.WaitForOutput("40 120", 15*time.Second)
	require.Contains(t, out, "40 120")

	for _, dim := range [][2]int{{200, 50}, {40, 10}, {100, 30}} {
		cols, rows := dim[0], dim[1]
		ws.Resize(cols, rows)

		want := fmt.Sprintf("%d %d", rows, cols)
		require.Eventuallyf(t, func() bool {
			ws.SendLine("stty size")
			time.Sleep(300 * time.Millisecond) // let the remote echo its reply
			return strings.Contains(ws.Output(), want)
		}, 20*time.Second, 500*time.Millisecond,
			"remote never reported %dx%d", cols, rows)
	}
}

func TestRealResizeRejectsGarbage(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	ws.SendLine("stty size")
	ws.WaitForOutput("40 120", 15*time.Second)

	for _, bad := range [][2]int{{0, 0}, {1, 1}, {-5, 10}, {100000, 100000}} {
		ws.Resize(bad[0], bad[1])
	}
	time.Sleep(500 * time.Millisecond) // allow any bad resize to land

	ws.SendLine("echo MARK; stty size")
	out := ws.WaitForOutput("MARK", 15*time.Second)

	require.NotContains(t, out, "1 1", "a 1x1 PTY would break every full-screen program")
	require.NotContains(t, out, "0 0")
}

// TestRealInteractiveProgram is a proxy for "renders correctly": the program
// enters the alternate screen, survives a resize, accepts input, and exits
// cleanly.
func TestRealInteractiveProgram(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 100, 30)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	// `less` is present in the base image where vim may not be.
	ws.SendLine("printf 'line1\\nline2\\nline3\\n' > /tmp/pager.txt && less /tmp/pager.txt")
	ws.WaitForOutput("line1", 15*time.Second)

	ws.Resize(200, 50)
	time.Sleep(500 * time.Millisecond) // let the program repaint

	ws.Send([]byte("q"))
	ws.SendLine("echo BACK-AT-PROMPT")
	ws.WaitForOutput("BACK-AT-PROMPT", 15*time.Second)
}

// TestRealHighThroughput catches backpressure bugs where a slow socket drops
// PTY output.
func TestRealHighThroughput(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	const lines = 20000
	start := time.Now()

	// The terminal echoes the command line back, so the completion marker is
	// assembled at runtime: waiting for a literal that appears in the command
	// itself would match the echo and pass before any output arrived.
	ws.SendLine(fmt.Sprintf(
		"for i in $(seq 1 %d); do printf '\\033[3%%dmSEQ:%%08d\\033[0m padding\\n' $((i%%8)) $i; done; printf 'STORM%%sDONE\\n' _",
		lines))

	ws.WaitForOutput("STORM_DONE", 120*time.Second)
	elapsed := time.Since(start)

	out := ws.Output()
	require.Contains(t, out, "SEQ:00000001", "the first line must survive")
	require.Contains(t, out, fmt.Sprintf("SEQ:%08d", lines), "the last line must survive")
	t.Logf("streamed %d lines in %s", lines, elapsed.Round(time.Millisecond))
}

// TestRealUpload covers the SFTP path end to end against a real subsystem.
func TestRealUpload(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	content := []byte(strings.Repeat("PNGDATA", 600))
	resp, body := c.Upload(sess.ID, "shot.png", content)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "upload failed: %s", body)

	var out struct{ Path, Name string }
	require.NoError(t, json.Unmarshal(body, &out))

	// Verify on the remote filesystem, not through our own abstraction.
	remoteSum := sshd.Exec(t, "sha256sum", out.Path)
	want := fmt.Sprintf("%x", sha256.Sum256(content))
	require.Containsf(t, remoteSum, want, "remote checksum mismatch for %s", out.Path)

	perms := sshd.Exec(t, "stat", "-c", "%a", out.Path)
	require.Equal(t, "600", strings.TrimSpace(perms), "uploads must be owner-only")

	// The path is injected at the cursor with no trailing newline.
	ws.WaitForEvent("upload", 10*time.Second, nil)
	time.Sleep(500 * time.Millisecond)
	require.Contains(t, ws.Output(), out.Path,
		"the remote shell must echo the injected path at the prompt")
}

// TestRealImageReachesAgentInSession is the end-to-end proof for the headline
// workflow: an image dropped into the browser must arrive on the remote host
// byte-identical and be readable, at the injected path, by a process running in
// that same shell — an agent session, for instance.
func TestRealImageReachesAgentInSession(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	// The same 64x64 PNG the browser drag-and-drop test drops.
	local, err := os.ReadFile(fixturePath(t, "screenshot.png"))
	require.NoError(t, err)
	require.Greater(t, len(local), 1000)
	require.Equal(t, []byte{0x89, 'P', 'N', 'G'}, local[:4], "fixture must be a real PNG")

	localSum := fmt.Sprintf("%x", sha256.Sum256(local))

	sess := c.CreateSession(sshd.LogicalHost, "agent", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)
	ws.SendLine("echo READY")
	ws.WaitForOutput("READY", 15*time.Second)

	// Exactly the request the browser makes on drop.
	resp, body := c.Upload(sess.ID, "screenshot.png", local)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "upload failed: %s", body)

	var out struct{ Path, Name string }
	require.NoError(t, json.Unmarshal(body, &out))
	require.Equal(t, "screenshot.png", out.Name)
	require.True(t, path.IsAbs(out.Path),
		"the injected path must be absolute so it resolves from any working directory")

	// 1. The bytes survived the transfer intact.
	remoteSum := sshd.Exec(t, "sha256sum", out.Path)
	require.Containsf(t, remoteSum, localSum,
		"remote checksum differs: the image was corrupted in transit\nremote: %s", remoteSum)

	// 2. It is still a valid image on the far side, not merely the right length.
	require.Equal(t, fmt.Sprintf("%d", len(local)),
		strings.TrimSpace(sshd.Exec(t, "stat", "-c", "%s", out.Path)))
	magic := sshd.Exec(t, "head", "-c", "4", out.Path)
	require.Contains(t, magic, "PNG", "the remote file is not a PNG")

	// 3. The path is sitting at the shell cursor, ready for the user to press
	//    enter, which is how it reaches an agent's prompt.
	ws.WaitForEvent("upload", 10*time.Second, nil)
	ws.WaitForOutput(out.Path, 10*time.Second)

	// 4. Nothing ran on its own: no newline was injected.
	require.NotContains(t, ws.Output(), "Permission denied")
	require.NotContains(t, ws.Output(), "command not found")

	// 5. A process in that same shell can read the file at that path. Clear the
	//    pending line first (Ctrl+U), then read it as the logged-in user rather
	//    than via docker exec, which proves the permissions are workable.
	ws.Send([]byte{0x15})
	ws.SendLine(fmt.Sprintf("wc -c < %s && echo SIZE-OK", sshx.ShellQuote(out.Path)))
	readOut := ws.WaitForOutput("SIZE-OK", 15*time.Second)
	require.Containsf(t, readOut, fmt.Sprintf("%d", len(local)),
		"the shell user could not read the uploaded image at the injected path")

	// 6. Owner-only, since uploads may be screenshots of anything.
	require.Equal(t, "600", strings.TrimSpace(sshd.Exec(t, "stat", "-c", "%a", out.Path)))
}

// TestRealImageReachesRunningProgramStdin models the real workflow: the tab is
// not sitting at a shell prompt, it is running an interactive program that reads
// stdin — an agent session. The injected path must arrive in that program's
// input, not be swallowed by the shell.
func TestRealImageReachesRunningProgramStdin(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	local, err := os.ReadFile(fixturePath(t, "screenshot.png"))
	require.NoError(t, err)

	sess := c.CreateSession(sshd.LogicalHost, "agent", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	// Stand in for the agent: a program that reads lines from stdin and reports
	// what it received, then reads the file it was handed.
	//
	// The markers are split with string concatenation so the terminal's echo of
	// this command line does not itself contain them; otherwise the waits below
	// would match the echo and pass before the program produced any output.
	ws.SendLine(`while read -r line; do echo "AGENT""-GOT:$line"; ` +
		`if [ -r "$line" ]; then echo "AGENT""-READ:$(wc -c < "$line")"; ` +
		`else echo "AGENT""-UNREADABLE"; fi; done`)
	time.Sleep(time.Second) // let the loop start reading

	resp, body := c.Upload(sess.ID, "screenshot.png", local)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "upload failed: %s", body)

	var out struct{ Path string }
	require.NoError(t, json.Unmarshal(body, &out))

	// The path is at the cursor with no newline, exactly as a user would see it
	// before choosing to submit. Pressing enter is what hands it to the agent.
	ws.SendLine("")

	got := ws.WaitForOutput("AGENT-GOT:", 20*time.Second)
	require.Containsf(t, got, out.Path,
		"the running program did not receive the injected path in its stdin")

	// And the program could actually open it.
	readOut := ws.WaitForOutput("AGENT-READ:", 20*time.Second)
	require.Containsf(t, readOut, fmt.Sprintf("AGENT-READ:%d", len(local)),
		"the running program could not read the image it was handed")
	require.NotContains(t, readOut, "AGENT-UNREADABLE")
}

// TestRealMultipleImagesInOneSession covers dropping several files at once.
func TestRealMultipleImagesInOneSession(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	local, err := os.ReadFile(fixturePath(t, "screenshot.png"))
	require.NoError(t, err)
	localSum := fmt.Sprintf("%x", sha256.Sum256(local))

	sess := c.CreateSession(sshd.LogicalHost, "multi", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	var paths []string
	for _, name := range []string{"one.png", "two.png", "three.png"} {
		resp, body := c.Upload(sess.ID, name, local)
		require.Equalf(t, http.StatusOK, resp.StatusCode, "upload of %s: %s", name, body)

		var out struct{ Path string }
		require.NoError(t, json.Unmarshal(body, &out))
		paths = append(paths, out.Path)
	}

	// Distinct destinations, all intact. The timestamp prefix is what keeps two
	// files with the same name from colliding.
	seen := map[string]bool{}
	for _, p := range paths {
		require.Falsef(t, seen[p], "two uploads collided on %s", p)
		seen[p] = true
		require.Contains(t, sshd.Exec(t, "sha256sum", p), localSum)
	}

	require.Equal(t, 1, st.Pool.Len(), "all uploads share the one pooled connection")
}

// fixturePath resolves a file under test/fixtures.
func fixturePath(t *testing.T, name string) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	dir := wd
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "test", "fixtures", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		dir = filepath.Dir(dir)
	}

	t.Fatalf("could not locate fixture %q from %s", name, wd)
	return ""
}

// TestRealUploadTraversal is S5 against a real filesystem: the hostile name must
// not touch authorized_keys.
func TestRealUploadTraversal(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	before, err := sshd.TryExec("sha256sum", "/config/.ssh/authorized_keys")
	require.NoError(t, err)

	for _, name := range []string{
		"../../../.ssh/authorized_keys",
		"../.ssh/authorized_keys",
		"/config/.ssh/authorized_keys",
		`..\..\.ssh\authorized_keys`,
	} {
		resp, body := c.Upload(sess.ID, name, []byte("PWNED"))
		require.Equalf(t, http.StatusOK, resp.StatusCode, "upload of %q: %s", name, body)

		var out struct{ Path string }
		require.NoError(t, json.Unmarshal(body, &out))

		// The file must sit directly in the upload directory. Its *name* may
		// still contain the characters ".ssh" once separators are replaced,
		// which is harmless; what matters is that no directory was traversed.
		require.Equalf(t, "/config/.papaya-uploads", path.Dir(out.Path),
			"hostile name %q escaped the upload directory: %s", name, out.Path)
		require.NotContainsf(t, path.Base(out.Path), "/",
			"hostile name %q produced a nested path: %s", name, out.Path)
	}

	after, err := sshd.TryExec("sha256sum", "/config/.ssh/authorized_keys")
	require.NoError(t, err)
	require.Equal(t, before, after, "authorized_keys was modified by a traversal upload")
}

// TestRealCommandInjection is S6 against a real shell.
func TestRealCommandInjection(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	for _, name := range []string{
		"$(touch /tmp/PWNED-a).png",
		"`touch /tmp/PWNED-b`.png",
		"a;touch /tmp/PWNED-c.png",
		"a|touch /tmp/PWNED-d.png",
		"a&&touch /tmp/PWNED-e.png",
	} {
		c.Upload(sess.ID, name, []byte("data"))
	}

	// Press enter so the shell evaluates whatever was injected.
	ws.SendLine("")
	time.Sleep(1 * time.Second)

	out, _ := sshd.TryExec("sh", "-c", "ls /tmp/PWNED-* 2>/dev/null | wc -l")
	require.Equal(t, "0", strings.TrimSpace(out),
		"a quoted upload path executed a command on the remote")
}

// TestRealPoolMultiplexing is the connection math from the spec, against a real
// server: ten tabs, one host, one TCP connection and one handshake.
func TestRealPoolMultiplexing(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{MaxSessions: 10})
	c := st.Client(owner)

	var wss []*harness.WSClient
	for i := 0; i < 10; i++ {
		sess := c.CreateSession(sshd.LogicalHost, fmt.Sprintf("tab-%d", i), 80, 24)
		ws := c.DialWS(sess.ID)
		ws.WaitForState("connected", 30*time.Second)
		wss = append(wss, ws)
	}

	require.Equal(t, int32(1), st.Dialer.Dials.Load(),
		"10 tabs on one host must open exactly one connection")
	require.Equal(t, 1, st.Pool.Len())

	// Every tab must work independently on that shared connection.
	for i, ws := range wss {
		ws.SendLine(fmt.Sprintf("echo TAB-VALUE-%d", i))
	}
	for i, ws := range wss {
		ws.WaitForOutput(fmt.Sprintf("TAB-VALUE-%d", i), 20*time.Second)
	}
}

// TestRealProcessSurvivesDisconnect is the M5 gate against a real shell: a
// background process must outlive the browser.
func TestRealProcessSurvivesDisconnect(t *testing.T) {
	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)

	ws.SendLine("sleep 300 & echo STARTED-$!")
	ws.WaitForOutput("STARTED-", 15*time.Second)

	for i := 0; i < 100; i++ {
		ws.SendLine(fmt.Sprintf("echo MARKER-%03d", i))
	}
	ws.WaitForOutput("MARKER-099", 20*time.Second)

	ws.KillTCP()
	require.True(t, ws.WaitForClose(10*time.Second))

	time.Sleep(5 * time.Second) // stay disconnected

	ws2 := c.DialWS(sess.ID)
	ws2.WaitForState("connected", 30*time.Second)

	// Scrollback replays in full.
	replay := ws2.WaitForOutput("MARKER-099", 15*time.Second)
	require.Contains(t, replay, "MARKER-000", "the whole ring must replay")

	// The background process is still running in the same shell.
	ws2.SendLine("jobs")
	out := ws2.WaitForOutput("sleep 300", 15*time.Second)
	require.Contains(t, out, "sleep 300", "the background process must have survived")
}

// TestRealKeepaliveDetectsBlackHole pauses the container rather than stopping
// it: a clean reset is the easy case, a silently black-holed connection is what
// keepalive exists for.
func TestRealKeepaliveDetectsBlackHole(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: waits for the keepalive to time out")
	}

	st, sshd := realStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(sshd.LogicalHost, "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 30*time.Second)
	ws.SendLine("echo ALIVE")
	ws.WaitForOutput("ALIVE", 15*time.Second)

	sshd.Pause(t)

	// The keepalive ticks every 20s; allow generous slack for detection.
	ws.WaitForEvent("status", 180*time.Second, func(ev map[string]any) bool {
		return ev["state"] == string(hub.StateClosed)
	})

	require.Eventually(t, func() bool { return st.Pool.Len() == 0 },
		60*time.Second, time.Second,
		"a black-holed connection must eventually be evicted from the pool")

	sshd.Unpause(t)
}

// TestRealHostKeyVerification is the M7 gate against a real server key.
func TestRealHostKeyVerification(t *testing.T) {
	sshd := harness.StartSSHD(t)

	t.Run("pinned key is accepted", func(t *testing.T) {
		d := harness.NewCountingDialer(map[string]string{sshd.LogicalHost: sshd.Addr})
		client, err := sshx.Dial(t.Context(), d, sshd.LogicalHost, sshd.User, sshx.Config{
			UseTailscaleSSH: true,
			KeyPath:         sshd.KeyPath,
			KnownHostsPath:  sshd.KnownHosts,
		})
		require.NoError(t, err)
		client.Close()
	})

	t.Run("unknown host is rejected with a fingerprint", func(t *testing.T) {
		empty := t.TempDir() + "/known_hosts"
		require.NoError(t, writeEmpty(empty))

		d := harness.NewCountingDialer(map[string]string{sshd.LogicalHost: sshd.Addr})
		_, err := sshx.Dial(t.Context(), d, sshd.LogicalHost, sshd.User, sshx.Config{
			UseTailscaleSSH: true,
			KeyPath:         sshd.KeyPath,
			KnownHostsPath:  empty,
		})
		require.Error(t, err, "an unknown host must never be silently trusted")
		require.Contains(t, err.Error(), "unknown host")
		require.Contains(t, err.Error(), "SHA256:")
	})
}

func writeEmpty(path string) error {
	return os.WriteFile(path, nil, 0o600)
}
