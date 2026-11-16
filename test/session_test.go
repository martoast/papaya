package test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

const alice = "alice@example.com"

func newStack(t *testing.T, opts harness.StackOptions) (*harness.Stack, *harness.SSHServer) {
	t.Helper()
	srv := harness.NewSSHServer(t, harness.AuthNone)
	st := harness.NewStack(t, map[string]*harness.SSHServer{"devbox-1": srv}, opts)
	return st, srv
}

// --- §5.1 basic round trip ---

func TestEchoRoundTrip(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	ws.SendLine("echo HELLO-ROUNDTRIP")
	ws.WaitForOutput("HELLO-ROUNDTRIP", 5*time.Second)
}

func TestSessionReachesConnectedState(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	require.Equal(t, "connecting", sess.State, "POST returns immediately without awaiting SSH")

	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	require.Eventually(t, func() bool {
		return c.ListSessions()[0].State == "connected"
	}, 5*time.Second, 20*time.Millisecond)
}

// --- §5.2 PTY dimensions: the remote reports its own geometry ---

func TestPTYCreatedWithRealDimensions(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	require.Eventually(t, func() bool {
		cols, rows := srv.LastDims()
		return cols == 120 && rows == 40
	}, 5*time.Second, 20*time.Millisecond,
		"a session created at 120x40 must never request an 80x24 PTY")

	out := ws.RunCommand("stty size", 5*time.Second)
	require.Contains(t, out, "40 120", "remote must report rows cols as 40 120")
}

func TestResizePropagates(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)
	ws.RunCommand("stty size", 5*time.Second)

	for _, dim := range [][2]int{{120, 40}, {200, 50}, {40, 10}, {80, 24}} {
		cols, rows := dim[0], dim[1]
		ws.Resize(cols, rows)

		want := fmt.Sprintf("%d %d", rows, cols)
		require.Eventuallyf(t, func() bool {
			return strings.Contains(ws.RunCommand("stty size", 5*time.Second), want)
		}, 5*time.Second, 50*time.Millisecond,
			"remote never reported %dx%d", cols, rows)
	}
}

// TestResizeRejectsGarbage is the server-side guard against the hidden-terminal
// measurement bug: a terminal measured while hidden computes 0x0 or 1x1, and if
// that reached the remote PTY it would destroy the geometry.
func TestResizeRejectsGarbage(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 120, 40)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	require.Eventually(t, func() bool {
		cols, rows := srv.LastDims()
		return cols == 120 && rows == 40
	}, 5*time.Second, 20*time.Millisecond)

	for _, bad := range [][2]int{{0, 0}, {1, 1}, {-5, 10}, {100000, 100000}} {
		ws.Resize(bad[0], bad[1])
	}

	// Give any bad resize time to land before asserting it did not.
	time.Sleep(200 * time.Millisecond) // no event marks "a resize did not happen"

	cols, rows := srv.LastDims()
	require.NotEqual(t, 0, cols, "a 0-column PTY must never be requested")
	require.NotEqual(t, 1, cols, "a 1-column PTY must never be requested")
	require.LessOrEqual(t, cols, 1000, "absurd dimensions must be clamped")
	require.LessOrEqual(t, rows, 1000)
	require.GreaterOrEqual(t, cols, 2)
	require.GreaterOrEqual(t, rows, 2)

	out := ws.RunCommand("stty size", 5*time.Second)
	require.NotContains(t, out, "1 1", "remote must never end up at 1x1")
	require.NotContains(t, out, "0 0")
}

// --- §6.2 isolation between tabs sharing a connection ---

func TestSessionsAreIsolated(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	var wss []*harness.WSClient
	for i := 0; i < 3; i++ {
		sess := c.CreateSession("devbox-1", fmt.Sprintf("tab-%d", i), 80, 24)
		ws := c.DialWS(sess.ID)
		ws.WaitForState("connected", 10*time.Second)
		wss = append(wss, ws)
	}

	require.Equal(t, int32(1), st.Dialer.Dials.Load(), "3 tabs on one host share one connection")

	// Each tab echoes a distinct value; none may see another's.
	for i, ws := range wss {
		ws.SendLine(fmt.Sprintf("echo VALUE-%d", i))
	}
	for i, ws := range wss {
		ws.WaitForOutput(fmt.Sprintf("VALUE-%d", i), 5*time.Second)
		for j := range wss {
			if j != i {
				require.NotContainsf(t, ws.Output(), fmt.Sprintf("VALUE-%d", j),
					"tab %d saw output belonging to tab %d", i, j)
			}
		}
	}

	// Resizing one tab must not disturb the others: PTY geometry is per channel.
	wss[0].Resize(200, 50)
	require.Eventually(t, func() bool {
		return strings.Contains(wss[0].RunCommand("stty size", 5*time.Second), "50 200")
	}, 5*time.Second, 50*time.Millisecond)

	for i := 1; i < len(wss); i++ {
		out := wss[i].RunCommand("stty size", 5*time.Second)
		require.Containsf(t, out, "24 80", "tab %d geometry leaked from tab 0", i)
	}
}

// --- §6.3 limits and ownership ---

func TestSessionLimit(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{MaxSessions: 10})
	c := st.Client(alice)

	for i := 0; i < 10; i++ {
		c.CreateSession("devbox-1", "", 80, 24)
	}

	resp, _ := c.CreateSessionRaw("devbox-1", "", 80, 24)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode,
		"the cap must be enforced server-side, not just in the UI")
	require.Len(t, c.ListSessions(), 10, "the existing sessions stay healthy")
}

func TestLimitIsPerOwner(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{MaxSessions: 10})

	a := st.Client(alice)
	for i := 0; i < 10; i++ {
		a.CreateSession("devbox-1", "", 80, 24)
	}
	resp, _ := a.CreateSessionRaw("devbox-1", "", 80, 24)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	b := st.Client("other@example.com")
	for i := 0; i < 10; i++ {
		resp, body := b.CreateSessionRaw("devbox-1", "", 80, 24)
		require.Equalf(t, http.StatusCreated, resp.StatusCode,
			"a second owner must get their own quota: %s", body)
	}
}

// TestCrossOwnerReturns404 asserts 404 rather than 403 on every session route:
// 403 would confirm that another user's session exists.
func TestCrossOwnerReturns404(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})

	a := st.Client(alice)
	sess := a.CreateSession("devbox-1", "secret", 80, 24)

	b := st.Client("other@example.com")

	resp, _ := b.PatchSession(sess.ID, map[string]any{"name": "stolen"})
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "PATCH must 404, not 403")

	resp, _ = b.DeleteSession(sess.ID)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "DELETE must 404, not 403")

	resp, _ = b.Upload(sess.ID, "x.png", []byte("data"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "upload must 404, not 403")

	_, wsResp, err := b.DialWSRaw(sess.ID, nil)
	require.Error(t, err, "WS attach to another owner's session must fail")
	require.NotNil(t, wsResp)
	require.Equal(t, http.StatusNotFound, wsResp.StatusCode, "WS must 404, not 403")

	require.Equal(t, "secret", a.ListSessions()[0].Name, "the owner's session is untouched")
}

func TestListShowsOnlyOwned(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})

	a := st.Client(alice)
	b := st.Client("other@example.com")

	a.CreateSession("devbox-1", "a1", 80, 24)
	a.CreateSession("devbox-1", "a2", 80, 24)
	b.CreateSession("devbox-1", "b1", 80, 24)

	require.Len(t, a.ListSessions(), 2)
	require.Len(t, b.ListSessions(), 1)
	require.Equal(t, "b1", b.ListSessions()[0].Name)
}

// --- §6.4 naming ---

func TestNameSurvivesReconnect(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	require.Equal(t, "devbox-1", sess.Name, "the default name is the host")

	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	resp, _ := c.PatchSession(sess.ID, map[string]any{"name": "billing-api"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	ws.KillTCP()
	require.True(t, ws.WaitForClose(5*time.Second))

	ws2 := c.DialWS(sess.ID)
	ws2.WaitForEvent("rename", 5*time.Second, func(ev map[string]any) bool {
		return ev["name"] == "billing-api"
	})
	require.Equal(t, "billing-api", c.ListSessions()[0].Name,
		"the name lives server-side so it survives a new browser")
}

func TestAutoNameDoesNotOverrideManual(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)

	c.PatchSession(sess.ID, map[string]any{"name": "mine"})
	c.PatchSession(sess.ID, map[string]any{"name": "auto-title", "auto": true})

	require.Equal(t, "mine", c.ListSessions()[0].Name,
		"a shell title sequence must never clobber a user-typed name")
}

func TestAutoNameAppliesWhenUnnamed(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	c.PatchSession(sess.ID, map[string]any{"name": "projects", "auto": true})

	require.Equal(t, "projects", c.ListSessions()[0].Name)
}

func TestRenameIsSanitized(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	c.PatchSession(sess.ID, map[string]any{"name": "\x1b[2J\x1b[31mevil"})

	name := c.ListSessions()[0].Name
	require.NotContains(t, name, "\x1b", "escape sequences must not reach the tab bar")
	for _, r := range name {
		require.False(t, r < 0x20 || r == 0x7f)
	}
}

func TestRenameBroadcastsToAttachedClient(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	// A PATCH from another device must reach the attached socket.
	c.PatchSession(sess.ID, map[string]any{"name": "renamed-elsewhere"})

	ws.WaitForEvent("rename", 5*time.Second, func(ev map[string]any) bool {
		return ev["name"] == "renamed-elsewhere"
	})
}

// --- §6.1 ordering ---

func TestSessionOrderIsStable(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	for i := 0; i < 5; i++ {
		c.CreateSession("devbox-1", fmt.Sprintf("tab-%d", i), 80, 24)
	}

	list := c.ListSessions()
	require.Len(t, list, 5)
	for i, s := range list {
		require.Equal(t, i, s.Order)
		require.Equal(t, fmt.Sprintf("tab-%d", i), s.Name)
	}

	// Reordering is persisted and reflected in list order.
	c.PatchSession(list[4].ID, map[string]any{"order": 0})
	c.PatchSession(list[0].ID, map[string]any{"order": 4})

	reordered := c.ListSessions()
	require.Equal(t, "tab-4", reordered[0].Name)
	require.Equal(t, "tab-0", reordered[len(reordered)-1].Name)
}

func TestDeleteRemovesSession(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	resp, _ := c.DeleteSession(sess.ID)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Empty(t, c.ListSessions())

	require.True(t, ws.WaitForClose(5*time.Second), "the socket must close when the session is deleted")

	require.Eventually(t, func() bool { return st.Pool.Len() == 0 },
		5*time.Second, 20*time.Millisecond,
		"deleting the last session must release the pooled connection")
}

// --- §3.2 stdin serialization ---

// TestStdinWritesAreAtomic drives the shell's stdin from many goroutines at
// once, through both paths that write it: keystrokes and upload injection.
// Without the mutex the lines interleave and arrive mangled.
func TestStdinWritesAreAtomic(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	hubSess, err := st.Hub.Get(alice, sess.ID)
	require.NoError(t, err)

	const writers = 10
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			line := fmt.Sprintf("L%02d-%s\r", id, strings.Repeat(string(rune('a'+id)), 200))
			require.NoError(t, hubSess.WriteInput([]byte(line)))
		}(i)
	}

	// Concurrent uploads write to the same stdin through a different path.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c.Upload(sess.ID, fmt.Sprintf("u%d.png", id), []byte("payload"))
		}(i)
	}
	wg.Wait()

	for i := 0; i < writers; i++ {
		want := fmt.Sprintf("GOT:L%02d-%s", i, strings.Repeat(string(rune('a'+i)), 200))
		ws.WaitForOutput(want, 10*time.Second)
	}
}

// --- §3.1 single-writer invariant ---

// TestNoConcurrentWebSocketWrites pushes terminal output, control events, and
// upload events at one socket simultaneously. gorilla permits a single
// concurrent writer; if anything writes outside the write pump, frames
// interleave and the JSON text frames arrive malformed. WSClient fails the test
// on any unparseable text frame.
func TestNoConcurrentWebSocketWrites(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	hubSess, err := st.Hub.Get(alice, sess.ID)
	require.NoError(t, err)

	var wg sync.WaitGroup

	// Output producers.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				hubSess.WriteInput([]byte("storm 20\r"))
			}
		}()
	}

	// Rename events.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.PatchSession(sess.ID, map[string]any{"name": fmt.Sprintf("n%d-%d", id, j)})
			}
		}(i)
	}

	// Upload events.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.Upload(sess.ID, fmt.Sprintf("f%d-%d.png", id, j), []byte("x"))
			}
		}(i)
	}

	wg.Wait()

	// Drain briefly so late frames are still checked for well-formedness.
	time.Sleep(500 * time.Millisecond) // allow queued frames to arrive

	binFrames, txtFrames := ws.FrameCounts()
	require.Greater(t, binFrames, 0, "expected terminal output")
	require.Greater(t, txtFrames, 0, "expected control events")

	for _, ev := range ws.Events() {
		require.NotNil(t, ev["type"], "every control event must carry a type")
	}
}
