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
	"fmt"
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
	// Team is the people, besides the admin, who may use this papaya, each
	// limited to their own machines. Empty means papaya behaves exactly as it
	// always has: only the configured users, with full access.
	Team []Member `json:"team,omitempty"`
}

// Member is a teammate: a tailnet identity (a login name such as
// "ariosto@example.com", or a tag such as "tag:ariosto") and the machines they
// may open terminals on. A member sees only their own tabs, only on those
// machines, and can change no setting.
type Member struct {
	Login    string   `json:"login"`
	Machines []string `json:"machines"`
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
	out.Team = make([]Member, 0, len(s.Team))
	for _, m := range s.Team {
		out.Team = append(out.Team, Member{Login: m.Login, Machines: append([]string(nil), m.Machines...)})
	}
	if len(out.Team) == 0 {
		out.Team = nil
	}
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

// ErrInvalidTeam is returned by SetTeam for a team that cannot be saved.
var ErrInvalidTeam = errors.New("invalid team")

// Team returns a copy of the team.
func (st *Store) Team() []Member {
	return st.Get().Team
}

// SetTeam replaces the team. Every machine named must be one already chosen in
// setup, and is stored under that machine's name; logins must be unique. The
// admin's own identities can never be members (isAdmin reports them), so a
// member entry can never shadow, or be confused with, full access.
func (st *Store) SetTeam(team []Member, isAdmin func(login string) bool) error {
	st.mu.Lock()
	machines := append([]Machine(nil), st.s.Machines...)
	st.mu.Unlock()

	seen := map[string]bool{}
	clean := make([]Member, 0, len(team))
	for _, m := range team {
		login := strings.TrimSpace(m.Login)
		if login == "" || strings.ContainsAny(login, " \t\r\n") || len(login) > 200 {
			return fmt.Errorf("%w: %q is not a tailnet login or tag", ErrInvalidTeam, m.Login)
		}
		key := strings.ToLower(login)
		if seen[key] {
			return fmt.Errorf("%w: %s is listed twice", ErrInvalidTeam, login)
		}
		if isAdmin != nil && isAdmin(login) {
			return fmt.Errorf("%w: %s already has full access", ErrInvalidTeam, login)
		}
		seen[key] = true

		var names []string
		have := map[string]bool{}
		for _, name := range m.Machines {
			mc, ok := find(machines, name)
			if !ok || norm(mc.Name) != norm(name) && norm(mc.FQDN) != norm(name) {
				return fmt.Errorf("%w: %s is not one of the machines set up in papaya", ErrInvalidTeam, name)
			}
			if !have[mc.Name] {
				have[mc.Name] = true
				names = append(names, mc.Name)
			}
		}
		sort.Strings(names)
		clean = append(clean, Member{Login: login, Machines: names})
	}
	sort.Slice(clean, func(i, j int) bool { return strings.ToLower(clean[i].Login) < strings.ToLower(clean[j].Login) })
	return st.Update(func(s *Settings) { s.Team = clean })
}

// MemberOf matches a caller to a team member: by login (without regard to
// case), or, for a tagged machine with no login, by one of its tags. It returns
// the member's identity as stored, which becomes the owner of their tabs.
func (st *Store) MemberOf(login string, tags []string) (string, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if login != "" {
		for _, m := range st.s.Team {
			if !strings.HasPrefix(m.Login, "tag:") && strings.EqualFold(m.Login, login) {
				return m.Login, true
			}
		}
	}
	for _, t := range tags {
		for _, m := range st.s.Team {
			if strings.HasPrefix(m.Login, "tag:") && m.Login == t {
				return m.Login, true
			}
		}
	}
	return "", false
}

// IsMember reports whether key (as returned by MemberOf) is on the team now.
func (st *Store) IsMember(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, m := range st.s.Team {
		if m.Login == key {
			return true
		}
	}
	return false
}

// MemberMachines is the machines a member may use, read fresh so a change in
// the team takes effect on the very next request.
func (st *Store) MemberMachines(key string) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, m := range st.s.Team {
		if m.Login == key {
			return append([]string(nil), m.Machines...)
		}
	}
	return nil
}

// MachineFor resolves host to one of the allowed machine names, strictly: the
// name itself, the machine's full tailnet name, or (for a bare name with no
// dots) its first label. An address matches only if it is literally listed.
// The returned name is what papaya dials, so a member can never steer a
// connection to a host merely sharing a first label with their machine.
func (st *Store) MachineFor(allowed []string, host string) (string, bool) {
	h := norm(host)
	if h == "" {
		return "", false
	}
	st.mu.Lock()
	machines := append([]Machine(nil), st.s.Machines...)
	st.mu.Unlock()

	isIP := net.ParseIP(h) != nil
	for _, name := range allowed {
		n := norm(name)
		// Only a machine that is still set up in papaya counts: one the admin
		// removed is gone for its member too.
		mc, ok := find(machines, name)
		if !ok || norm(mc.Name) != n {
			continue
		}
		if h == n {
			return name, true
		}
		if isIP {
			continue
		}
		if mc.FQDN != "" && h == norm(mc.FQDN) {
			return name, true
		}
		if !strings.Contains(h, ".") && net.ParseIP(n) == nil && h == firstLabel(n) {
			return name, true
		}
	}
	return "", false
}

// PruneTeam drops from every member any machine that is no longer set up, so
// removing a machine in setup removes it from its teammate as well.
func PruneTeam(s *Settings) {
	for i := range s.Team {
		var keep []string
		for _, name := range s.Team[i].Machines {
			if mc, ok := find(s.Machines, name); ok && norm(mc.Name) == norm(name) {
				keep = append(keep, name)
			}
		}
		s.Team[i].Machines = keep
	}
}
