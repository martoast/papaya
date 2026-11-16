package hub

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRingUnderCapacity(t *testing.T) {
	r := NewRing(1024)
	r.Write([]byte("hello world"))
	require.Equal(t, "hello world", string(r.Snapshot()))
	require.Equal(t, 11, r.Len())
}

func TestRingExactlyCapacity(t *testing.T) {
	r := NewRing(16)
	in := []byte("0123456789abcdef")
	r.Write(in)
	require.Equal(t, in, r.Snapshot())
	require.Equal(t, 16, r.Len())
}

func TestRingOverCapacityKeepsTail(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("0123456789abcdef")) // 2x capacity in one write
	require.Equal(t, "89abcdef", string(r.Snapshot()),
		"only the most recent bytes survive, in order")
}

func TestRingWrapsAcrossWrites(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("abcde"))
	r.Write([]byte("fghij"))
	require.Equal(t, "cdefghij", string(r.Snapshot()))
}

// TestRingChunkingIsIrrelevant guards the wrap arithmetic: how output was split
// into writes must not change the replayed bytes.
func TestRingChunkingIsIrrelevant(t *testing.T) {
	full := []byte(fmt.Sprintf("%0*d", 500, 7))

	big := NewRing(128)
	big.Write(full)

	small := NewRing(128)
	for _, b := range full {
		small.Write([]byte{b})
	}

	require.Equal(t, big.Snapshot(), small.Snapshot(),
		"one big write and many small writes must replay identically")
}

func TestRingEmpty(t *testing.T) {
	r := NewRing(64)
	require.Empty(t, r.Snapshot())
	require.Equal(t, 0, r.Len())

	n, err := r.Write(nil)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.Empty(t, r.Snapshot())
}

func TestRingSnapshotIsACopy(t *testing.T) {
	r := NewRing(16)
	r.Write([]byte("abcd"))

	snap := r.Snapshot()
	snap[0] = 'X'

	require.Equal(t, "abcd", string(r.Snapshot()), "Snapshot must not alias the buffer")
}

// TestRingConcurrentAccess must be run under -race; it asserts no torn reads
// while output streams in.
func TestRingConcurrentAccess(t *testing.T) {
	r := NewRing(4096)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			chunk := bytes.Repeat([]byte{byte('a' + id)}, 64)
			for i := 0; i < 500; i++ {
				r.Write(chunk)
			}
		}(w)
	}
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if got := r.Len(); got > 4096 {
					t.Errorf("Len() reported %d, above capacity", got)
					return
				}
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()

	require.Equal(t, 4096, r.Len())
}

// FuzzRing asserts the defining property: a snapshot is always the last
// min(capacity, total) bytes written.
func FuzzRing(f *testing.F) {
	f.Add(64, []byte("hello"))
	f.Add(8, []byte("0123456789"))
	f.Add(1, []byte(""))
	f.Add(16, bytes.Repeat([]byte("x"), 100))

	f.Fuzz(func(t *testing.T, capacity int, data []byte) {
		if capacity <= 0 || capacity > 1<<16 {
			t.Skip()
		}

		r := NewRing(capacity)

		// Split the input into pseudo-random chunks to exercise wrapping.
		var written []byte
		for i := 0; i < len(data); {
			step := 1 + int(data[i])%17
			end := min(i+step, len(data))
			r.Write(data[i:end])
			written = append(written, data[i:end]...)
			i = end
		}

		want := written
		if len(want) > capacity {
			want = want[len(want)-capacity:]
		}

		got := r.Snapshot()
		if !bytes.Equal(got, want) {
			t.Fatalf("capacity=%d total=%d\n got=%q\nwant=%q", capacity, len(written), got, want)
		}
		if r.Len() != len(want) {
			t.Fatalf("Len()=%d want %d", r.Len(), len(want))
		}
	})
}
