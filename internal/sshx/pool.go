package sshx

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// errPoolClosed is returned when a dial completes into a pool that was torn
// down while it was in flight.
var errPoolClosed = errors.New("connection pool closed during dial")

const (
	keepaliveInterval = 20 * time.Second

	// keepaliveTimeout bounds how long a keepalive may wait for its reply.
	//
	// ssh.Client.SendRequest takes no context and blocks until the peer answers.
	// On a black-holed connection - a paused host, a silently dropped route -
	// the peer never answers and never sends a reset, so an unbounded
	// SendRequest hangs forever and the connection is never detected as dead.
	// That is the exact failure keepalives exist to catch.
	//
	// 45s rather than 15s: this box reaches the tailnet over a USB Wi-Fi dongle
	// that drops for a few seconds and recovers several times a day, and the
	// TCP connection underneath survives that. At 15s one blip killed every
	// connection — and with it every tab — although nothing on the far side
	// had gone anywhere. OpenSSH's own default (ServerAliveCountMax 3) is as
	// patient.
	keepaliveTimeout = 45 * time.Second
)

type clientKey struct{ Host, User string }

type pooledClient struct {
	client   *ssh.Client
	refs     int
	keepStop chan struct{}

	// ready is closed once the dial that created this entry has finished, at
	// which point exactly one of client or err is set. The entry is published
	// before the dial starts, so callers that arrive mid-dial wait here instead
	// of opening a second connection to the same host.
	ready chan struct{}
	err   error
}

// Pool multiplexes tabs onto shared SSH connections.
//
// SSH multiplexes channels natively, so ten tabs across two hosts should cost
// two TCP dials and two handshakes, not ten of each. Opening a connection per
// tab is the mistake this type exists to prevent.
type Pool struct {
	mu      sync.Mutex
	dialer  Dialer
	cfg     Config
	clients map[clientKey]*pooledClient

	// OnDeath is invoked when a pooled connection dies underneath its users, so
	// the hub can mark every dependent session closed. A tab must never show a
	// live cursor on a dead connection. Set before first use.
	//
	// The dead client is passed because (host, user) does not identify a
	// connection: a replacement may already have been dialed for the same pair,
	// and its sessions are healthy. Only sessions actually using this client
	// should be closed.
	OnDeath func(host, user string, client *ssh.Client, err error)
}

func NewPool(d Dialer, cfg Config) *Pool {
	return &Pool{
		dialer:  d,
		cfg:     cfg,
		clients: make(map[clientKey]*pooledClient),
	}
}

// Acquire returns a client for (host, user), dialing only if none is pooled.
// The caller must pair every successful Acquire with exactly one Release; a
// failed Acquire needs none.
//
// The dial runs outside the lock. Holding the lock across it is simpler, but it
// stalls every other host: an unreachable peer holds a dial open for the whole
// context, and a Tailscale SSH policy in "check" mode extends the handshake
// deadline further still. Nothing anywhere could open or close a tab meanwhile.
//
// What makes dialing outside the lock safe is publishing the entry first, with
// this caller's reference already on it. A concurrent Acquire then waits on that
// entry rather than dialing a second connection, and a concurrent Release cannot
// take the count to zero underneath a connection that is about to be handed out.
func (p *Pool) Acquire(ctx context.Context, host, user string) (*ssh.Client, func(), error) {
	k := clientKey{host, user}

	p.mu.Lock()
	if pc, ok := p.clients[k]; ok {
		pc.refs++
		p.mu.Unlock()
		return p.await(ctx, k, pc)
	}

	pc := &pooledClient{refs: 1, keepStop: make(chan struct{}), ready: make(chan struct{})}
	p.clients[k] = pc
	p.mu.Unlock()

	c, err := Dial(ctx, p.dialer, host, user, p.cfg)

	p.mu.Lock()
	if err != nil {
		pc.err = err
		// Withdraw only our own entry: a later dial for the same key may
		// already have replaced it.
		if cur, ok := p.clients[k]; ok && cur == pc {
			delete(p.clients, k)
		}
		p.mu.Unlock()
		close(pc.ready)
		return nil, nil, err
	}

	// CloseAll may have run while this dial was in flight. Handing back a
	// connection the pool no longer tracks would leak it, along with the two
	// goroutines below.
	if cur, ok := p.clients[k]; !ok || cur != pc {
		p.mu.Unlock()
		pc.err = errPoolClosed
		close(pc.ready)
		c.Close()
		return nil, nil, errPoolClosed
	}

	pc.client = c
	p.mu.Unlock()
	close(pc.ready)

	go p.keepalive(pc)
	go p.watch(k, pc)

	log.Printf("pool: dialed %s@%s", user, host)
	return c, p.releaser(k, pc), nil
}

// await waits for an in-flight dial on an entry this caller already holds a
// reference to.
func (p *Pool) await(ctx context.Context, k clientKey, pc *pooledClient) (*ssh.Client, func(), error) {
	select {
	case <-pc.ready:
	case <-ctx.Done():
		// Both may be ready at once, and select picks at random. Prefer a real
		// result over a cancellation that arrived at the same moment.
		select {
		case <-pc.ready:
		default:
			p.drop(pc)
			return nil, nil, ctx.Err()
		}
	}

	if pc.err != nil {
		p.drop(pc)
		return nil, nil, pc.err
	}
	return pc.client, p.releaser(k, pc), nil
}

// releaser returns the release for one specific acquisition.
//
// Scoping it to the entry rather than to (host, user) is what makes it safe
// across a connection dying. A session that acquired connection N releases
// connection N, even if by then the pool has moved on to N+1 — releasing by key
// alone would decrement whichever connection happened to be current, and a
// session releasing late after an abrupt disconnect would take a freshly dialed
// replacement to zero references underneath its new owner.
//
// Calling it more than once counts once.
func (p *Pool) releaser(k clientKey, pc *pooledClient) func() {
	var once sync.Once
	return func() {
		once.Do(func() { p.release(k, pc) })
	}
}

// drop undoes the reference taken for an Acquire that then failed.
//
// It never closes anything. Either the dial failed, so there is no connection
// and the entry is already unmapped, or the dial is still in flight and the
// caller that started it still holds the reference that keeps the count above
// zero. A mapped entry can therefore never reach zero references here.
func (p *Pool) drop(pc *pooledClient) {
	p.mu.Lock()
	pc.refs--
	p.mu.Unlock()
}

// release drops one reference to a specific connection, closing it when the
// last tab on it goes away.
func (p *Pool) release(k clientKey, pc *pooledClient) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pc.refs--
	if pc.refs > 0 {
		return
	}

	// An entry already unmapped — by watch() after the connection died, or by
	// CloseAll — is somebody else's business now. Deleting by key here is what
	// would remove a replacement that has since taken this key.
	cur, ok := p.clients[k]
	if !ok || cur != pc {
		return
	}

	delete(p.clients, k)
	close(pc.keepStop)
	closeClient(pc)
	log.Printf("pool: closed %s@%s (no remaining sessions)", k.User, k.Host)
}

// keepalive runs once per connection, not once per tab.
//
// Closing the client on failure is what makes death observable: it wakes the
// blocked Wait() in watch(), which evicts the connection and closes every
// dependent session.
func (p *Pool) keepalive(pc *pooledClient) {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()

	for {
		select {
		case <-pc.keepStop:
			return

		case <-t.C:
			reply := make(chan error, 1)
			go func() {
				_, _, err := pc.client.SendRequest("keepalive@openssh.com", true, nil)
				reply <- err
			}()

			select {
			case <-pc.keepStop:
				return

			case err := <-reply:
				if err != nil {
					log.Printf("pool: keepalive failed, closing connection: %v", err)
					pc.client.Close()
					return
				}

			case <-time.After(keepaliveTimeout):
				// The peer is not merely quiet, it is unreachable. Closing here
				// also unblocks the pending SendRequest goroutine above.
				log.Printf("pool: keepalive timed out after %s, closing connection", keepaliveTimeout)
				pc.client.Close()
				return
			}
		}
	}
}

// watch propagates connection death to every dependent session.
func (p *Pool) watch(k clientKey, pc *pooledClient) {
	err := pc.client.Wait()

	p.mu.Lock()
	// Only evict if this exact client is still the pooled one; a newer dial for
	// the same key must not be torn down by an older connection's death.
	if cur, ok := p.clients[k]; ok && cur == pc {
		delete(p.clients, k)
		select {
		case <-pc.keepStop:
		default:
			close(pc.keepStop)
		}
	}
	p.mu.Unlock()

	if p.OnDeath != nil {
		p.OnDeath(k.Host, k.User, pc.client, err)
	}
}

// Len reports the number of live pooled connections. Used by tests to assert
// that N tabs on M hosts produce M connections.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clients)
}

// Refs reports the reference count for a connection, or 0 if not pooled.
func (p *Pool) Refs(host, user string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pc, ok := p.clients[clientKey{host, user}]; ok {
		return pc.refs
	}
	return 0
}

// CloseAll tears down every pooled connection.
func (p *Pool) CloseAll() {
	p.mu.Lock()
	clients := make([]*pooledClient, 0, len(p.clients))
	for k, pc := range p.clients {
		clients = append(clients, pc)
		delete(p.clients, k)
	}
	p.mu.Unlock()

	for _, pc := range clients {
		select {
		case <-pc.keepStop:
		default:
			close(pc.keepStop)
		}
		closeClient(pc)
	}
}

// closeClient closes a pooled connection if it has one. An entry whose dial is
// still in flight has no client yet; that dial finds its entry gone and closes
// the connection itself.
func closeClient(pc *pooledClient) {
	if pc.client != nil {
		pc.client.Close()
	}
}
