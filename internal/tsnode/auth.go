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
)

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

		login, ok := a.identify(who)
		if !ok {
			log.Printf("auth: denied login=%q tags=%v path=%s", login, tagsOf(who), r.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserKey, login)
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

// identify returns the owner string to attribute sessions to, and whether the
// caller is authorized at all.
func (a *Auth) identify(who *apitype.WhoIsResponse) (string, bool) {
	if who == nil {
		return "", false
	}

	var login string
	if who.UserProfile != nil {
		login = who.UserProfile.LoginName
	}
	if login != "" && a.allowedUsers[login] {
		return login, true
	}

	// Tagged nodes have no user login, so they are matched on tag instead and
	// the tag becomes the session owner.
	for _, t := range tagsOf(who) {
		if a.allowedTags[t] {
			return t, true
		}
	}
	return login, false
}

func tagsOf(who *apitype.WhoIsResponse) []string {
	if who == nil || who.Node == nil {
		return nil
	}
	return who.Node.Tags
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
