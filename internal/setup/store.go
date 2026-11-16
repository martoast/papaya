// Package setup keeps the choices made in papaya's first-run setup screen:
// which machines to use, which account to log in as on each, and whether setup
// has been finished.
//
// The file lives in papaya's state directory next to the tailnet identity, so a
// restart or an upgrade keeps it. Environment settings still win wherever they
// overlap, which keeps every existing configuration working unchanged.
package setup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Methods a machine can let papaya in by.
const (
	// MethodTailscale is Tailscale SSH: the tailnet identity is the credential,
	// so nothing has to be copied onto the machine except one setting.
	MethodTailscale = "tailscale"
	// MethodKey is papaya's own SSH key, added to the machine's
	// authorized_keys, for machines that run their own SSH server.
	MethodKey = "key"
)

// Machine is one machine chosen in the setup screen.
type Machine struct {
	Name   string `json:"name"`
	FQDN   string `json:"fqdn,omitempty"`
	User   string `json:"user,omitempty"`
	Method string `json:"method,omitempty"`
	// Ready is set once a test connection has succeeded.
	Ready bool `json:"ready,omitempty"`
}

// Settings is everything the setup screen saves.
type Settings struct {
	Version  int       `json:"version"`
	Complete bool      `json:"complete"`
	Machines []Machine `json:"machines,omitempty"`
	// Seen lists every tailnet machine the owner has been shown, so a machine
	// that joins later can be pointed out as new.
	Seen []string `json:"seen,omitempty"`
}

// Store is the settings file, safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	s    Settings
}

// Open reads the settings at path. A missing file is a fresh install, not an
// error.
func Open(path string) (*Store, error) {
	st := &Store{path: path, s: Settings{Version: 1}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return st, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &st.s); err != nil {
		return nil, err
	}
	return st, nil
}

// Get returns a copy of the current settings.
func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return clone(st.s)
}

// Update applies fn and saves the result. The file is replaced atomically, so
// a crash mid-write leaves the previous version intact rather than half a file.
func (st *Store) Update(fn func(*Settings)) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	next := clone(st.s)
	fn(&next)
	next.Version = 1

	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		os.Remove(tmp)
		return err
	}
	st.s = next
	return nil
}

// Machine returns the saved entry for host, matched by short name, full name,
// or first DNS label, so either spelling of a machine finds it.
func (st *Store) Machine(host string) (Machine, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return find(st.s.Machines, host)
}

// UserFor is the account chosen for host in the setup screen, or "".
func (st *Store) UserFor(host string) string {
	m, ok := st.Machine(host)
	if !ok {
		return ""
	}
	return m.User
}

// Hosts lists the chosen machines by name, in a stable order.
func (st *Store) Hosts() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]string, 0, len(st.s.Machines))
	for _, m := range st.s.Machines {
		out = append(out, m.Name)
	}
	sort.Strings(out)
	return out
}

func find(ms []Machine, host string) (Machine, bool) {
	h := norm(host)
	if h == "" {
		return Machine{}, false
	}
	label := firstLabel(h)
	for _, m := range ms {
		if norm(m.Name) == h || norm(m.FQDN) == h {
			return m, true
		}
	}
	for _, m := range ms {
		if firstLabel(norm(m.Name)) == label || firstLabel(norm(m.FQDN)) == label {
			return m, true
		}
	}
	return Machine{}, false
}

func norm(h string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), ".")) }

func firstLabel(h string) string {
	if net.ParseIP(h) != nil {
		return h
	}
	if i := strings.Index(h, "."); i > 0 {
		return h[:i]
	}
	return h
}

func clone(s Settings) Settings {
	out := s
	out.Machines = append([]Machine(nil), s.Machines...)
	out.Seen = append([]string(nil), s.Seen...)
	return out
}

// AccountResolver combines the three places a host's remote account can come
// from, in order: an explicit per-host setting (PAPAYA_HOSTS "user@host"), the
// account chosen in the setup screen, then the one default (PAPAYA_SSH_USER).
// Environment settings win wherever they say something, so an existing
// configuration behaves exactly as before.
func AccountResolver(explicit, chosen func(string) string, fallback string) func(string) string {
	return func(host string) string {
		if explicit != nil {
			if u := explicit(host); u != "" {
				return u
			}
		}
		if chosen != nil {
			if u := chosen(host); u != "" {
				return u
			}
		}
		return fallback
	}
}
