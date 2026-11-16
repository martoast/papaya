package hub

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// TestSanitizeName covers tab labels, which are attacker-influenced: they come
// from a PATCH body and from OSC title sequences emitted by the remote shell.
func TestSanitizeName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "billing-api", "billing-api"},
		{"whitespace collapsed", "  spaced   out  ", "spaced out"},
		{"newline becomes space", "line\nbreak", "line break"},
		{"tab becomes space", "a\tb", "a b"},
		{"control chars stripped", "\x00\x07\x1b", ""},
		{"del stripped", "a\x7fb", "ab"},
		{"empty", "", ""},
		{"only whitespace", "   ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sanitizeName(tc.in))
		})
	}
}

// TestSanitizeNameStripsControlChars is the ANSI-injection guard (S8). The
// escape byte itself must never survive into a tab label.
func TestSanitizeNameStripsControlChars(t *testing.T) {
	hostile := []string{
		"\x1b[31mred\x1b[0m",
		"\x1b[2J\x1b[31m",
		"\x1b]0;pwned\x07",
		"a\x08\x08\x08b",
		"\x1b[999999D",
	}

	for _, h := range hostile {
		got := sanitizeName(h)
		for _, r := range got {
			require.Falsef(t, r < 0x20 || r == 0x7f,
				"control character U+%04X survived sanitizing of %q -> %q", r, h, got)
		}
		require.True(t, utf8.ValidString(got))
	}
}

// TestSanitizeNameTruncatesByRunes catches the classic bug: cutting at 40 bytes
// instead of 40 runes splits a multi-byte character and produces mojibake.
func TestSanitizeNameTruncatesByRunes(t *testing.T) {
	cases := []string{
		strings.Repeat("x", 100),
		strings.Repeat("日", 100),
		strings.Repeat("🎉", 100),
		strings.Repeat("日本語プロジェクト", 20),
		strings.Repeat("é", 100),
	}

	for _, in := range cases {
		got := sanitizeName(in)
		require.LessOrEqual(t, utf8.RuneCountInString(got), 40,
			"must cap at 40 runes")
		require.Truef(t, utf8.ValidString(got),
			"truncation split a multi-byte rune: %q", got)
	}

	require.Equal(t, 40, utf8.RuneCountInString(sanitizeName(strings.Repeat("日", 100))))
}

func FuzzSanitizeName(f *testing.F) {
	seeds := []string{
		"billing-api", "\x1b[31m", strings.Repeat("日", 100), "", "  ",
		"\x00\x01\x02", "🎉🎉🎉", "a\nb\tc",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out := sanitizeName(in)

		if !utf8.ValidString(out) {
			t.Fatalf("produced invalid UTF-8 from %q: %q", in, out)
		}
		if n := utf8.RuneCountInString(out); n > 40 {
			t.Fatalf("exceeded the 40 rune cap (%d): %q", n, out)
		}
		for _, r := range out {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("control character U+%04X survived from %q", r, in)
			}
		}
		if strings.HasPrefix(out, " ") || strings.HasSuffix(out, " ") {
			t.Fatalf("whitespace was not trimmed: %q", out)
		}
	})
}

// TestSanitizeErrorLeaksNothing is S10: error payloads reach the browser, so
// they must never carry key paths, internal addresses, or secrets.
func TestSanitizeErrorLeaksNothing(t *testing.T) {
	leaky := []error{
		errString("load key /etc/papaya/keys/id_ed25519: permission denied"),
		errString("ssh handshake tester@100.64.1.5: unable to authenticate, no supported methods remain"),
		errString("tailnet dial devbox-1.tail1234.ts.net:22: connection refused"),
		errString("host key mismatch for devbox-1: offered ssh-ed25519 SHA256:abc"),
	}

	forbidden := []string{"/etc/", "id_ed25519", "100.64.", "ts.net", "BEGIN", "SHA256:"}

	for _, err := range leaky {
		msg := sanitizeError("connect", err)
		for _, bad := range forbidden {
			require.NotContainsf(t, msg, bad,
				"error payload leaked %q: %q", bad, msg)
		}
		require.NotEmpty(t, msg)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
