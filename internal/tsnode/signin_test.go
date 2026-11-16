package tsnode

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The sign-in announcement is the first thing a new user reads: the link must
// be there whole, framed, with a QR code under it for a phone.
func TestSignInShowsTheLinkAndAQRCode(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	printSignIn("https://login.tailscale.com/a/abc123")
	os.Stderr = old
	w.Close()
	out, err := io.ReadAll(r)
	require.NoError(t, err)

	text := string(out)
	require.Contains(t, text, "https://login.tailscale.com/a/abc123", "the link is printed whole")
	require.Contains(t, text, "Sign in", "it says what to do")
	require.Contains(t, text, "┌")
	require.Greater(t, strings.Count(text, "\n"), 15, "a QR code follows the box")
	require.True(t, strings.ContainsAny(text, "█▀▄"), "the QR code is drawn in block characters")
}
