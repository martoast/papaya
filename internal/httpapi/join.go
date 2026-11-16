package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/martoast/papaya/internal/setup"
	"github.com/martoast/papaya/internal/tsnode"
)

// Connecting a machine with one pasted line.
//
// Asking people for their username on each machine lost them: most have never
// had to know it. Setup already asked them to run one thing on each machine,
// so that one thing now does everything:
//
//	curl -fsSL http://papaya.<tailnet>.ts.net/join | sh
//
// The script (GET /join) adds Papaya's public key for whoever runs it, checks
// that the machine's SSH server is listening, and reports back
// (POST /api/setup/join) who it is. Papaya learns which machine called from
// Tailscale itself, never from the request body, and only machines that pass
// the same identity check as everything else (by default: the owner's own
// devices) are heard. The machine's card on the setup screen then turns green
// by itself.

// joined is what the last join from a machine reported.
type joined struct {
	User string    `json:"user"`
	SSHD bool      `json:"sshd"`
	OS   string    `json:"os"`
	At   time.Time `json:"at"`
}

func (s *Server) joinRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /join", s.joinScript)
	mux.HandleFunc("POST /api/setup/join", s.join)
}

// joinScript serves the script, with Papaya's address and key filled in.
func (s *Server) joinScript(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	if s.setup == nil || s.setup.d.PublicKey == "" {
		http.Error(w, "setup is not available", http.StatusNotFound)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, joinScriptTemplate, shQuote(base), shQuote(strings.TrimSpace(s.setup.d.PublicKey)))
}

// join records what a machine reported about itself.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	if s.setup == nil {
		writeError(w, http.StatusNotFound, "setup is not available")
		return
	}
	caller, ok := tsnode.CallerFrom(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "could not tell which machine this is")
		return
	}
	name := shortName(caller.Name, caller.Hostname)
	if name == "" {
		writeError(w, http.StatusForbidden, "could not tell which machine this is")
		return
	}
	var req struct {
		User string `json:"user"`
		SSHD bool   `json:"sshd"`
		OS   string `json:"os"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.User = strings.TrimSpace(req.User)
	if req.User == "" || len(req.User) > 64 || strings.ContainsAny(req.User, " \t\r\n/@:") {
		writeError(w, http.StatusBadRequest, "that does not look like a username")
		return
	}
	if s.setup.d.SSHUserAllowed != nil && !s.setup.d.SSHUserAllowed(req.User) {
		writeError(w, http.StatusBadRequest, "run it as your everyday account, not root or an admin account")
		return
	}

	err := s.setup.d.Store.Update(func(st *setup.Settings) {
		for i := range st.Machines {
			if strings.EqualFold(st.Machines[i].Name, name) {
				st.Machines[i].User = req.User
				st.Machines[i].Method = setup.MethodKey
				st.Machines[i].Ready = false
				return
			}
		}
		// Not chosen yet: running the line is choosing it.
		st.Machines = append(st.Machines, setup.Machine{Name: name, FQDN: caller.Name, User: req.User, Method: setup.MethodKey})
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save: "+err.Error())
		return
	}
	s.setup.mu.Lock()
	if s.setup.joins == nil {
		s.setup.joins = map[string]joined{}
	}
	s.setup.joins[strings.ToLower(name)] = joined{User: req.User, SSHD: req.SSHD, OS: cleanLabel(req.OS, 20), At: time.Now().UTC()}
	s.setup.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"machine": name, "user": req.User})
}

// joinOf returns what a machine last reported, if it ran the line.
func (a *setupAPI) joinOf(name string) (joined, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	j, ok := a.joins[strings.ToLower(name)]
	return j, ok
}

// shQuote quotes a string for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// joinScriptTemplate is filled with the address and the key, both shQuoted.
// Plain POSIX sh: it runs on macOS and on any Linux, piped into sh.
const joinScriptTemplate = `#!/bin/sh
# Papaya: connect this machine.
# Adds Papaya's key for your account and tells Papaya who you are.
set -u
PAPAYA=%s
KEY=%s

say() { printf '%%s\n' "$*"; }
me=$(id -un)
if [ "$me" = root ]; then
	say "Run this as your everyday account (without sudo), not as root."
	exit 1
fi

mkdir -p "$HOME/.ssh" && chmod 700 "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys" && chmod 600 "$HOME/.ssh/authorized_keys"
grep -qF "$KEY" "$HOME/.ssh/authorized_keys" 2>/dev/null || printf '%%s\n' "$KEY" >> "$HOME/.ssh/authorized_keys"

# Is an SSH server listening here? (Papaya logs in through it.)
sshd=false
if command -v nc >/dev/null 2>&1 && nc -z 127.0.0.1 22 >/dev/null 2>&1; then sshd=true
elif command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q '[:.]22 '; then sshd=true
elif command -v netstat >/dev/null 2>&1 && netstat -an 2>/dev/null | grep -i listen | grep -q '[:.]22 '; then sshd=true
elif grep -qs ':0016 [0-9A-F:]* 0A ' /proc/net/tcp /proc/net/tcp6; then sshd=true
fi
os=$(uname -s)

if ! curl -fsS -m 10 -X POST "$PAPAYA/api/setup/join" -H 'Content-Type: application/json' \
	-d "{\"user\":\"$me\",\"sshd\":$sshd,\"os\":\"$os\"}" >/dev/null; then
	say "Couldn't reach Papaya at $PAPAYA. Is Tailscale on and signed in on this machine?"
	exit 1
fi

say ""
say "  ✓ Papaya can reach this machine as $me."
if [ "$sshd" = false ]; then
	if [ "$os" = Darwin ]; then
		say "  One more thing: turn on Remote Login (it's opening now):"
		say "  System Settings → General → Sharing → Remote Login."
		open "x-apple.systempreferences:com.apple.Sharing-Settings.extension" 2>/dev/null || true
	else
		say "  One more thing: this machine needs an SSH server."
		say "  On Ubuntu or Debian: sudo apt install openssh-server"
	fi
fi
say "  Head back to Papaya; it's turning green."
say ""
`
