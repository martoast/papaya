package sshx

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// tmuxNameRe is an allowlist. The workspace name reaches a remote shell inside a
// command, so anything outside this set is replaced rather than escaped: tmux
// also treats "." and ":" as address syntax, and a name containing them attaches
// to something other than what was asked for.
var tmuxNameRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// tmuxPrefix marks a tmux session as one of ours, so listing the host's
// sessions can tell papaya's apart from whatever else the user runs there.
const tmuxPrefix = "papaya-"

// TmuxWorkspace converts a user-supplied session label into a tmux session name.
//
// The name is what makes a session resumable: reconnecting with the same label
// reattaches the same remote tmux session, so the work inside it survives papaya
// restarting, the laptop sleeping, or the browser going away for a day.
func TmuxWorkspace(label, fallback string) string {
	clean := tmuxNameRe.ReplaceAllString(strings.TrimSpace(label), "-")
	clean = strings.Trim(clean, "-")

	if clean == "" {
		clean = tmuxNameRe.ReplaceAllString(strings.TrimSpace(fallback), "-")
		clean = strings.Trim(clean, "-")
	}
	if clean == "" {
		clean = "default"
	}
	if len(clean) > 60 {
		clean = strings.Trim(clean[:60], "-")
	}
	return tmuxPrefix + clean
}

// KillTmuxCommand builds the command that ends a tmux session by name.
//
// The name is prefixed with "=" because tmux target matching falls back to a
// prefix match when no session matches exactly: with a bare name, ending a tab
// called "dev" kills a session called "dev-work" when "dev" itself is already
// gone. "=" demands an exact match, so the only session this can end is the one
// named.
func KillTmuxCommand(name string) string {
	return "tmux kill-session -t " + ShellQuote("="+name)
}

// tmuxAbsentMsgs are the remote responses that mean the goal is already met:
// there is no such session, no tmux server at all, or no tmux on the host.
// None of them is a failure to report.
var tmuxAbsentMsgs = []string{
	"can't find session",
	"no server running",
	"no such file or directory",
	"not found",
}

// KillTmuxSession ends the named tmux session on the remote host.
//
// It opens one channel on the existing pooled connection, so it costs no
// handshake. Being unable to find the session is success, not an error — the
// caller only wants it gone.
//
// The context bounds the whole command. ssh.Session.CombinedOutput takes no
// context and returns only when the peer sends an exit status, so on a host that
// has stopped answering — black-holed route, wedged tmux server — it blocks
// forever. Closing the session is what unblocks it: the same reason the pool's
// keepalive closes the connection rather than waiting on a reply that will never
// come.
func KillTmuxSession(ctx context.Context, client *ssh.Client, name string) error {
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer sess.Close()

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.CombinedOutput(KillTmuxCommand(name))
		done <- result{out, err}
	}()

	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		// Wakes the goroutine above, which would otherwise outlive this call.
		sess.Close()
		return fmt.Errorf("tmux kill-session %s: %w", name, ctx.Err())
	}

	if r.err == nil {
		return nil
	}

	msg := strings.ToLower(string(r.out))
	for _, absent := range tmuxAbsentMsgs {
		if strings.Contains(msg, absent) {
			return nil
		}
	}
	return fmt.Errorf("tmux kill-session %s: %w: %s",
		name, r.err, strings.TrimSpace(string(r.out)))
}

// ShellCommand builds the command run on the remote host for a session.
//
// With tmux enabled the shell runs inside a named tmux session: `new-session -A`
// attaches to an existing one or creates it, which is what makes a session
// outlive papaya. If tmux is not installed the command falls back to a normal
// login shell, so enabling the option cannot lock anyone out of a host.
//
// The workspace name is both sanitised and quoted. Sanitising alone would rely
// on the allowlist being perfect; quoting alone would still let tmux's own
// address syntax through.
func ShellCommand(tmux bool, workspace string) string {
	if !tmux {
		return "" // empty means "start the user's login shell"
	}
	return ShellCommandFor(tmux, TmuxWorkspace(workspace, ""))
}

// ShellCommandFor is ShellCommand for a workspace name that has already been
// resolved — one the caller may have adjusted, as the hub does when two tabs
// would otherwise land on the same remote session. The name is used as given
// and is expected to have come from TmuxWorkspace.
func ShellCommandFor(tmux bool, workspace string) string {
	if !tmux || workspace == "" {
		return "" // empty means "start the user's login shell"
	}

	name := ShellQuote(workspace)

	// Tailscale SSH hands the command a bare environment: no LANG, every LC_*
	// at POSIX. A normal sshd would have set the system locale through PAM;
	// this one does not, and under a POSIX locale tmux decides the terminal
	// cannot do UTF-8 and replaces every multibyte character — an accented
	// Spanish word reaches the browser as underscores. The browser end is
	// always UTF-8, so say so on both sides of tmux: LANG for the programs in
	// the panes (readline, ls, anything locale-aware), and -u so the tmux
	// client renders UTF-8 regardless of what the environment claims. A host
	// without en_US.UTF-8 at worst warns and falls back, and -u still holds.
	//
	// A locale the connection already carries is respected: only empty is
	// replaced.
	//
	// set-clipboard on makes tmux emit OSC 52 for its own copy-mode selections,
	// which is what carries copied text back to the browser's clipboard. The
	// default, "external", only forwards sequences from programs running inside
	// tmux — so copying in tmux itself would go to a buffer on the remote host
	// and never reach the machine the user is sitting at.
	//
	// It is a server option, so it applies to that host's whole tmux server. It
	// is allowed to fail: it is a nicety, and losing the shell over it would not
	// be. Hence a separate command rather than tmux's ";" chaining, where an
	// error takes the session down with it.
	return fmt.Sprintf(
		`export LANG="${LANG:-en_US.UTF-8}"; `+
			`if command -v tmux >/dev/null 2>&1; then `+
			`tmux set -s set-clipboard on >/dev/null 2>&1; `+
			`exec tmux -u new-session -A -s %s; `+
			`else exec "${SHELL:-/bin/sh}" -l; fi`, name)
}

// Workspace is a tmux session on the remote host: one papaya created, or any
// other the user already had running there (another terminal app's, their own
// "main"), which papaya can pick up too.
type Workspace struct {
	// Name is the full tmux session name, e.g. "papaya-reports".
	Name string `json:"name"`
	// Label is what a tab reattaching to it should be called: Name without
	// papaya's own "papaya-" prefix, or the whole name for anyone else's.
	Label string `json:"label"`
	// Windows is how many windows the session has, a rough sign of life.
	Windows int `json:"windows"`
	// Created is the unix time tmux started the session.
	Created int64 `json:"created"`
	// Attached reports whether a client is currently looking at it. After
	// papaya restarts this is false for everything it left behind.
	Attached bool `json:"attached"`
}

// listWorkspacesCmd asks tmux for its sessions in a parseable form.
//
// The separator is a comma, not a tab, because tmux sanitizes control
// characters out of -F output: a tab comes back as "_", silently collapsing
// every field into one. A comma survives, and cannot appear in a name this
// package produces — TmuxWorkspace allows only [A-Za-z0-9_-].
//
// A host with no tmux server prints "no server running..." to stderr and exits
// non-zero, which is not an error worth reporting: it means no workspaces.
const listWorkspacesCmd = `tmux list-sessions ` +
	`-F '#{session_name},#{session_windows},#{session_created},#{?session_attached,1,0}' ` +
	`2>/dev/null || true`

// ListWorkspaces returns the tmux sessions on a host that papaya can attach
// to: its own, and any other whose name is safe to put in a command (letters,
// digits, - and _). So work started elsewhere, in plain tmux or another web
// terminal, can be picked up from papaya without restarting it.
//
// This is how work survives papaya itself: after a restart the remote sessions
// are still running, but nothing in papaya remembers them, so without asking the
// host they are invisible and reachable only by guessing the original tab name.
func ListWorkspaces(ctx context.Context, client *ssh.Client) ([]Workspace, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}
	defer sess.Close()

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.Output(listWorkspacesCmd)
		done <- result{out, err}
	}()

	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		// Unbounded reads are how a wedged host takes the caller with it; the
		// close wakes the goroutine above.
		sess.Close()
		return nil, fmt.Errorf("list workspaces: %w", ctx.Err())
	}
	if r.err != nil {
		return nil, fmt.Errorf("list workspaces: %w", r.err)
	}

	var out []Workspace
	for _, line := range strings.Split(string(r.out), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), ",")
		if len(f) != 4 || !ValidWorkspaceName(f[0]) {
			continue
		}
		w := Workspace{
			Name:     f[0],
			Label:    strings.TrimPrefix(f[0], tmuxPrefix),
			Attached: f[3] == "1",
		}
		w.Windows, _ = strconv.Atoi(f[1])
		w.Created, _ = strconv.ParseInt(f[2], 10, 64)
		out = append(out, w)
	}
	return out, nil
}

// ValidWorkspaceName reports whether a tmux session name is safe to attach to.
// A workspace name reaches a remote shell inside a command, and one arriving
// from a client is not to be trusted just because it looks plausible: only
// letters, digits, - and _ are allowed (no spaces, quotes, ".", ":" or "$"),
// and it may not start with "-", where tmux would read it as a flag.
func ValidWorkspaceName(name string) bool {
	return name != "" && len(name) <= 128 && workspaceRe.MatchString(name)
}

var workspaceRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)
