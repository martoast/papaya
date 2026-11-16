package hub

import "sync"

// DefaultRingSize is the per-session scrollback retained for replay on
// reattach.
const DefaultRingSize = 256 << 10 // 256 KB

// Ring is a fixed-size circular byte buffer holding the most recent output of a
// session. It is what makes a dropped WebSocket survivable: output keeps
// draining into the ring while no client is attached, and the whole buffer is
// replayed when one returns.
type Ring struct {
	mu   sync.Mutex
	buf  []byte
	off  int  // next write index
	full bool // buffer has wrapped at least once
}

func NewRing(size int) *Ring {
	if size <= 0 {
		size = DefaultRingSize
	}
	return &Ring{buf: make([]byte, size)}
}

// Write never fails and never blocks; the oldest bytes are overwritten.
func (r *Ring) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// A single write larger than the buffer: only its tail can survive.
	if n >= len(r.buf) {
		copy(r.buf, p[n-len(r.buf):])
		r.off = 0
		r.full = true
		return n, nil
	}

	written := copy(r.buf[r.off:], p)
	if written < n {
		copy(r.buf, p[written:])
		r.full = true
	}
	r.off = (r.off + n) % len(r.buf)
	if r.off == 0 {
		r.full = true
	}
	return n, nil
}

// Snapshot returns a copy of the buffered bytes in write order.
func (r *Ring) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		out := make([]byte, r.off)
		copy(out, r.buf[:r.off])
		return out
	}

	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.off:])
	copy(out[n:], r.buf[:r.off])
	return out
}

// Len reports how many bytes are currently buffered.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return len(r.buf)
	}
	return r.off
}
