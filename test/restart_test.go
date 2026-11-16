package test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

// An update restarts papaya. The work in each tab never stopped (it runs in
// tmux on the target host); the tabs themselves must come back too, with the
// same ids, so browsers that were open just reconnect.
func TestTabsComeBackAfterARestart(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)
	sess := c.CreateSession("devbox-1", "precious", 80, 24)

	var workspace string
	require.Eventually(t, func() bool {
		for _, s := range c.ListSessions() {
			if s.ID == sess.ID && s.Workspace != "" && live(s.State) {
				workspace = s.Workspace
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)

	path := filepath.Join(t.TempDir(), "sessions.json")
	require.NoError(t, st.Hub.SaveSessions(path))

	// Shutting down must not overwrite the list with an empty one.
	st.Hub.CloseAll("restarting")
	require.NoError(t, st.Hub.SaveSessions(path))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(b), sess.ID)

	// A new papaya process on the same machines: its own pool and hub.
	st2 := harness.NewStack(t, map[string]*harness.SSHServer{"devbox-1": srv}, harness.StackOptions{Tmux: true})
	n, err := st2.Hub.RestoreSessions(path)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	c2 := st2.Client(alice)
	require.Eventually(t, func() bool {
		for _, s := range c2.ListSessions() {
			if s.ID == sess.ID && s.Name == "precious" && s.Workspace == workspace && live(s.State) {
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "the tab is back with its id, name and workspace")
}

func TestWithoutTmuxNothingIsRestored(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	path := filepath.Join(t.TempDir(), "sessions.json")
	require.NoError(t, os.WriteFile(path, []byte(`[{"id":"x","owner":"alice@example.com","host":"devbox-1","user":"tester","name":"n","order":0,"workspace":"papaya-n"}]`), 0o600))
	n, err := st.Hub.RestoreSessions(path)
	require.NoError(t, err)
	require.Zero(t, n)
}

// live is a tab whose shell is running: attached to a browser or not.
func live(state string) bool { return state == "connected" || state == "detached" }
