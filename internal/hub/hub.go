package hub

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/martoast/papaya/internal/sshx"
)

// ErrTooManySessions is returned when an owner is at their session cap. It maps
// to HTTP 429.
var ErrTooManySessions = errors.New("session limit reached")

// ErrNotFound covers both "no such session" and "not yours". Callers must not
// distinguish the two: a different owner's session ID must look identical to a
// nonexistent one.
var ErrNotFound = errors.New("session not found")

// Options configures the hub.
type Options struct {
	MaxSessions int
	UploadDir   string
	DetachedTTL time.Duration
	RingSize    int
	// Tmux runs each session inside a named tmux session on the remote host, so
	// the work outlives papaya rather than dying with the SSH connection.
	Tmux bool
}

// Hub is the registry of live sessions.
type Hub struct {
	pool *sshx.Pool
	opts Options

	mu       sync.Mutex
	sessions map[string]*Session

	// cleanup tracks remote workspace teardowns still in flight. Closing a tab
	// must return immediately, so the teardown runs in the background; shutdown
	// and tests wait on this rather than guessing.
	cleanup sync.WaitGroup

	// unreachable remembers hosts the resumable scan could not reach, and
	// when, so the next scan within unreachableFor skips them instead of
	// waiting out another full dial timeout. See Resumable.
	unreachMu   sync.Mutex
	unreachable map[Target]time.Time

	// persistMu orders saving the tab list against shutting tabs down: a save
	// in flight finishes before CloseAll empties the registry, and none starts
	// after (closing), so the saved list is never the empty one.
	persistMu sync.Mutex
	closing   bool
}

func New(pool *sshx.Pool, opts Options) *Hub {
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = 10
	}
	if opts.RingSize <= 0 {
		opts.RingSize = DefaultRingSize
	}
	if opts.DetachedTTL <= 0 {
		opts.DetachedTTL = 30 * time.Minute
	}

	h := &Hub{
		pool:        pool,
		opts:        opts,
		sessions:    make(map[string]*Session),
		unreachable: make(map[Target]time.Time),
	}

	// A dead connection must not leave tabs looking alive.
	pool.OnDeath = h.onClientDeath
	return h
}

// Create registers a session and starts connecting in the background.
//
// The SSH handshake is not awaited: POST returns immediately with state
// "connecting" and the socket reports progress, so a slow or unreachable host
// does not block the request.
func (h *Hub) Create(owner, host, user, name string, cols, rows int) (*Session, error) {
	return h.create(owner, host, user, name, "", cols, rows)
}

// Resume opens a tab attached to an existing remote workspace, rather than one
// named after the tab. It is how work that outlived papaya gets picked back up
// without the user having to remember, and retype, the original tab name.
func (h *Hub) Resume(owner, host, user, name, workspace string, cols, rows int) (*Session, error) {
	if !sshx.ValidWorkspaceName(workspace) {
		return nil, errors.New("not a workspace name this server could have created")
	}
	return h.create(owner, host, user, name, workspace, cols, rows)
}

func (h *Hub) create(owner, host, user, name, adopt string, cols, rows int) (*Session, error) {
	if owner == "" || host == "" || user == "" {
		return nil, errors.New("owner, host and user are required")
	}

	cols, rows = sshx.ClampDims(cols, rows)

	clean := sanitizeName(name)
	if clean == "" {
		clean = host
	}

	h.mu.Lock()
	// The cap is per owner, and enforced here rather than in the UI: a scripted
	// client must get 429 just like the browser would.
	count, maxOrder := 0, -1
	for _, s := range h.sessions {
		if s.Owner == owner {
			count++
			if o := s.View().Order; o > maxOrder {
				maxOrder = o
			}
		}
	}
	if count >= h.opts.MaxSessions {
		h.mu.Unlock()
		return nil, ErrTooManySessions
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:        uuid.NewString(),
		Host:      host,
		User:      user,
		Owner:     owner,
		CreatedAt: time.Now().UTC(),
		hub:       h,
		uploadDir: h.opts.UploadDir,
		ring:      NewRing(h.opts.RingSize),
		cancel:    cancel,
		name:      clean,
		userNamed: name != "",
		order:     maxOrder + 1,
		state:     StateConnecting,
		lastSeen:  time.Now(),
		cols:      cols,
		rows:      rows,
		tmux:      h.opts.Tmux,
		adopt:     adopt,
		// Resuming already knows which workspace it is going to, so the create
		// response can carry it rather than leaving the client to wait for the
		// shell to start. A browser restoring a grid needs the workspace of every
		// panel at once, and a panel whose workspace was still empty could not be
		// matched to its saved position — the arrangement rebuilt as a default
		// tiling even though the sessions themselves came back correctly.
		//
		// tmuxAttached is deliberately NOT set here. Naming a workspace is not the
		// same as having attached to it, and closing a tab whose shell never
		// started must not destroy work it was never part of.
		tmuxName: adopt,
	}
	h.sessions[s.ID] = s
	h.mu.Unlock()

	go s.run(ctx)
	return s, nil
}

// Get returns a session only if it belongs to owner. A mismatch is reported as
// not-found so the API never confirms that another user's session exists.
func (h *Hub) Get(owner, id string) (*Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	s, ok := h.sessions[id]
	if !ok || s.Owner != owner {
		return nil, ErrNotFound
	}
	return s, nil
}

// List returns the owner's sessions in tab order.
func (h *Hub) List(owner string) []*Session {
	h.mu.Lock()
	out := make([]*Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		if s.Owner == owner {
			out = append(out, s)
		}
	}
	h.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].View(), out[j].View()
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	return out
}

// Delete closes and unregisters a session.
func (h *Hub) Delete(owner, id string) error {
	s, err := h.Get(owner, id)
	if err != nil {
		return err
	}

	h.mu.Lock()
	delete(h.sessions, id)
	h.mu.Unlock()

	// Closing a tab means the user is finished with that work, not that they are
	// stepping away, so end the remote tmux session too. Disconnects and the
	// reaper deliberately do not do this: a closed laptop must not destroy a
	// running job.
	//
	// It runs in the background because the tab has to disappear now. Reaching
	// the host can require a fresh dial, and an unreachable one costs the whole
	// timeout — which the user would otherwise spend watching a tab that will
	// not close. The teardown takes its own pool reference, so it does not
	// depend on Close below leaving the connection up.
	h.cleanup.Add(1)
	go func() {
		defer h.cleanup.Done()
		s.killRemoteWorkspace()
	}()

	s.Close("closed by user")
	return nil
}

// WaitForCleanup blocks until every background workspace teardown has finished.
// Used on shutdown, and by tests that need to observe the result.
func (h *Hub) WaitForCleanup() {
	h.cleanup.Wait()
}

// reserveWorkspace decides which remote tmux session this tab attaches to, and
// records it on the session so a concurrent Create sees it and picks elsewhere.
//
// A tab the user named keeps that name even when another tab already holds it:
// pointing two tabs at one workspace deliberately is exactly what
// reconnect-by-name is for. Tabs the user never named all key on the host
// instead, so they used to land on one shared workspace and mirror each other —
// same output in both, and a running fight over the terminal size, since tmux
// sizes a window to its smallest attached client. Those get the first free
// suffix.
func (h *Hub) reserveWorkspace(s *Session, label string, userNamed bool) string {
	base := sshx.TmuxWorkspace(label, "")

	h.mu.Lock()
	defer h.mu.Unlock()

	name := base
	if !userNamed {
		for n := 2; h.workspaceTaken(s, name); n++ {
			name = fmt.Sprintf("%s-%d", base, n)
		}
	}

	s.mu.Lock()
	s.tmuxName = name
	s.mu.Unlock()
	return name
}

// workspaceTaken reports whether a live session other than s already attaches to
// name on the same host and account. The caller holds h.mu.
//
// Closed sessions are ignored: they are unregistered lazily by the reaper, and
// treating one as an occupant would push a new tab onto a needless suffix and
// stop the last live tab from ever ending its workspace.
func (h *Hub) workspaceTaken(s *Session, name string) bool {
	for _, other := range h.sessions {
		if other == s || other.Host != s.Host || other.User != s.User {
			continue
		}
		other.mu.Lock()
		taken := other.tmuxName == name && other.state != StateClosed
		other.mu.Unlock()
		if taken {
			return true
		}
	}
	return false
}

// workspaceUser reports another live session sharing s's remote tmux workspace,
// or "" if s is the last one using it.
//
// Two tabs the user never renamed both key on the host name, so they attach to
// the same remote session. Ending it because one of them closed would kill a
// shell the other is still showing.
func (h *Hub) workspaceUser(s *Session) string {
	s.mu.Lock()
	name := s.tmuxName
	s.mu.Unlock()
	if name == "" {
		return ""
	}

	// Snapshot under the hub lock and inspect outside it. Create does take a
	// session lock while holding the hub lock, so that ordering is established
	// and safe, but there is no reason to hold both here.
	h.mu.Lock()
	others := make([]*Session, 0, len(h.sessions))
	for _, other := range h.sessions {
		if other != s && other.Host == s.Host && other.User == s.User {
			others = append(others, other)
		}
	}
	h.mu.Unlock()

	for _, other := range others {
		other.mu.Lock()
		// A closed session is not using anything. Counting one would leave the
		// workspace running with no tab left to end it.
		same := other.tmuxName == name && other.state != StateClosed
		other.mu.Unlock()
		if same {
			return other.ID
		}
	}
	return ""
}

// Count reports the number of registered sessions for an owner.
func (h *Hub) Count(owner string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.sessions {
		if s.Owner == owner {
			n++
		}
	}
	return n
}

// onClientDeath marks every session on a dead connection as closed.
func (h *Hub) onClientDeath(host, user string, client *ssh.Client, err error) {
	h.mu.Lock()
	candidates := make([]*Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		if s.Host == host && s.User == user {
			candidates = append(candidates, s)
		}
	}
	h.mu.Unlock()

	// (host, user) does not identify a connection. By the time a death is
	// reported the pool may already have dialed a replacement for the same pair,
	// and the sessions on it are healthy — closing those would tear down working
	// tabs because an older connection they never used went away.
	var victims []*Session
	for _, s := range candidates {
		// A shell inside tmux survives its connection; its own run loop puts
		// it back on the workspace, so it is not closed here.
		if s.usesClient(client) && !s.canReattach() {
			victims = append(victims, s)
		}
	}

	if len(victims) == 0 {
		return
	}
	log.Printf("pool: connection %s@%s died (%v), closing %d session(s)", user, host, err, len(victims))
	for _, s := range victims {
		s.emitEvent(map[string]any{
			"type":  "error",
			"msg":   "connection to host lost",
			"fatal": true,
		})
		s.Close("connection lost")
	}
}

// Reap closes sessions that have had no client for longer than the TTL, and
// unregisters sessions that are already closed.
func (h *Hub) Reap(now time.Time) int {
	h.mu.Lock()
	candidates := make([]*Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		candidates = append(candidates, s)
	}
	h.mu.Unlock()

	var expired []*Session
	for _, s := range candidates {
		idle, clientless := s.idleSince(now)
		if !clientless || idle <= h.opts.DetachedTTL {
			continue
		}
		expired = append(expired, s)
	}

	for _, s := range expired {
		h.mu.Lock()
		delete(h.sessions, s.ID)
		h.mu.Unlock()
		log.Printf("reaper: closing session %s (%s) after %s without a client",
			s.ID, s.Host, h.opts.DetachedTTL)
		s.Close("timed out")
	}
	return len(expired)
}

// RunReaper ticks until ctx is cancelled.
func (h *Hub) RunReaper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.Reap(now)
		}
	}
}

// CloseAll terminates every session. Used on shutdown.
func (h *Hub) CloseAll(reason string) {
	h.persistMu.Lock()
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()
	h.persistMu.Unlock()

	h.mu.Lock()
	all := make([]*Session, 0, len(h.sessions))
	for id, s := range h.sessions {
		all = append(all, s)
		delete(h.sessions, id)
	}
	h.mu.Unlock()

	for _, s := range all {
		s.Close(reason)
	}
}

// workspaceListTimeout bounds asking one host what it is running. The whole
// point is a fast answer for the browser, so an unreachable host is skipped
// rather than waited on.
const workspaceListTimeout = 8 * time.Second

// unreachableFor is how long a host that failed a resumable scan is skipped by
// later scans.
//
// The scan answers only when its slowest host does, and the page asks for it on
// every load and every return from the background. One machine asleep for a
// couple of days therefore cost a full workspaceListTimeout on
// every reopen of the app, and while that request was open the page's resync
// was locked, so a second reopen inside those seconds did nothing at all. A host
// that has just failed is very unlikely to answer a few seconds later; after
// this long it is asked again.
const unreachableFor = 60 * time.Second

func (h *Hub) recentlyUnreachable(t Target, now time.Time) bool {
	h.unreachMu.Lock()
	defer h.unreachMu.Unlock()
	at, ok := h.unreachable[t]
	if !ok {
		return false
	}
	if now.Sub(at) >= unreachableFor {
		delete(h.unreachable, t)
		return false
	}
	return true
}

func (h *Hub) markReachable(t Target, ok bool) {
	h.unreachMu.Lock()
	defer h.unreachMu.Unlock()
	if ok {
		delete(h.unreachable, t)
	} else {
		h.unreachable[t] = time.Now()
	}
}

// Resumable is a remote workspace with no tab attached to it.
type Resumable struct {
	sshx.Workspace
	Host string `json:"host"`
	User string `json:"user"`
}

// Target is a host to scan and the account to scan it as.
type Target struct {
	Host string
	User string
}

// Resumable lists workspaces still running on the given hosts that no live
// session of this owner is attached to.
//
// This is what closes the loop on persistence. Work survives papaya restarting,
// but nothing in papaya remembers it, so until now the only way back in was to
// recreate a tab with exactly the original name — which meant knowing the naming
// scheme, including the "-2" a second unnamed tab silently got.
//
// Hosts are queried concurrently and failures are dropped: a host that is down
// has nothing to resume, and reporting that as an error would make one dead
// machine hide every other machine's work.
//
// Each target carries its own account, because one account across every host
// only holds while the hosts are alike. A Mac running its own sshd and a Linux
// box behind Tailscale SSH generally do not share a username, and scanning the
// Mac as the Linux account fails in the quietest possible way: the host simply
// never appears in the list, looking like it has no work rather than like a
// login that could not succeed.
func (h *Hub) Resumable(ctx context.Context, owner string, targets []Target) []Resumable {
	taken := h.attachedWorkspaces(owner)

	type reply struct {
		target Target
		ws     []sshx.Workspace
	}
	replies := make(chan reply, len(targets))

	var wg sync.WaitGroup
	now := time.Now()
	for _, t := range targets {
		if h.recentlyUnreachable(t, now) {
			continue
		}
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(ctx, workspaceListTimeout)
			defer cancel()

			client, release, err := h.pool.Acquire(ctx, t.Host, t.User)
			if err != nil {
				log.Printf("resumable: cannot reach %s@%s: %v", t.User, t.Host, err)
				// Only a dial that timed out on its own counts. A browser
				// giving up on the request cancels ctx too, and that says
				// nothing about the host.
				if ctx.Err() == context.DeadlineExceeded {
					h.markReachable(t, false)
				}
				return
			}
			defer release()
			h.markReachable(t, true)

			ws, err := sshx.ListWorkspaces(ctx, client)
			if err != nil {
				log.Printf("resumable: listing %s@%s: %v", t.User, t.Host, err)
				return
			}
			replies <- reply{t, ws}
		}(t)
	}
	wg.Wait()
	close(replies)

	out := []Resumable{}
	for r := range replies {
		for _, w := range r.ws {
			if taken[clientKey{r.target.Host, r.target.User, w.Name}] {
				continue
			}
			out = append(out, Resumable{Workspace: w, Host: r.target.Host, User: r.target.User})
		}
	}

	// Newest first: the thing you were just working on is the thing you want
	// back.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// clientKey identifies one workspace on one account of one host.
type clientKey struct{ host, user, workspace string }

// attachedWorkspaces is the set an owner's live sessions already occupy, so
// resuming never offers a tab the user is already looking at.
func (h *Hub) attachedWorkspaces(owner string) map[clientKey]bool {
	h.mu.Lock()
	sessions := make([]*Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		if s.Owner == owner {
			sessions = append(sessions, s)
		}
	}
	h.mu.Unlock()

	out := map[clientKey]bool{}
	for _, s := range sessions {
		s.mu.Lock()
		name, live := s.tmuxName, s.state != StateClosed
		s.mu.Unlock()
		if name != "" && live {
			out[clientKey{s.Host, s.User, name}] = true
		}
	}
	return out
}
