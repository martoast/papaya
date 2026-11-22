package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/martoast/papaya/internal/setup"
	"github.com/martoast/papaya/internal/sshx"
)

// HTTPSState says whether papaya can be reached over HTTPS yet.
type HTTPSState struct {
	// On means an HTTPS listener is serving.
	On bool `json:"on"`
	// URL is the address to open, https when On.
	URL string `json:"url"`
	// Available means the tailnet offers this node a certificate. When false,
	// HTTPS (or MagicDNS) is switched off for the tailnet, and the setup screen
	// says where to switch it on.
	Available bool `json:"available"`
}

// SetupDeps is what the first-run setup screen needs.
type SetupDeps struct {
	Store *setup.Store
	// Peers is the tailnet status the machine list is built from.
	Peers Statuser
	// Dialer and SSH are used for the screen's test connections: the same
	// dialer and verification the real sessions use.
	Dialer sshx.Dialer
	SSH    sshx.Config
	// PublicKey is papaya's own SSH key as one authorized_keys line.
	PublicKey string
	// Owner is the tailnet login papaya belongs to.
	Owner func() string
	// HTTPS reports the current HTTPS state.
	HTTPS func() HTTPSState
	// SSHUserAllowed applies the same account rules sessions do.
	SSHUserAllowed func(string) bool
	// Self is this node's own name, left out of the machine list.
	Self func() string
}

type setupAPI struct {
	d SetupDeps
	// testing serializes test connections per host, so a double click does not
	// race two dials against one machine.
	mu      sync.Mutex
	testing map[string]bool
	// joins is what each machine reported when its one-line join ran.
	joins map[string]joined
}

// EnableSetup turns on the setup screen's API.
func (s *Server) EnableSetup(d SetupDeps) { s.setup = &setupAPI{d: d, testing: map[string]bool{}} }

func (s *Server) setupRoutes(mux *http.ServeMux) {
	// Setup is the admin's: a team member can neither see the machine list nor
	// change it.
	mux.HandleFunc("GET /api/setup", s.adminOnly(s.getSetup))
	mux.HandleFunc("PUT /api/setup/machines", s.adminOnly(s.putSetupMachines))
	mux.HandleFunc("POST /api/setup/test", s.adminOnly(s.testSetupMachine))
	mux.HandleFunc("POST /api/setup/complete", s.adminOnly(s.completeSetup))
	mux.HandleFunc("POST /api/setup/seen", s.adminOnly(s.seenSetup))
	s.joinRoutes(mux)
	s.teamRoutes(mux)
}

// MachineView is one tailnet machine as the setup screen shows it.
type MachineView struct {
	Name   string `json:"name"`
	FQDN   string `json:"fqdn"`
	OS     string `json:"os"`
	Online bool   `json:"online"`
	// TailscaleSSH means the machine already accepts Tailscale SSH: it
	// advertises SSH host keys to the tailnet.
	TailscaleSSH bool   `json:"tailscaleSSH"`
	Selected     bool   `json:"selected"`
	User         string `json:"user"`
	Method       string `json:"method"`
	Ready        bool   `json:"ready"`
	New          bool   `json:"new"`
	// Joined is set once the one-line join ran on this machine; SSHD says
	// whether it found an SSH server listening there.
	Joined   bool      `json:"joined"`
	SSHD     bool      `json:"sshd"`
	JoinedAt time.Time `json:"joinedAt,omitempty"`
}

// SetupView is the whole state of the setup screen.
type SetupView struct {
	Complete  bool          `json:"complete"`
	Owner     string        `json:"owner"`
	Self      string        `json:"self"`
	Tailnet   string        `json:"tailnet"`
	HTTPS     HTTPSState    `json:"https"`
	PublicKey string        `json:"publicKey"`
	LastUser  string        `json:"lastUser"`
	Machines  []MachineView `json:"machines"`
	// NewMachines counts machines that joined since the owner last looked,
	// for the hint on the settings button.
	NewMachines int `json:"newMachines"`
	// PolicySnippet is the tailnet SSH rule to paste so Tailscale SSH accepts
	// papaya without a browser re-login every 12 hours.
	PolicySnippet string `json:"policySnippet"`
}

const policySnippet = `"ssh": [
  {
    "action": "accept",
    "src":    ["autogroup:member"],
    "dst":    ["autogroup:self"],
    "users":  ["autogroup:nonroot"]
  }
]`

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	if s.setup == nil {
		// Without the setup screen (the test server, for one) the app simply
		// carries on as configured.
		writeJSON(w, http.StatusOK, SetupView{Complete: true})
		return
	}
	writeJSON(w, http.StatusOK, s.setup.view(r.Context()))
}

func (a *setupAPI) view(ctx context.Context) SetupView {
	st := a.d.Store.Get()
	v := SetupView{
		Complete:      st.Complete,
		PublicKey:     a.d.PublicKey,
		PolicySnippet: policySnippet,
	}
	if a.d.Owner != nil {
		v.Owner = a.d.Owner()
	}
	if a.d.HTTPS != nil {
		v.HTTPS = a.d.HTTPS()
	}
	if a.d.Self != nil {
		v.Self = a.d.Self()
	}

	seen := map[string]bool{}
	for _, n := range st.Seen {
		seen[strings.ToLower(n)] = true
	}
	for _, m := range st.Machines {
		if m.User != "" {
			v.LastUser = m.User
		}
	}

	v.Machines = []MachineView{}
	if a.d.Peers != nil {
		if status, err := a.d.Peers.Status(ctx); err == nil && status != nil {
			if status.CurrentTailnet != nil {
				v.Tailnet = status.CurrentTailnet.MagicDNSSuffix
			}
			if v.Tailnet == "" {
				//lint:ignore SA1019 fallback for nodes that do not fill CurrentTailnet
				v.Tailnet = status.MagicDNSSuffix
			}
			for _, p := range status.Peer {
				if p == nil {
					continue
				}
				fqdn := strings.TrimSuffix(p.DNSName, ".")
				name := shortName(fqdn, p.HostName)
				if name == "" || strings.EqualFold(name, v.Self) {
					continue
				}
				mv := MachineView{
					Name:         name,
					FQDN:         fqdn,
					OS:           p.OS,
					Online:       p.Online,
					TailscaleSSH: len(p.SSH_HostKeys) > 0,
					Method:       setup.MethodTailscale,
				}
				if !mv.TailscaleSSH && strings.EqualFold(p.OS, "macOS") {
					// Tailscale SSH runs only on Linux servers; a Mac is reached
					// through its own SSH server, with papaya's key.
					mv.Method = setup.MethodKey
				}
				if m, ok := a.d.Store.Machine(name); ok {
					mv.Selected = true
					mv.User = m.User
					if m.Method != "" {
						mv.Method = m.Method
					}
					mv.Ready = m.Ready
				}
				if j, ok := a.joinOf(name); ok {
					mv.Joined, mv.SSHD, mv.JoinedAt = true, j.SSHD, j.At
					mv.Selected = true
					if mv.User == "" {
						mv.User = j.User
					}
				}
				mv.New = !seen[strings.ToLower(name)] && !mv.Selected
				if mv.New && st.Complete {
					v.NewMachines++
				}
				v.Machines = append(v.Machines, mv)
			}
		}
	}
	// Online first, then the ones already chosen, then by name: what can be
	// set up now sits at the top.
	sort.Slice(v.Machines, func(i, j int) bool {
		mi, mj := v.Machines[i], v.Machines[j]
		if mi.Online != mj.Online {
			return mi.Online
		}
		if mi.Selected != mj.Selected {
			return mi.Selected
		}
		return mi.Name < mj.Name
	})
	return v
}

type machineChoice struct {
	Name   string `json:"name"`
	FQDN   string `json:"fqdn"`
	User   string `json:"user"`
	Method string `json:"method"`
}

// putSetupMachines saves which machines to use and how. A machine whose account
// or method changed has to pass its test again.
func (s *Server) putSetupMachines(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	if s.setup == nil {
		writeError(w, http.StatusNotFound, "setup is not available")
		return
	}
	var req struct {
		Machines []machineChoice `json:"machines"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	var bad []string
	for i := range req.Machines {
		c := &req.Machines[i]
		c.Name, c.User = strings.TrimSpace(c.Name), strings.TrimSpace(c.User)
		if c.Method != setup.MethodKey {
			c.Method = setup.MethodTailscale
		}
		if c.User != "" && s.setup.d.SSHUserAllowed != nil && !s.setup.d.SSHUserAllowed(c.User) {
			bad = append(bad, c.Name)
		}
	}
	if len(bad) > 0 {
		writeError(w, http.StatusBadRequest, "This Papaya only allows certain accounts (PAPAYA_ALLOWED_SSH_USERS); not "+
			strings.Join(bad, ", ")+".")
		return
	}

	peers := s.setup.peerNames(r.Context())
	err := s.setup.d.Store.Update(func(st *setup.Settings) {
		prev := st.Machines
		st.Machines = nil
		for _, c := range req.Machines {
			if c.Name == "" {
				continue
			}
			m := setup.Machine{Name: c.Name, FQDN: c.FQDN, User: c.User, Method: c.Method}
			for _, p := range prev {
				if strings.EqualFold(p.Name, c.Name) && p.User == c.User && p.Method == c.Method {
					m.Ready = p.Ready
				}
			}
			st.Machines = append(st.Machines, m)
		}
		st.Seen = mergeSeen(st.Seen, peers)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save: "+err.Error())
		return
	}
	// A machine taken out of setup forgets its one-line join too.
	keep := map[string]bool{}
	for _, c := range req.Machines {
		keep[strings.ToLower(c.Name)] = true
	}
	s.setup.mu.Lock()
	for name := range s.setup.joins {
		if !keep[name] {
			delete(s.setup.joins, name)
		}
	}
	s.setup.mu.Unlock()
	writeJSON(w, http.StatusOK, s.setup.view(r.Context()))
}

func (s *Server) completeSetup(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	if s.setup == nil {
		writeError(w, http.StatusNotFound, "setup is not available")
		return
	}
	var req struct {
		Complete bool `json:"complete"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	peers := s.setup.peerNames(r.Context())
	if err := s.setup.d.Store.Update(func(st *setup.Settings) {
		st.Complete = req.Complete
		st.Seen = mergeSeen(st.Seen, peers)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.setup.view(r.Context()))
}

// seenSetup marks every current machine as seen, clearing the "new machine"
// hint.
func (s *Server) seenSetup(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	if s.setup == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	peers := s.setup.peerNames(r.Context())
	if err := s.setup.d.Store.Update(func(st *setup.Settings) { st.Seen = mergeSeen(st.Seen, peers) }); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *setupAPI) peerNames(ctx context.Context) []string {
	if a.d.Peers == nil {
		return nil
	}
	st, err := a.d.Peers.Status(ctx)
	if err != nil || st == nil {
		return nil
	}
	var out []string
	for _, p := range st.Peer {
		if p == nil {
			continue
		}
		if n := shortName(strings.TrimSuffix(p.DNSName, "."), p.HostName); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func mergeSeen(seen, add []string) []string {
	have := map[string]bool{}
	for _, n := range seen {
		have[strings.ToLower(n)] = true
	}
	for _, n := range add {
		if !have[strings.ToLower(n)] {
			have[strings.ToLower(n)] = true
			seen = append(seen, n)
		}
	}
	sort.Strings(seen)
	return seen
}

// TestResult is the outcome of a test connection, phrased for a person.
type TestResult struct {
	// Status is "ok", "trust" (a first-time host key to confirm) or "error".
	Status string `json:"status"`
	// Code names the kind of failure, for the screen to pick its hint.
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	KeyType     string `json:"keyType,omitempty"`
	// URL is a link the person may need, such as a Tailscale approval page.
	URL string `json:"url,omitempty"`
}

var urlRE = regexp.MustCompile(`https://\S+`)

// testSetupMachine tries a real SSH login to one machine and reports what
// happened in words a person can act on.
func (s *Server) testSetupMachine(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	if s.setup == nil {
		writeError(w, http.StatusNotFound, "setup is not available")
		return
	}
	var req struct {
		Host   string `json:"host"`
		User   string `json:"user"`
		Method string `json:"method"`
		// Trust is a host key fingerprint the person has seen and confirmed.
		Trust string `json:"trust"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Host, req.User = strings.TrimSpace(req.Host), strings.TrimSpace(req.User)
	if req.Host == "" {
		writeError(w, http.StatusBadRequest, "host is required")
		return
	}
	if req.Method != setup.MethodKey {
		req.Method = setup.MethodTailscale
	}

	a := s.setup
	a.mu.Lock()
	if a.testing[req.Host] {
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, TestResult{Status: "error", Code: "busy",
			Message: "Already testing " + req.Host + "; one moment."})
		return
	}
	a.testing[req.Host] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.testing, req.Host)
		a.mu.Unlock()
	}()

	res := a.test(r.Context(), req.Host, req.User, req.Method, req.Trust)
	if res.Status == "ok" {
		if err := a.d.Store.Update(func(st *setup.Settings) {
			for i := range st.Machines {
				if strings.EqualFold(st.Machines[i].Name, req.Host) {
					st.Machines[i].Ready = true
					st.Machines[i].User = req.User
					st.Machines[i].Method = req.Method
					return
				}
			}
			st.Machines = append(st.Machines, setup.Machine{
				Name: req.Host, User: req.User, Method: req.Method, Ready: true})
		}); err != nil {
			log.Printf("setup: saving test result for %s: %v", req.Host, err)
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *setupAPI) test(ctx context.Context, host, user, method, trust string) TestResult {
	if user == "" {
		return TestResult{Status: "error", Code: "no-user",
			Message: "Type the account you log in with on " + host + "."}
	}
	if a.d.SSHUserAllowed != nil && !a.d.SSHUserAllowed(user) {
		return TestResult{Status: "error", Code: "not-allowed",
			Message: "This Papaya only allows certain accounts (PAPAYA_ALLOWED_SSH_USERS), and " + user + " is not one of them."}
	}

	cfg := a.d.SSH
	cfg.DialTimeout = 10 * time.Second
	// A Tailscale SSH policy in "check" mode answers with a browser approval
	// prompt and waits. The screen should explain that rather than hang.
	cfg.InteractiveTimeout = 6 * time.Second
	var promptMu sync.Mutex
	var prompt string
	cfg.OnPrompt = func(instruction string, questions []string) {
		promptMu.Lock()
		prompt += instruction + " " + strings.Join(questions, " ")
		promptMu.Unlock()
	}
	if trust != "" {
		cfg.TrustOnFirstUse = func(hostname string, key ssh.PublicKey) bool {
			if ssh.FingerprintSHA256(key) != trust {
				return false
			}
			if err := sshx.TrustHostKey(a.d.SSH.TrustedHostsPath, hostname, key); err != nil {
				log.Printf("setup: saving host key for %s: %v", hostname, err)
				return false
			}
			log.Printf("setup: trusted host key %s for %s", ssh.FingerprintSHA256(key), hostname)
			return true
		}
	}

	dctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client, err := sshx.Dial(dctx, a.d.Dialer, host, user, cfg)
	if err == nil {
		err = runTrue(client)
		client.Close()
		if err != nil {
			return TestResult{Status: "error", Code: "shell",
				Message: fmt.Sprintf("Papaya got into %s, but couldn't start a shell as %q: %v. "+
					"Check the account exists there.", host, user, err)}
		}
		return TestResult{Status: "ok", Message: host + " is ready."}
	}

	promptMu.Lock()
	p := prompt
	promptMu.Unlock()
	return explain(err, host, user, method, p)
}

// runTrue opens a session and runs a no-op, proving the account can get a
// shell — Tailscale SSH can accept a login for an account that does not exist.
func runTrue(c *ssh.Client) error {
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	done := make(chan error, 1)
	go func() { done <- sess.Run("true") }()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		return errors.New("timed out")
	}
}

// explain turns an SSH failure into one sentence and the thing to do about it.
func explain(err error, host, user, method, prompt string) TestResult {
	var unknown *sshx.UnknownHostError
	if errors.As(err, &unknown) {
		return TestResult{Status: "trust", Code: "new-host",
			Fingerprint: unknown.Fingerprint(), KeyType: unknown.Key.Type(),
			Message: "First time connecting to " + host + ". Check this is the machine you expect, then trust it."}
	}

	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "host key mismatch"):
		return TestResult{Status: "error", Code: "host-changed",
			Message: host + "'s identity has changed since you trusted it. If you reinstalled it, " +
				"remove its old entry from papaya's known_hosts and test again; otherwise, don't connect."}

	case strings.Contains(prompt, "login.tailscale.com") ||
		(strings.Contains(low, "timed out with the server silent") && method == setup.MethodTailscale):
		res := TestResult{Status: "error", Code: "check-mode",
			Message: host + " asks for a browser re-login before every connection (Tailscale SSH \"check\" mode). " +
				"Paste the policy snippet into your Tailscale access controls so it says \"accept\", then test again."}
		if u := urlRE.FindString(prompt); u != "" {
			res.URL = u
		}
		return res

	case strings.Contains(low, "tailnet dial") && strings.Contains(low, "refused"):
		hint := "run `sudo tailscale set --ssh` on it"
		if method == setup.MethodKey {
			hint = "turn on its SSH server (on a Mac: System Settings, General, Sharing, Remote Login)"
		}
		return TestResult{Status: "error", Code: "refused",
			Message: "Nothing answered SSH on " + host + ". To fix it, " + hint + ", then test again."}

	case strings.Contains(low, "tailnet dial"):
		return TestResult{Status: "error", Code: "offline",
			Message: host + " can't be reached. Make sure it's switched on and connected to Tailscale, then test again."}

	case strings.Contains(low, "unable to authenticate") || strings.Contains(low, "no supported methods"):
		if method == setup.MethodKey {
			return TestResult{Status: "error", Code: "denied",
				Message: host + " didn't accept Papaya's key for " + user + ". Run the command shown above on " +
					host + " while logged in as " + user + ", then test again."}
		}
		return TestResult{Status: "error", Code: "denied",
			Message: host + " said no to Papaya as " + user + ". Check that " + user + " is your account on " +
				host + " and the policy snippet is saved, then test again. If " + host +
				" can't run Tailscale SSH, switch it to \"Papaya key\"."}

	case strings.Contains(low, "timed out") || strings.Contains(low, "i/o timeout"):
		return TestResult{Status: "error", Code: "timeout",
			Message: host + " didn't answer in time. Check it's awake and on Tailscale, then test again."}
	}
	return TestResult{Status: "error", Code: "other", Message: "Couldn't connect to " + host + ": " + msg}
}
