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
