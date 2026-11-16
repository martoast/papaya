package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/internal/tsnode"
)

// owner extracts the authenticated identity. Its absence means the middleware
// was bypassed, which is a wiring bug, not a client error.
func owner(w http.ResponseWriter, r *http.Request) (string, bool) {
	u, ok := tsnode.UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	return u, true
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	sessions := s.hub.List(user)
	views := make([]hub.View, 0, len(sessions))
	for _, sess := range sessions {
		views = append(views, sess.View())
	}
	writeJSON(w, http.StatusOK, views)
}

type createRequest struct {
	Host string `json:"host"`
	User string `json:"user"`
	Name string `json:"name"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	var req createRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	req.Host = strings.TrimSpace(req.Host)
	if req.Host == "" {
		writeError(w, http.StatusBadRequest, "host is required")
		return
	}
	if req.User = strings.TrimSpace(req.User); req.User == "" {
		req.User = s.sshUserFor(req.Host)
	}
	if req.User == "" {
		writeError(w, http.StatusBadRequest, noAccountMessage(req.Host))
		return
	}

	// Both fields come from the client, and the picker is only a suggestion: a
	// scripted request can name any host and any account. These are the checks
	// that make the configured limits real rather than cosmetic.
	if s.opts.HostAllowed != nil && !s.opts.HostAllowed(req.Host) {
		log.Printf("denied: %s requested host %q, which is not in the allow list", user, req.Host)
		writeError(w, http.StatusForbidden, "that host is not permitted")
		return
	}
	if s.opts.SSHUserAllowed != nil && !s.opts.SSHUserAllowed(req.User) {
		log.Printf("denied: %s requested remote user %q on %s", user, req.User, req.Host)
		writeError(w, http.StatusForbidden, "that remote user is not permitted")
		return
	}

	sess, err := s.hub.Create(user, req.Host, req.User, req.Name, req.Cols, req.Rows)
	switch {
	case errors.Is(err, hub.ErrTooManySessions):
		writeError(w, http.StatusTooManyRequests, "session limit reached")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, sess.View())
}

type patchRequest struct {
	Name  *string `json:"name"`
	Order *int    `json:"order"`
	Auto  bool    `json:"auto"`
}

func (s *Server) patchSession(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	sess, err := s.hub.Get(user, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	var req patchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Name != nil {
		// Auto-titles from the shell must never clobber a name the user typed;
		// the frontend guards this too, this is the authoritative check.
		sess.SetName(*req.Name, req.Auto)
	}
	if req.Order != nil {
		sess.SetOrder(*req.Order)
	}

	writeJSON(w, http.StatusOK, sess.View())
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	if err := s.hub.Delete(user, r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// markSeen clears a session's attention flag, so a reload does not show a badge
// for something the user has already dealt with.
func (s *Server) markSeen(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	sess, err := s.hub.Get(user, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	sess.ClearAttention()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	sess, err := s.hub.Get(user, r.URL.Query().Get("session"))
	if err != nil {
		// Another owner's session must be indistinguishable from a missing one.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	hub.ServeWS(s.up, w, r, sess)
}

// sshUserFor is the account to connect as on host, defaulting to the one used
// everywhere when no per-host account is configured.
func (s *Server) sshUserFor(host string) string {
	if s.opts.SSHUserForHost != nil {
		if u := strings.TrimSpace(s.opts.SSHUserForHost(host)); u != "" {
			return u
		}
	}
	return s.opts.DefaultSSHUser
}

// Host is an entry in the new-session host picker.
type Host struct {
	Name string `json:"name"`
	FQDN string `json:"fqdn"`
	// User is the account this host is reached as, so the picker can offer it
	// rather than making the user remember which machine wants which login.
	User   string `json:"user"`
	Online bool   `json:"online"`
	// Known reports whether Online is authoritative. A host configured via
	// PAPAYA_HOSTS but absent from tailnet status has no known state, and the
	// picker must still offer it: treating "unknown" as "offline" would make
	// every host unselectable whenever peer status is unavailable.
	Known bool `json:"known"`
}

// listHosts derives the picker from live tailnet peers, merged with any
// statically configured hosts.
func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}

	byName := make(map[string]Host)
	// Maps every address and name a peer answers to, so a statically configured
	// host given as a 100.x address is recognised as that peer rather than
	// listed a second time with unknown state.
	aliasToName := make(map[string]string)

	if s.status != nil {
		if st, err := s.status.Status(r.Context()); err == nil && st != nil {
			for _, peer := range st.Peer {
				if peer == nil {
					continue
				}
				fqdn := strings.TrimSuffix(peer.DNSName, ".")
				name := shortName(fqdn, peer.HostName)
				if name == "" {
					continue
				}
				byName[name] = Host{Name: name, FQDN: fqdn, Online: peer.Online, Known: true}

				aliasToName[strings.ToLower(name)] = name
				if fqdn != "" {
					aliasToName[strings.ToLower(fqdn)] = name
				}
				if peer.HostName != "" {
					aliasToName[strings.ToLower(peer.HostName)] = name
				}
				for _, ip := range peer.TailscaleIPs {
					aliasToName[ip.String()] = name
				}
			}
		}
	}

	for _, h := range s.opts.StaticHosts {
		h = strings.TrimSuffix(h, ".")
		if h == "" {
			continue
		}
		// Already known as a peer under some other spelling: keep the richer
		// entry rather than adding a duplicate with unknown state.
		if _, ok := aliasToName[strings.ToLower(h)]; ok {
			continue
		}

		name := shortName(h, h)
		if name == "" {
			continue
		}
		if _, exists := byName[name]; !exists {
			byName[name] = Host{Name: name, FQDN: h, Online: false, Known: false}
		}
	}

	hosts := make([]Host, 0, len(byName))
	for _, h := range byName {
		// Never offer a host the API would refuse; an entry the user cannot
		// connect to is worse than one that is simply absent.
		if s.opts.HostAllowed != nil && !s.opts.HostAllowed(h.Name) && !s.opts.HostAllowed(h.FQDN) {
			continue
		}
		// Resolved here rather than where the entry was built, so a peer and a
		// statically configured host get the account by the same rule whichever
		// spelling it was configured under.
		h.User = s.sshUserFor(h.Name)
		if h.User == "" || (s.opts.SSHUserAllowed != nil && !s.opts.SSHUserAllowed(h.User)) {
			h.User = s.opts.DefaultSSHUser
		}
		hosts = append(hosts, h)
	}
	// Selectable hosts first, then name order. A host of unknown state sorts
	// with the selectable ones because it can still be tried.
	selectable := func(h Host) bool { return h.Online || !h.Known }
	sort.Slice(hosts, func(i, j int) bool {
		if selectable(hosts[i]) != selectable(hosts[j]) {
			return selectable(hosts[i])
		}
		return hosts[i].Name < hosts[j].Name
	})

	writeJSON(w, http.StatusOK, hosts)
}

// shortName reduces an FQDN to its first label, falling back to the hostname.
//
// An IP address is returned whole: splitting 100.64.0.10 at the first dot
// would name the host "100".
func shortName(fqdn, fallback string) string {
	if fqdn == "" {
		return fallback
	}
	if _, err := netip.ParseAddr(fqdn); err == nil {
		return fqdn
	}
	if i := strings.Index(fqdn, "."); i > 0 {
		return fqdn[:i]
	}
	return fqdn
}

// listResumable reports remote workspaces still running that no tab is showing.
//
// Work outlives papaya on purpose, but until this existed nothing pointed at
// it: getting back in meant recreating a tab with exactly the original name,
// which the user had to remember — including the "-2" a second unnamed tab was
// silently given.
func (s *Server) listResumable(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	targets := make([]hub.Target, 0, len(s.opts.StaticHosts))
	for _, h := range s.opts.StaticHosts {
		// The allow list is the authority on where a session may go, so there is
		// no point offering to resume somewhere a session could not be created.
		if s.opts.HostAllowed != nil && !s.opts.HostAllowed(h) {
			continue
		}
		// Each host is scanned as its own account. Skipping a disallowed one
		// rather than abandoning the whole scan keeps one bad entry from hiding
		// every other host's work.
		u := s.sshUserFor(h)
		if u == "" || (s.opts.SSHUserAllowed != nil && !s.opts.SSHUserAllowed(u)) {
			continue
		}
		targets = append(targets, hub.Target{Host: h, User: u})
	}

	writeJSON(w, http.StatusOK, s.hub.Resumable(r.Context(), user, targets))
}

type resumeRequest struct {
	Host      string `json:"host"`
	User      string `json:"user"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// resumeSession opens a tab attached to an existing remote workspace.
func (s *Server) resumeSession(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	var req resumeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	req.Host = strings.TrimSpace(req.Host)
	req.Workspace = strings.TrimSpace(req.Workspace)
	if req.Host == "" || req.Workspace == "" {
		writeError(w, http.StatusBadRequest, "host and workspace are required")
		return
	}
	if req.User = strings.TrimSpace(req.User); req.User == "" {
		req.User = s.sshUserFor(req.Host)
	}
	if req.User == "" {
		writeError(w, http.StatusBadRequest, noAccountMessage(req.Host))
		return
	}

	// The same gates as creating a session. Resuming names a host and an account
	// just as directly, and the workspace name reaches a remote shell, so none
	// of it is trusted for having come from a list this server produced.
	if s.opts.HostAllowed != nil && !s.opts.HostAllowed(req.Host) {
		log.Printf("denied: %s tried to resume on host %q, which is not in the allow list", user, req.Host)
		writeError(w, http.StatusForbidden, "that host is not permitted")
		return
	}
	if s.opts.SSHUserAllowed != nil && !s.opts.SSHUserAllowed(req.User) {
		log.Printf("denied: %s tried to resume as remote user %q on %s", user, req.User, req.Host)
		writeError(w, http.StatusForbidden, "that remote user is not permitted")
		return
	}

	sess, err := s.hub.Resume(user, req.Host, req.User, req.Name, req.Workspace, req.Cols, req.Rows)
	switch {
	case errors.Is(err, hub.ErrTooManySessions):
		writeError(w, http.StatusTooManyRequests, "session limit reached")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess.View())
}

// findTab resolves a tab the way a person names it: its label as shown in the
// tab bar, compared without case, or its tmux workspace. A unique prefix is
// accepted too. Ambiguity is refused rather than guessed — a message delivered
// to the wrong agent is worse than one not delivered.
func findTab(sessions []*hub.Session, name string) (*hub.Session, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	want = strings.TrimPrefix(want, "papaya-")
	if want == "" {
		return nil, errors.New("name a tab")
	}
	var prefix []*hub.Session
	for _, sess := range sessions {
		v := sess.View()
		label := strings.ToLower(v.Name)
		ws := strings.TrimPrefix(strings.ToLower(v.Workspace), "papaya-")
		if label == want || (ws != "" && ws == want) {
			return sess, nil
		}
		if strings.HasPrefix(label, want) || (ws != "" && strings.HasPrefix(ws, want)) {
			prefix = append(prefix, sess)
		}
	}
	switch len(prefix) {
	case 1:
		return prefix[0], nil
	case 0:
		return nil, errors.New("no open tab is called " + name)
	default:
		names := make([]string, 0, len(prefix))
		for _, p := range prefix {
			names = append(names, p.View().Name)
		}
		return nil, errors.New(name + " could mean " + strings.Join(names, ", ") + "; use the full name")
	}
}

// sayTabs lists the open tabs as plain text, one per line, so a shell script
// on a machine without a JSON parser can read it.
func (s *Server) sayTabs(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	var b strings.Builder
	for _, sess := range s.hub.List(user) {
		v := sess.View()
		fmt.Fprintf(&b, "%-20s %-10s %s@%s  %s\n", v.Name, v.State, v.User, v.Host, v.Workspace)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}

type sayRequest struct {
	To   string `json:"to"`
	Text string `json:"text"`
	// Enter submits the message. It defaults to true: the point of saying
	// something to an agent is for it to read it.
	Enter *bool `json:"enter"`
	// Force pastes even over text somebody typed into the target's input box.
	Force bool `json:"force"`
}

// say types a message into another tab's workspace. Agents use it to talk to
// each other through papaya, by tab name, from any machine on the tailnet.
func (s *Server) say(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	var req sayRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, sshx.MaxSayBytes+1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json or message too long")
		return
	}
	sess, err := findTab(s.hub.List(user), req.To)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	enter := req.Enter == nil || *req.Enter
	if err := sess.Say(r.Context(), req.Text, enter, req.Force); err != nil {
		if errors.Is(err, hub.ErrNoWorkspace) || errors.Is(err, hub.ErrDraftPending) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		log.Printf("say %s: %v", sess.ID, err)
		writeError(w, http.StatusBadGateway, "could not reach that tab's host")
		return
	}
	// Scenes draw messages passing between tabs. Names only, never the text.
	s.events.publish(user, map[string]any{"type": "say", "from": sayFrom(req.Text), "to": sess.Name()})
	w.WriteHeader(http.StatusNoContent)
}

// agentStatus is papaya-status: the program in a tab saying what it is doing,
// so the tab can show it exactly (Claude Code hooks call it on submit, stop and
// notification). The tab is found by name, as for papaya-say.
func (s *Server) agentStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	var req struct {
		Tab   string `json:"tab"`
		State string `json:"state"`
		Msg   string `json:"msg"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !hub.AgentStates[req.State] {
		writeError(w, http.StatusBadRequest, "state is working, done, needs-you or idle")
		return
	}
	sess, err := findTab(s.hub.List(user), req.Tab)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	sess.SetAgentStatus(req.State, cleanLabel(req.Msg, 200))
	if req.State == "done" || req.State == "needs-you" {
		s.events.publish(user, map[string]any{"type": "agent", "tab": sess.Name(), "state": req.State})
	}
	w.WriteHeader(http.StatusNoContent)
}

// sayFrom reads the sender from a message signed "<name> here: ...".
func sayFrom(text string) string {
	head, _, found := strings.Cut(text, " here:")
	if !found || len(head) > 40 || strings.ContainsAny(head, "\n\r") {
		return ""
	}
	return cleanLabel(head, 40)
}

// screen returns the last lines of a tab's workspace as plain text.
func (s *Server) screen(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	sess, err := findTab(s.hub.List(user), r.URL.Query().Get("to"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	out, err := sess.Screen(r.Context(), lines)
	if err != nil {
		if errors.Is(err, hub.ErrNoWorkspace) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		log.Printf("screen %s: %v", sess.ID, err)
		writeError(w, http.StatusBadGateway, "could not reach that tab's host")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(out))
}

// noAccountMessage explains a request that names no remote account for a host
// that has none configured. There is deliberately no built-in default.
func noAccountMessage(host string) string {
	return "no account chosen for " + host + " yet: open Settings (the gear) and add it, " +
		"or set PAPAYA_SSH_USER"
}
