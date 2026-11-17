package test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

// papaya-status lets the program in a tab say what it is doing. The tab is
// found by name; the status reaches browsers already watching and is replayed
// to ones that attach later, and needs-you raises attention like a bell.
func TestAgentStatusReachesTheTab(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)
	sess := c.CreateSession("devbox-1", "api", 80, 24)
	require.Eventually(t, func() bool {
		for _, s := range c.ListSessions() {
			if s.ID == sess.ID && live(s.State) {
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)

	resp, body := c.Do("POST", "/api/status", map[string]any{"tab": "api", "state": "needs-you", "msg": "Claude needs your permission to use Bash"})
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(body))

	// A browser that opens the tab afterwards learns it straight away.
	ws := c.DialWS(sess.ID)
	ws.WaitForEvent("agent", 5*time.Second, func(ev map[string]any) bool {
		return ev["state"] == "needs-you" && ev["msg"] == "Claude needs your permission to use Bash"
	})

	resp, _ = c.Do("POST", "/api/status", map[string]any{"tab": "api", "state": "nonsense"})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = c.Do("POST", "/api/status", map[string]any{"tab": "nope", "state": "done"})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// A tab's theme is one truth for every device: a browser that attaches (or
// reattaches after sleeping through a change) is told the tab's current theme.
func TestATabsThemeReachesEveryBrowserThatAttaches(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)
	sess := c.CreateSession("devbox-1", "themed", 80, 24)
	hs, err := st.Hub.Get(alice, sess.ID)
	require.NoError(t, err)
	hs.SetTheme("koi-pond")

	ws := c.DialWS(sess.ID)
	ws.WaitForEvent("theme", 5*time.Second, func(ev map[string]any) bool { return ev["theme"] == "koi-pond" })

	// Back to the shared theme: a later browser is told that too.
	hs.SetTheme("")
	ws2 := c.DialWS(sess.ID)
	ws2.WaitForEvent("theme", 5*time.Second, func(ev map[string]any) bool { return ev["theme"] == "" })
}

// A program asks which tab it runs in by its tmux session, and gets the tab's
// current name, also after a rename; status and messages still reach it by
// either name.
func TestATabKnowsItsCurrentName(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)
	sess := c.CreateSession("devbox-1", "worker", 80, 24)
	var ws string
	require.Eventually(t, func() bool {
		for _, s := range c.ListSessions() {
			if s.ID == sess.ID && s.Workspace != "" {
				ws = s.Workspace
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)

	resp, _ := c.Do("PATCH", "/api/sessions/"+sess.ID, map[string]any{"name": "Henry"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, body := c.Do("GET", "/api/say/me?workspace="+ws, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Henry", string(body))

	resp, body = c.Do("POST", "/api/status", map[string]any{"tab": ws, "state": "working"})
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "status by tmux name still finds the renamed tab: %s", body)
	resp, _ = c.Do("GET", "/api/say/me?workspace=nope", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
