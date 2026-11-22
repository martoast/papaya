package httpapi

import (
	"log"
	"net/http"
	"strings"

	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/setup"
	"github.com/martoast/papaya/internal/tsnode"
)

// Teams.
//
// An admin (whoever papaya was configured for) has full access, as always. A
// member is a teammate an admin added in the Team panel: they see and use only
// their own tabs, only on the machines the admin gave them, and can change no
// setting. Everything here is enforced on the server for every request; the
// interface only hides what the server would refuse anyway.
//
// The machine list is read from the settings on every request, so taking a
// machine away (or removing the member) cuts off their very next request,
// including tabs they already have open on it.

// scope is who is asking and what they may reach.
type scope struct {
	user   string
	member bool
	// machines is a member's allowed machines; meaningless for an admin.
	machines []string
}

// scopeOf resolves the caller's scope, writing a 403 if there is none.
func (s *Server) scopeOf(w http.ResponseWriter, r *http.Request) (scope, bool) {
	user, ok := owner(w, r)
	if !ok {
		return scope{}, false
	}
	sc := scope{user: user}
	if tsnode.RoleFrom(r.Context()) == tsnode.RoleMember {
		sc.member = true
		if st := s.store(); st != nil {
			sc.machines = st.MemberMachines(user)
		}
	}
	return sc, true
}

// requireAdmin admits only admins. Members get the same 403 an unknown caller
// would, so settings endpoints reveal nothing to them.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return false
	}
	if sc.member {
		log.Printf("denied: member %s called %s %q, which is for admins", sc.user, r.Method, r.URL.Path)
		writeError(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

// machine resolves host to one of a member's machines (the name papaya dials).
// For an admin every host passes unchanged.
func (s *Server) machine(sc scope, host string) (string, bool) {
	if !sc.member {
		return host, true
	}
	st := s.store()
	if st == nil || len(sc.machines) == 0 {
		return "", false
	}
	return st.MachineFor(sc.machines, host)
}

// isTeamMember reports whether owner is currently on the team, so an admin is
// shown teammates' tabs but never another admin's.
func (s *Server) isTeamMember(owner string) bool {
	st := s.store()
	return st != nil && st.IsMember(owner)
}

// sees reports whether the caller may reach sess.
func (s *Server) sees(sc scope, sess *hub.Session) bool {
	if sc.member {
		if sess.Owner != sc.user {
			return false
		}
		_, ok := s.machine(sc, sess.Host)
		return ok
	}
	return sess.Owner == sc.user || s.isTeamMember(sess.Owner)
}

// sessionsFor lists the tabs the caller may reach. An admin's own come first,
// so a tab name they share with a teammate's resolves to their own.
func (s *Server) sessionsFor(sc scope) []*hub.Session {
	if sc.member {
		out := []*hub.Session{}
		for _, sess := range s.hub.List(sc.user) {
			if s.sees(sc, sess) {
				out = append(out, sess)
			}
		}
		return out
	}
	own := s.hub.List(sc.user)
	team := s.hub.ListOwners(func(o string) bool { return o != sc.user && s.isTeamMember(o) })
	return append(own, team...)
}

// sessionFor finds one tab the caller may reach; anything else is not-found.
func (s *Server) sessionFor(sc scope, id string) (*hub.Session, error) {
	return s.hub.GetFunc(id, func(sess *hub.Session) bool { return s.sees(sc, sess) })
}

// viewFor is a tab as the caller sees it: an admin is told whose a teammate's
// tab is.
func viewFor(sc scope, sess *hub.Session) hub.View {
	v := sess.View()
	if !sc.member && sess.Owner != sc.user {
		v.Owner = sess.Owner
	}
	return v
}

// memberTarget applies a member's limits to a host and account named in a
// request: the host must be one of their machines (and is replaced by its
// canonical name), and the account must be the one that machine is set up
// with. ok=false means refuse with msg.
func (s *Server) memberTarget(sc scope, host, user string) (string, string, string, bool) {
	if !sc.member {
		return host, user, "", true
	}
	canon, ok := s.machine(sc, host)
	if !ok {
		log.Printf("denied: member %s asked for host %q, which is not one of theirs", sc.user, host)
		return "", "", "that machine is not one of yours", false
	}
	acct := s.sshUserFor(canon)
	if strings.TrimSpace(user) != "" && user != acct {
		log.Printf("denied: member %s asked for account %q on %s", sc.user, user, canon)
		return "", "", "that account is not permitted", false
	}
	return canon, acct, "", true
}

// store is the settings file, where the team lives; nil when setup is off.
func (s *Server) store() *setup.Store {
	if s.setup == nil {
		return nil
	}
	return s.setup.d.Store
}
