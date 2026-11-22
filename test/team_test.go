package test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/setup"
	"github.com/martoast/papaya/test/harness"
)

// Teams: an admin gives a teammate their own machine, and the teammate can
// reach that machine and nothing else — enforced by the server on every path,
// not by what the interface happens to show.

const (
	bob        = "bob@example.com"
	adminOther = "other@example.com" // also an admin (on the allow list)
	bobBox     = "devbox-1"
	adminBox   = "devbox-2"
)

func newTeamStack(t *testing.T, tmux bool) *harness.Stack {
	t.Helper()
	servers := map[string]*harness.SSHServer{
		bobBox:   harness.NewSSHServer(t, harness.AuthNone),
		adminBox: harness.NewSSHServer(t, harness.AuthNone),
	}
	return harness.NewStack(t, servers, harness.StackOptions{
		Tmux: tmux,
		Machines: []setup.Machine{
			{Name: bobBox, FQDN: bobBox + ".tailnet.ts.net", User: "tester", Method: setup.MethodKey, Ready: true},
			{Name: adminBox, FQDN: adminBox + ".tailnet.ts.net", User: "tester", Method: setup.MethodKey, Ready: true},
		},
		Team: []setup.Member{{Login: bob, Machines: []string{bobBox}}},
	})
}

func waitLive(t *testing.T, c *harness.Client, id string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, s := range c.ListSessions() {
			if s.ID == id && live(s.State) {
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)
}

func TestAMemberOpensTabsOnlyOnTheirOwnMachine(t *testing.T) {
	st := newTeamStack(t, false)
	m := st.Client(bob)

	own := m.CreateSession(bobBox, "work", 80, 24)
	require.Equal(t, bobBox, own.Host)

	for _, host := range []string{adminBox, adminBox + ".tailnet.ts.net", bobBox + ".evil.example", "100.64.0.9", "localhost"} {
		resp, body := m.CreateSessionRaw(host, "", 80, 24)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "host %q: %s", host, body)
	}

	// The full tailnet name of their own machine works, and is dialled as the
	// machine's own name.
	resp, body := m.Do("POST", "/api/sessions", map[string]any{"host": bobBox + ".tailnet.ts.net", "cols": 80, "rows": 24})
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	require.Contains(t, string(body), `"host":"`+bobBox+`"`)

	// Only the account the machine was set up with.
	resp, _ = m.Do("POST", "/api/sessions", map[string]any{"host": bobBox, "user": "root", "cols": 80, "rows": 24})
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Resuming is held to the same limits.
	resp, _ = m.Do("POST", "/api/sessions/resume", map[string]any{"host": adminBox, "workspace": "papaya-x"})
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// The machine picker names only their machine.
	_, body = m.Get("/api/hosts")
	require.Contains(t, string(body), bobBox)
	require.NotContains(t, string(body), adminBox)
}

func TestAMemberCannotReachAnyoneElsesTab(t *testing.T) {
	st := newTeamStack(t, false)
	admin := st.Client(alice)
	m := st.Client(bob)

	// The admin's tabs, including one on the member's own machine.
	onAdminBox := admin.CreateSession(adminBox, "secret", 80, 24)
	onBobBox := admin.CreateSession(bobBox, "admin-on-bob", 80, 24)

	require.Empty(t, m.ListSessions(), "a member sees none of the admin's tabs")
	for _, id := range []string{onAdminBox.ID, onBobBox.ID} {
		resp, _ := m.PatchSession(id, map[string]any{"name": "pwned"})
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp, _ = m.Do("POST", "/api/sessions/"+id+"/seen", nil)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp, _ = m.Upload(id, "x.txt", []byte("data"))
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		_, wsResp, err := m.DialWSRaw(id, nil)
		require.Error(t, err)
		require.NotNil(t, wsResp)
		require.Equal(t, http.StatusNotFound, wsResp.StatusCode)
		resp, _ = m.DeleteSession(id)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	}
	names := []string{}
	for _, s := range admin.ListSessions() {
		names = append(names, s.Name)
	}
	require.ElementsMatch(t, []string{"secret", "admin-on-bob"}, names, "nothing the member tried changed the admin's tabs")
}

func TestTheAdminSeesAndControlsTheMembersTabs(t *testing.T) {
	st := newTeamStack(t, false)
	admin := st.Client(alice)
	m := st.Client(bob)

	mine := m.CreateSession(bobBox, "bob-work", 80, 24)

	var seen *harness.Session
	for _, s := range admin.ListSessions() {
		if s.ID == mine.ID {
			s := s
			seen = &s
		}
	}
	require.NotNil(t, seen, "the admin is shown the member's tab")
	require.Equal(t, bob, seen.Owner, "and told whose it is")

	ws := admin.DialWS(mine.ID)
	ws.WaitForState("connected", 10*time.Second)
	ws.SendLine("echo hi-from-admin")
	ws.WaitForOutput("hi-from-admin", 5*time.Second)

	resp, _ := admin.DeleteSession(mine.ID)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Empty(t, m.ListSessions())

	// Another admin's tabs stay that admin's own, team or no team.
	other := st.Client(adminOther)
	o := other.CreateSession(adminBox, "other-admin", 80, 24)
	for _, s := range admin.ListSessions() {
		require.NotEqual(t, o.ID, s.ID)
	}
}

func TestPapayaSayStaysInsideTheMembersScope(t *testing.T) {
	st := newTeamStack(t, true)
	admin := st.Client(alice)
	m := st.Client(bob)

	secret := admin.CreateSession(adminBox, "secret", 80, 24)
	waitLive(t, admin, secret.ID)
	mine := m.CreateSession(bobBox, "bobtab", 80, 24)
	waitLive(t, m, mine.ID)

	resp, _ := m.Do("POST", "/api/say", map[string]any{"to": "secret", "text": "rm -rf ~"})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = m.Get("/api/say/screen?to=secret")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = m.Do("POST", "/api/status", map[string]any{"tab": "secret", "state": "done"})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	_, body := m.Get("/api/say/tabs")
	require.NotContains(t, string(body), "secret")
	require.NotContains(t, string(body), adminBox)
	require.Contains(t, string(body), "bobtab")

	// The admin can talk to the member's tab.
	resp, body = admin.Do("POST", "/api/say", map[string]any{"to": "bobtab", "text": "echo from-admin", "enter": true})
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(body))
}

func TestMembersGetNoSettings(t *testing.T) {
	st := newTeamStack(t, false)
	m := st.Client(bob)

	for _, r := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/setup", nil},
		{"PUT", "/api/setup/machines", map[string]any{"machines": []any{}}},
		{"POST", "/api/setup/test", map[string]any{"host": adminBox, "user": "tester"}},
		{"POST", "/api/setup/complete", map[string]any{"complete": false}},
		{"POST", "/api/setup/seen", nil},
		{"GET", "/api/team", nil},
		{"PUT", "/api/team", map[string]any{"members": []any{map[string]any{"login": bob, "machines": []string{bobBox, adminBox}}}}},
		{"PUT", "/api/voice/key", map[string]any{"key": "sk-x"}},
		{"DELETE", "/api/voice/key", nil},
		{"PUT", "/api/themes/evil", map[string]any{"colors": map[string]string{"background": "#000000"}}},
		{"PUT", "/api/themes/evil/scene", "<script>alert(1)</script>"},
		{"DELETE", "/api/themes/default", nil},
	} {
		resp, body := m.Do(r.method, r.path, r.body)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "%s %s: %s", r.method, r.path, body)
	}
	// The member's list was not widened by any of that.
	require.Equal(t, []string{bobBox}, st.Store.MemberMachines(bob))

	caps := m.Capabilities()
	require.Equal(t, "member", caps.Role)
	require.False(t, caps.VoiceSettable)
	require.Equal(t, "admin", st.Client(alice).Capabilities().Role)

	// Picking a theme for themselves is fine.
	resp, _ := m.Get("/api/themes")
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestTakingAMachineAwayCutsOffItsTabsAtOnce(t *testing.T) {
	st := newTeamStack(t, false)
	admin := st.Client(alice)
	m := st.Client(bob)
	mine := m.CreateSession(bobBox, "work", 80, 24)

	resp, body := admin.Do("PUT", "/api/team", map[string]any{"members": []any{map[string]any{"login": bob, "machines": []string{}}}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	require.Empty(t, m.ListSessions())
	resp, _ = m.PatchSession(mine.ID, map[string]any{"name": "x"})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = m.CreateSessionRaw(bobBox, "", 80, 24)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Removed from the team altogether: an unknown caller again.
	resp, _ = admin.Do("PUT", "/api/team", map[string]any{"members": []any{}})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = m.Get("/api/sessions")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestTheTeamPanelRefusesWhatWouldWidenAccess(t *testing.T) {
	st := newTeamStack(t, false)
	admin := st.Client(alice)

	for _, members := range [][]map[string]any{
		{{"login": bob, "machines": []string{"not-a-machine"}}},
		{{"login": alice, "machines": []string{bobBox}}}, // an admin cannot be demoted into a member entry
		{{"login": "", "machines": []string{bobBox}}},
		{{"login": bob, "machines": []string{bobBox}}, {"login": "BOB@example.com", "machines": []string{adminBox}}},
	} {
		resp, body := admin.Do("PUT", "/api/team", map[string]any{"members": members})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	}
	require.Equal(t, []string{bobBox}, st.Store.MemberMachines(bob))

	resp, body := admin.Get("/api/team")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), bob)
}

func TestAMembersMachineCanJoinOnlyItself(t *testing.T) {
	st := newTeamStack(t, false)

	// papaya-say, the join line and friends run ON a machine and are identified
	// by the account that machine is logged in as. Bob's account on a machine
	// that is not his gets nowhere.
	st.SetCallerMachine(bob, adminBox)
	resp, body := st.Client(bob).Do("POST", "/api/setup/join", map[string]any{"user": "root", "sshd": true})
	require.Equal(t, http.StatusForbidden, resp.StatusCode, string(body))
	mc, _ := st.Store.Machine(adminBox)
	require.Equal(t, "tester", mc.User, "the admin's machine was not touched")

	st.SetCallerMachine(bob, bobBox)
	resp, body = st.Client(bob).Do("POST", "/api/setup/join", map[string]any{"user": "bob", "sshd": true})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	mc, _ = st.Store.Machine(bobBox)
	require.Equal(t, "bob", mc.User)
	require.Len(t, st.Store.Get().Machines, 2, "joining never adds a machine for a member")
}

func TestATaggedMachineCanBeAMemberButNeverMore(t *testing.T) {
	st := newTeamStack(t, false)
	resp, body := st.Client(alice).Do("PUT", "/api/team", map[string]any{"members": []any{
		map[string]any{"login": "tag:bob-box", "machines": []string{bobBox}},
		// A member literally named like Tailscale's placeholder for tagged
		// devices must not match a tagged machine by that login.
		map[string]any{"login": "tagged-devices", "machines": []string{adminBox}},
	}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	c := st.TaggedMemberClient("tag:bob-box")
	require.Equal(t, "member", c.Capabilities().Role)
	resp, _ = c.CreateSessionRaw(adminBox, "", 80, 24)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp, _ = c.CreateSessionRaw(bobBox, "", 80, 24)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	unknown := st.TaggedMemberClient("tag:nobody")
	resp, _ = unknown.Get("/api/sessions")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAnInstallWithNoTeamIsUnchanged(t *testing.T) {
	// Setup on, no team: admins behave exactly as before, and nobody else gets in.
	servers := map[string]*harness.SSHServer{bobBox: harness.NewSSHServer(t, harness.AuthNone)}
	st := harness.NewStack(t, servers, harness.StackOptions{
		Machines: []setup.Machine{{Name: bobBox, User: "tester", Method: setup.MethodKey, Ready: true}},
	})
	a := st.Client(alice)
	o := st.Client(adminOther)
	s1 := a.CreateSession(bobBox, "a", 80, 24)
	o.CreateSession(bobBox, "o", 80, 24)
	require.Len(t, a.ListSessions(), 1)
	require.Equal(t, s1.ID, a.ListSessions()[0].ID)
	require.Empty(t, a.ListSessions()[0].Owner)
	require.Equal(t, "admin", a.Capabilities().Role)
	resp, _ := st.Client(bob).Get("/api/sessions")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
