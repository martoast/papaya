package httpapi

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/martoast/papaya/internal/setup"
	"github.com/martoast/papaya/internal/sshx"
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
	sc, ok := s.scopeOf(w, r)
	if !ok {
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
	// A teammate's machine may report itself only if the admin has already
	// given it to that teammate. It can never add itself, or touch (or pin
	// host keys for) any other machine.
	if sc.member {
		canon, ok := s.machine(sc, name)
		if !ok {
			log.Printf("denied: member %s ran the join line on %q, which is not one of their machines", sc.user, name)
			writeError(w, http.StatusForbidden, "ask the admin to add this machine to your team entry first")
			return
		}
		name = canon
	}
	var req struct {
		User string `json:"user"`
		SSHD bool   `json:"sshd"`
		OS   string `json:"os"`
		// HostKeys are the machine's own SSH host public keys, read from
		// /etc/ssh by the script. Pinning them means the first connection
		// needs no "is this the right machine?" prompt: the report came from
		// that machine, authenticated by Tailscale, moments ago.
		HostKeys []string `json:"hostkeys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.User = strings.TrimSpace(req.User)
	if req.User == "" || len(req.User) > 64 || strings.ContainsAny(req.User, " \t\r\n/@:") {
		writeError(w, http.StatusBadRequest, "that does not look like a username")
		return
	}
	if s.setup.d.SSHUserAllowed != nil && !s.setup.d.SSHUserAllowed(req.User) {
		writeError(w, http.StatusBadRequest, "this Papaya is set to only allow certain accounts (PAPAYA_ALLOWED_SSH_USERS), and "+req.User+" is not one of them")
		return
	}

	pinned := 0
	for i, line := range req.HostKeys {
		if i >= 6 {
			break
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
		if err != nil {
			continue
		}
		for _, h := range []string{name, caller.Name} {
			if h == "" {
				continue
			}
			if err := sshx.TrustHostKey(s.setup.d.SSH.TrustedHostsPath, h, key); err == nil {
				pinned++
			}
		}
	}
	if pinned > 0 {
		log.Printf("setup: %s joined as %s; pinned its host keys", name, req.User)
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

# This machine's SSH host keys (public), so Papaya knows it is talking to the
# right machine without asking you to compare fingerprints.
keys=""
for f in /etc/ssh/ssh_host_*_key.pub; do
	[ -r "$f" ] || continue
	k=$(cut -d' ' -f1,2 "$f")
	keys="$keys${keys:+,}\"$k\""
done

if ! curl -fsS -m 10 -X POST "$PAPAYA/api/setup/join" -H 'Content-Type: application/json' \
	-d "{\"user\":\"$me\",\"sshd\":$sshd,\"os\":\"$os\",\"hostkeys\":[$keys]}" >/dev/null; then
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
