package tsnode

import (
	"context"
	"log"
	"net/http"
	"strings"

	"tailscale.com/client/tailscale/apitype"
)

type ctxKey int

const (
	ctxUserKey ctxKey = iota
	ctxNodeKey
	ctxRoleKey
)

// Role is how much of papaya a caller may use.
type Role int

const (
	// RoleAdmin is everyone papaya was configured for (its owner, or
	// PAPAYA_ALLOWED_USERS / PAPAYA_ALLOWED_TAGS): full access, as always.
	RoleAdmin Role = iota
	// RoleMember is a teammate added by an admin: their own tabs, on their own
	// machines, and no settings.
	RoleMember
)

// MemberLookup matches a caller who is not an admin to a team member,
// returning the identity their tabs belong to.
type MemberLookup func(login string, tags []string) (string, bool)

// WhoIser resolves a tailnet remote address to an identity. *local.Client
// implements it; tests substitute a fake.
type WhoIser interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

// Auth authorizes requests by tailnet identity.
//
// There is no password, cookie, or token anywhere in papaya. The tailnet is the
// authentication layer; this middleware is the authorization layer on top of it.
type Auth struct {
	lc           WhoIser
	allowedUsers map[string]bool
	allowedTags  map[string]bool
	// members is consulted only for callers who are not admins. Nil means no
	// team, which is exactly the behaviour before teams existed.
	members MemberLookup
}

// SetMembers enables team members. Admins are always matched first, so a
// member entry can never narrow, or widen, an admin's access.
func (a *Auth) SetMembers(fn MemberLookup) { a.members = fn }

// IsAdmin reports whether a login (or tag) is one of the admin identities.
func (a *Auth) IsAdmin(login string) bool {
	if strings.HasPrefix(login, "tag:") {
		return a.allowedTags[login]
	}
	for u := range a.allowedUsers {
		if strings.EqualFold(u, login) {
			return true
		}
	}
	return false
}

// NewAuth builds the middleware. Both maps being empty is rejected in config
// validation, not here — by this point the access list is known non-empty.
func NewAuth(lc WhoIser, users, tags map[string]bool) *Auth {
	return &Auth{lc: lc, allowedUsers: users, allowedTags: tags}
}

// Middleware rejects any request whose tailnet identity is not on the list.
// It is applied to every route except /healthz.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := a.lc.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			log.Printf("auth: whois %s failed: %v", r.RemoteAddr, err)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		login, role, ok := a.identify(who)
		if !ok {
			log.Printf("auth: denied login=%q tags=%v path=%s", login, tagsOf(who), r.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserKey, login)
		ctx = context.WithValue(ctx, ctxRoleKey, role)
		if who.Node != nil {
			c := Caller{Name: strings.TrimSuffix(who.Node.Name, "."), Tagged: len(who.Node.Tags) > 0}
			if who.Node.Hostinfo.Valid() {
				c.Hostname = who.Node.Hostinfo.Hostname()
			}
			ctx = context.WithValue(ctx, ctxNodeKey, c)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// identify returns the owner string to attribute sessions to, the caller's
// role, and whether the caller is authorized at all.
//
// A machine calling papaya (papaya-say, papaya-status, the join script) is
// identified the same way as a person: by the tailnet account that machine is
// logged in as, or its tags. So a teammate's machine, logged in as the
// teammate, gets the teammate's member scope and never more.
func (a *Auth) identify(who *apitype.WhoIsResponse) (string, Role, bool) {
	if who == nil {
		return "", RoleAdmin, false
	}

	var login string
	if who.UserProfile != nil {
		login = who.UserProfile.LoginName
	}
	if login != "" && a.allowedUsers[login] {
		return login, RoleAdmin, true
	}

	// Tagged nodes have no user login, so they are matched on tag instead and
	// the tag becomes the session owner.
	for _, t := range tagsOf(who) {
		if a.allowedTags[t] {
			return t, RoleAdmin, true
		}
	}

	if a.members != nil {
		// A tagged node belongs to no person: Tailscale reports a placeholder
		// profile for it, which must not be matched as a member's login.
		tagged := len(tagsOf(who)) > 0
		var memberLogin string
		if !tagged {
			memberLogin = login
		}
		if key, ok := a.members(memberLogin, tagsOf(who)); ok {
			return key, RoleMember, true
		}
	}
	return login, RoleAdmin, false
}

func tagsOf(who *apitype.WhoIsResponse) []string {
	if who == nil || who.Node == nil {
		return nil
	}
	return who.Node.Tags
}

// RoleFrom returns the caller's role attached by Middleware. A context without
// one (only possible in tests, which inject a user directly) is an admin, which
// is what every caller was before teams existed.
func RoleFrom(ctx context.Context) Role {
	r, ok := ctx.Value(ctxRoleKey).(Role)
	if !ok {
		return RoleAdmin
	}
	return r
}

// WithRole injects a role. Test-only helper.
func WithRole(ctx context.Context, r Role) context.Context {
	return context.WithValue(ctx, ctxRoleKey, r)
}

// UserFrom returns the authenticated identity attached by Middleware.
func UserFrom(ctx context.Context) (string, bool) {
	u, ok := ctx.Value(ctxUserKey).(string)
	return u, ok && u != ""
}

// Caller is the tailnet machine a request came from, as Tailscale reports it.
// Name is its MagicDNS name (e.g. "macbook.tail1234.ts.net").
type Caller struct {
	Name     string
	Hostname string
	Tagged   bool
}

// CallerFrom returns the machine a request came from, attached by Middleware.
func CallerFrom(ctx context.Context) (Caller, bool) {
	n, ok := ctx.Value(ctxNodeKey).(Caller)
	return n, ok && (n.Name != "" || n.Hostname != "")
}

// WithCaller injects a calling machine. Test-only helper.
func WithCaller(ctx context.Context, n Caller) context.Context {
	return context.WithValue(ctx, ctxNodeKey, n)
}

// WithUser injects an identity. Test-only helper.
func WithUser(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, ctxUserKey, user)
}
