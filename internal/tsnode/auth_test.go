package tsnode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

type whoIs struct {
	login string
	tags  []string
}

func (w whoIs) WhoIs(context.Context, string) (*apitype.WhoIsResponse, error) {
	return &apitype.WhoIsResponse{Node: &tailcfg.Node{Tags: w.tags}, UserProfile: &tailcfg.UserProfile{LoginName: w.login}}, nil
}

func call(t *testing.T, a *Auth) (int, string, Role) {
	t.Helper()
	var user string
	var role Role
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ = UserFrom(r.Context())
		role = RoleFrom(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	return rec.Code, user, role
}

func TestAdminsWinOverAMemberEntry(t *testing.T) {
	members := func(login string, tags []string) (string, bool) { return login, login != "" }
	a := NewAuth(whoIs{login: "alex@example.com"}, map[string]bool{"alex@example.com": true}, nil)
	a.SetMembers(members)
	code, user, role := call(t, a)
	require.Equal(t, 200, code)
	require.Equal(t, "alex@example.com", user)
	require.Equal(t, RoleAdmin, role)
}

func TestAMemberIsAMemberAndAStrangerIsNobody(t *testing.T) {
	members := func(login string, tags []string) (string, bool) { return login, login == "ariosto@example.com" }
	admins := map[string]bool{"alex@example.com": true}

	a := NewAuth(whoIs{login: "ariosto@example.com"}, admins, nil)
	a.SetMembers(members)
	code, user, role := call(t, a)
	require.Equal(t, 200, code)
	require.Equal(t, "ariosto@example.com", user)
	require.Equal(t, RoleMember, role)

	a = NewAuth(whoIs{login: "stranger@example.com"}, admins, nil)
	a.SetMembers(members)
	code, _, _ = call(t, a)
	require.Equal(t, 403, code)

	// No team configured: exactly the old behaviour.
	a = NewAuth(whoIs{login: "ariosto@example.com"}, admins, nil)
	code, _, _ = call(t, a)
	require.Equal(t, 403, code)
}

func TestATaggedNodesPlaceholderLoginIsNotAMemberLogin(t *testing.T) {
	var gotLogin string
	members := func(login string, tags []string) (string, bool) { gotLogin = login; return "", false }
	a := NewAuth(whoIs{login: "tagged-devices", tags: []string{"tag:x"}}, map[string]bool{"alex@example.com": true}, nil)
	a.SetMembers(members)
	code, _, _ := call(t, a)
	require.Equal(t, 403, code)
	require.Empty(t, gotLogin)
}

func TestIsAdmin(t *testing.T) {
	a := NewAuth(whoIs{}, map[string]bool{"Alex@example.com": true}, map[string]bool{"tag:ci": true})
	require.True(t, a.IsAdmin("alex@example.com"))
	require.True(t, a.IsAdmin("tag:ci"))
	require.False(t, a.IsAdmin("tag:other"))
	require.False(t, a.IsAdmin("ariosto@example.com"))
}
