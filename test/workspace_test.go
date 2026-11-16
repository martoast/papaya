package test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

// killedWorkspaces returns the tmux session names the server was asked to end.
func killedWorkspaces(srv *harness.SSHServer) []string {
	var out []string
	for _, cmd := range srv.Execs() {
		if strings.Contains(cmd, "kill-session") {
			out = append(out, cmd)
		}
	}
	return out
}

// attachedWorkspaces returns the workspace each tab's shell attached to, in the
// order the sessions started.
func attachedWorkspaces(srv *harness.SSHServer) []string {
	const marker = "new-session -A -s "
	var out []string
	for _, cmd := range srv.Execs() {
		i := strings.Index(cmd, marker)
		if i < 0 {
			continue
		}
		name := strings.TrimPrefix(cmd[i+len(marker):], "'")
		if j := strings.Index(name, "'"); j >= 0 {
			name = name[:j]
		}
		out = append(out, name)
	}
	return out
}

// sessionState reports a session's current state, or "" if it is gone.
func sessionState(c *harness.Client, id string) string {
	for _, s := range c.ListSessions() {
		if s.ID == id {
			return s.State
		}
	}
	return ""
}

// Closing a tab means the user is finished, so the remote tmux session has to
// go with it. Without this every tab ever opened left a shell running on the
// target host forever.
func TestClosingATabEndsItsRemoteWorkspace(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "reports", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	resp, _ := c.DeleteSession(sess.ID)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	st.Hub.WaitForCleanup()

	killed := killedWorkspaces(srv)
	require.Len(t, killed, 1, "closing a tab must end exactly one remote session")
	require.Contains(t, killed[0], "'=papaya-reports'",
		"the target must be the exact workspace this tab attached to")
}

// The counterpart, and the more important of the two: a session that ends any
// other way must leave the remote workspace running. That is the whole reason
// tmux is here — a closed laptop must not destroy a running job.
func TestOnlyAnExplicitCloseEndsTheRemoteWorkspace(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "long-job", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	// The browser goes away without deleting anything.
	ws.Close()
	require.Eventually(t, func() bool {
		return sessionState(c, sess.ID) == "detached"
	}, 5*time.Second, 20*time.Millisecond, "the session should be waiting for a reconnect")

	// And papaya itself goes down.
	st.Hub.CloseAll("simulated restart")
	st.Hub.WaitForCleanup()

	require.Empty(t, killedWorkspaces(srv),
		"neither a dropped websocket nor a restart may end the remote session")
}

// Two tabs the user gave the same name share one workspace deliberately — that
// is what reconnect-by-name is for — so closing one must not end it. Tabs the
// user never named no longer share; see the test below.
func TestClosingOneOfTwoTabsSharingAWorkspaceSparesIt(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	first := c.CreateSession("devbox-1", "shared", 80, 24)
	ws1 := c.DialWS(first.ID)
	ws1.WaitForState("connected", 10*time.Second)

	second := c.CreateSession("devbox-1", "shared", 80, 24)
	ws2 := c.DialWS(second.ID)
	ws2.WaitForState("connected", 10*time.Second)

	names := attachedWorkspaces(srv)
	require.Len(t, names, 2)
	require.Equal(t, names[0], names[1],
		"a name the user typed is a deliberate request to share that workspace")

	resp, _ := c.DeleteSession(first.ID)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	st.Hub.WaitForCleanup()

	require.Empty(t, killedWorkspaces(srv),
		"the surviving tab is still attached to that workspace")

	// Once the last one goes, it is cleaned up.
	resp, _ = c.DeleteSession(second.ID)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	st.Hub.WaitForCleanup()

	require.Len(t, killedWorkspaces(srv), 1,
		"the last tab on a workspace must still end it")
}

// A tab that never got a shell never attached to anything. A workspace of that
// name may still exist from an earlier run of papaya, holding real work.
func TestClosingATabWhoseShellNeverStartedKillsNothing(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "precious", 80, 24)

	// Kill the transport before a shell can start, then close the tab.
	srv.DropAllConns()
	require.Eventually(t, func() bool {
		st := sessionState(c, sess.ID)
		return st == "closed" || st == "error"
	}, 10*time.Second, 20*time.Millisecond)

	resp, _ := c.DeleteSession(sess.ID)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	st.Hub.WaitForCleanup()

	for _, cmd := range killedWorkspaces(srv) {
		require.NotContains(t, cmd, "papaya-precious",
			"a session that never attached must not end a workspace of the same name")
	}
}

// Cleanup runs in the background precisely so this holds: the tab disappears
// now, and an unreachable host is dealt with afterwards.
func TestClosingATabDoesNotWaitForAnUnresponsiveHost(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "wedged", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	// From here the host accepts commands and never answers them.
	hang := make(chan struct{})
	srv.SetExecHang(hang)
	defer close(hang)

	start := time.Now()
	resp, _ := c.DeleteSession(sess.ID)
	elapsed := time.Since(start)

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Less(t, elapsed, 3*time.Second,
		"the delete must return without waiting on the remote teardown")
	require.Empty(t, c.ListSessions(), "the tab is gone regardless")
}

// Two tabs the user never named both key on the host, so they used to attach to
// one remote workspace: the same output appeared in both, and they fought over
// the terminal size, since tmux sizes a window to its smallest attached client.
// They now get separate workspaces.
func TestUnnamedTabsOnOneHostDoNotShareAWorkspace(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	first := c.CreateSession("devbox-1", "", 80, 24)
	c.DialWS(first.ID).WaitForState("connected", 10*time.Second)

	second := c.CreateSession("devbox-1", "", 80, 24)
	c.DialWS(second.ID).WaitForState("connected", 10*time.Second)

	names := attachedWorkspaces(srv)
	require.Len(t, names, 2)
	require.NotEqual(t, names[0], names[1],
		"two tabs the user never named must not land on the same workspace")

	// Closing one ends its own workspace and leaves the other's alone. The
	// suffix scheme only works because the kill demands an exact match: a bare
	// name would prefix-match "papaya-devbox-1" onto "papaya-devbox-1-2".
	resp, _ := c.DeleteSession(first.ID)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	st.Hub.WaitForCleanup()

	killed := killedWorkspaces(srv)
	require.Len(t, killed, 1)
	require.Contains(t, killed[0], "'="+names[0]+"'",
		"it must end exactly the workspace the closed tab was on")
	require.Equal(t, "connected", sessionState(c, second.ID),
		"the other tab keeps its shell")
}
