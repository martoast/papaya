package httpapi

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Authentication here is ambient: it comes from the tailnet source address, not
// from a cookie or token the page has to present. That makes every
// state-changing endpoint a cross-site request forgery target, because the
// browser attaches the caller's identity to requests the user never intended to
// make — including ones issued by a hostile page they merely visited.
//
// multipart/form-data is a CORS-safelisted content type, so a cross-origin POST
// to /upload or /api/transcribe is *sent* without a preflight. The attacker
// cannot read the response, but the side effect has already happened: a file
// planted on the remote host, a path injected at the shell cursor, credits spent.
//
// The defence is to require that state-changing requests come from this origin.

// sameOrigin reports whether a request originated from the page this server
// serves.
//
// A browser always sets Origin on a cross-origin POST, so an absent Origin means
// a non-browser client — curl, a script — which already had to reach the tailnet
// to get here and is not what CSRF protects against.
func sameOrigin(r *http.Request, allowed map[string]bool) bool {
	// Sec-Fetch-Site is set by the browser itself and cannot be forged by page
	// script, so when present it is the most reliable signal available.
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		// same-site still means a different origin (sibling subdomain).
		return false
	case "same-origin", "none":
		return true
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser
	}

	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return false
	}

	// An origin is scheme + host + port, and all three matter. Comparing host
	// alone would treat another service on the same machine as same-origin, and
	// on a tailnet a single node routinely serves several ports.
	if !strings.EqualFold(u.Scheme, requestScheme(r)) {
		return false
	}
	if portOf(u.Port(), u.Scheme) != portOf(hostPort(r.Host), requestScheme(r)) {
		return false
	}

	host := strings.ToLower(u.Hostname())
	return host == strings.ToLower(hostOnly(r.Host)) || allowed[host]
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// portOf resolves an explicit port, or the default for the scheme.
func portOf(port, scheme string) string {
	if port != "" {
		return port
	}
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}

// hostPort extracts the port from a Host header value, if it carries one.
func hostPort(h string) string {
	if strings.HasPrefix(h, "[") {
		if i := strings.LastIndex(h, "]"); i >= 0 {
			if rest := h[i+1:]; strings.HasPrefix(rest, ":") {
				return rest[1:]
			}
			return ""
		}
	}
	if i := strings.LastIndex(h, ":"); i > 0 {
		return h[i+1:]
	}
	return ""
}

// hostOnly strips a port from a host[:port], unwrapping IPv6 brackets so it can
// be compared with url.Hostname, which returns the address without them.
func hostOnly(h string) string {
	if strings.HasPrefix(h, "[") {
		if i := strings.LastIndex(h, "]"); i >= 0 {
			return h[1:i]
		}
	}
	if i := strings.LastIndex(h, ":"); i > 0 {
		return h[:i]
	}
	return h
}

// csrfGuard refuses state-changing requests that did not come from this origin.
//
// Read-only methods are left alone: without CORS headers a hostile page cannot
// read the response, and none of them change anything.
func csrfGuard(allowedHosts []string, next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedHosts))
	for _, h := range allowedHosts {
		if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
			allowed[h] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		if !sameOrigin(r, allowed) {
			log.Printf("blocked cross-origin %s %s from origin %q",
				r.Method, r.URL.Path, r.Header.Get("Origin"))
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets the response headers that constrain what a browser will
// do with this page.
func securityHeaders(next http.Handler) http.Handler {
	// Everything the app loads is bundled and served from this origin, so the
	// policy can be strict. 'unsafe-inline' is needed for styles only: xterm
	// positions its rows and cursor with inline style attributes.
	const csp = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; " +
		"font-src 'self'; " +
		"media-src 'self' blob:; " +
		"connect-src 'self' ws: wss:; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'none'; " +
		"frame-ancestors 'none'"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		// frame-ancestors covers modern browsers; this covers the rest. Without
		// it a hostile page can frame the terminal invisibly and harvest
		// keystrokes the user believes are going somewhere else.
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// The microphone is needed by this origin and nobody else. Writing to
		// the clipboard is stated rather than left to the browser default,
		// because copying out of a remote shell depends on it. Reading is not
		// granted to any embedded context, and the terminal never answers an
		// OSC 52 read either — see installClipboardBridge.
		h.Set("Permissions-Policy",
			"microphone=(self), clipboard-write=(self), camera=(), geolocation=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")

		// Only when actually served over TLS: asserting HSTS from a plain-HTTP
		// deployment would pin browsers to a scheme that does not answer.
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		// Session lists and capabilities are not for the browser cache or any
		// intermediary to keep.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}

		next.ServeHTTP(w, r)
	})
}

// rateLimiter caps how often an identity may call an expensive endpoint.
//
// Transcription costs money per request. Even with CSRF closed, a compromised
// device on the tailnet should not be able to turn the terminal into a bill.
type rateLimiter struct {
	mu     sync.Mutex
	seen   map[string][]time.Time
	limit  int
	window time.Duration
	lastGC time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		seen:   make(map[string][]time.Time),
		limit:  limit,
		window: window,
		lastGC: time.Now(),
	}
}

// allow records an attempt and reports whether it is within the limit.
func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-l.window)

	// Periodically drop keys that have gone quiet, so an unbounded set of
	// identities cannot grow the map forever.
	if now.Sub(l.lastGC) > l.window {
		for k, ts := range l.seen {
			if len(ts) == 0 || ts[len(ts)-1].Before(cutoff) {
				delete(l.seen, k)
			}
		}
		l.lastGC = now
	}

	kept := l.seen[key][:0]
	for _, t := range l.seen[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) >= l.limit {
		l.seen[key] = kept
		return false
	}

	l.seen[key] = append(kept, now)
	return true
}
