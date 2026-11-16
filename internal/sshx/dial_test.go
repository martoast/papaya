package sshx_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/test/harness"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestAuthChainNoneMethod is the empirical confirmation the spec asked for
// first: Tailscale SSH in accept mode authorizes on tailnet identity alone, and
// x/crypto/ssh must reach that path via the automatic "none" attempt even
// though "none" is never in the configured method list.
func TestAuthChainNoneMethod(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	cfg := sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  srv.KnownHostsFile(t, "devbox-1"),
	}

	client, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", cfg)
	require.NoError(t, err, "none-auth handshake must succeed")
	defer client.Close()

	require.Equal(t, int32(1), d.Dials.Load())
}

// TestAuthChainKeyboardInteractive covers Tailscale SSH check mode, where sshd
// uses keyboard-interactive prompts to surface a browser approval URL.
func TestAuthChainKeyboardInteractive(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthKeyboardInteractive)
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	cfg := sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  srv.KnownHostsFile(t, "devbox-1"),
	}

	client, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", cfg)
	require.NoError(t, err)
	defer client.Close()
}

// TestAuthChainPublicKeyFallback covers hosts that are not running Tailscale
// SSH, where PAPAYA_KEY_PATH is the only way in.
func TestAuthChainPublicKeyFallback(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthPublicKey)

	keyPath := writeTestKey(t)
	signer := mustSigner(t, keyPath)
	srv.AuthorizeKey(signer.PublicKey())

	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})
	cfg := sshx.Config{
		UseTailscaleSSH: true, // both methods offered; only the key works here
		KeyPath:         keyPath,
		KnownHostsPath:  srv.KnownHostsFile(t, "devbox-1"),
	}

	client, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", cfg)
	require.NoError(t, err)
	defer client.Close()
}

func TestAuthChainFailsWithoutValidCredential(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthPublicKey) // authorizes no keys
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	cfg := sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  srv.KnownHostsFile(t, "devbox-1"),
	}

	_, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssh handshake")
}

func TestDialUnreachableHost(t *testing.T) {
	d := harness.NewCountingDialer(map[string]string{"devbox-1": "127.0.0.1:1"})
	cfg := sshx.Config{UseTailscaleSSH: true, InsecureSkipHostKey: true}

	_, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tailnet dial")
}

// --- host key verification (M7) ---

func TestHostKeyAcceptsPinnedKey(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	client, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  srv.KnownHostsFile(t, "devbox-1"),
	})
	require.NoError(t, err)
	client.Close()
}

// TestHostKeyRejectsUnknownHost is the M7 gate: an unknown host must produce a
// fingerprint error, never silent trust.
func TestHostKeyRejectsUnknownHost(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	// known_hosts pins a different host name, so devbox-1 is unknown.
	_, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  srv.KnownHostsFile(t, "someone-else"),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown host")
	require.Contains(t, err.Error(), "SHA256:", "error must show the fingerprint to pin")
}

func TestHostKeyRejectsMismatch(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	other := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	// Pin the *other* server's key under devbox-1: a classic MITM signature.
	kh := other.KnownHostsFile(t, "devbox-1")

	_, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  kh,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "host key mismatch")
}

func TestHostKeyMissingFileIsFatal(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	_, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  filepath.Join(t.TempDir(), "does-not-exist"),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "unreadable")
}

func TestHostKeyInsecureFlagAllowsUnknown(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	client, err := sshx.Dial(testCtx(t), d, "devbox-1", "tester", sshx.Config{
		UseTailscaleSSH:     true,
		InsecureSkipHostKey: true,
	})
	require.NoError(t, err)
	client.Close()
}

func writeTestKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600))
	return path
}

func mustSigner(t *testing.T, path string) ssh.Signer {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	s, err := ssh.ParsePrivateKey(b)
	require.NoError(t, err)
	return s
}

func TestClampDims(t *testing.T) {
	cases := []struct {
		inC, inR int
		wantC    int
		wantR    int
		reason   string
	}{
		{120, 40, 120, 40, "valid dims pass through"},
		{0, 0, 80, 24, "zero from a hidden terminal falls back to defaults"},
		{1, 1, 80, 24, "1x1 from a mis-measured fit falls back"},
		{-5, 10, 80, 10, "negative cols falls back, valid rows kept"},
		{100000, 100000, 1000, 1000, "absurd dims clamp to the maximum"},
	}
	for _, c := range cases {
		gotC, gotR := sshx.ClampDims(c.inC, c.inR)
		require.Equalf(t, c.wantC, gotC, "cols: %s", c.reason)
		require.Equalf(t, c.wantR, gotR, "rows: %s", c.reason)
	}
}
