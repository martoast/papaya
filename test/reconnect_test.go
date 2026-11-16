package test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

// TestReattachReplaysRing is the M5 gate: a dropped socket must not cost the
// user their screen.
func TestReattachReplaysRing(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	const markers = 100
	for i := 0; i < markers; i++ {
		ws.SendLine(fmt.Sprintf("echo MARKER-%03d", i))
	}
	ws.WaitForOutput(fmt.Sprintf("MARKER-%03d", markers-1), 15*time.Second)

	sessionsBefore := srv.Sessions.Load()

	// An abrupt RST, not a clean close: this is what dropped wifi looks like.
	ws.KillTCP()
	require.True(t, ws.WaitForClose(5*time.Second))

	require.Eventually(t, func() bool {
		return c.ListSessions()[0].State == "detached"
	}, 10*time.Second, 20*time.Millisecond, "a dropped socket must detach, not close")

	ws2 := c.DialWS(sess.ID)
	ws2.WaitForState("connected", 10*time.Second)

	// The whole ring replays, in order.
	replay := ws2.WaitForOutput(fmt.Sprintf("MARKER-%03d", markers-1), 10*time.Second)
	for i := 0; i < markers; i++ {
		require.Containsf(t, replay, fmt.Sprintf("MARKER-%03d", i),
			"marker %d missing from the replayed scrollback", i)
	}

	lastIdx := strings.Index(replay, "MARKER-099")
	firstIdx := strings.Index(replay, "MARKER-000")
	require.Less(t, firstIdx, lastIdx, "scrollback must replay in order")

	require.Equal(t, sessionsBefore, srv.Sessions.Load(),
		"reattaching must reuse the existing shell, not start a new one")
}

// TestProcessSurvivesDisconnect proves the SSH session is deliberately kept
// alive while no client is attached.
func TestProcessSurvivesDisconnect(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)
	ws.RunCommand("echo BEFORE-DROP", 5*time.Second)

	channelsBefore := srv.Sessions.Load()
	require.Equal(t, 1, st.Pool.Len())

	ws.KillTCP()
	require.True(t, ws.WaitForClose(5*time.Second))

	time.Sleep(2 * time.Second) // deliberately outlive the drop before reattaching

	require.Equal(t, 1, st.Pool.Len(), "the pooled connection must stay up while detached")

	ws2 := c.DialWS(sess.ID)
	ws2.WaitForState("connected", 10*time.Second)

	out := ws2.RunCommand("echo AFTER-REATTACH", 5*time.Second)
	require.Contains(t, out, "AFTER-REATTACH", "the same shell must still be responsive")
	require.Equal(t, channelsBefore, srv.Sessions.Load(),
		"no new SSH channel: the original shell process survived")
}

func TestDetachedReaper(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{DetachedTTL: 2 * time.Second})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	ws.KillTCP()
	require.True(t, ws.WaitForClose(5*time.Second))

	require.Eventually(t, func() bool {
		return st.Hub.Reap(time.Now()) > 0
	}, 10*time.Second, 200*time.Millisecond, "the reaper must expire a client-less session")

	require.Empty(t, c.ListSessions(), "the expired session must be gone from the API")
	require.Eventually(t, func() bool { return st.Pool.Len() == 0 },
		5*time.Second, 20*time.Millisecond,
		"reaping must release the pooled connection")
}

func TestReaperSparesAttachedSessions(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{DetachedTTL: time.Millisecond})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	time.Sleep(100 * time.Millisecond) // well past the TTL

	require.Equal(t, 0, st.Hub.Reap(time.Now()), "an attached session must never be reaped")
	require.Len(t, c.ListSessions(), 1)
}

// TestClientDeathClosesAllSessions is §3.4: when the shared connection dies,
// every tab on it must be told. A tab showing a live cursor on a dead
// connection is the failure this prevents.
func TestClientDeathClosesAllSessions(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	var wss []*harness.WSClient
	for i := 0; i < 4; i++ {
		sess := c.CreateSession("devbox-1", fmt.Sprintf("tab-%d", i), 80, 24)
		ws := c.DialWS(sess.ID)
		ws.WaitForState("connected", 10*time.Second)
		wss = append(wss, ws)
	}
	require.Equal(t, 1, st.Pool.Len())

	srv.DropAllConns()

	for i, ws := range wss {
		ws.WaitForEvent("status", 30*time.Second, func(ev map[string]any) bool {
			return ev["state"] == "closed"
		})
		_ = i
	}

	require.Eventually(t, func() bool { return st.Pool.Len() == 0 },
		15*time.Second, 50*time.Millisecond,
		"the dead connection must be evicted from the pool")

	for _, s := range c.ListSessions() {
		require.Equal(t, "closed", s.State)
	}
}

// --- §3.5 leak detection ---

// TestSessionChurnNoLeakCleanClose creates and tears down many sessions the
// orderly way.
func TestSessionChurnNoLeakCleanClose(t *testing.T) {
	testSessionChurn(t, false)
}

// TestSessionChurnNoLeakAbruptClose kills the TCP connection instead of closing
// it. This is where leaks actually live.
func TestSessionChurnNoLeakAbruptClose(t *testing.T) {
	testSessionChurn(t, true)
}

func testSessionChurn(t *testing.T, abrupt bool) {
	t.Helper()

	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	// Warm up so one-off goroutines are not counted as leaks.
	warm := c.CreateSession("devbox-1", "", 80, 24)
	warmWS := c.DialWS(warm.ID)
	warmWS.WaitForState("connected", 10*time.Second)
	warmWS.Close()
	c.DeleteSession(warm.ID)

	require.Eventually(t, func() bool { return st.Pool.Len() == 0 },
		10*time.Second, 20*time.Millisecond)

	runtime.GC()
	baseline := runtime.NumGoroutine()

	const rounds = 50
	for i := 0; i < rounds; i++ {
		sess := c.CreateSession("devbox-1", "", 80, 24)
		ws := c.DialWS(sess.ID)
		ws.WaitForState("connected", 10*time.Second)
		ws.SendLine("echo CHURN")
		ws.WaitForOutput("CHURN", 10*time.Second)

		if abrupt {
			ws.KillTCP()
		} else {
			ws.Close()
		}
		c.DeleteSession(sess.ID)
	}

	require.Empty(t, c.ListSessions())

	var final int
	ok := func() bool {
		for i := 0; i < 100; i++ {
			runtime.GC()
			final = runtime.NumGoroutine()
			if final <= baseline+5 {
				return true
			}
			time.Sleep(100 * time.Millisecond) // goroutine teardown is asynchronous
		}
		return false
	}()

	require.Truef(t, ok,
		"goroutine leak after %d sessions (abrupt=%v): baseline %d, now %d",
		rounds, abrupt, baseline, final)

	require.Equal(t, 0, st.Pool.Len(), "every pooled connection must be released")
}
