package sshx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// MaxSayBytes bounds one message to another agent. It is a paste into a chat
// box, not a file transfer.
const MaxSayBytes = 32 << 10

// CleanSayText removes what must never reach another terminal as typed input.
//
// ESC is the dangerous one: a message carrying "\x1b[201~" would end the
// bracketed paste early and the rest would run as keystrokes. Every other
// control character goes too, except newline and tab, which are message text.
func CleanSayText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// SayCommand builds the remote command that pastes stdin into a tmux session
// and, when enter is set, submits it.
//
// The paste is bracketed (-p), so a multi-line message arrives as one paste and
// not as a burst of Enters that would submit a half-written prompt. The buffer
// has a name of its own and is deleted by the paste (-d), so two senders at
// once cannot paste each other's text. The pause before Enter lets a TUI such
// as Claude Code finish taking the paste; without it the Enter can land inside
// the paste and be swallowed. "=name:" is an exact session match — tmux would
// otherwise prefix-match and deliver "dev" to "dev-work".
func SayCommand(workspace string, enter bool) string {
	buf := "papaya-say-" + randHex(6)
	target := ShellQuote("=" + workspace + ":")
	cmd := fmt.Sprintf("tmux load-buffer -b %s - && tmux paste-buffer -p -d -b %s -t %s",
		buf, buf, target)
	if enter {
		cmd += " && sleep 0.8 && tmux send-keys -t " + target + " Enter"
	}
	return cmd
}

// SnapshotCommand prints what tmux shows in the workspace, with colours, for a
// browser that is (re)attaching: up to history lines of scrollback and the
// visible screen (just the screen when a full-screen program is in its
// alternate screen), then a record separator (0x1e) and the cursor as
// "x y height". tmux's own screen is already drawn: replaying the raw output
// instead re-runs every cursor jump a spinner made, at whatever width it was
// drawn for, and comes out garbled on a screen of another size.
func SnapshotCommand(workspace string, history int) string {
	t := ShellQuote("=" + workspace + ":")
	return fmt.Sprintf(`if [ "$(tmux display-message -p -t %[1]s '#{alternate_on}')" = 1 ]; then `+
		`tmux capture-pane -e -p -t %[1]s; else tmux capture-pane -e -p -S -%[2]d -t %[1]s; fi; `+
		`printf '\036'; tmux display-message -p -t %[1]s '#{cursor_x} #{cursor_y} #{pane_height}'`, t, history)
}

// ParseSnapshot turns SnapshotCommand's output into bytes for a fresh
// terminal: the lines with CR LF endings, then the cursor put back where tmux
// has it.
func ParseSnapshot(out []byte) ([]byte, bool) {
	i := bytes.LastIndexByte(out, 0x1e)
	if i < 0 {
		return nil, false
	}
	var cx, cy, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out[i+1:])), "%d %d %d", &cx, &cy, &h); err != nil {
		return nil, false
	}
	// Every line as tmux printed it (the visible screen is the last h of
	// them, blank ones included), so the cursor can be placed relative to the
	// bottom: right whatever height the browser's terminal has.
	text := strings.TrimSuffix(string(out[:i]), "\n")
	lines := strings.Split(text, "\n")
	var b strings.Builder
	b.WriteString("\x1b[0m")
	for n, l := range lines {
		if n > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(l)
	}
	b.WriteString("\x1b[0m")
	if up := h - 1 - cy; up > 0 && up < len(lines) {
		fmt.Fprintf(&b, "\x1b[%dA", up)
	}
	b.WriteString("\r")
	if cx > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", cx)
	}
	return []byte(b.String()), true
}

// CaptureCommand prints the last lines of the tmux session's visible pane.
func CaptureCommand(workspace string, lines int) string {
	return fmt.Sprintf("tmux capture-pane -p -J -S -%s -t %s",
		strconv.Itoa(lines), ShellQuote("="+workspace+":"))
}

// RunTmux runs a tmux command on the remote host, feeding stdin when given.
//
// Bounded by ctx for the same reason as KillTmuxSession: CombinedOutput takes
// no context and blocks forever on a host that stopped answering.
func RunTmux(ctx context.Context, client *ssh.Client, cmd string, stdin []byte) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}
	defer sess.Close()
	if stdin != nil {
		sess.Stdin = bytes.NewReader(stdin)
	}

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.CombinedOutput(cmd)
		done <- result{out, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return r.out, fmt.Errorf("%w: %s", r.err, strings.TrimSpace(string(r.out)))
		}
		return r.out, nil
	case <-ctx.Done():
		sess.Close()
		return nil, ctx.Err()
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DraftCaptureCommand prints the bottom of the pane with its colours, which is
// what tells a draft somebody typed from Claude Code's dimmed suggestion.
func DraftCaptureCommand(workspace string) string {
	return "tmux capture-pane -e -p -t " + ShellQuote("="+workspace+":") + " | tail -n 40"
}

// HasDraft reports whether the last Claude Code prompt line ("❯ ...") in a
// coloured pane capture holds text somebody typed and has not sent.
//
// A paste lands after whatever is already in the box, so pasting over a
// person's half-written message sends theirs and ours as one (it happened on
// the first live test). Claude Code also fills an idle box with a suggestion,
// drawn dim (SGR 2); that is not a draft and must not block a message. A pane
// with no prompt line is not Claude Code, and nothing is assumed about it.
func HasDraft(capture string) bool {
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		at := strings.LastIndex(lines[i], "❯")
		if at < 0 {
			continue
		}
		return typedText(lines[i][at+len("❯"):])
	}
	return false
}

// typedText walks a line, tracking SGR dim, and reports whether any visible
// character is drawn at normal intensity.
func typedText(s string) bool {
	dim := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				params := strings.Split(s[i+2:j], ";")
				for k := 0; k < len(params); k++ {
					switch params[k] {
					case "", "0", "22":
						dim = false
					case "2":
						dim = true
					case "38", "48", "58":
						// colour arguments are not attributes: skip "5;n" or "2;r;g;b"
						if k+1 < len(params) && params[k+1] == "5" {
							k += 2
						} else if k+1 < len(params) && params[k+1] == "2" {
							k += 4
						}
					}
				}
			}
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if unicode.IsSpace(r) || r == ' ' || r == '│' {
			continue
		}
		if !dim {
			return true
		}
	}
	return false
}
