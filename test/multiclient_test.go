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

// The shell has one screen, so with two viewers it has to fit the smaller —
// the rule tmux uses. Letting the last resize win would have a phone and a
// laptop stretching and shrinking each other's terminal indefinitely.
func TestGeometryFitsTheSmallestViewer(t *testing.T) {
	st, srv := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "sizing", 200, 50)
	laptop := c.DialWS(sess.ID)
	laptop.WaitForState("connected", 10*time.Second)
	laptop.Resize(200, 50)

	require.Eventually(t, func() bool {
		cols, rows := srv.LastDims()
		return cols == 200 && rows == 50
	}, 5*time.Second, 20*time.Millisecond, "the only viewer should get the size it asked for")

	phone := c.DialWS(sess.ID)
	phone.WaitForState("connected", 10*time.Second)
	phone.Resize(45, 20)

	require.Eventually(t, func() bool {
		cols, rows := srv.LastDims()
		return cols == 45 && rows == 20
	}, 5*time.Second, 20*time.Millisecond,
		"with a phone attached the shell must fit the phone")

	// The laptop asking for its own size again must not win: the phone still
	// cannot display it.
	laptop.Resize(200, 50)
	require.Never(t, func() bool {
		cols, _ := srv.LastDims()
		return cols > 45
	}, 2*time.Second, 100*time.Millisecond,
		"a larger viewer must not resize the shell beyond what a smaller one can show")

	// When the phone goes away the laptop gets its full terminal back.
	phone.Close()
	require.Eventually(t, func() bool {
		cols, rows := srv.LastDims()
		return cols == 200 && rows == 50
	}, 10*time.Second, 50*time.Millisecond,
		"the last small viewer leaving must restore the remaining one's size")
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
