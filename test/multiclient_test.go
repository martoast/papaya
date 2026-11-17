package test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

// Opening the same session on a second device used to evict the first, which
// reconnected, which evicted the second. Both browsers ping-ponged about once a
// second forever, and the page appeared to flash between connected and
// disconnected.
func TestASecondClientDoesNotEvictTheFirst(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "shared-view", 80, 24)

	laptop := c.DialWS(sess.ID)
	laptop.WaitForState("connected", 10*time.Second)

	phone := c.DialWS(sess.ID)
	phone.WaitForState("connected", 10*time.Second)

	// The first socket has to still be usable. Proving that means getting output
	// on it after the second attached — a closed socket cannot deliver any.
	// The fixture shell does not echo input, so a plain marker is unambiguous.
	laptop.SendLine("echo BOTH-CLIENTS-LIVE")
	laptop.WaitForOutput("BOTH-CLIENTS-LIVE", 10*time.Second)

	require.False(t, laptop.WaitForClose(200*time.Millisecond),
		"the first client must not be dropped when a second attaches")
	require.False(t, phone.WaitForClose(200*time.Millisecond))
}

// One PTY, one screen: both devices see the same thing, whichever one typed.
func TestOutputReachesEveryAttachedClient(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "mirrored", 80, 24)
	laptop := c.DialWS(sess.ID)
	laptop.WaitForState("connected", 10*time.Second)
	phone := c.DialWS(sess.ID)
	phone.WaitForState("connected", 10*time.Second)

	laptop.SendLine("echo TYPED-ON-LAPTOP")
	laptop.WaitForOutput("TYPED-ON-LAPTOP", 10*time.Second)
	phone.WaitForOutput("TYPED-ON-LAPTOP", 10*time.Second)

	// And input from the second device reaches the same shell.
	phone.SendLine("echo TYPED-ON-PHONE")
	phone.WaitForOutput("TYPED-ON-PHONE", 10*time.Second)
	laptop.WaitForOutput("TYPED-ON-PHONE", 10*time.Second)
}

// One shell, several devices: the shell takes the size of the device you are
// using. A phone that merely opens the tab shrinks nothing; typing on it gives
// it the size; touching the laptop takes it back; a hidden device never counts.
func TestGeometryFollowsTheDeviceInUse(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{})
	c := st.Client(alice)
	dims := func(cols, rows int, why string) {
		t.Helper()
		require.Eventually(t, func() bool {
			cc, rr := srv.LastDims()
			return cc == cols && rr == rows
		}, 5*time.Second, 20*time.Millisecond, why)
	}

	sess := c.CreateSession("devbox-1", "sizing", 200, 50)
	laptop := c.DialWS(sess.ID)
	laptop.WaitForState("connected", 10*time.Second)
	laptop.Resize(200, 50)
	dims(200, 50, "the only viewer gets the size it asked for")

	phone := c.DialWS(sess.ID)
	phone.WaitForState("connected", 10*time.Second)
	phone.Resize(45, 20)
	require.Never(t, func() bool { cc, _ := srv.LastDims(); return cc == 45 }, 1500*time.Millisecond, 100*time.Millisecond,
		"a phone that only opened the tab must not shrink the laptop")

	phone.Send([]byte("x"))
	dims(45, 20, "typing on the phone gives it the size")

	laptop.Send([]byte("y"))
	dims(200, 50, "typing on the laptop takes the size back")

	phone.SendJSON(map[string]any{"type": "presence", "visible": true, "active": true})
	dims(45, 20, "switching to the tab on the phone is using it")
	phone.SendJSON(map[string]any{"type": "presence", "visible": false})
	dims(200, 50, "the phone going to the background gives the laptop its size back")

	phone.Close()
	laptop.Send([]byte("z"))
	dims(200, 50, "the laptop alone keeps its size")
}

// One of two devices closing is not a disconnect; the session is only detached
// once nobody is watching.
func TestSessionStaysConnectedWhileAnyClientRemains(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "still-here", 80, 24)
	laptop := c.DialWS(sess.ID)
	laptop.WaitForState("connected", 10*time.Second)
	phone := c.DialWS(sess.ID)
	phone.WaitForState("connected", 10*time.Second)

	phone.Close()

	// Never becomes detached while the laptop is still attached.
	require.Never(t, func() bool {
		return sessionState(c, sess.ID) == "detached"
	}, 3*time.Second, 100*time.Millisecond)

	laptop.Close()
	require.Eventually(t, func() bool {
		return sessionState(c, sess.ID) == "detached"
	}, 10*time.Second, 50*time.Millisecond,
		"the last client leaving does detach the session")
}
