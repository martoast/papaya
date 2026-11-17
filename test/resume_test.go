package test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

// Work outlives papaya on purpose, but nothing in papaya remembers it across a
// restart. Until this existed the only way back in was to recreate a tab with
// exactly the original name — which the user had to have memorised, including
// the "-2" a second unnamed tab was silently given.
func TestRunningWorkspacesAreOfferedForResume(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	// What a previous run of papaya left behind on the host.
	srv.SetWorkspaces("papaya-reports", "papaya-devbox-1", "papaya-devbox-1-2")

	got := c.Resumable()
	require.Len(t, got, 3)

	labels := map[string]bool{}
	for _, w := range got {
		labels[w["label"].(string)] = true
		require.Equal(t, "devbox-1", w["host"], "a workspace has to say where it is")
	}
	require.True(t, labels["reports"])
	require.True(t, labels["devbox-1-2"],
		"the suffixed workspace is the one a user could never guess their way back to")
}

// Sessions unrelated to papaya belong to the user, not to us.
// Every tmux session already running on a machine is offered, papaya's and
// anyone else's (plain tmux, another web terminal), so work can move to papaya
// without restarting it. Papaya's own keep their short label.
func TestResumeOffersEveryTmuxSession(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	srv.SetWorkspaces("papaya-mine", "irc", "tsterm-Henry")

	got := c.Resumable()
	labels := map[string]string{}
	for _, w := range got {
		labels[w["name"].(string)] = w["label"].(string)
	}
	require.Equal(t, map[string]string{"papaya-mine": "mine", "irc": "irc", "tsterm-Henry": "tsterm-Henry"}, labels)
}

// A workspace already on screen is not something to offer to reopen.
func TestResumeExcludesWorkspacesATabIsAlreadyShowing(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "live", 80, 24)
	c.DialWS(sess.ID).WaitForState("connected", 10*time.Second)

	srv.SetWorkspaces("papaya-live", "papaya-abandoned")

	got := c.Resumable()
	require.Len(t, got, 1)
	require.Equal(t, "abandoned", got[0]["label"],
		"the workspace the open tab is attached to must not be offered")
}

// The point of the whole thing: reattach to the exact remote session, not to
// one named after the tab.
func TestResumingAttachesToTheNamedWorkspace(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	srv.SetWorkspaces("papaya-devbox-1-2")

	sess := c.Resume("devbox-1", "recovered", "papaya-devbox-1-2", 80, 24)
	c.DialWS(sess.ID).WaitForState("connected", 10*time.Second)

	attached := attachedWorkspaces(srv)
	require.Len(t, attached, 1)
	require.Equal(t, "papaya-devbox-1-2", attached[0],
		"resuming must attach to the workspace asked for, not to one derived from the tab name")

	// And it is no longer on offer, because it is on screen.
	require.Empty(t, c.Resumable())
}

// A resumed tab owns its workspace like any other, so closing it ends the work
// — that is the difference between closing a tab and merely disconnecting.
func TestClosingAResumedTabEndsThatWorkspace(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	srv.SetWorkspaces("papaya-old-job")

	sess := c.Resume("devbox-1", "", "papaya-old-job", 80, 24)
	c.DialWS(sess.ID).WaitForState("connected", 10*time.Second)

	resp, _ := c.DeleteSession(sess.ID)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	st.Hub.WaitForCleanup()

	killed := killedWorkspaces(srv)
	require.Len(t, killed, 1)
	require.Contains(t, killed[0], "'=papaya-old-job'")
}

// The workspace name reaches a remote shell inside a command. It arrives from a
// client, and being shaped like something this server produced is not a reason
// to trust it.
func TestResumeRefusesUnsafeWorkspaceNames(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{Tmux: true})
	c := st.Client(alice)

	hostile := []string{
		"papaya-a; touch /tmp/papaya-RESUME-PWNED",
		"papaya-a$(touch /tmp/papaya-RESUME-PWNED)",
		"papaya-a`touch /tmp/papaya-RESUME-PWNED`",
		"papaya-a b",
		"papaya-win.0",  // tmux reads "." as address syntax
		"papaya-sess:1", // and ":" likewise
		"../../etc",
		"-t", // tmux would read it as a flag
		"a'b",
		"",
	}

	for _, name := range hostile {
		resp, body := c.ResumeRaw("devbox-1", "", name, 80, 24)
		require.NotEqualf(t, http.StatusCreated, resp.StatusCode,
			"resume accepted %q: %s", name, body)
	}
}

// Resuming names a host and an account as directly as creating does, so it gets
// the same gates. A list this server produced is not a capability.
func TestResumeEnforcesTheSameLimitsAsCreate(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{
		Tmux:            true,
		AllowedHosts:    []string{"devbox-1"},
		AllowedSSHUsers: []string{"tester"},
	})
	c := st.Client(alice)

	resp, _ := c.ResumeRaw("not-allowed-host", "", "papaya-x", 80, 24)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	resp, body := c.ResumeAsRaw("devbox-1", "root", "", "papaya-x", 80, 24)
	require.Equalf(t, http.StatusForbidden, resp.StatusCode, "root was allowed to resume: %s", body)
}
