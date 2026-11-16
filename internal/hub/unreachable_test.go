package hub

import (
	"testing"
	"time"
)

// A host that just failed the resumable scan is skipped by the next one, and
// asked again once unreachableFor has passed or it answers.
func TestUnreachableHostsAreSkippedForAWhile(t *testing.T) {
	h := &Hub{unreachable: map[Target]time.Time{}}
	t1 := Target{Host: "workstation-2", User: "alice"}
	other := Target{Host: "workstation", User: "alice"}
	now := time.Now()

	if h.recentlyUnreachable(t1, now) {
		t.Fatal("unknown host treated as unreachable")
	}
	h.markReachable(t1, false)
	if !h.recentlyUnreachable(t1, time.Now()) {
		t.Fatal("host that just failed was not skipped")
	}
	if h.recentlyUnreachable(other, time.Now()) {
		t.Fatal("one host failing skipped another")
	}
	if h.recentlyUnreachable(t1, time.Now().Add(unreachableFor+time.Second)) {
		t.Fatal("host still skipped after unreachableFor")
	}

	h.markReachable(t1, false)
	h.markReachable(t1, true)
	if h.recentlyUnreachable(t1, time.Now()) {
		t.Fatal("host that answered is still skipped")
	}
}
