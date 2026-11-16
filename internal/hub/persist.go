package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"sort"
	"time"

	"github.com/martoast/papaya/internal/sshx"
)

// Tabs that survive papaya itself.
//
// With tmux on, the work in a tab already outlives papaya: it runs in a tmux
// workspace on the target host. What did not survive was papaya's list of
// tabs, which lived only in memory. After an update or a restart every browser
// came back to an empty bar and had to pick its work out of the "resume" list,
// and papaya-say saw no tabs at all until someone did.
//
// So the list is kept on disk: each tab's id, owner, host, account, name,
// order and workspace. On start the hub recreates every tab with its original
// id and attaches it to its workspace again. A browser that was open simply
// reconnects to the ids it already had, and the tabs are back as if nothing
// happened.

// savedSession is one tab as it is kept on disk.
type savedSession struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Host      string    `json:"host"`
	User      string    `json:"user"`
	Name      string    `json:"name"`
	UserNamed bool      `json:"userNamed,omitempty"`
	Order     int       `json:"order"`
	Workspace string    `json:"workspace"`
	Cols      int       `json:"cols"`
	Rows      int       `json:"rows"`
	CreatedAt time.Time `json:"createdAt"`
}

// snapshot lists the tabs worth restoring: open, with a tmux workspace.
func (h *Hub) snapshot() []savedSession {
	h.mu.Lock()
	all := make([]*Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		all = append(all, s)
	}
	h.mu.Unlock()

	var out []savedSession
	for _, s := range all {
		s.mu.Lock()
		if s.tmux && s.tmuxName != "" && s.state != StateClosed {
			out = append(out, savedSession{
				ID: s.ID, Owner: s.Owner, Host: s.Host, User: s.User,
				Name: s.name, UserNamed: s.userNamed, Order: s.order,
				Workspace: s.tmuxName, Cols: s.cols, Rows: s.rows, CreatedAt: s.CreatedAt,
			})
		}
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Order < out[j].Order
	})
	return out
}

// SaveSessions writes the tab list to path, unless the hub is shutting its
// tabs down (that would record an empty list over the real one).
func (h *Hub) SaveSessions(path string) error {
	h.persistMu.Lock()
	defer h.persistMu.Unlock()
	h.mu.Lock()
	closing := h.closing
	h.mu.Unlock()
	if closing {
		return nil
	}
	b, err := json.MarshalIndent(h.snapshot(), "", "  ")
	if err != nil {
		return err
	}
	return writeIfChanged(path, b)
}

func writeIfChanged(path string, b []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RunPersist keeps path current until ctx ends, then writes it one last time
// while the tabs are still open.
func (h *Hub) RunPersist(ctx context.Context, path string, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := h.SaveSessions(path); err != nil {
				log.Printf("save tabs: %v", err)
			}
			return
		case <-t.C:
			if err := h.SaveSessions(path); err != nil {
				log.Printf("save tabs: %v", err)
			}
		}
	}
}

// RestoreSessions recreates the tabs saved at path, each with its original id,
// attached to its workspace again. It returns how many came back. A missing
// file is not an error: it is a first start.
func (h *Hub) RestoreSessions(path string) (int, error) {
	if !h.opts.Tmux {
		return 0, nil // without tmux there is no work left to reattach to
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var saved []savedSession
	if err := json.Unmarshal(b, &saved); err != nil {
		return 0, err
	}
	n := 0
	for _, sv := range saved {
		if sv.ID == "" || sv.Owner == "" || sv.Host == "" || sv.User == "" || !sshx.ValidWorkspaceName(sv.Workspace) {
			continue
		}
		if h.restore(sv) {
			n++
		}
	}
	return n, nil
}

func (h *Hub) restore(sv savedSession) bool {
	cols, rows := sshx.ClampDims(sv.Cols, sv.Rows)
	name := sanitizeName(sv.Name)
	if name == "" {
		name = sv.Host
	}

	h.mu.Lock()
	if _, dup := h.sessions[sv.ID]; dup {
		h.mu.Unlock()
		return false
	}
	count := 0
	for _, s := range h.sessions {
		if s.Owner == sv.Owner {
			count++
		}
	}
	if count >= h.opts.MaxSessions {
		h.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:        sv.ID,
		Host:      sv.Host,
		User:      sv.User,
		Owner:     sv.Owner,
		CreatedAt: sv.CreatedAt,
		hub:       h,
		uploadDir: h.opts.UploadDir,
		ring:      NewRing(h.opts.RingSize),
		cancel:    cancel,
		name:      name,
		userNamed: sv.UserNamed,
		order:     sv.Order,
		state:     StateConnecting,
		lastSeen:  time.Now(),
		cols:      cols,
		rows:      rows,
		tmux:      true,
		adopt:     sv.Workspace,
		tmuxName:  sv.Workspace,
	}
	h.sessions[s.ID] = s
	h.mu.Unlock()

	go s.run(ctx)
	return true
}
