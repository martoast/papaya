package sshx_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/test/harness"
)

func newTestPool(t *testing.T, hosts map[string]string, kh string) (*sshx.Pool, *harness.CountingDialer) {
	t.Helper()
	d := harness.NewCountingDialer(hosts)
	p := sshx.NewPool(d, sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  kh,
	})
	t.Cleanup(p.CloseAll)
	return p, d
}

// TestPoolReusesOneConnection is the headline invariant: SSH multiplexes
// channels, so N tabs on one host must cost one dial.
func TestPoolReusesOneConnection(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	p, d := newTestPool(t, map[string]string{"devbox-1": srv.Addr}, srv.KnownHostsFile(t, "devbox-1"))

	ctx := testCtx(t)
	for i := 0; i < 10; i++ {
		_, _, err := p.Acquire(ctx, "devbox-1", "tester")
		require.NoError(t, err)
	}

	require.Equal(t, int32(1), d.Dials.Load(), "10 tabs on one host must dial once")
	require.Equal(t, 1, p.Len())
	require.Equal(t, 10, p.Refs("devbox-1", "tester"))
}

// TestTenTabsTwoHostsTwoDials is §6.1: the connection math from the spec.
func TestTenTabsTwoHostsTwoDials(t *testing.T) {
	a := harness.NewSSHServer(t, harness.AuthNone)
	b := harness.NewSSHServer(t, harness.AuthNone)

	kh := writeKnownHosts(t, map[string]*harness.SSHServer{"host-a": a, "host-b": b})
	p, d := newTestPool(t, map[string]string{"host-a": a.Addr, "host-b": b.Addr}, kh)

	ctx := testCtx(t)
	for i := 0; i < 5; i++ {
		_, _, err := p.Acquire(ctx, "host-a", "tester")
		require.NoError(t, err)
	}
	for i := 0; i < 5; i++ {
		_, _, err := p.Acquire(ctx, "host-b", "tester")
		require.NoError(t, err)
	}

	require.Equal(t, int32(2), d.Dials.Load(), "10 tabs across 2 hosts must dial twice, not ten times")
	require.Equal(t, int32(1), a.Conns.Load())
	require.Equal(t, int32(1), b.Conns.Load())
}

// TestPoolSeparatesByUser proves the key includes the user, not just the host.
func TestPoolSeparatesByUser(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	p, d := newTestPool(t, map[string]string{"devbox-1": srv.Addr}, srv.KnownHostsFile(t, "devbox-1"))

	ctx := testCtx(t)
	_, _, err := p.Acquire(ctx, "devbox-1", "alice")
	require.NoError(t, err)
	_, _, err = p.Acquire(ctx, "devbox-1", "bob")
	require.NoError(t, err)

	require.Equal(t, int32(2), d.Dials.Load())
	require.Equal(t, 2, p.Len())
}

func TestPoolReleaseClosesLastReference(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	p, _ := newTestPool(t, map[string]string{"devbox-1": srv.Addr}, srv.KnownHostsFile(t, "devbox-1"))

	ctx := testCtx(t)
	var releases []func()
	for i := 0; i < 3; i++ {
		_, release, err := p.Acquire(ctx, "devbox-1", "tester")
		require.NoError(t, err)
		releases = append(releases, release)
	}

	releases[0]()
	releases[1]()
	require.Equal(t, 1, p.Len(), "connection stays open while a tab still uses it")

	releases[2]()
	require.Equal(t, 0, p.Len(), "last tab closing must close the connection")
}

// TestPoolConcurrentAcquireRelease hammers the refcount from many goroutines.
//
// A baseline reference is held for the duration so the expected dial count is
// deterministic: without it, a moment where every worker happens to be between
// Release and Acquire would legitimately close and redial the connection.
func TestPoolConcurrentAcquireRelease(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	p, d := newTestPool(t, map[string]string{"devbox-1": srv.Addr}, srv.KnownHostsFile(t, "devbox-1"))

	ctx := testCtx(t)
	_, releaseBaseline, err := p.Acquire(ctx, "devbox-1", "tester") // baseline reference
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c, release, err := p.Acquire(ctx, "devbox-1", "tester")
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				// Using the client proves the pool never hands out a closed one.
				if sess, err := c.NewSession(); err == nil {
					sess.Close()
				}
				release()
			}
		}()
	}
	wg.Wait()

	require.Equal(t, int32(1), d.Dials.Load(), "the connection must never have been redialed")
	require.Equal(t, 1, p.Refs("devbox-1", "tester"), "only the baseline reference should remain")

	releaseBaseline()
	require.Equal(t, 0, p.Len(), "pool must be empty once every reference is dropped")
}

// TestPoolAcquireDuringRelease targets the documented teardown race: a tab
// closing while another opens on the same host can drop refs to zero mid-dial.
// The assertion is that a client handed out is always usable.
func TestPoolAcquireDuringRelease(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	p, _ := newTestPool(t, map[string]string{"devbox-1": srv.Addr}, srv.KnownHostsFile(t, "devbox-1"))

	ctx := testCtx(t)
	for i := 0; i < 300; i++ {
		_, releaseFirst, err := p.Acquire(ctx, "devbox-1", "tester")
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(2)

		// Racer A drops the last reference.
		go func() {
			defer wg.Done()
			releaseFirst()
		}()

		// Racer B acquires at the same instant.
		var acquired atomic.Bool
		var releaseSecond func()
		go func() {
			defer wg.Done()
			c, release, err := p.Acquire(ctx, "devbox-1", "tester")
			if err != nil {
				t.Errorf("iteration %d: acquire during release: %v", i, err)
				return
			}
			sess, err := c.NewSession()
			if err != nil {
				t.Errorf("iteration %d: pool handed out an unusable client: %v", i, err)
				return
			}
			sess.Close()
			releaseSecond = release
			acquired.Store(true)
		}()

		wg.Wait()
		if acquired.Load() {
			releaseSecond()
		}
		if t.Failed() {
			t.Fatalf("aborting at iteration %d", i)
		}
	}

	require.Equal(t, 0, p.Len())
}

// TestPoolDialFailureDoesNotPoison ensures a failed dial leaves no entry
// behind: the next attempt must retry rather than reuse a broken record.
func TestPoolDialFailureDoesNotPoison(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	d := harness.NewCountingDialer(map[string]string{"devbox-1": srv.Addr})

	var failNext atomic.Bool
	failNext.Store(true)
	d.Fail = func(addr string) error {
		if failNext.Swap(false) {
			return errors.New("simulated network failure")
		}
		return nil
	}

	p := sshx.NewPool(d, sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  srv.KnownHostsFile(t, "devbox-1"),
	})
	t.Cleanup(p.CloseAll)

	ctx := testCtx(t)
	_, _, err := p.Acquire(ctx, "devbox-1", "tester")
	require.Error(t, err, "first dial is forced to fail")
	require.Equal(t, 0, p.Len(), "a failed dial must not leave an entry in the pool")

	c, _, err := p.Acquire(ctx, "devbox-1", "tester")
	require.NoError(t, err, "the pool must retry after a failure")
	require.NotNil(t, c)
	require.Equal(t, 1, p.Len())
}

// TestPoolOnDeathFires covers failure propagation: when a connection dies, every
// dependent session must be told, so no tab shows a live cursor on a dead link.
func TestPoolOnDeathFires(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)
	p, _ := newTestPool(t, map[string]string{"devbox-1": srv.Addr}, srv.KnownHostsFile(t, "devbox-1"))

	deaths := make(chan string, 4)
	p.OnDeath = func(host, user string, _ *ssh.Client, err error) { deaths <- host + "/" + user }

	ctx := testCtx(t)
	_, _, err := p.Acquire(ctx, "devbox-1", "tester")
	require.NoError(t, err)

	srv.DropAllConns()

	select {
	case got := <-deaths:
		require.Equal(t, "devbox-1/tester", got)
	case <-time.After(10 * time.Second):
		t.Fatal("OnDeath never fired after the connection dropped")
	}

	require.Eventually(t, func() bool { return p.Len() == 0 },
		5*time.Second, 20*time.Millisecond,
		"a dead connection must be evicted from the pool")
}

// writeKnownHosts merges per-server known_hosts entries into one file so a
// single pool can verify several hosts.
func writeKnownHosts(t *testing.T, servers map[string]*harness.SSHServer) string {
	t.Helper()
	var merged []byte
	for name, srv := range servers {
		b, err := os.ReadFile(srv.KnownHostsFile(t, name))
		require.NoError(t, err)
		merged = append(merged, b...)
	}

	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, merged, 0o600))
	return path
}

// blockingDialer lets a test hold a dial open for a chosen address.
type blockingDialer struct {
	inner  *harness.CountingDialer
	before func(addr string)
}

func (d *blockingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.before(addr)
	return d.inner.DialContext(ctx, network, addr)
}

// newBlockingPool builds a pool whose dials the test can hold open.
func newBlockingPool(t *testing.T, hosts map[string]string, kh string,
	before func(addr string)) (*sshx.Pool, *harness.CountingDialer) {
	t.Helper()

	inner := harness.NewCountingDialer(hosts)
	p := sshx.NewPool(&blockingDialer{inner: inner, before: before},
		sshx.Config{UseTailscaleSSH: true, KnownHostsPath: kh})
	t.Cleanup(p.CloseAll)
	return p, inner
}

// A dial that hangs must not stop other hosts from being reached.
//
// The dial used to run with the pool lock held, so one unreachable host froze
// every Acquire and Release in the process. Closing a tab reaches this path too,
// which is what turned a single dead host into a system-wide stall.
func TestPoolSlowDialDoesNotBlockOtherHosts(t *testing.T) {
	a := harness.NewSSHServer(t, harness.AuthNone)
	b := harness.NewSSHServer(t, harness.AuthNone)

	kh := writeKnownHosts(t, map[string]*harness.SSHServer{"stuck": a, "healthy": b})

	dialing := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	p, _ := newBlockingPool(t, map[string]string{"stuck": a.Addr, "healthy": b.Addr}, kh,
		func(addr string) {
			if addr == "stuck:22" {
				once.Do(func() { close(dialing) })
				<-release
			}
		})

	ctx := testCtx(t)
	stuck := make(chan error, 1)
	go func() {
		_, _, err := p.Acquire(ctx, "stuck", "tester")
		stuck <- err
	}()

	<-dialing // the wedged dial is now in flight

	done := make(chan error, 1)
	var releaseHealthy func()
	go func() {
		_, release, err := p.Acquire(ctx, "healthy", "tester")
		releaseHealthy = release
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("a healthy host was unreachable while another host's dial was in flight")
	}

	// Releasing takes the same lock, so it has to stay responsive too.
	releaseHealthy()

	close(release)
	require.NoError(t, <-stuck)
}

// Callers arriving during an in-flight dial must wait for it rather than open a
// second connection to the same host.
func TestPoolConcurrentAcquireDialsOnce(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)

	dialing := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	p, d := newBlockingPool(t, map[string]string{"devbox-1": srv.Addr},
		srv.KnownHostsFile(t, "devbox-1"), func(string) {
			once.Do(func() {
				close(dialing)
				<-release
			})
		})

	ctx := testCtx(t)
	first := make(chan error, 1)
	go func() {
		_, _, err := p.Acquire(ctx, "devbox-1", "tester")
		first <- err
	}()
	<-dialing

	const waiters = 8
	var wg sync.WaitGroup
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := p.Acquire(ctx, "devbox-1", "tester")
			errs <- err
		}()
	}

	close(release)
	require.NoError(t, <-first)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	require.Equal(t, int32(1), d.Dials.Load(),
		"nine callers racing on a cold pool must still produce one connection")
	require.Equal(t, 1, p.Len())
	require.Equal(t, waiters+1, p.Refs("devbox-1", "tester"),
		"every successful Acquire must hold exactly one reference")
}

// A caller that gives up while waiting on someone else's dial must not leave a
// reference behind, or the connection could never be closed.
func TestPoolAbandonedWaiterLeavesNoReference(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)

	dialing := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	p, _ := newBlockingPool(t, map[string]string{"devbox-1": srv.Addr},
		srv.KnownHostsFile(t, "devbox-1"), func(string) {
			once.Do(func() {
				close(dialing)
				<-release
			})
		})

	ctx := testCtx(t)
	first := make(chan error, 1)
	var releaseFirst func()
	go func() {
		_, release, err := p.Acquire(ctx, "devbox-1", "tester")
		releaseFirst = release
		first <- err
	}()
	<-dialing

	giveUp, cancel := context.WithCancel(context.Background())
	abandoned := make(chan error, 1)
	go func() {
		_, _, err := p.Acquire(giveUp, "devbox-1", "tester")
		abandoned <- err
	}()

	cancel()
	require.Error(t, <-abandoned)

	close(release)
	require.NoError(t, <-first)

	require.Equal(t, 1, p.Refs("devbox-1", "tester"),
		"the caller that gave up must not still be counted")

	releaseFirst()
	require.Equal(t, 0, p.Len(), "the last real reference must close the connection")
}

// A release belongs to the connection it was taken on, not to the (host, user)
// pair. Those are not the same thing the moment a connection dies and is
// replaced.
//
// The sequence below is what session churn produces: a session's connection
// dies, watch() unmaps it, a new session starts dialing a replacement, and only
// then does the dead session get around to releasing. Keyed on host and user
// alone, that release lands on the replacement and takes it to zero references
// before its dial has even finished.
func TestPoolStaleReleaseSparesTheReplacement(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthNone)

	dialing := make(chan struct{})
	proceed := make(chan struct{})
	var arm atomic.Bool

	p, _ := newBlockingPool(t, map[string]string{"devbox-1": srv.Addr},
		srv.KnownHostsFile(t, "devbox-1"), func(string) {
			if arm.Load() {
				arm.Store(false)
				close(dialing)
				<-proceed
			}
		})

	ctx := testCtx(t)

	// A session is live on the first connection.
	_, releaseFirst, err := p.Acquire(ctx, "devbox-1", "tester")
	require.NoError(t, err)

	// That connection dies. watch() unmaps it, but the session has not yet
	// noticed and so has not released.
	srv.DropAllConns()
	require.Eventually(t, func() bool { return p.Len() == 0 },
		10*time.Second, 20*time.Millisecond, "the dead connection must be evicted")

	// A new session dials a replacement.
	arm.Store(true)
	second := make(chan error, 1)
	go func() {
		_, _, err := p.Acquire(ctx, "devbox-1", "tester")
		second <- err
	}()
	<-dialing

	// Now the dead session finally releases. This must not touch the
	// replacement, whose dial is still in flight.
	releaseFirst()

	close(proceed)
	require.NoError(t, <-second,
		"a release from a dead connection must not cancel the replacement's dial")
	require.Equal(t, 1, p.Len(), "the replacement must still be pooled")
	require.Equal(t, 1, p.Refs("devbox-1", "tester"),
		"only the new session's reference may be counted")
}
