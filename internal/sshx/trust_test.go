package sshx_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/test/harness"
)

// papaya makes its own key once and keeps it: a restart must never ask anyone
// to paste a new line onto their machines.
func TestEnsureKeyIsGeneratedOnceAndKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "papaya_ed25519")

	first, err := sshx.EnsureKey(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(first, "ssh-ed25519 "), first)
	require.True(t, strings.HasSuffix(first, " papaya"), first)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the private key is private")

	second, err := sshx.EnsureKey(path)
	require.NoError(t, err)
	require.Equal(t, first, second, "the same key on every call")

	_, _, _, _, err = ssh.ParseAuthorizedKey([]byte(first))
	require.NoError(t, err, "the line pastes straight into authorized_keys")
}

// The generated key really logs in to a machine that has it in authorized_keys.
func TestTheGeneratedKeyLogsIn(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthPublicKey)
	d := harness.NewCountingDialer(map[string]string{"laptop": srv.Addr})
	path := filepath.Join(t.TempDir(), "papaya_ed25519")

	line, err := sshx.EnsureKey(path)
	require.NoError(t, err)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	require.NoError(t, err)
	srv.AuthorizeKey(pub)

	client, err := sshx.Dial(testCtx(t), d, "laptop", "alice", sshx.Config{
		KeyPath:        path,
		KnownHostsPath: srv.KnownHostsFile(t, "laptop"),
	})
	require.NoError(t, err)
	client.Close()
}

// A host nothing vouches for is refused with its fingerprint attached, so the
// setup screen can show it; once trusted, it connects, and the trust persists.
func TestTrustOnFirstUseAfterConfirmation(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"server": srv.Addr})
	trusted := filepath.Join(t.TempDir(), "known_hosts")
	cfg := sshx.Config{
		UseTailscaleSSH:  true,
		KnownHostsPath:   filepath.Join(t.TempDir(), "no-operator-file"),
		TrustedHostsPath: trusted,
		TrustOnFirstUse:  func(string, ssh.PublicKey) bool { return false },
	}

	_, err := sshx.Dial(testCtx(t), d, "server", "alice", cfg)
	var unknown *sshx.UnknownHostError
	require.True(t, errors.As(err, &unknown), "got %v", err)
	require.Equal(t, "server", unknown.Host)
	want := ssh.FingerprintSHA256(srv.Signer.PublicKey())
	require.Equal(t, want, unknown.Fingerprint())

	// The person confirms that fingerprint.
	cfg.TrustOnFirstUse = func(hostname string, key ssh.PublicKey) bool {
		if ssh.FingerprintSHA256(key) != want {
			return false
		}
		require.NoError(t, sshx.TrustHostKey(trusted, hostname, key))
		return true
	}
	client, err := sshx.Dial(testCtx(t), d, "server", "alice", cfg)
	require.NoError(t, err)
	client.Close()

	// Later connections need no confirmation: the key is in papaya's file.
	cfg.TrustOnFirstUse = nil
	client, err = sshx.Dial(testCtx(t), d, "server", "alice", cfg)
	require.NoError(t, err, "trust must persist")
	client.Close()

	require.NoError(t, sshx.TrustHostKey(trusted, "server:22", srv.Signer.PublicKey()))
	raw, err := os.ReadFile(trusted)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(raw), "\n"), "trusting twice writes one line")
}

// A Tailscale SSH machine advertises its host keys to the tailnet, which is
// how `tailscale ssh` verifies it — so papaya needs no known_hosts entry.
func TestTailnetAdvertisedHostKeysAreTrusted(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"server": srv.Addr})
	cfg := sshx.Config{
		UseTailscaleSSH:  true,
		TrustedHostsPath: filepath.Join(t.TempDir(), "known_hosts"),
		TailnetHostKeys: func(host string) []string {
			if host == "server" {
				return []string{srv.AuthorizedHostKey()}
			}
			return nil
		},
	}
	client, err := sshx.Dial(testCtx(t), d, "server", "alice", cfg)
	require.NoError(t, err)
	client.Close()

	// A different advertised key does not vouch for this one.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, err := ssh.NewSignerFromKey(other)
	require.NoError(t, err)
	cfg.TailnetHostKeys = func(string) []string {
		return []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey())))}
	}
	_, err = sshx.Dial(testCtx(t), d, "server", "alice", cfg)
	var unknown *sshx.UnknownHostError
	require.True(t, errors.As(err, &unknown), "got %v", err)
}

// Trust is only ever additive: a key that contradicts a pinned one is refused
// even when the tailnet or a confirmation would accept it.
func TestAPinnedKeyCannotBeOverridden(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"server": srv.Addr})

	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, err := ssh.NewSignerFromKey(other)
	require.NoError(t, err)
	trusted := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, sshx.TrustHostKey(trusted, "server:22", otherSigner.PublicKey()))

	_, err = sshx.Dial(testCtx(t), d, "server", "alice", sshx.Config{
		UseTailscaleSSH:  true,
		TrustedHostsPath: trusted,
		TailnetHostKeys:  func(string) []string { return []string{srv.AuthorizedHostKey()} },
		TrustOnFirstUse:  func(string, ssh.PublicKey) bool { return true },
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatch")
}
