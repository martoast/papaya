//go:build live

// Package live drives the real papaya stack against a real host on the operator's
// tailnet.
//
// It substitutes a plain net.Dialer for the tsnet one, which works because the
// machine running the test is already joined to the tailnet. That covers
// everything except the tsnet listener itself: the real Tailscale SSH auth
// chain, real host key verification, a real PTY, a real shell, and real SFTP —
// against the actual target machine rather than a fixture.
//
//	PAPAYA_LIVE_HOST=100.64.0.10 PAPAYA_LIVE_USER=ubuntu \
//	  go test -tags=live -v ./test/live/...
package live

import (
	"context"
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

	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/test/harness"
)

const owner = "alice@example.com"

type target struct {
	Host       string
	User       string
	KnownHosts string
}

func liveTarget(t *testing.T) target {
	t.Helper()

	host := os.Getenv("PAPAYA_LIVE_HOST")
	if host == "" {
		t.Skip("set PAPAYA_LIVE_HOST to run live tests against a real tailnet host")
	}

	user := os.Getenv("PAPAYA_LIVE_USER")
	if user == "" {
		user = "ubuntu"
	}

	kh := os.Getenv("PAPAYA_LIVE_KNOWN_HOSTS")
	if kh == "" {
		kh = repoFile(t, "deploy/known_hosts")
	}
	if _, err := os.Stat(kh); err != nil {
		t.Fatalf("known_hosts %q not readable: %v", kh, err)
	}

	return target{Host: host, User: user, KnownHosts: kh}
}

// liveStack wires the full server to the real host over the already-joined
// tailnet.
func liveStack(t *testing.T, opts harness.StackOptions) (*harness.Stack, target) {
	t.Helper()

	tgt := liveTarget(t)

	// The logical name maps to the real address; the dialer is a plain one
	// because this machine is already on the tailnet.
	dialer := harness.NewCountingDialer(nil)

	pool := sshx.NewPool(dialer, sshx.Config{
		UseTailscaleSSH: true, // the whole point: no key, tailnet identity only
		KnownHostsPath:  tgt.KnownHosts,
		DialTimeout:     20 * time.Second,
	})
	t.Cleanup(pool.CloseAll)

	if opts.UploadDir == "" {
		opts.UploadDir = ".papaya-uploads"
	}
	opts.AllowedUsers = []string{owner}
	opts.DefaultSSHUser = tgt.User

	st := harness.NewStackWithPool(t, pool, dialer, opts)
	return st, tgt
}

// TestLiveAuthChain is the §7.5 confirmation against production Tailscale SSH:
// no key, no password, authorized purely by tailnet identity.
func TestLiveAuthChain(t *testing.T) {
	tgt := liveTarget(t)
	dialer := harness.NewCountingDialer(nil)

	ctx, cancel := contextWithTimeout(t, 30*time.Second)
	defer cancel()

	client, err := sshx.Dial(ctx, dialer, tgt.Host, tgt.User, sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  tgt.KnownHosts,
		DialTimeout:     20 * time.Second,
	})
	require.NoError(t, err, "Tailscale SSH must authorize on tailnet identity alone")
	defer client.Close()

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	out, err := sess.CombinedOutput("whoami && hostname")
	require.NoError(t, err)
	t.Logf("remote says:\n%s", out)
	require.Contains(t, string(out), tgt.User)
}

// TestLiveHostKeyRejectsUnknown proves verification is really on: the same host
// must be refused when its key is not pinned.
func TestLiveHostKeyRejectsUnknown(t *testing.T) {
	tgt := liveTarget(t)
	dialer := harness.NewCountingDialer(nil)

	empty := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))

	ctx, cancel := contextWithTimeout(t, 30*time.Second)
	defer cancel()

	_, err := sshx.Dial(ctx, dialer, tgt.Host, tgt.User, sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  empty,
		DialTimeout:     20 * time.Second,
	})
	require.Error(t, err, "an unpinned host must never be silently trusted")
	require.Contains(t, err.Error(), "unknown host")
	require.Contains(t, err.Error(), "SHA256:")
	t.Logf("correctly refused: %v", err)
}

// TestLiveShellAndResize exercises a real bash PTY on the real host.
func TestLiveShellAndResize(t *testing.T) {
	st, tgt := liveStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(tgt.Host, "live", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 45*time.Second)

	ws.SendLine("echo LIVE-OK-$$")
	ws.WaitForOutput("LIVE-OK-", 20*time.Second)

	// The remote reports its own geometry; a session made at 120x40 must not
	// have requested 80x24.
	ws.SendLine("stty size")
	ws.WaitForOutput("40 120", 20*time.Second)

	for _, dim := range [][2]int{{200, 50}, {90, 30}} {
		cols, rows := dim[0], dim[1]
		ws.Resize(cols, rows)

		want := fmt.Sprintf("%d %d", rows, cols)
		require.Eventuallyf(t, func() bool {
			ws.SendLine("stty size")
			time.Sleep(400 * time.Millisecond) // let the remote reply
			return strings.Contains(ws.Output(), want)
		}, 25*time.Second, 600*time.Millisecond, "remote never reported %dx%d", cols, rows)
	}
}

// TestLivePoolMultiplexing is the connection math against the real host.
func TestLivePoolMultiplexing(t *testing.T) {
	st, tgt := liveStack(t, harness.StackOptions{MaxSessions: 10})
	c := st.Client(owner)

	var wss []*harness.WSClient
	for i := 0; i < 5; i++ {
		sess := c.CreateSession(tgt.Host, fmt.Sprintf("tab-%d", i), 100, 30)
		ws := c.DialWS(sess.ID)
		ws.WaitForState("connected", 45*time.Second)
		wss = append(wss, ws)
	}

	require.Equal(t, int32(1), st.Dialer.Dials.Load(),
		"5 tabs on one host must open exactly one connection")
	require.Equal(t, 1, st.Pool.Len())

	for i, ws := range wss {
		ws.SendLine(fmt.Sprintf("echo TAB-%d-VALUE", i))
	}
	for i, ws := range wss {
		ws.WaitForOutput(fmt.Sprintf("TAB-%d-VALUE", i), 25*time.Second)
	}
	t.Logf("5 concurrent shells over 1 SSH connection, all responding")
}

// TestLiveImageToAgent is the headline workflow against the real machine: an
// image dropped in the browser must land intact and be readable by a program
// running in that session.
func TestLiveImageToAgent(t *testing.T) {
	st, tgt := liveStack(t, harness.StackOptions{})
	c := st.Client(owner)

	local, err := os.ReadFile(repoFile(t, "test/fixtures/screenshot.png"))
	require.NoError(t, err)
	localSum := fmt.Sprintf("%x", sha256.Sum256(local))

	sess := c.CreateSession(tgt.Host, "agent", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 45*time.Second)
	ws.SendLine("echo READY")
	ws.WaitForOutput("READY", 20*time.Second)

	// Exactly the request the browser makes on drop.
	resp, body := c.Upload(sess.ID, "screenshot.png", local)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "upload failed: %s", body)

	var out struct{ Path, Name string }
	require.NoError(t, json.Unmarshal(body, &out))
	require.True(t, path.IsAbs(out.Path))
	t.Logf("uploaded to %s", out.Path)

	// The path lands at the cursor, unquoted, ready to submit.
	ws.WaitForEvent("upload", 20*time.Second, nil)
	ws.WaitForOutput(out.Path, 20*time.Second)
	require.NotContains(t, ws.Output(), "'"+out.Path+"'",
		"the path must not be quoted, or a stdin-reading agent cannot open it")

	// Verify on the real remote filesystem, through the shell itself.
	ws.Send([]byte{0x15}) // Ctrl+U, clear the pending line
	ws.SendLine(fmt.Sprintf("sha256sum %s | cut -d' ' -f1", out.Path))
	sumOut := ws.WaitForOutput(localSum, 30*time.Second)
	require.Containsf(t, sumOut, localSum,
		"the image was corrupted in transit to the real host")

	ws.SendLine(fmt.Sprintf("stat -c '%%a %%s' %s", out.Path))
	permOut := ws.WaitForOutput(fmt.Sprintf("600 %d", len(local)), 25*time.Second)
	require.Contains(t, permOut, "600", "uploads must be owner-only")

	t.Logf("image verified on %s: sha256 matches, mode 0600, %d bytes", tgt.Host, len(local))
}

// TestLiveImageIntoRunningProgram is the actual agent case on the real host: the
// tab is running a program reading stdin, not sitting at a prompt.
func TestLiveImageIntoRunningProgram(t *testing.T) {
	st, tgt := liveStack(t, harness.StackOptions{})
	c := st.Client(owner)

	local, err := os.ReadFile(repoFile(t, "test/fixtures/screenshot.png"))
	require.NoError(t, err)

	sess := c.CreateSession(tgt.Host, "agent-stdin", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 45*time.Second)

	// Wait for the shell to finish starting before sending anything. bash
	// flushes pending terminal input when readline initialises, so a command
	// sent the instant the channel opens is echoed by the tty and then
	// discarded. A real user never hits this because they wait for a prompt.
	ws.SendLine("echo SHELL-READY")
	ws.WaitForOutput("SHELL-READY", 20*time.Second)

	// Markers are split so the terminal's echo of this command line does not
	// itself contain them.
	ws.SendLine(`while read -r line; do echo "AGENT""-GOT:$line"; ` +
		`if [ -r "$line" ]; then echo "AGENT""-READ:$(wc -c < "$line")"; ` +
		`else echo "AGENT""-UNREADABLE"; fi; done`)
	time.Sleep(2 * time.Second)

	resp, body := c.Upload(sess.ID, "screenshot.png", local)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "upload failed: %s", body)

	var out struct{ Path string }
	require.NoError(t, json.Unmarshal(body, &out))

	ws.SendLine("") // the user pressing enter

	got := ws.WaitForOutput("AGENT-GOT:", 25*time.Second)
	require.Containsf(t, got, out.Path,
		"the running program did not receive the injected path")

	readOut := ws.WaitForOutput("AGENT-READ:", 25*time.Second)
	require.Containsf(t, readOut, fmt.Sprintf("AGENT-READ:%d", len(local)),
		"the running program could not read the image it was handed")
	require.NotContains(t, readOut, "AGENT-UNREADABLE")

	t.Logf("a program reading stdin on %s received %s and read all %d bytes",
		tgt.Host, out.Path, len(local))
}

// TestLiveDetachReattach is the M5 gate against the real host.
func TestLiveDetachReattach(t *testing.T) {
	st, tgt := liveStack(t, harness.StackOptions{})
	c := st.Client(owner)

	sess := c.CreateSession(tgt.Host, "survivor", 100, 30)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 45*time.Second)

	ws.SendLine("sleep 300 & echo STARTED-$!")
	ws.WaitForOutput("STARTED-", 20*time.Second)

	for i := 0; i < 50; i++ {
		ws.SendLine(fmt.Sprintf("echo MARKER-%03d", i))
	}
	ws.WaitForOutput("MARKER-049", 30*time.Second)

	ws.KillTCP()
	require.True(t, ws.WaitForClose(15*time.Second))

	require.Eventually(t, func() bool {
		return c.ListSessions()[0].State == "detached"
	}, 20*time.Second, 200*time.Millisecond)

	time.Sleep(5 * time.Second) // stay away

	ws2 := c.DialWS(sess.ID)
	ws2.WaitForState("connected", 45*time.Second)

	replay := ws2.WaitForOutput("MARKER-049", 25*time.Second)
	require.Contains(t, replay, "MARKER-000", "the whole ring must replay")

	ws2.SendLine("jobs")
	jobs := ws2.WaitForOutput("sleep 300", 25*time.Second)
	require.Contains(t, jobs, "sleep 300", "the background process must have survived")

	t.Logf("session survived a hard disconnect on %s with scrollback intact", tgt.Host)
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	dir := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not locate the repository root from %s", wd)
	return ""
}

func contextWithTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), d)
}

// TestLiveVoiceToRealHost drives the full voice path against both the real
// OpenAI API and the real remote host: speech in, transcript at the shell
// cursor on the far side.
//
//	PAPAYA_LIVE_HOST=... PAPAYA_OPENAI_API_KEY=sk-... go test -tags=live ./test/live/...
func TestLiveVoiceToRealHost(t *testing.T) {
	apiKey := os.Getenv("PAPAYA_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("set PAPAYA_OPENAI_API_KEY to run the live transcription test")
	}

	st, tgt := liveStack(t, harness.StackOptions{
		OpenAIKey:     apiKey,
		MaxAudioBytes: 25 << 20,
	})
	c := st.Client(owner)

	audio, err := os.ReadFile(repoFile(t, "test/fixtures/voice.wav"))
	require.NoError(t, err)
	require.Greater(t, len(audio), 1000)

	sess := c.CreateSession(tgt.Host, "voice", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 45*time.Second)
	ws.SendLine("echo READY")
	ws.WaitForOutput("READY", 20*time.Second)

	resp, body := c.Transcribe(sess.ID, "recording.wav", audio)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "transcription failed: %s", body)

	var out struct {
		Text      string   `json:"text"`
		Languages []string `json:"languages"`
		Injected  bool     `json:"injected"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	t.Logf("transcript: %q (languages=%v)", out.Text, out.Languages)

	require.NotEmpty(t, out.Text)
	require.True(t, out.Injected)
	require.NotContains(t, out.Text, "\n", "a newline would submit the command")

	// The words actually spoken must come back.
	lower := strings.ToLower(out.Text)
	require.Contains(t, lower, "git status")

	// And the transcript reaches the real remote shell, echoed by its PTY.
	ws.WaitForOutput(out.Text, 20*time.Second)

	// Nothing ran: no newline was injected.
	require.NotContains(t, ws.Output(), "command not found")
}

// TestLiveTmuxSurvivesRestart is the reason tmux is worth having: work started
// in a session must still be running after papaya itself goes away, not merely
// after the browser disconnects.
func TestLiveTmuxSurvivesRestart(t *testing.T) {
	tgt := liveTarget(t)

	// First "run of papaya": start something long-lived inside the workspace.
	st1, _ := liveStack(t, harness.StackOptions{Tmux: true})
	c1 := st1.Client(owner)

	sess1 := c1.CreateSession(tgt.Host, "persist-check", 120, 40)
	ws1 := c1.DialWS(sess1.ID)
	ws1.WaitForState("connected", 45*time.Second)

	waitForShell(t, ws1)

	marker := fmt.Sprintf("SURVIVOR-%d", time.Now().UnixNano())
	ws1.SendLine("MARK=" + marker)
	ws1.SendLine("sleep 600 & echo STARTED-$!")
	ws1.WaitForOutput("STARTED-", 25*time.Second)

	// Tear the whole server down, not just the socket: pool, hub, sessions.
	st1.Hub.CloseAll("simulated papaya restart")
	st1.Pool.CloseAll()
	time.Sleep(3 * time.Second)

	// Second "run of papaya": a brand new stack, new session id, same label.
	st2, _ := liveStack(t, harness.StackOptions{Tmux: true})
	c2 := st2.Client(owner)

	sess2 := c2.CreateSession(tgt.Host, "persist-check", 120, 40)
	require.NotEqual(t, sess1.ID, sess2.ID, "this is a genuinely new session")

	ws2 := c2.DialWS(sess2.ID)
	ws2.WaitForState("connected", 45*time.Second)

	waitForShell(t, ws2)

	// The shell variable and the background job are still there, which can only
	// be true if the remote tmux session was reattached rather than recreated.
	ws2.SendLine("echo VAR=$MARK")
	out := ws2.WaitForOutput("VAR="+marker, 30*time.Second)
	require.Contains(t, out, marker, "the workspace did not survive the restart")

	ws2.SendLine("jobs")
	jobs := ws2.WaitForOutput("sleep 600", 25*time.Second)
	require.Contains(t, jobs, "sleep 600", "the background process should still be running")

	t.Logf("workspace survived a full papaya restart on %s", tgt.Host)

	// Leave the host clean.
	// "=" for the same reason the product uses it: a bare target falls back to a
	// prefix match, so this would end papaya-persist-check-2 if it existed.
	ws2.SendLine("kill %1 2>/dev/null; tmux kill-session -t '=papaya-persist-check'")
	time.Sleep(2 * time.Second)
}


// waitForShell blocks until the remote shell is actually executing what we send.
//
// tmux switches to the alternate screen and repaints when it attaches, and
// anything typed before that is wiped by the redraw instead of reaching the
// shell. The probe's expected output is computed by the shell, so it cannot be
// satisfied by the terminal echoing the command back — which is what makes this
// a real readiness check rather than a sleep.
func waitForShell(t *testing.T, ws *harness.WSClient) {
	t.Helper()

	require.Eventually(t, func() bool {
		ws.SendLine("echo SHELL$((6*7))OK")
		time.Sleep(700 * time.Millisecond)
		return strings.Contains(ws.Output(), "SHELL42OK")
	}, 45*time.Second, time.Second, "the remote shell never became ready for input")
}
