package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func base() map[string]string {
	return map[string]string{
		"TS_AUTHKEY":           "tskey-auth-abc123",
		"PAPAYA_ALLOWED_USERS": "alice@example.com",
		"PAPAYA_SSH_USER":      "alice",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := loadFrom(env(base()))
	require.NoError(t, err)

	require.Equal(t, "papaya", cfg.Hostname)
	require.Equal(t, "/var/lib/papaya", cfg.StateDir)
	require.True(t, cfg.Ephemeral)
	require.Equal(t, "alice", cfg.SSHUser, "taken from the environment; there is no built-in account")
	require.Equal(t, ".papaya-uploads", cfg.UploadDir)
	require.Equal(t, int64(25<<20), cfg.MaxUploadBytes)
	require.Equal(t, 10, cfg.MaxSessions)
	require.True(t, cfg.UseTailscaleSSH)
	require.Equal(t, "/etc/papaya/known_hosts", cfg.KnownHostsPath)
	require.False(t, cfg.InsecureSkipHostKey, "host key checking must default to on")
	require.Equal(t, 30*time.Minute, cfg.DetachedTTL)
}

// S3: an empty access list is a configuration error, never an invitation to let
// everyone in.
func TestS3EmptyAllowListIsFatal(t *testing.T) {
	e := base()
	delete(e, "PAPAYA_ALLOWED_USERS")

	_, err := loadFrom(env(e))
	require.Error(t, err, "must refuse to start with no allowed users and no allowed tags")
	require.Contains(t, err.Error(), "refusing to start")
}

func TestEmptyStringAllowListIsFatal(t *testing.T) {
	e := base()
	e["PAPAYA_ALLOWED_USERS"] = "  ,  ,"
	e["PAPAYA_ALLOWED_TAGS"] = ""

	_, err := loadFrom(env(e))
	require.Error(t, err, "whitespace-only lists must not count as configured")
}

func TestTagsAloneAreSufficient(t *testing.T) {
	e := base()
	delete(e, "PAPAYA_ALLOWED_USERS")
	e["PAPAYA_ALLOWED_TAGS"] = "tag:ci"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)
	require.True(t, cfg.AllowedTags["tag:ci"])
}

func TestTagsMustBePrefixed(t *testing.T) {
	e := base()
	e["PAPAYA_ALLOWED_TAGS"] = "ci"

	_, err := loadFrom(env(e))
	require.Error(t, err, "a tag without the tag: prefix never matches and is a typo")
	require.Contains(t, err.Error(), "tag:")
}

func TestMissingAuthKeyIsFatal(t *testing.T) {
	e := base()
	delete(e, "TS_AUTHKEY")

	_, err := loadFrom(env(e))
	require.Error(t, err)
	require.Contains(t, err.Error(), "TS_AUTHKEY")
}

func TestErrorsAreReportedTogether(t *testing.T) {
	_, err := loadFrom(env(map[string]string{}))
	require.Error(t, err)

	msg := err.Error()
	require.Contains(t, msg, "TS_AUTHKEY", "all problems should be reported at once")
	require.Contains(t, msg, "refusing to start")
}

func TestInvalidValuesAreRejected(t *testing.T) {
	cases := []struct{ key, val, want string }{
		{"PAPAYA_EPHEMERAL", "yesplease", "boolean"},
		{"PAPAYA_USE_TAILSCALE_SSH", "maybe", "boolean"},
		{"PAPAYA_INSECURE_SKIP_HOST_KEY", "sure", "boolean"},
		{"PAPAYA_MAX_UPLOAD_MB", "lots", "integer"},
		{"PAPAYA_MAX_UPLOAD_MB", "0", "positive"},
		{"PAPAYA_MAX_UPLOAD_MB", "-5", "positive"},
		{"PAPAYA_MAX_SESSIONS", "-1", "positive"},
		{"PAPAYA_MAX_SESSIONS", "abc", "integer"},
		{"PAPAYA_DETACHED_TTL", "forever", "duration"},
		{"PAPAYA_DETACHED_TTL", "-5m", "positive"},
	}

	for _, c := range cases {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			e := base()
			e[c.key] = c.val

			_, err := loadFrom(env(e))
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), c.want)
		})
	}
}

func TestUnreadableKeyPathIsFatal(t *testing.T) {
	e := base()
	e["PAPAYA_KEY_PATH"] = filepath.Join(t.TempDir(), "nope")

	_, err := loadFrom(env(e))
	require.Error(t, err, "a configured key that cannot be read is a startup error")
	require.Contains(t, err.Error(), "not readable")
}

func TestReadableKeyPathIsAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(path, []byte("key"), 0o600))

	e := base()
	e["PAPAYA_KEY_PATH"] = path

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)
	require.Equal(t, path, cfg.KeyPath)
}

func TestListParsing(t *testing.T) {
	e := base()
	e["PAPAYA_ALLOWED_USERS"] = " alice@example.com , bob@example.com ,, "
	e["PAPAYA_HOSTS"] = "devbox-1, devbox-2 ,"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)

	require.True(t, cfg.AllowedUsers["alice@example.com"])
	require.True(t, cfg.AllowedUsers["bob@example.com"])
	require.Len(t, cfg.AllowedUsers, 2, "empty entries must be dropped")
	require.Equal(t, []string{"devbox-1", "devbox-2"}, cfg.Hosts)
}

func TestOverrides(t *testing.T) {
	e := base()
	e["PAPAYA_HOSTNAME"] = "papaya-ci"
	e["PAPAYA_EPHEMERAL"] = "false"
	e["PAPAYA_MAX_SESSIONS"] = "3"
	e["PAPAYA_MAX_UPLOAD_MB"] = "50"
	e["PAPAYA_DETACHED_TTL"] = "90s"
	e["PAPAYA_SSH_USER"] = "deploy"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)

	require.Equal(t, "papaya-ci", cfg.Hostname)
	require.False(t, cfg.Ephemeral)
	require.Equal(t, 3, cfg.MaxSessions)
	require.Equal(t, int64(50<<20), cfg.MaxUploadBytes)
	require.Equal(t, 90*time.Second, cfg.DetachedTTL)
	require.Equal(t, "deploy", cfg.SSHUser)
}

// --- SSH user allow list ---

// root must be refused unless deliberately named. The tailnet policy may permit
// it, but a web terminal should not hand out uid 0 by default.
func TestRootIsDeniedByDefault(t *testing.T) {
	cfg, err := loadFrom(env(base()))
	require.NoError(t, err)

	for _, u := range []string{"root", "ROOT", "Root", "toor", "admin"} {
		require.Falsef(t, cfg.SSHUserAllowed(u), "%q must be refused by default", u)
	}
	require.True(t, cfg.SSHUserAllowed("alice"))
	require.True(t, cfg.SSHUserAllowed("ubuntu"))
	require.False(t, cfg.SSHUserAllowed(""), "an empty user is not a user")
}

func TestSSHUserAllowListIsExclusive(t *testing.T) {
	e := base()
	e["PAPAYA_ALLOWED_SSH_USERS"] = "ubuntu, alice"
	e["PAPAYA_SSH_USER"] = "ubuntu"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)

	require.True(t, cfg.SSHUserAllowed("ubuntu"))
	require.True(t, cfg.SSHUserAllowed("alice"))
	require.False(t, cfg.SSHUserAllowed("root"))
	require.False(t, cfg.SSHUserAllowed("deploy"))
	require.False(t, cfg.SSHUserAllowed("ec2-user"))
}

// root is still possible, but only by naming it, which is a deliberate act.
func TestRootRequiresExplicitOptIn(t *testing.T) {
	e := base()
	e["PAPAYA_ALLOWED_SSH_USERS"] = "root"
	e["PAPAYA_SSH_USER"] = "root"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)
	require.True(t, cfg.SSHUserAllowed("root"))
}

// A default user the allow list rejects would make every unqualified session
// fail at request time, so it is caught at startup instead.
func TestDefaultSSHUserMustBeAllowed(t *testing.T) {
	e := base()
	e["PAPAYA_ALLOWED_SSH_USERS"] = "ubuntu"
	e["PAPAYA_SSH_USER"] = "alice"

	_, err := loadFrom(env(e))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not permitted")
}

func TestDefaultSSHUserMustNotBeRoot(t *testing.T) {
	e := base()
	e["PAPAYA_SSH_USER"] = "root"

	_, err := loadFrom(env(e))
	require.Error(t, err, "defaulting every session to root must not be possible by accident")
}

// --- host allow list ---

func TestHostAllowListIsExclusive(t *testing.T) {
	e := base()
	e["PAPAYA_ALLOWED_HOSTS"] = "build-server, alices-laptop"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)

	allowed := []string{
		"build-server",
		// The same machine by FQDN: matching is on the first DNS label.
		"build-server.tail1234.ts.net",
		"build-server.tail1234.ts.net.",
		"BUILD-SERVER",
		"alices-laptop",
		"alices-laptop.tail1234.ts.net",
	}
	for _, h := range allowed {
		require.Truef(t, cfg.HostAllowed(h), "%q should be permitted", h)
	}

	denied := []string{
		"alices-desktop",
		"alices-desktop.tail1234.ts.net",
		"iphone-13-pro",
		"evil.example.com",
		"",
		// A near-miss must not slip through on a prefix match.
		"build-server-evil",
		"notalices-laptop",
	}
	for _, h := range denied {
		require.Falsef(t, cfg.HostAllowed(h), "%q must be refused", h)
	}
}

func TestHostAllowListAcceptsAddresses(t *testing.T) {
	e := base()
	e["PAPAYA_ALLOWED_HOSTS"] = "100.64.0.10, 100.64.0.11"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)

	require.True(t, cfg.HostAllowed("100.64.0.10"))
	require.True(t, cfg.HostAllowed("100.64.0.11"))
	require.False(t, cfg.HostAllowed("100.64.0.12"))
	// An address must not be split at the first dot and matched as a label.
	require.False(t, cfg.HostAllowed("100.1.1.1"))
}

func TestUnsetHostAllowListPermitsAnything(t *testing.T) {
	cfg, err := loadFrom(env(base()))
	require.NoError(t, err)
	require.True(t, cfg.HostAllowed("anything"))
	require.True(t, cfg.HostAllowed("100.1.2.3"))
}

// Hosts do not have to share a remote account. A Mac running its own sshd and a
// Linux box behind Tailscale SSH rarely do, and before "user@host" existed the
// mismatch was close to invisible: creating a session worked, because the picker
// sends a user explicitly, while the resumable scan failed against an account
// that does not exist there and simply never offered that host's work back.
func TestPerHostSSHUser(t *testing.T) {
	e := base()
	e["PAPAYA_SSH_USER"] = "ubuntu"
	e["PAPAYA_HOSTS"] = "server,alice@laptop"
	e["PAPAYA_ALLOWED_SSH_USERS"] = "ubuntu,alice"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)

	// The picker shows hosts, never "user@host".
	require.Equal(t, []string{"server", "laptop"}, cfg.Hosts)

	require.Equal(t, "alice", cfg.UserForHost("laptop"), "the per-host account wins")
	require.Equal(t, "ubuntu", cfg.UserForHost("server"), "an entry without one falls back")
	require.Equal(t, "ubuntu", cfg.UserForHost("unlisted"), "so does a host not listed at all")
}

// A host may be configured short and asked for long, or the reverse. Matching on
// the first label keeps one spelling from silently getting the wrong account.
func TestPerHostSSHUserMatchesEitherSpelling(t *testing.T) {
	e := base()
	e["PAPAYA_SSH_USER"] = "ubuntu"
	e["PAPAYA_HOSTS"] = "alice@laptop"
	e["PAPAYA_ALLOWED_SSH_USERS"] = "ubuntu,alice"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)

	require.Equal(t, "alice", cfg.UserForHost("laptop.tail1234.ts.net"))
	require.Equal(t, "alice", cfg.UserForHost("LAPTOP"))
	require.Equal(t, "alice", cfg.UserForHost("laptop."))
}

// A per-host account must clear the same bar as the default one. Left to fail at
// request time this reads as the host being down, not as a typo.
func TestPerHostSSHUserMustBeAllowed(t *testing.T) {
	e := base()
	e["PAPAYA_HOSTS"] = "root@laptop"
	e["PAPAYA_ALLOWED_SSH_USERS"] = "alice"

	_, err := loadFrom(env(e))
	require.Error(t, err)
	require.Contains(t, err.Error(), "PAPAYA_ALLOWED_SSH_USERS")
}

// "user@" names no host. Guessing which was meant would be worse than dropping it.
func TestHostEntryWithNoHostIsDropped(t *testing.T) {
	e := base()
	e["PAPAYA_HOSTS"] = "alice@,server"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)
	require.Equal(t, []string{"server"}, cfg.Hosts)
}

// There is no built-in remote account. A default name would log in as the wrong
// person on somebody else's machine, so a missing one is a startup error that
// says exactly what to set.
func TestNoDefaultSSHUser(t *testing.T) {
	e := base()
	delete(e, "PAPAYA_SSH_USER")

	_, err := loadFrom(env(e))
	require.Error(t, err)
	require.Contains(t, err.Error(), "PAPAYA_SSH_USER is required")
}

func TestEveryListedHostCanCarryItsOwnAccount(t *testing.T) {
	e := base()
	delete(e, "PAPAYA_SSH_USER")
	e["PAPAYA_HOSTS"] = "alice@laptop,ubuntu@server"

	cfg, err := loadFrom(env(e))
	require.NoError(t, err)
	require.Equal(t, "", cfg.SSHUser)
	require.Equal(t, "ubuntu", cfg.UserForHost("server"))
}

func TestAHostWithNoAccountIsNamed(t *testing.T) {
	e := base()
	delete(e, "PAPAYA_SSH_USER")
	e["PAPAYA_HOSTS"] = "alice@laptop,server"

	_, err := loadFrom(env(e))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no remote account for server")
}

// Installs from before the rename keep their TSTERM_* settings working, and a
// PAPAYA_* setting wins when both are present.
func TestLegacySettingNamesStillWork(t *testing.T) {
	e := map[string]string{
		"TS_AUTHKEY":           "tskey-auth-abc123",
		"TSTERM_ALLOWED_USERS": "alice@example.com",
		"TSTERM_SSH_USER":      "alice",
		"TSTERM_HOSTNAME":      "old-name",
		"TSTERM_MAX_SESSIONS":  "4",
	}
	cfg, err := loadFrom(env(e))
	require.NoError(t, err)
	require.Equal(t, "old-name", cfg.Hostname)
	require.Equal(t, "alice", cfg.SSHUser)
	require.Equal(t, 4, cfg.MaxSessions)
	require.True(t, cfg.AllowedUsers["alice@example.com"])

	e["PAPAYA_HOSTNAME"] = "new-name"
	cfg, err = loadFrom(env(e))
	require.NoError(t, err)
	require.Equal(t, "new-name", cfg.Hostname, "the new name wins")
}
