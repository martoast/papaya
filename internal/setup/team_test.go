package setup

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func teamStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "settings.json"))
	require.NoError(t, err)
	require.NoError(t, st.Update(func(s *Settings) {
		s.Machines = []Machine{
			{Name: "ariosto-box", FQDN: "ariosto-box.tail1234.ts.net", User: "ariosto"},
			{Name: "hp", FQDN: "hp.tail1234.ts.net", User: "hp"},
		}
	}))
	return st
}

func TestATeamSurvivesARestart(t *testing.T) {
	st := teamStore(t)
	require.NoError(t, st.SetTeam([]Member{{Login: "Ariosto@Example.com", Machines: []string{"ariosto-box.tail1234.ts.net"}}}, nil))

	again, err := Open(st.path)
	require.NoError(t, err)
	require.Equal(t, []Member{{Login: "Ariosto@Example.com", Machines: []string{"ariosto-box"}}}, again.Team())
}

func TestMembersAreMatchedByLoginOrTagOnly(t *testing.T) {
	st := teamStore(t)
	require.NoError(t, st.SetTeam([]Member{
		{Login: "ariosto@example.com", Machines: []string{"ariosto-box"}},
		{Login: "tag:kiosk", Machines: []string{"ariosto-box"}},
	}, nil))

	k, ok := st.MemberOf("ARIOSTO@example.com", nil)
	require.True(t, ok)
	require.Equal(t, "ariosto@example.com", k)
	k, ok = st.MemberOf("", []string{"tag:kiosk"})
	require.True(t, ok)
	require.Equal(t, "tag:kiosk", k)

	_, ok = st.MemberOf("tag:kiosk", nil) // a login can never claim a tag entry
	require.False(t, ok)
	_, ok = st.MemberOf("someone@example.com", []string{"tag:other"})
	require.False(t, ok)
}

func TestMachineForIsStrict(t *testing.T) {
	st := teamStore(t)
	allowed := []string{"ariosto-box"}
	for host, want := range map[string]bool{
		"ariosto-box":                 true,
		"ARIOSTO-BOX.":                true,
		"ariosto-box.tail1234.ts.net": true,
		"ariosto-box.evil.example":    false,
		"ariosto-box.tail9999.ts.net": false,
		"hp":                          false,
		"hp.tail1234.ts.net":          false,
		"100.64.0.1":                  false,
		"":                            false,
	} {
		got, ok := st.MachineFor(allowed, host)
		require.Equal(t, want, ok, host)
		if ok {
			require.Equal(t, "ariosto-box", got, host)
		}
	}
	_, ok := st.MachineFor(nil, "ariosto-box")
	require.False(t, ok)
}

func TestSetTeamRefusesWhatItCannotHonour(t *testing.T) {
	st := teamStore(t)
	isAdmin := func(l string) bool { return l == "alex@example.com" }
	for _, team := range [][]Member{
		{{Login: "x@example.com", Machines: []string{"nope"}}},
		{{Login: "x@example.com", Machines: []string{"ariosto"}}}, // a prefix is not a machine
		{{Login: "alex@example.com", Machines: []string{"hp"}}},
		{{Login: " ", Machines: nil}},
		{{Login: "a b@example.com"}},
		{{Login: "x@example.com"}, {Login: "X@example.com"}},
	} {
		require.ErrorIs(t, st.SetTeam(team, isAdmin), ErrInvalidTeam, "%+v", team)
	}
	require.Empty(t, st.Team())
}

func TestRemovingAMachineFromSetupRemovesItFromItsTeammate(t *testing.T) {
	st := teamStore(t)
	require.NoError(t, st.SetTeam([]Member{{Login: "a@example.com", Machines: []string{"ariosto-box"}}}, nil))

	require.NoError(t, st.Update(func(s *Settings) {
		s.Machines = s.Machines[1:] // ariosto-box taken out of setup
		PruneTeam(s)
	}))
	require.Empty(t, st.MemberMachines("a@example.com"))
	_, ok := st.MachineFor([]string{"ariosto-box"}, "ariosto-box")
	require.False(t, ok, "a machine no longer set up is never reachable, list or no list")
}
