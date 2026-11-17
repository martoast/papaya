package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"github.com/martoast/papaya/internal/sshx"
)

// State is the lifecycle state of a session, mirrored to the browser as a
// status event and rendered as the tab's colored dot.
type State string

const (
	StateConnecting State = "connecting"
	StateConnected  State = "connected"
	StateDetached   State = "detached"
	StateClosed     State = "closed"
)

var (
	// ErrNotConnected is returned when an operation needs a live shell.
	ErrNotConnected = errors.New("session is not connected")
)

// Session is one tab: a PTY on a pooled SSH connection, plus the scrollback
// needed to survive a client going away.
//
// Name lives here rather than in the browser so tab labels survive reconnecting
// from a different browser or machine.
type Session struct {
	ID        string
	Host      string
	User      string
	Owner     string
	CreatedAt time.Time

	hub       *Hub
	uploadDir string
	tmux      bool
	ring      *Ring

	cancel context.CancelFunc
	// releasePoolRef drops this session's pool reference. It is scoped to the
	// connection actually acquired, so a session that dies and releases late
	// cannot decrement a replacement dialed for the same host in the meantime.
	// Nil until the connection is up; safe to call more than once.
	releasePoolRef func()

	mu        sync.Mutex
	name      string
	userNamed bool
	// theme is the tab's own theme name; empty follows the owner's theme.
	theme string

	// outBuckets are the recent 400 ms slots in which the program printed
	// (unix ms / 400), not counting the echo of typing; lastOut and lastIn are
	// the last output and the last keystroke. Sent to a browser as it attaches,
	// so a tab whose agent is working shows it from the first frame instead of
	// after a few seconds of fresh output.
	outBuckets []int64
	lastOut    time.Time
	lastIn     time.Time
	// adopt, when set, is an existing remote workspace this tab is resuming
	// rather than one to be reserved. It is validated before it reaches here.
	adopt string
	// tmuxAttached records that the shell really started inside tmuxName, as
	// opposed to the name merely being reserved. Only an attached session may
	// end its workspace.
	tmuxAttached bool
	// tmuxName is the remote tmux session this one actually attached to, fixed
	// at the moment the shell started. Renaming a tab afterwards changes
	// workspaceName() but not the session already running on the far side, so
	// only the recorded name is safe to hand to kill-session.
	tmuxName  string
	order     int
	state     State
	lastSeen  time.Time
	cols      int
	rows      int
	attention bool
	lastBell  time.Time

	// agentState and agentMsg are what the program in this tab last said about
	// itself through papaya-status (working, done, needs-you, idle). Replayed
	// to every browser that attaches.
	agentState string
	agentMsg   string
	// clients are every socket watching this session. A phone opening the same
	// tab as a laptop is a second viewer, not a replacement: evicting the first
	// made both browsers reconnect and evict each other about once a second,
	// forever.
	clients map[*wsConn]clientView
	// sizedOnce is set once a viewer's geometry has been applied.
	sizedOnce bool
	pty       *sshx.PTY
	sshClient *ssh.Client
}

// clientView is one viewer's terminal geometry and presence.
//
// The shell has a single screen, so with several viewers one size must be
// chosen. It is the size of the device you are using: the viewer that most
// recently typed, tapped, focused its window or switched to this tab (tmux's
// "window-size latest"). A device that is hidden (the app in the background,
// the phone locked) does not count at all, and when nobody has used the tab
// yet the biggest visible screen wins. So a phone open on the desk no longer
// shrinks the laptop you are typing on, and picking the phone up and typing
// gives it the size until you touch the laptop again.
//
// Reconnecting is not "using": a phone that wakes up reattaches every tab, and
// that alone must not shrink them all.
type clientView struct {
	cols, rows int
	active     time.Time // last real use from this viewer
	hidden     bool      // its page is not visible
}

// View is the JSON representation returned by the REST API.
type View struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	User      string    `json:"user"`
	State     State     `json:"state"`
	Order     int       `json:"order"`
	CreatedAt time.Time `json:"createdAt"`
	// Attention is set when the session rang the terminal bell and nobody has
	// looked since, so a reload still shows which panel was asking.
	Attention bool `json:"attention"`
	// Workspace is the remote tmux session this tab is attached to, empty until
	// the shell has actually started or when tmux is off.
	//
	// It is the only durable handle on the work: session ids live in memory and
	// die with papaya, while the workspace outlives it on the remote host. A
	// browser that remembers this can put a tab back on the same running shell
	// after a restart, which an id could never do.
	Workspace string `json:"workspace"`
	// Theme is the tab's own theme; empty means it follows the owner's.
	Theme string `json:"theme,omitempty"`
}

func (s *Session) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	return View{
		ID:        s.ID,
		Name:      s.name,
		Host:      s.Host,
		User:      s.User,
		State:     s.state,
		Order:     s.order,
		CreatedAt: s.CreatedAt,
		Attention: s.attention,
		Workspace: s.tmuxName,
		Theme:     s.theme,
	}
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Session) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// run owns the whole SSH lifecycle for this session.
func (s *Session) run(ctx context.Context) {
	client, release, err := s.hub.pool.Acquire(ctx, s.Host, s.User)
	if err != nil {
		s.fail("connect", err)
		return
	}

	s.mu.Lock()
	s.sshClient = client
	s.releasePoolRef = release
	cols, rows := s.cols, s.rows
	s.mu.Unlock()

	// The workspace is named after the tab label, so reconnecting with the same
	// name reattaches the same remote tmux session and its running work. The
	// name is reserved before the shell starts, so a tab opening at the same
	// moment sees it and does not land on the same workspace by accident.
	label, userNamed := s.workspaceLabel()
	workspace := ""
	switch {
	case !s.tmux:
	case s.adopt != "":
		// Resuming names the workspace outright, so there is nothing to choose
		// and nothing to avoid colliding with: the collision is the point.
		workspace = s.adopt
		s.mu.Lock()
		s.tmuxName = workspace
		s.mu.Unlock()
	default:
		workspace = s.hub.reserveWorkspace(s, label, userNamed)
	}

	pty, err := sshx.OpenPTY(client, cols, rows, sessionSink{s},
		sshx.ShellCommandFor(s.tmux, workspace))
	if err != nil {
		s.releasePool()
		s.fail("start shell", err)
		return
	}

	s.mu.Lock()
	s.pty = pty
	// Reserving a name is not the same as owning the workspace. A workspace of
	// this name may well already exist from an earlier run of papaya; only now
	// that the shell is actually running has this session attached to it, and
	// closing a tab whose shell never started must not destroy days of work it
	// was never part of.
	s.tmuxAttached = s.tmux
	hasClient := len(s.clients) > 0
	s.mu.Unlock()

	if hasClient {
		s.setState(StateConnected, "")
	} else {
		s.setState(StateDetached, "")
	}

	for {
		waitErr := pty.Wait()
		// Ask before releasing: dropping the last reference closes a healthy
		// connection, and a closed one would then look lost.
		lost := s.State() != StateClosed && !connectionAlive(client)
		s.releasePool()

		if !lost || !s.canReattach() {
			msg := "shell exited"
			if waitErr != nil {
				msg = "shell exited: " + waitErr.Error()
			}
			s.close(msg)
			return
		}

		// The connection died under a shell that lives in tmux on the far
		// side, so the work is still running there. Losing the tab here is what
		// made a Wi-Fi blip on this box read as "the Mac keeps dropping me" —
		// and closing the dead tab then ran kill-session on the very workspace
		// the blip had left intact. Put the tab back on it instead.
		var ok bool
		client, pty, ok = s.reattach(ctx)
		if !ok {
			s.close("connection lost")
			return
		}
	}
}

// canReattach reports whether this session's work outlives its connection:
// only a shell that really started inside a named tmux workspace does.
func (s *Session) canReattach() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tmux && s.tmuxAttached && s.tmuxName != ""
}

// reattachMaxBackoff caps the wait between attempts to reach a host again. A
// tab can wait as long as it is open; the user closing it is what ends this.
const reattachMaxBackoff = 30 * time.Second

// reattach dials the host again and attaches to the same tmux workspace,
// retrying until it works or the session is closed.
func (s *Session) reattach(ctx context.Context) (*ssh.Client, *sshx.PTY, bool) {
	s.mu.Lock()
	name := s.tmuxName
	s.mu.Unlock()

	log.Printf("session %s: connection to %s@%s lost, reattaching %s", s.ID, s.User, s.Host, name)
	s.setState(StateConnecting, "connection lost — reconnecting")

	backoff := time.Second
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil || s.State() == StateClosed {
			return nil, nil, false
		}

		client, release, err := s.hub.pool.Acquire(ctx, s.Host, s.User)
		if err == nil {
			s.mu.Lock()
			s.sshClient = client
			s.releasePoolRef = release
			cols, rows := s.cols, s.rows
			s.mu.Unlock()

			pty, perr := sshx.OpenPTY(client, cols, rows, sessionSink{s}, sshx.ShellCommandFor(true, name))
			if perr == nil {
				s.mu.Lock()
				if s.state == StateClosed {
					s.mu.Unlock()
					pty.Close()
					release()
					return nil, nil, false
				}
				s.pty = pty
				hasClient := len(s.clients) > 0
				s.mu.Unlock()

				log.Printf("session %s: reattached %s on %s after %d attempt(s)", s.ID, name, s.Host, attempt)
				if hasClient {
					s.setState(StateConnected, "")
				} else {
					s.setState(StateDetached, "")
				}
				return client, pty, true
			}
			release()
			err = perr
		}

		if attempt == 1 || attempt%10 == 0 {
			log.Printf("session %s: reattach attempt %d to %s@%s failed: %v", s.ID, attempt, s.User, s.Host, err)
		}

		select {
		case <-ctx.Done():
			return nil, nil, false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, reattachMaxBackoff)
	}
}

// connectionAlive reports whether an SSH connection still answers. A shell
// that exited on its own leaves its connection healthy; one that died with
// its connection leaves a closed client, which fails at once.
func connectionAlive(c *ssh.Client) bool {
	if c == nil {
		return false
	}
	reply := make(chan error, 1)
	go func() {
		_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
		reply <- err
	}()
	select {
	case err := <-reply:
		return err == nil
	case <-time.After(5 * time.Second):
		return false
	}
}

// workspaceLabel is the label a tmux session is keyed on — the tab name if the
// user chose one, otherwise the host — and whether the user chose it.
//
// The distinction matters: a name the user typed is a deliberate request to
// reattach that workspace, while the host fallback is just a default two tabs
// can arrive at without meaning to share anything.
func (s *Session) workspaceLabel() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userNamed && s.name != "" {
		return s.name, true
	}
	return s.Host, false
}

// fail marks the session dead with a client-safe message.
func (s *Session) fail(stage string, err error) {
	log.Printf("session %s: %s failed: %v", s.ID, stage, err)
	s.emitEvent(map[string]any{
		"type":  "error",
		"msg":   sanitizeError(stage, err),
		"fatal": true,
	})
	s.close(stage + " failed")
}

func (s *Session) releasePool() {
	s.mu.Lock()
	release := s.releasePoolRef
	s.mu.Unlock()
	if release != nil {
		release()
	}
}

// workspaceKillTimeout bounds the cleanup of a remote tmux session. Closing a
// tab must feel instant, so an unreachable host is given a few seconds and then
// abandoned rather than blocking the delete.
const workspaceKillTimeout = 10 * time.Second

// killRemoteWorkspace ends the tmux session this tab attached to.
//
// Only an explicit close by the user reaches here. Every other way a session
// ends — a dropped websocket, the detached reaper, papaya restarting — leaves
// the remote tmux session running on purpose, because that is what makes work
// resumable. The cost of that design is that nothing ever cleaned up after a
// tab the user was actually finished with, so every tab ever opened left a
// shell running on the target host forever.
func (s *Session) killRemoteWorkspace() {
	s.mu.Lock()
	name := s.tmuxName
	attached := s.tmuxAttached
	s.mu.Unlock()

	if !s.tmux || name == "" || !attached {
		// Either tmux is off, so the shell dies with the channel and there is
		// nothing to clean up, or this session only ever reserved the name and
		// never got a shell running inside it — in which case the workspace, if
		// one exists under that name at all, belongs to somebody else.
		return
	}

	// Another tab may be attached to the same remote workspace: two tabs the
	// user never renamed both key on the host name. Ending it here would kill a
	// shell somebody is still looking at.
	if other := s.hub.workspaceUser(s); other != "" {
		log.Printf("session %s: leaving tmux session %s on %s alone, session %s is still using it",
			s.ID, name, s.Host, other)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), workspaceKillTimeout)
	defer cancel()

	// Take a reference of this call's own rather than reusing s.sshClient. The
	// PTY goroutine may release the last reference at any moment, which closes
	// the pooled connection; acquiring keeps it alive for the kill. If it is
	// already gone this dials again, which is the only way to clean up after a
	// session whose transport died before the user closed the tab.
	client, release, err := s.hub.pool.Acquire(ctx, s.Host, s.User)
	if err != nil {
		log.Printf("session %s: cannot reach %s@%s to end tmux session %s, "+
			"it is still running there: %v", s.ID, s.User, s.Host, name, err)
		return
	}
	defer release()

	if err := sshx.KillTmuxSession(ctx, client, name); err != nil {
		log.Printf("session %s: could not end tmux session %s on %s, "+
			"it is still running there: %v", s.ID, name, s.Host, err)
		return
	}
	log.Printf("session %s: ended tmux session %s on %s", s.ID, name, s.Host)
}

// close transitions to the terminal state and tears down the channel. It is
// safe to call repeatedly and from any goroutine.
func (s *Session) close(reason string) {
	s.mu.Lock()
	if s.state == StateClosed {
		s.mu.Unlock()
		return
	}
	s.state = StateClosed
	pty := s.pty
	viewers := make([]*wsConn, 0, len(s.clients))
	for c := range s.clients {
		viewers = append(viewers, c)
	}
	s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
	if pty != nil {
		pty.Close()
	}

	// Deliver the final status before dropping the sockets, so every tab shows a
	// dead connection rather than a live cursor.
	for _, c := range viewers {
		s.sendEvent(c, map[string]any{"type": "status", "state": StateClosed, "msg": reason})
	}
	if len(viewers) > 0 {
		go func() {
			time.Sleep(100 * time.Millisecond)
			for _, c := range viewers {
				c.close()
			}
		}()
	}
	s.releasePool()
}

// Close terminates the session from an external caller (DELETE, shutdown).
func (s *Session) Close(reason string) { s.close(reason) }

func (s *Session) setState(st State, msg string) {
	s.mu.Lock()
	if s.state == StateClosed || s.state == st {
		s.mu.Unlock()
		return
	}
	s.state = st
	// Carried on the status event because the workspace is not known when the
	// session is created — the shell starts a moment later, and the name is
	// chosen there. Without this a tab learns its workspace only on the next
	// full page load, so a browser could not remember work it had just started.
	workspace := s.tmuxName
	s.mu.Unlock()

	s.emitEvent(map[string]any{
		"type": "status", "state": st, "msg": msg, "workspace": workspace,
	})
}

// SetTheme gives the tab its own theme ("" goes back to the owner's) and
// tells every attached browser.
func (s *Session) SetTheme(name string) {
	s.mu.Lock()
	if s.theme == name {
		s.mu.Unlock()
		return
	}
	s.theme = name
	s.mu.Unlock()
	s.emitEvent(map[string]any{"type": "theme", "theme": name})
}

// SetName updates the label. auto marks the change as coming from a terminal
// title sequence, which must never override a name the user typed.
func (s *Session) SetName(name string, auto bool) (string, bool) {
	clean := sanitizeName(name)
	if clean == "" {
		clean = s.Host
	}

	s.mu.Lock()
	if auto && s.userNamed {
		cur := s.name
		s.mu.Unlock()
		return cur, false
	}
	if !auto {
		s.userNamed = true
	}
	if s.name == clean {
		s.mu.Unlock()
		return clean, false
	}
	s.name = clean
	s.mu.Unlock()

	// Broadcast so a rename from one device updates every attached client.
	s.emitEvent(map[string]any{"type": "rename", "name": clean})
	return clean, true
}

func (s *Session) SetOrder(order int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = order
}

// Resize updates the remote PTY geometry.
func (s *Session) Resize(cols, rows int) {
	s.resizeFrom(nil, cols, rows)
}

// resizeFrom records one viewer's geometry and applies the size the shell
// should actually have. A nil client sets the size directly, for callers that
// are not a socket.
func (s *Session) resizeFrom(c *wsConn, cols, rows int) {
	cols, rows = sshx.ClampDims(cols, rows)

	s.mu.Lock()
	if c == nil {
		s.cols, s.rows = cols, rows
	} else if v, ok := s.clients[c]; ok {
		v.cols, v.rows = cols, rows
		s.clients[c] = v
	}
	s.mu.Unlock()
	s.applyGeometry()
}

// presence records whether a viewer's page is visible and whether it is in use
// right now, then applies the size that follows from it.
func (s *Session) presence(c *wsConn, visible, active bool) {
	s.mu.Lock()
	if v, ok := s.clients[c]; ok {
		v.hidden = !visible
		if active && visible {
			v.active = time.Now()
		}
		s.clients[c] = v
	}
	s.mu.Unlock()
	s.applyGeometry()
}

// markUsed records real use (typing) from a viewer. Cheap when it is already
// the viewer in charge: the size only changes when another device takes over.
func (s *Session) markUsed(c *wsConn) {
	s.mu.Lock()
	v, ok := s.clients[c]
	if !ok {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	recent := !v.hidden && now.Sub(v.active) < time.Second
	v.active, v.hidden = now, false
	s.clients[c] = v
	s.mu.Unlock()
	if !recent {
		s.applyGeometry()
	}
}

// chooseLocked picks the geometry the shell should have (see clientView). The
// caller holds s.mu; ok is false when no viewer has a size yet.
func (s *Session) chooseLocked() (cols, rows int, ok bool) {
	var best clientView
	found := false
	for _, v := range s.clients {
		if v.hidden || v.cols <= 0 || v.rows <= 0 {
			continue
		}
		switch {
		case !found:
			best, found = v, true
		case v.active.After(best.active):
			best = v
		case v.active.Equal(best.active) && v.cols*v.rows > best.cols*best.rows:
			best = v
		}
	}
	if !found {
		return 0, 0, false
	}
	return best.cols, best.rows, true
}

// applyGeometry resizes the shell to the chosen size, if it changed. Called
// when a viewer resizes, comes or goes, is used, or is hidden.
func (s *Session) applyGeometry() {
	s.mu.Lock()
	cols, rows, ok := s.chooseLocked()
	if !ok || (cols == s.cols && rows == s.rows && s.sizedOnce) {
		s.mu.Unlock()
		return
	}
	s.cols, s.rows, s.sizedOnce = cols, rows, true
	pty := s.pty
	s.mu.Unlock()

	if pty != nil {
		if err := pty.Resize(cols, rows); err != nil {
			log.Printf("session %s: resize: %v", s.ID, err)
		}
	}
}

// WriteInput forwards keystrokes to the remote shell.
func (s *Session) WriteInput(b []byte) error {
	s.mu.Lock()
	pty := s.pty
	s.lastIn = time.Now()
	s.mu.Unlock()
	if pty == nil {
		return ErrNotConnected
	}
	_, err := pty.Write(b)
	return err
}

// Upload streams a file to the remote host and injects its quoted path at the
// shell cursor. No trailing newline: the user reviews the command first.
func (s *Session) Upload(name string, r io.Reader) (string, error) {
	s.mu.Lock()
	client, pty, st := s.sshClient, s.pty, s.state
	s.mu.Unlock()

	if st == StateClosed || client == nil || pty == nil {
		return "", ErrNotConnected
	}

	remote, err := sshx.Upload(client, s.uploadDir, name, r)
	if err != nil {
		return "", err
	}

	// Quoted only if it needs to be: a program reading stdin directly, rather
	// than a shell, would receive the quotes as part of the filename.
	if _, err := pty.Write([]byte(sshx.QuoteIfNeeded(remote) + " ")); err != nil {
		return remote, err
	}

	s.emitEvent(map[string]any{"type": "upload", "path": remote, "name": name})
	return remote, nil
}

// InjectText writes text at the shell cursor without submitting it.
//
// Used by voice transcription. Like an uploaded file's path, the text is placed
// where the user can read and edit it; no newline is appended, so nothing runs
// until they choose to press enter. Control characters are stripped because the
// text originates from a remote service and lands directly in a shell.
func (s *Session) InjectText(text string) error {
	clean := sanitizeInjected(text)
	if clean == "" {
		return nil
	}

	s.mu.Lock()
	pty, st := s.pty, s.state
	s.mu.Unlock()

	if st == StateClosed || pty == nil {
		return ErrNotConnected
	}

	_, err := pty.Write([]byte(clean))
	return err
}

// sanitizeInjected strips anything that would act on the terminal rather than
// appear in it, most importantly newlines, which would submit the line.
func sanitizeInjected(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\v', '\f', '\r':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// noteOutput records when the program printed, in the same 400 ms slots the
// browser uses to tell "working" from quiet.
func (s *Session) noteOutput(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastOut = now
	if now.Sub(s.lastIn) < 800*time.Millisecond {
		return // the echo of what was typed is not the program working
	}
	b := now.UnixMilli() / 400
	if n := len(s.outBuckets); n == 0 || s.outBuckets[n-1] != b {
		s.outBuckets = append(s.outBuckets, b)
		if len(s.outBuckets) > 16 {
			s.outBuckets = s.outBuckets[len(s.outBuckets)-16:]
		}
	}
}

// activityEvent is the recent output timing, as ages in ms (clock-free), for a
// browser that is attaching.
func (s *Session) activityEvent(now time.Time) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	nowMs := now.UnixMilli()
	ages := []int64{}
	for _, b := range s.outBuckets {
		if age := nowMs - b*400 - 200; age >= 0 && age < 6400 {
			ages = append(ages, age)
		}
	}
	quiet := int64(-1)
	if !s.lastOut.IsZero() {
		quiet = now.Sub(s.lastOut).Milliseconds()
	}
	return map[string]any{"type": "activity", "ages": ages, "quietMs": quiet}
}

// sessionSink adapts the session to an io.Writer for the PTY's output.
type sessionSink struct{ s *Session }

func (w sessionSink) Write(p []byte) (int, error) {
	w.s.emitOutput(p)
	return len(p), nil
}

// emitOutput records terminal bytes in the ring and forwards them to the
// attached client, if any.
func (s *Session) emitOutput(p []byte) {
	s.ring.Write(p)
	s.noteOutput(time.Now())

	// BEL is how a program asks for attention, and agents ring it when they
	// finish or need an answer. With several panels streaming at once this is
	// the difference between watching them and being told.
	if bytes.IndexByte(p, 0x07) >= 0 {
		s.markAttention()
	}

	viewers := s.viewers()
	if len(viewers) == 0 {
		return
	}

	// The ssh package reuses p, so the queued copy must be our own.
	buf := make([]byte, len(p))
	copy(buf, p)
	for _, c := range viewers {
		c.enqueue(wsMessage{binary: true, data: buf})
	}
}

// markAttention records that the session wants attention and tells any attached
// client. Rate limited: a program in a loop can ring the bell continuously, and
// one event per bell would flood the socket.
func (s *Session) markAttention() {
	s.mu.Lock()
	now := time.Now()
	if now.Sub(s.lastBell) < time.Second {
		s.mu.Unlock()
		return
	}
	s.lastBell = now
	s.attention = true
	s.mu.Unlock()

	s.emitEvent(map[string]any{"type": "bell"})
}

// ClearAttention is called when the user looks at the session.
func (s *Session) ClearAttention() {
	s.mu.Lock()
	s.attention = false
	s.mu.Unlock()
}

// emitEvent sends a JSON control event to the attached client. Events are not
// recorded in the ring: it holds terminal bytes only.
func (s *Session) emitEvent(v any) {
	for _, c := range s.viewers() {
		s.sendEvent(c, v)
	}
}

// viewers snapshots the attached sockets so fan-out never holds the lock while
// enqueuing, which could block on a full queue.
func (s *Session) viewers() []*wsConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*wsConn, 0, len(s.clients))
	for c := range s.clients {
		out = append(out, c)
	}
	return out
}

func (s *Session) sendEvent(c *wsConn, v any) {
	if c == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.enqueue(wsMessage{binary: false, data: data})
}

// attach binds a WebSocket to the session and replays the scrollback so the
// client sees a complete screen.
//
// Several clients may watch one session at once — the same tab open on a laptop
// and a phone. They mirror each other, exactly as two tmux clients on one
// session do, because there is one PTY underneath and it has one screen.
func (s *Session) attach(c *wsConn) {
	s.mu.Lock()
	if s.clients == nil {
		s.clients = map[*wsConn]clientView{}
	}
	// No geometry until this client reports its own (and it does not count as
	// "in use" until it is), so attaching resizes nothing for anyone else.
	s.clients[c] = clientView{}
	s.lastSeen = time.Now()
	st := s.state
	s.mu.Unlock()

	if replay := s.ring.Snapshot(); len(replay) > 0 {
		c.enqueue(wsMessage{binary: true, data: replay})
	}

	if st == StateDetached {
		s.setState(StateConnected, "")
	} else {
		s.sendEvent(c, map[string]any{"type": "status", "state": st, "msg": ""})
	}
	s.sendEvent(c, map[string]any{"type": "rename", "name": s.Name()})
	s.mu.Lock()
	as, am, theme := s.agentState, s.agentMsg, s.theme
	s.mu.Unlock()
	if as != "" {
		s.sendEvent(c, map[string]any{"type": "agent", "state": as, "msg": am})
	}
	// The tab's theme, every time a browser attaches: one that was asleep or
	// offline when it changed on another device catches up here, so every
	// device shows the same theme for the same tab.
	s.sendEvent(c, map[string]any{"type": "theme", "theme": theme})
	// What the program has been doing, so the tab's working/quiet state is
	// right at once rather than after a few seconds of new output.
	s.sendEvent(c, s.activityEvent(time.Now()))
}

// AgentStates are what a program may say about itself with papaya-status.
var AgentStates = map[string]bool{"working": true, "done": true, "needs-you": true, "idle": true}

// SetAgentStatus records what the program in this tab says it is doing and
// tells every attached browser. needs-you also raises attention, as a bell
// does, so it survives a browser that was not open.
func (s *Session) SetAgentStatus(state, msg string) {
	s.mu.Lock()
	s.agentState, s.agentMsg = state, msg
	if state == "needs-you" {
		s.attention = true
	}
	s.mu.Unlock()
	s.emitEvent(map[string]any{"type": "agent", "state": state, "msg": msg})
}

// detach unbinds a WebSocket. The SSH session deliberately stays alive: that is
// what makes a dropped connection recoverable.
func (s *Session) detach(c *wsConn) {
	s.mu.Lock()
	if _, ok := s.clients[c]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.clients, c)
	s.lastSeen = time.Now()
	remaining := len(s.clients)
	closed := s.state == StateClosed
	s.mu.Unlock()

	// Only the last viewer leaving detaches the session. One of two devices
	// closing must not tell the other it has been disconnected.
	if remaining > 0 {
		s.applyGeometry()
		return
	}
	if !closed {
		s.setState(StateDetached, "client disconnected")
	}
}

// idleSince reports how long the session has had no attached client.
func (s *Session) idleSince(now time.Time) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.clients) > 0 {
		return 0, false
	}
	return now.Sub(s.lastSeen), true
}

// sanitizeName strips control characters so a hostile or careless title
// sequence cannot inject ANSI escapes into the tab bar, then bounds the length.
func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\v', '\f', '\r':
			// Whitespace controls become separators rather than vanishing:
			// deleting them outright would turn "line\nbreak" into "linebreak".
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 40 {
		// Truncate by runes, not bytes: cutting mid-rune produces mojibake.
		s = string([]rune(s)[:40])
		// The cut can land immediately after a word, leaving the trailing space
		// that the collapse above had already removed.
		s = strings.TrimSpace(s)
	}
	return s
}

// sanitizeError produces a message safe to show the browser: no key paths, no
// internal addresses, no secrets.
func sanitizeError(stage string, err error) string {
	if err == nil {
		return stage + " failed"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timed out with the server silent"):
		// Names the cause without echoing hostnames or addresses.
		return "host did not complete the SSH handshake — if it runs Tailscale SSH, " +
			"its policy is likely \"check\", which a headless node cannot satisfy"
	case strings.Contains(msg, "host key mismatch"), strings.Contains(msg, "unknown host"):
		return "host key verification failed"
	case strings.Contains(msg, "unable to authenticate"), strings.Contains(msg, "no supported methods"):
		return "authentication failed — check Tailscale SSH ACLs or PAPAYA_KEY_PATH"
	case strings.Contains(msg, "tailnet dial"):
		return "could not reach host over the tailnet"
	case strings.Contains(msg, "handshake"):
		return "SSH handshake failed"
	default:
		return stage + " failed"
	}
}

// usesClient reports whether this session is running on the given connection.
//
// Compared by identity, not by (host, user): a session that has since been
// moved onto a replacement connection must not be closed because the old one
// died. A session with no connection yet matches nothing.
func (s *Session) usesClient(c *ssh.Client) bool {
	if c == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sshClient == c
}

// ErrNoWorkspace is returned when a session has no tmux workspace to talk to:
// tmux is off, or the shell has not started yet.
var ErrNoWorkspace = errors.New("this tab has no tmux workspace yet")

// ErrDraftPending is returned when the target's input box holds text a person
// typed and has not sent; pasting would send both as one message.
var ErrDraftPending = errors.New("someone has unsent text in that tab's input box")

// sayTimeout bounds a message or a screen read. Both are a single short remote
// command; a host that takes longer is not answering.
const sayTimeout = 20 * time.Second

// remoteTmux runs one tmux command against this session's workspace over its
// own pool reference, so the call works whether or not a browser is attached
// and cannot race the PTY releasing the connection.
func (s *Session) remoteTmux(ctx context.Context, build func(workspace string) string, stdin []byte) ([]byte, error) {
	s.mu.Lock()
	name, attached := s.tmuxName, s.tmuxAttached
	s.mu.Unlock()
	if !s.tmux || name == "" || !attached {
		return nil, ErrNoWorkspace
	}

	ctx, cancel := context.WithTimeout(ctx, sayTimeout)
	defer cancel()
	client, release, err := s.hub.pool.Acquire(ctx, s.Host, s.User)
	if err != nil {
		return nil, err
	}
	defer release()
	return sshx.RunTmux(ctx, client, build(name), stdin)
}

// Say pastes text into this tab's workspace as if it had been typed there,
// and presses Enter when asked. It is how one agent talks to another.
func (s *Session) Say(ctx context.Context, text string, enter, force bool) error {
	text = sshx.CleanSayText(text)
	if strings.TrimSpace(text) == "" {
		return errors.New("empty message")
	}
	if !force {
		screen, err := s.remoteTmux(ctx, sshx.DraftCaptureCommand, nil)
		if err != nil {
			return err
		}
		if sshx.HasDraft(string(screen)) {
			return ErrDraftPending
		}
	}
	_, err := s.remoteTmux(ctx, func(ws string) string { return sshx.SayCommand(ws, enter) }, []byte(text))
	return err
}

// Screen returns the last lines of this tab's workspace, so a sender can see
// the message land and read the answer.
func (s *Session) Screen(ctx context.Context, lines int) (string, error) {
	if lines <= 0 || lines > 2000 {
		lines = 40
	}
	out, err := s.remoteTmux(ctx, func(ws string) string { return sshx.CaptureCommand(ws, lines) }, nil)
	return string(out), err
}
