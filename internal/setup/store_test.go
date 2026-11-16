package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAFreshInstallHasNoSettings(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "settings.json"))
	require.NoError(t, err)
	got := st.Get()
	require.False(t, got.Complete)
	require.Empty(t, got.Machines)
}

// Choices survive a restart: written, reopened, read back the same.
func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "settings.json")
	st, err := Open(path)
	require.NoError(t, err)

	require.NoError(t, st.Update(func(s *Settings) {
		s.Complete = true
		s.Machines = []Machine{
			{Name: "laptop", FQDN: "laptop.tail1234.ts.net", User: "alice", Method: MethodKey, Ready: true},
			{Name: "server", User: "ubuntu", Method: MethodTailscale},
		}
		s.Seen = []string{"laptop", "server"}
	}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "settings are private to papaya")
	_, err = os.Stat(path + ".tmp")
	require.True(t, os.IsNotExist(err), "the temporary file is renamed into place, never left behind")

	again, err := Open(path)
	require.NoError(t, err)
	require.Equal(t, st.Get(), again.Get())
	require.True(t, again.Get().Complete)
	require.Equal(t, []string{"laptop", "server"}, again.Hosts())
}

func TestMachinesAreFoundByAnySpelling(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "settings.json"))
	require.NoError(t, err)
	require.NoError(t, st.Update(func(s *Settings) {
		s.Machines = []Machine{{Name: "laptop", FQDN: "laptop.tail1234.ts.net", User: "alice"}}
	}))

	for _, h := range []string{"laptop", "LAPTOP", "laptop.tail1234.ts.net", "laptop.tail1234.ts.net.", "laptop.other"} {
		require.Equal(t, "alice", st.UserFor(h), h)
	}
	require.Equal(t, "", st.UserFor("server"))
	require.Equal(t, "", st.UserFor("100.64.0.1"), "an address never matches by its first octet")
}

func TestGetReturnsACopy(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "settings.json"))
	require.NoError(t, err)
	require.NoError(t, st.Update(func(s *Settings) { s.Machines = []Machine{{Name: "a"}} }))

	got := st.Get()
	got.Machines[0].Name = "changed"
	require.Equal(t, "a", st.Get().Machines[0].Name)
}

// Environment settings win where they say something; the setup screen's choice
// fills the gap; PAPAYA_SSH_USER is the last resort.
func TestEnvironmentOverridesTheSetupScreen(t *testing.T) {
	explicit := func(h string) string {
		if h == "server" {
			return "deploy"
		}
		return ""
	}
	chosen := func(h string) string {
		if h == "server" || h == "laptop" {
			return "alice"
		}
		return ""
	}
	resolve := AccountResolver(explicit, chosen, "fallback")
	require.Equal(t, "deploy", resolve("server"), "PAPAYA_HOSTS user@host wins")
	require.Equal(t, "alice", resolve("laptop"), "then the setup screen")
	require.Equal(t, "fallback", resolve("other"), "then PAPAYA_SSH_USER")
	require.Equal(t, "", AccountResolver(nil, nil, "")("x"))
}
