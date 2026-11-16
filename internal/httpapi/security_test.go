package httpapi

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func req(t *testing.T, method, host, origin string, https bool, secFetch string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "/api/sessions", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if secFetch != "" {
		r.Header.Set("Sec-Fetch-Site", secFetch)
	}
	if https {
		r.TLS = &tls.ConnectionState{}
	}
	return r
}

func TestSameOrigin(t *testing.T) {
	allowed := map[string]bool{"papaya": true}

	cases := []struct {
		name     string
		host     string
		origin   string
		https    bool
		secFetch string
		want     bool
	}{
		{"exact match over TLS", "papaya.ts.net", "https://papaya.ts.net", true, "", true},
		{"exact match plain", "papaya:8099", "http://papaya:8099", false, "", true},
		{"no origin is a non-browser client", "papaya.ts.net", "", true, "", true},
		{"configured hostname", "100.64.0.1", "https://papaya", true, "", true},

		{"different host", "papaya.ts.net", "https://evil.com", true, "", false},
		{"subdomain of the attacker", "papaya.ts.net", "https://papaya.evil.com", true, "", false},
		// A sibling service on the same machine is a different origin, and on a
		// tailnet one node routinely serves several ports.
		{"same host, different port", "papaya.ts.net", "https://papaya.ts.net:8443", true, "", false},
		{"same host, plain vs TLS", "papaya.ts.net", "http://papaya.ts.net", true, "", false},
		{"garbage origin", "papaya.ts.net", "://///", true, "", false},
		{"null origin", "papaya.ts.net", "null", true, "", false},

		// Sec-Fetch-Site is set by the browser and cannot be forged from script.
		{"browser says cross-site", "papaya.ts.net", "https://papaya.ts.net", true, "cross-site", false},
		{"browser says same-site is still cross-origin", "papaya.ts.net", "https://papaya.ts.net", true, "same-site", false},
		{"browser says same-origin", "papaya.ts.net", "https://evil.com", true, "same-origin", true},
		{"browser says none (typed in the bar)", "papaya.ts.net", "", true, "none", true},

		// IPv6 hosts arrive bracketed in Host and unbracketed from url.Hostname.
		{"ipv6 exact", "[fd7a::1]:443", "https://[fd7a::1]", true, "", true},
		{"ipv6 different address", "[fd7a::1]:443", "https://[fd7a::2]", true, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameOrigin(req(t, http.MethodPost, tc.host, tc.origin, tc.https, tc.secFetch), allowed)
			require.Equal(t, tc.want, got)
		})
	}
}

// Read-only methods are left alone: without CORS headers a hostile page cannot
// read the response, and none of them change state.
func TestCSRFGuardAllowsReads(t *testing.T) {
	var reached bool
	h := csrfGuard([]string{"papaya"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))

	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		reached = false
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(t, m, "papaya", "https://evil.com", true, "cross-site"))
		require.Truef(t, reached, "%s must not be blocked", m)
	}
}

func TestCSRFGuardBlocksWrites(t *testing.T) {
	h := csrfGuard([]string{"papaya"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(t, m, "papaya", "https://evil.com", true, ""))
		require.Equalf(t, http.StatusForbidden, w.Code, "%s from a foreign origin must be refused", m)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(t, http.MethodGet, "papaya", "", true, ""))

	head := w.Header()
	require.Equal(t, "DENY", head.Get("X-Frame-Options"))
	require.Equal(t, "nosniff", head.Get("X-Content-Type-Options"))
	require.Equal(t, "no-referrer", head.Get("Referrer-Policy"))
	require.Contains(t, head.Get("Content-Security-Policy"), "frame-ancestors 'none'")
	require.Contains(t, head.Get("Content-Security-Policy"), "object-src 'none'")
	// The page needs the microphone; nothing else does.
	require.Contains(t, head.Get("Permissions-Policy"), "microphone=(self)")
	require.Contains(t, head.Get("Permissions-Policy"), "camera=()")
	// HSTS only when actually served over TLS.
	require.NotEmpty(t, head.Get("Strict-Transport-Security"))

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, http.MethodGet, "papaya", "", false, ""))
	require.Empty(t, w.Header().Get("Strict-Transport-Security"),
		"asserting HSTS over plain HTTP would pin browsers to a scheme that does not answer")
}

func TestAPIResponsesAreNotCached(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	h.ServeHTTP(w, r)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(3, time.Minute)
	now := time.Now()

	for i := 0; i < 3; i++ {
		require.Truef(t, l.allow("alice", now), "request %d should be within budget", i+1)
	}
	require.False(t, l.allow("alice", now), "the fourth exceeds the budget")

	// The budget is per identity.
	require.True(t, l.allow("someone-else", now))

	// And it recovers once the window passes.
	require.True(t, l.allow("alice", now.Add(2*time.Minute)))
}

func TestRateLimiterDoesNotGrowForever(t *testing.T) {
	l := newRateLimiter(5, time.Second)
	start := time.Now()

	for i := 0; i < 500; i++ {
		l.allow(string(rune('a'+i%26))+string(rune('a'+i/26)), start)
	}
	before := len(l.seen)
	require.Greater(t, before, 0)

	// Well after the window, a fresh call sweeps the idle keys.
	l.allow("trigger", start.Add(time.Hour))
	require.Lessf(t, len(l.seen), before,
		"idle identities must be reclaimed rather than accumulating forever")
}
