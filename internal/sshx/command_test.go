package sshx_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/test/harness"
)

// The workspace name comes from a user-typed tab label and ends up inside a
// command run on the remote host, so it is the injection surface here.
func TestTmuxWorkspaceSanitizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude-api", "papaya-claude-api"},
		{"Build 42", "papaya-Build-42"},
		{"", "papaya-default"},
		{"   ", "papaya-default"},
		{"$(id)", "papaya-id"},
		{"`whoami`", "papaya-whoami"},
		{"a;rm -rf /", "papaya-a-rm--rf"},
		{"a|b", "papaya-a-b"},
		{"a&&b", "papaya-a--b"}, // each replaced character yields its own dash
		// tmux treats "." and ":" as address syntax, so a name containing them
		// would attach to something other than what was asked for.
		{"win.0", "papaya-win-0"},
		{"sess:1", "papaya-sess-1"},
		{"../../etc", "papaya-etc"},
		{"日本語", "papaya-default"},
		{strings.Repeat("x", 200), "papaya-" + strings.Repeat("x", 60)},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := sshx.TmuxWorkspace(tc.in, "")
			require.Equal(t, tc.want, got)
			require.NotContains(t, got, " ")
			require.NotContains(t, got, ";")
			require.NotContains(t, got, "$")
			require.NotContains(t, got, "`")
			require.NotContains(t, got, ".")
			require.NotContains(t, got, ":")
		})
	}
}

func TestTmuxWorkspaceFallsBackToHost(t *testing.T) {
	require.Equal(t, "papaya-devbox-1", sshx.TmuxWorkspace("", "devbox-1"))
	require.Equal(t, "papaya-devbox-1", sshx.TmuxWorkspace("$$$", "devbox-1"))
}

// The same label must always produce the same workspace, or a session would not
// be resumable: that stability is the whole point.
func TestTmuxWorkspaceIsStable(t *testing.T) {
	for i := 0; i < 5; i++ {
		require.Equal(t, "papaya-claude-api", sshx.TmuxWorkspace("claude-api", "devbox-1"))
	}
}

func TestShellCommandWithoutTmuxIsEmpty(t *testing.T) {
	require.Empty(t, sshx.ShellCommand(false, "anything"),
		"an empty command means the plain login shell")
}

// The generated command must be inert: a hostile label must not execute
// anything when the remote shell parses the line.
func TestShellCommandIsNotInjectable(t *testing.T) {
	sentinel := "/tmp/papaya-TMUX-PWNED"
	hostile := []string{
		"a$(touch " + sentinel + ")",
		"a`touch " + sentinel + "`",
		"a;touch " + sentinel,
		"a\ntouch " + sentinel,
		"'; touch " + sentinel + "; '",
	}

	for _, label := range hostile {
		cmd := sshx.ShellCommand(true, label)
		require.NotContains(t, cmd, sentinel,
			"the sanitized label must not carry the payload through")

		// Parse the command with a real shell, without running tmux: if quoting
		// or sanitizing were wrong, the substitution would fire here.
		out, err := exec.Command("sh", "-c",
			strings.Replace(cmd, "exec tmux", "echo tmux", 1)).CombinedOutput()
		require.NoError(t, err, string(out))
	}
}

// Enabling tmux must never lock someone out of a host that does not have it.
func TestShellCommandFallsBackWhenTmuxMissing(t *testing.T) {
	cmd := sshx.ShellCommand(true, "workspace")
	require.Contains(t, cmd, "command -v tmux")
	require.Contains(t, cmd, "SHELL")
	require.Contains(t, cmd, "new-session -A",
		"-A attaches to an existing session, which is what makes work resumable")
}

// Copying is the one thing a remote shell cannot do on its own: the clipboard
// lives on the machine the user is sitting at. OSC 52 is how the text gets
// there, and tmux only emits it for its own selections when set-clipboard is on.
func TestShellCommandEnablesClipboardPassthrough(t *testing.T) {
	cmd := sshx.ShellCommand(true, "workspace")
	require.Contains(t, cmd, "set-clipboard on")

	// It must not be able to take the session down with it. An older tmux, or a
	// server that refuses the option, still has to get a shell.
	i := strings.Index(cmd, "set-clipboard on")
	require.Less(t, i, strings.Index(cmd, "new-session"),
		"the option has to be set before the session is created")

	// Just that statement, not the one that follows it.
	stmt := cmd[strings.LastIndex(cmd[:i], ";")+1 : i+strings.Index(cmd[i:], ";")]
	require.Contains(t, stmt, ">/dev/null 2>&1",
		"a failure here must be silent rather than fatal")
	require.NotContains(t, stmt, "exec ",
		"exec would replace the shell before the session is ever created")
}

// Tailscale SSH provides no locale at all — no LANG, LC_* at POSIX — and under
// a POSIX locale tmux mangles every multibyte character, so accented Spanish
// reaches the browser as underscores. The browser end is always UTF-8; the
// command has to say so on both sides of tmux.
func TestShellCommandSpeaksUtf8(t *testing.T) {
	cmd := sshx.ShellCommand(true, "workspace")

	require.Contains(t, cmd, `LANG="${LANG:-`,
		"a locale the connection already carries must be respected, only empty replaced")
	require.Contains(t, cmd, "UTF-8")
	require.Less(t, strings.Index(cmd, "LANG"), strings.Index(cmd, "command -v tmux"),
		"the locale must be exported before tmux starts, or the server inherits POSIX")

	require.Contains(t, cmd, "tmux -u new-session",
		"-u forces the client to render UTF-8 even when the environment claims POSIX")
}

// tmux target matching falls back to a prefix match when nothing matches
// exactly, so a bare name is not safe to kill by: with sessions "papaya-dev" and
// "papaya-dev-work", ending the first once it is already gone would end the
// second. "=" demands an exact match.
func TestKillTmuxCommandDemandsAnExactMatch(t *testing.T) {
	cmd := sshx.KillTmuxCommand("papaya-dev")
	require.Contains(t, cmd, "kill-session")
	require.Contains(t, cmd, "'=papaya-dev'",
		"without = a prefix match can end a different session than the one named")
}

// The name reaches a remote shell inside a command, so it has to arrive as data
// even when hostile. Unlike ShellCommand this only quotes — callers pass a name
// TmuxWorkspace already sanitized — so the payload text is present in the
// command string; what matters is that the shell never executes it.
func TestKillTmuxCommandIsNotInjectable(t *testing.T) {
	sentinel := "/tmp/papaya-KILL-PWNED"
	require.NoError(t, os.RemoveAll(sentinel))

	hostile := []string{
		"a$(touch " + sentinel + ")",
		"a`touch " + sentinel + "`",
		"a;touch " + sentinel,
		"a\ntouch " + sentinel,
		"'; touch " + sentinel + "; '",
	}

	for _, name := range hostile {
		cmd := sshx.KillTmuxCommand(name)

		// Parse the command with a real shell, without running tmux: if the
		// quoting were wrong, the substitution would fire here.
		out, err := exec.Command("sh", "-c",
			strings.Replace(cmd, "tmux kill-session", "echo kill-session", 1)).CombinedOutput()
		require.NoError(t, err, string(out))
		require.NoFileExists(t, sentinel,
			"the name must reach tmux as a single argument, never as a command")
	}
}

// A host can accept a command and then stop answering — a black-holed route, a
// wedged tmux server. ssh.Session.CombinedOutput has no deadline of its own and
// returns only when the peer sends an exit status, so without the context this
// blocks forever and takes the tab-close path down with it.
func TestKillTmuxSessionHonoursItsContext(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)

	hang := make(chan struct{})
	defer close(hang)
	srv.SetExecHang(hang)

	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})
	p := sshx.NewPool(d, sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  srv.KnownHostsFile(t, "devbox-1"),
	})
	t.Cleanup(p.CloseAll)

	client, _, err := p.Acquire(context.Background(), "devbox-1", "tester")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = sshx.KillTmuxSession(ctx, client, "papaya-wedged")
	require.Error(t, err, "a host that never answers must not be waited on forever")
	require.Less(t, time.Since(start), 5*time.Second,
		"the context must bound the command, not merely the connection to it")
}

// Any tmux session with a safe name can be attached, papaya's or not (another
// web terminal's, or the user's own); anything that could escape the command
// is refused.
func TestValidWorkspaceName(t *testing.T) {
	for _, ok := range []string{"papaya-reports", "tsterm-Henry", "main", "0", "work_1"} {
		require.Truef(t, sshx.ValidWorkspaceName(ok), "%q is safe", ok)
	}
	for _, bad := range []string{"", "-t", "a b", "a;b", "$(id)", "a.b", "a:b", "`x`", "a'b", strings.Repeat("x", 129)} {
		require.Falsef(t, sshx.ValidWorkspaceName(bad), "%q must be refused", bad)
	}
}

// A reattaching browser gets tmux's own drawing of the screen: lines with CR
// LF endings and the cursor put back, not the raw output replayed.
func TestParseSnapshot(t *testing.T) {
	// A 4-line screen, the cursor after "> " on its third line.
	out := []byte("first line\n\x1b[31mred\x1b[0m\n> \n\n\x1e2 2 4\n")
	b, ok := sshx.ParseSnapshot(out)
	require.True(t, ok)
	require.Equal(t, "\x1b[0mfirst line\r\n\x1b[31mred\x1b[0m\r\n> \r\n\x1b[0m\x1b[1A\r\x1b[2C", string(b))
	_, ok = sshx.ParseSnapshot([]byte("no separator"))
	require.False(t, ok)
	require.Contains(t, sshx.SnapshotCommand("tsterm-Henry", 500), "capture-pane -e -p -S -500")
}
