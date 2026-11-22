// Package httpapi wires the REST API, WebSocket endpoint, and static assets.
package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tailscale.com/ipn/ipnstate"

	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/themes"
	"github.com/martoast/papaya/internal/transcribe"
	"github.com/martoast/papaya/internal/tsnode"
)

// Statuser reports tailnet peers, used to populate the host picker.
type Statuser interface {
	Status(ctx context.Context) (*ipnstate.Status, error)
}

// Options configures the HTTP layer.
type Options struct {
	DefaultSSHUser string
	StaticHosts    []string
	MaxUploadBytes int64
	MaxAudioBytes  int64
	MaxSessions    int
	Hostname       string

	// HostAllowed and SSHUserAllowed gate what a session may be created against.
	// Nil means unrestricted, which is the behaviour when nothing is configured.
	HostAllowed    func(host string) bool
	SSHUserAllowed func(user string) bool

	// SSHUserForHost resolves the account to use on a given host, letting hosts
	// that do not share a username coexist. Nil falls back to DefaultSSHUser
	// everywhere, which is the behaviour when no "user@host" entry is set.
	SSHUserForHost func(host string) string

	// VoiceKeyPath is where a voice (OpenAI) key pasted in the app is kept.
	// Empty means it cannot be set from the app; VoiceKeyFromEnv means the
	// server's environment already provides one, which the app does not touch.
	VoiceKeyPath    string
	VoiceKeyFromEnv bool
}

// Server holds the handler dependencies.
type Server struct {
	hub         *hub.Hub
	auth        *tsnode.Auth
	status      Statuser
	up          *websocket.Upgrader
	static      fs.FS
	transcriber *transcribe.Client
	// Transcription bills per request, so it gets its own budget per identity.
	transcribeLimit *rateLimiter
	opts            Options
	// setup is the first-run setup screen's API; nil where it is not wired.
	setup *setupAPI
	// themes is the theme library; nil where it is not wired.
	themes *themes.Store
	// events fans theme changes, cues and papaya-say messages out to browsers.
	events *broker
	// sceneStatus is what browsers last reported about each scene they ran.
	statusMu    sync.Mutex
	sceneStatus map[string]sceneStatus
}

func New(h *hub.Hub, auth *tsnode.Auth, status Statuser, static fs.FS, opts Options) *Server {
	return NewWithTranscriber(h, auth, status, static, nil, opts)
}

// NewWithTranscriber wires in optional voice transcription. A nil client leaves
// the feature reported as unavailable rather than failing at request time.
func NewWithTranscriber(h *hub.Hub, auth *tsnode.Auth, status Statuser, static fs.FS,
	tr *transcribe.Client, opts Options) *Server {
	if opts.MaxAudioBytes <= 0 {
		opts.MaxAudioBytes = 25 << 20
	}
	return &Server{
		hub:             h,
		auth:            auth,
		status:          status,
		up:              hub.Upgrader([]string{opts.Hostname}),
		static:          static,
		transcriber:     tr,
		transcribeLimit: newRateLimiter(30, time.Minute),
		opts:            opts,
		events:          newBroker(),
	}
}

// Handler builds the full route table.
//
// Every route except /healthz sits behind the tailnet identity middleware,
// including /ws, /upload, and the static assets.
func (s *Server) Handler() http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/sessions", s.listSessions)
	protected.HandleFunc("POST /api/sessions", s.createSession)
	protected.HandleFunc("PATCH /api/sessions/{id}", s.patchSession)
	protected.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	protected.HandleFunc("POST /api/sessions/{id}/seen", s.markSeen)
	protected.HandleFunc("GET /api/say/tabs", s.sayTabs)
	protected.HandleFunc("GET /api/say/me", s.sayMe)
	protected.HandleFunc("POST /api/say", s.say)
	protected.HandleFunc("GET /api/say/screen", s.screen)
	protected.HandleFunc("POST /api/status", s.agentStatus)
	protected.HandleFunc("GET /api/hosts", s.listHosts)
	protected.HandleFunc("GET /api/resumable", s.listResumable)
	protected.HandleFunc("POST /api/sessions/resume", s.resumeSession)
	protected.HandleFunc("GET /ws", s.serveWS)
	protected.HandleFunc("POST /upload", s.upload)
	protected.HandleFunc("POST /api/transcribe", s.transcribeAudio)
	protected.HandleFunc("GET /api/capabilities", s.capabilities)
	// The voice key is the admin's (it bills their account); a member may use
	// voice but never set, replace or remove the key.
	protected.HandleFunc("PUT /api/voice/key", s.adminOnly(s.setVoiceKey))
	protected.HandleFunc("DELETE /api/voice/key", s.adminOnly(s.deleteVoiceKey))
	s.setupRoutes(protected)
	s.themeRoutes(protected)
	protected.Handle("/", s.staticHandler())

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok"))
	})
	root.Handle("/", s.auth.Middleware(protected))

	// Order matters. The CSRF guard sits outside the identity check so a
	// cross-origin request is refused before it can act on the caller's ambient
	// tailnet identity.
	return logRequests(securityHeaders(csrfGuard([]string{s.opts.Hostname}, root)))
}

func (s *Server) staticHandler() http.Handler {
	if s.static == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "web assets not built: run `make web` or `npm --prefix web run build`",
				http.StatusServiceUnavailable)
		})
	}
	fileServer := http.FileServer(http.FS(s.static))
	assets := newAssetCache(s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vite names everything under /assets/ by a hash of its content, so a
		// given URL can never change: the browser may keep it for good. Before
		// this every launch of the phone app re-downloaded the whole 480 KB
		// bundle, uncompressed, because "no-cache" was sent with no ETag or
		// Last-Modified to revalidate against (embedded files have no mtime).
		// Everything else (index.html above all, which names the current
		// hashes) is revalidated on every load, and answers 304 from its ETag.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}

		// Set explicitly, because nothing else would get it right here. Go's
		// built-in type table has no .webmanifest entry, and the runtime image
		// is alpine, which ships no /etc/mime.types to fall back on — so the
		// body would be sniffed as text/plain and browsers refuse a manifest
		// served that way. It works in development only because that container
		// happens to have the system table.
		if strings.HasSuffix(r.URL.Path, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}

		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if a := assets.get(r.URL.Path); a != nil {
				a.serve(w, r)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" {
			// %q on the path: it is attacker-controlled and a decoded newline
			// would otherwise forge log lines.
			log.Printf("%s %s %q %d %s",
				r.RemoteAddr, r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush lets server-sent events through the logging wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack lets the WebSocket upgrade succeed through the logging wrapper.
// Without it, gorilla cannot take over the connection.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return h.Hijack()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
