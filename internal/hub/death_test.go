package hub

import (
	"testing"

	"golang.org/x/crypto/ssh"
)

// A connection dying must close the sessions on that connection and nothing
// else.
//
// (host, user) does not identify a connection. After an abrupt disconnect the
// pool dials a replacement for the same pair, so by the time the death is
// reported there can be healthy sessions on the new connection. Closing by key
// alone tears those down — the user watches a working tab die because a
// connection it never used went away.
func TestClientDeathSparesSessionsOnAReplacement(t *testing.T) {
	h := &Hub{sessions: map[string]*Session{}, opts: Options{RingSize: 16}}

	// Two clients for the same (host, user): one dying, one healthy. Only
	// identity matters here, so distinct non-nil pointers are enough.
	dead := &ssh.Client{}
	live := &ssh.Client{}

	onDead := newTestSession(h, "on-dead", dead)
	onLive := newTestSession(h, "on-live", live)
	noConn := newTestSession(h, "connecting", nil)

	h.onClientDeath("devbox-1", "tester", dead, nil)

	if got := onDead.State(); got != StateClosed {
		t.Errorf("session on the dead connection: state %q, want %q", got, StateClosed)
	}
	if got := onLive.State(); got == StateClosed {
		t.Errorf("session on the replacement connection was closed; "+
			"it is on %p, which is alive, not on the connection that died", live)
	}
	if got := noConn.State(); got == StateClosed {
		t.Error("a session that has no connection yet was closed by another connection's death")
	}
}

func newTestSession(h *Hub, id string, client *ssh.Client) *Session {
	s := &Session{
		ID:        id,
		Host:      "devbox-1",
		User:      "tester",
		Owner:     "alice",
		hub:       h,
		ring:      NewRing(h.opts.RingSize),
		state:     StateConnected,
		sshClient: client,
	}
	h.sessions[id] = s
	return s
}
