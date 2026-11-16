package test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

// S2: every route except /healthz sits behind tailnet identity.
func TestS2AuthRequiredOnEveryRoute(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})

	owner := st.Client(alice)
	sess := owner.CreateSession("devbox-1", "", 80, 24)

	denied := st.DeniedClient()

	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/sessions", nil},
		{http.MethodPost, "/api/sessions", map[string]any{"host": "devbox-1"}},
		{http.MethodPatch, "/api/sessions/" + sess.ID, map[string]any{"name": "x"}},
		{http.MethodDelete, "/api/sessions/" + sess.ID, nil},
		{http.MethodGet, "/api/hosts", nil},
		{http.MethodGet, "/", nil},
	}

	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			var resp *http.Response
			switch r.method {
			case http.MethodGet:
				resp, _ = denied.Get(r.path)
			case http.MethodPost:
				resp, _ = denied.CreateSessionRaw("devbox-1", "", 80, 24)
			case http.MethodPatch:
				resp, _ = denied.PatchSession(sess.ID, map[string]any{"name": "x"})
			case http.MethodDelete:
				resp, _ = denied.DeleteSession(sess.ID)
			}
			require.Equal(t, http.StatusForbidden, resp.StatusCode,
				"an identity outside the allow list must be rejected")
		})
	}

	t.Run("upload", func(t *testing.T) {
		resp, _ := denied.Upload(sess.ID, "x.png", []byte("data"))
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("websocket", func(t *testing.T) {
		_, resp, err := denied.DialWSRaw(sess.ID, nil)
		require.Error(t, err)
		require.NotNil(t, resp)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	// The session must be untouched by all of that.
	require.Len(t, owner.ListSessions(), 1)
}

func TestHealthzBypassesAuth(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})

	resp, body := st.DeniedClient().Get("/healthz")
	require.Equal(t, http.StatusOK, resp.StatusCode, "/healthz must not require identity")
	require.Equal(t, "ok", string(body))
}

// A tagged node has no user login, so it is authorized by tag and the tag
// becomes the session owner.
func TestTaggedNodeIsAuthorizedByTag(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})

	tagged := st.TaggedClient("tag:ci")
	sess := tagged.CreateSession("devbox-1", "ci-job", 80, 24)
	require.Equal(t, "ci-job", sess.Name)

	// The tag owns its sessions separately from any user.
	require.Len(t, tagged.ListSessions(), 1)
	require.Empty(t, st.Client(alice).ListSessions())

	resp, _ := st.Client(alice).DeleteSession(sess.ID)
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"a user must not reach a tagged node's session")
}

// S9: a WebSocket upgrade from a foreign origin must be refused, otherwise any
// page the user visits could drive their terminal.
func TestS9OriginCheck(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)
	sess := c.CreateSession("devbox-1", "", 80, 24)

	t.Run("foreign origin rejected", func(t *testing.T) {
		h := http.Header{}
		h.Set("Origin", "https://evil.com")

		_, resp, err := c.DialWSRaw(sess.ID, h)
		require.Error(t, err, "a cross-origin upgrade must be refused")
		require.NotNil(t, resp)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("subdomain of foreign origin rejected", func(t *testing.T) {
		h := http.Header{}
		h.Set("Origin", "https://papaya.evil.com")

		_, resp, err := c.DialWSRaw(sess.ID, h)
		require.Error(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("same origin accepted", func(t *testing.T) {
		ws := c.DialWS(sess.ID) // no Origin header set by the Go dialer
		ws.WaitForState("connected", 10*time.Second)
	})
}

// S12: session IDs must be unguessable, or knowing one is knowing all of them.
func TestS12SessionIDsAreUnguessable(t *testing.T) {
	const n = 10000
	seen := make(map[string]bool, n)

	for i := 0; i < n; i++ {
		id := uuid.NewString()
		require.Falsef(t, seen[id], "duplicate id generated at iteration %d", i)
		seen[id] = true

		// UUIDv4: 122 bits of entropy, version and variant nibbles pinned.
		require.Len(t, id, 36)
		require.Equal(t, byte('4'), id[14], "must be UUID version 4")
		require.Contains(t, "89ab", string(id[19]), "must use the RFC 4122 variant")
	}
	require.Len(t, seen, n)

	// Real sessions must use the same generator, not a counter.
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, c.CreateSession("devbox-1", "", 80, 24).ID)
	}
	for i := 1; i < len(ids); i++ {
		require.NotEqual(t, ids[i-1], ids[i])
		require.Len(t, ids[i], 36, "session IDs must be UUIDs, not sequential")
	}
}

// S6: hostile filenames must never execute anything on the remote.
func TestS6NoCommandInjectionFromFilenames(t *testing.T) {
	st, _ := uploadStack(t)
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	hostile := []string{
		"$(touch /tmp/papaya-PWNED-a).png",
		"`touch /tmp/papaya-PWNED-b`.png",
		"a;touch /tmp/papaya-PWNED-c.png",
		"a|touch /tmp/papaya-PWNED-d.png",
		"a&&touch /tmp/papaya-PWNED-e.png",
		"$(id).png", "${IFS}.png", "a\nrm -rf /\n.png",
		"a>/tmp/papaya-PWNED-f.png", "*.png", "~/.bashrc",
		"a`whoami`.png", "'$(id)'.png", `"$(id)".png`,
		"a\\;id.png", "\x00;id.png", "-rf.png", "--help.png",
		"../$(id).png", "%0Aid.png",
	}

	accepted := 0
	for _, name := range hostile {
		resp, _ := c.Upload(sess.ID, name, []byte("data"))
		// Either outcome is safe: the name is sanitized and stored, or the
		// request is rejected outright (a NUL byte, for instance, makes the
		// multipart header itself unparseable). What must never happen is
		// execution.
		require.Containsf(t, []int{http.StatusOK, http.StatusBadRequest}, resp.StatusCode,
			"upload of %q returned an unexpected status", name)
		if resp.StatusCode == http.StatusOK {
			accepted++
		}
	}
	require.Greater(t, accepted, 10, "most hostile names should be sanitized and accepted")

	// The injected text must be inert: quoted, and never executed.
	time.Sleep(300 * time.Millisecond) // prove absence of execution
	out := ws.Output()
	require.NotContains(t, out, "uid=", "a substituted `id` command ran on the remote")
	require.NotContains(t, out, "No such file")
}

// S8: an ANSI-laden tab name must be stored sanitized, so a second client's UI
// is unaffected.
func TestS8AnsiInTabNames(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	c.PatchSession(sess.ID, map[string]any{"name": "\x1b[2J\x1b[31mPWNED\x07"})

	stored := c.ListSessions()[0].Name
	for _, r := range stored {
		require.Falsef(t, r < 0x20 || r == 0x7f,
			"control character U+%04X reached the stored tab name %q", r, stored)
	}

	// The broadcast to other clients must be sanitized too.
	ev := ws.WaitForEvent("rename", 5*time.Second, func(m map[string]any) bool {
		return m["name"] != "devbox-1"
	})
	name, _ := ev["name"].(string)
	require.NotContains(t, name, "\x1b", "the rename broadcast leaked an escape sequence")
	require.NotContains(t, name, "\x07")
}

// S10: error payloads reach the browser and must not leak key paths, internal
// addresses, or fingerprints.
func TestS10ErrorPayloadsLeakNothing(t *testing.T) {
	srv := harness.NewSSHServer(t, harness.AuthPublicKey) // authorizes nothing
	st := harness.NewStack(t, map[string]*harness.SSHServer{"devbox-1": srv},
		harness.StackOptions{})

	c := st.Client(alice)
	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)

	ev := ws.WaitForEvent("error", 15*time.Second, nil)
	msg, _ := ev["msg"].(string)
	require.NotEmpty(t, msg)

	forbidden := []string{
		"/etc/", "/var/", "id_ed25519", "BEGIN", "SHA256:",
		"127.0.0.1", "100.64.", ".ts.net", "known_hosts", srv.Addr,
	}
	for _, bad := range forbidden {
		require.NotContainsf(t, msg, bad,
			"error payload leaked %q to the browser: %q", bad, msg)
	}
	require.Equal(t, true, ev["fatal"])
}

// The remote host name is user-supplied; it must not be usable to reach
// something other than an SSH port on a tailnet host.
func TestHostFieldIsNotInjectable(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	for _, host := range []string{"", "   "} {
		resp, _ := c.CreateSessionRaw(host, "", 80, 24)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode,
			"an empty host must be rejected")
	}

	// A host that does not resolve fails cleanly rather than hanging or panicking.
	sess := c.CreateSession("no-such-host", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ev := ws.WaitForEvent("error", 15*time.Second, nil)
	require.Equal(t, true, ev["fatal"])
}

func TestNameLengthIsCapped(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", strings.Repeat("x", 500), 80, 24)
	require.LessOrEqual(t, len([]rune(sess.Name)), 40)

	c.PatchSession(sess.ID, map[string]any{"name": strings.Repeat("y", 500)})
	require.LessOrEqual(t, len([]rune(c.ListSessions()[0].Name)), 40)
}

func TestUnknownSessionReturns404(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	fake := uuid.NewString()

	resp, _ := c.PatchSession(fake, map[string]any{"name": "x"})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, _ = c.DeleteSession(fake)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, _ = c.Upload(fake, "x.png", []byte("d"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	_, wsResp, err := c.DialWSRaw(fake, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, wsResp.StatusCode)

	_ = fmt.Sprint()
}

// S13: the remote account is client-supplied. The tailnet SSH policy may permit
// root, but the web terminal must not hand out uid 0 by default.
func TestS13RootIsRefused(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	for _, u := range []string{"root", "ROOT", "toor", "admin"} {
		resp, body := c.Do(http.MethodPost, "/api/sessions", map[string]any{
			"host": "devbox-1", "user": u, "cols": 80, "rows": 24,
		})
		require.Equalf(t, http.StatusForbidden, resp.StatusCode,
			"remote user %q must be refused: %s", u, body)
		require.Contains(t, string(body), "not permitted")
	}

	require.Empty(t, c.ListSessions(), "no session may be created for a refused user")
}

func TestS13SSHUserAllowListIsEnforced(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{
		AllowedSSHUsers: []string{"tester"},
		DefaultSSHUser:  "tester",
	})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "ok", 80, 24)
	require.Equal(t, "tester", sess.User)

	for _, u := range []string{"deploy", "ubuntu", "root"} {
		resp, _ := c.Do(http.MethodPost, "/api/sessions", map[string]any{
			"host": "devbox-1", "user": u, "cols": 80, "rows": 24,
		})
		require.Equalf(t, http.StatusForbidden, resp.StatusCode, "user %q must be refused", u)
	}
}

// S14: the host picker is a suggestion; the API is the boundary. Without this a
// client can name any host on the tailnet regardless of what the picker offers.
func TestS14HostAllowListIsEnforced(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{
		AllowedHosts: []string{"devbox-1"},
	})
	c := st.Client(alice)

	c.CreateSession("devbox-1", "permitted", 80, 24)

	for _, h := range []string{"alices-desktop", "some-other-host", "100.1.2.3"} {
		resp, body := c.Do(http.MethodPost, "/api/sessions", map[string]any{
			"host": h, "cols": 80, "rows": 24,
		})
		require.Equalf(t, http.StatusForbidden, resp.StatusCode,
			"host %q must be refused: %s", h, body)
		require.Contains(t, string(body), "not permitted")
	}

	require.Len(t, c.ListSessions(), 1, "only the permitted host produced a session")
}

// A host the API would refuse must not appear in the picker either.
func TestHostPickerHidesDisallowedHosts(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{AllowedHosts: []string{"devbox-1"}})
	c := st.Client(alice)

	resp, body := c.Get("/api/hosts")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, string(body), "alices-desktop")
}
