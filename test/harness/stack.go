package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"

	"github.com/martoast/papaya/internal/config"
	"github.com/martoast/papaya/internal/httpapi"
	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/internal/transcribe"
	"github.com/martoast/papaya/internal/tsnode"
)

// fixedWhoIs reports one identity for every request. Each test user gets its own
// HTTP server wired to a fixed identity, which models distinct tailnet users
// without a tailnet.
type fixedWhoIs struct {
	login string
	tags  []string
	err   error
}

func (f fixedWhoIs) WhoIs(context.Context, string) (*apitype.WhoIsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &apitype.WhoIsResponse{
		Node:        &tailcfg.Node{Tags: f.tags},
		UserProfile: &tailcfg.UserProfile{LoginName: f.login},
	}, nil
}

// StackOptions configures a test stack.
type StackOptions struct {
	MaxSessions    int
	MaxUploadBytes int64
	DetachedTTL    time.Duration
	RingSize       int
	UploadDir      string
	AllowedUsers   []string
	Hosts          map[string]string // logical host name -> real address
	// DefaultSSHUser is the remote login used when a create request omits one.
	// Defaults to "tester", which is the account the fixture sshd provides.
	DefaultSSHUser string

	// OpenAI, when set, enables voice transcription against a stub endpoint.
	OpenAI *FakeOpenAI
	// OpenAIKey, when set, enables transcription against the real API.
	OpenAIKey string
	// MaxAudioBytes caps transcription uploads. Defaults to 25 MB.
	MaxAudioBytes int64

	// AllowedHosts and AllowedSSHUsers mirror the server's enforcing allow
	// lists. Empty means unrestricted.
	AllowedHosts    []string
	AllowedSSHUsers []string

	// Tmux runs remote shells inside a named tmux session.
	Tmux bool
}

// Stack is a full papaya server wired to in-process SSH, with tsnet replaced by
// a plain dialer and a fixed identity.
type Stack struct {
	T      *testing.T
	Hub    *hub.Hub
	Pool   *sshx.Pool
	Dialer *CountingDialer
	Opts   StackOptions

	mu      sync.Mutex
	servers map[string]*httptest.Server
	allowed map[string]bool
}

// NewStack builds a server around the given SSH servers.
func NewStack(t *testing.T, servers map[string]*SSHServer, opts StackOptions) *Stack {
	t.Helper()

	opts = applyDefaults(t, opts)

	rewrite := map[string]string{}
	for name, srv := range servers {
		rewrite[name] = srv.Addr
	}
	for k, v := range opts.Hosts {
		rewrite[k] = v
	}

	kh := writeMergedKnownHosts(t, servers)
	d := NewCountingDialer(rewrite)

	pool := sshx.NewPool(d, sshx.Config{
		UseTailscaleSSH: true,
		KnownHostsPath:  kh,
	})
	t.Cleanup(pool.CloseAll)

	return newStack(t, pool, d, opts)
}

// NewStackWithPool builds a server around a pool the caller already configured,
// for tests that dial a real sshd rather than the in-process server.
func NewStackWithPool(t *testing.T, pool *sshx.Pool, d *CountingDialer, opts StackOptions) *Stack {
	t.Helper()
	return newStack(t, pool, d, applyDefaults(t, opts))
}

func applyDefaults(t *testing.T, opts StackOptions) StackOptions {
	t.Helper()
	if opts.MaxSessions == 0 {
		opts.MaxSessions = 10
	}
	if opts.MaxUploadBytes == 0 {
		opts.MaxUploadBytes = 25 << 20
	}
	if opts.DetachedTTL == 0 {
		opts.DetachedTTL = 30 * time.Minute
	}
	if opts.UploadDir == "" {
		opts.UploadDir = t.TempDir()
	}
	if len(opts.AllowedUsers) == 0 {
		opts.AllowedUsers = []string{"alice@example.com", "other@example.com"}
	}
	if opts.DefaultSSHUser == "" {
		opts.DefaultSSHUser = "tester"
	}
	if opts.MaxAudioBytes == 0 {
		opts.MaxAudioBytes = 25 << 20
	}
	return opts
}

func newStack(t *testing.T, pool *sshx.Pool, d *CountingDialer, opts StackOptions) *Stack {
	t.Helper()

	h := hub.New(pool, hub.Options{
		MaxSessions: opts.MaxSessions,
		UploadDir:   opts.UploadDir,
		DetachedTTL: opts.DetachedTTL,
		RingSize:    opts.RingSize,
		Tmux:        opts.Tmux,
	})
	t.Cleanup(func() { h.CloseAll("test over") })

	allowed := map[string]bool{}
	for _, u := range opts.AllowedUsers {
		allowed[u] = true
	}

	return &Stack{
		T:       t,
		Hub:     h,
		Pool:    pool,
		Dialer:  d,
		Opts:    opts,
		servers: map[string]*httptest.Server{},
		allowed: allowed,
	}
}

// serverFor returns the HTTP server bound to a given identity.
func (s *Stack) serverFor(login string) *httptest.Server {
	s.mu.Lock()
	defer s.mu.Unlock()

	if srv, ok := s.servers[login]; ok {
		return srv
	}

	api := httpapi.NewWithTranscriber(s.Hub,
		tsnode.NewAuth(fixedWhoIs{login: login}, s.allowed, map[string]bool{"tag:ci": true}),
		nil, nil, s.transcriber(),
		httpapi.Options{
			DefaultSSHUser: s.Opts.DefaultSSHUser,
			StaticHosts:    s.hostNames(),
			MaxUploadBytes: s.Opts.MaxUploadBytes,
			MaxAudioBytes:  s.Opts.MaxAudioBytes,
			MaxSessions:    s.Opts.MaxSessions,
			Hostname:       "papaya",
			HostAllowed:    s.hostAllowed,
			SSHUserAllowed: s.sshUserAllowed,
		})

	srv := httptest.NewServer(api.Handler())
	s.T.Cleanup(srv.Close)
	s.servers[login] = srv
	return srv
}

// Client returns a REST/WebSocket client acting as the given tailnet identity.
func (s *Stack) Client(login string) *Client {
	return &Client{t: s.T, srv: s.serverFor(login), login: login}
}

// TaggedClient returns a client authenticated by tag rather than login.
func (s *Stack) TaggedClient(tag string) *Client {
	s.mu.Lock()
	key := "tag!" + tag
	srv, ok := s.servers[key]
	if !ok {
		api := httpapi.New(s.Hub,
			tsnode.NewAuth(fixedWhoIs{login: "", tags: []string{tag}}, s.allowed,
				map[string]bool{"tag:ci": true}),
			nil, nil,
			httpapi.Options{DefaultSSHUser: s.Opts.DefaultSSHUser, MaxUploadBytes: s.Opts.MaxUploadBytes,
				MaxAudioBytes: s.Opts.MaxAudioBytes, MaxSessions: s.Opts.MaxSessions, Hostname: "papaya"})
		srv = httptest.NewServer(api.Handler())
		s.T.Cleanup(srv.Close)
		s.servers[key] = srv
	}
	s.mu.Unlock()
	return &Client{t: s.T, srv: srv, login: tag}
}

// DeniedClient returns a client whose identity is not on the allow list.
func (s *Stack) DeniedClient() *Client {
	s.mu.Lock()
	srv, ok := s.servers["!denied"]
	if !ok {
		api := httpapi.New(s.Hub,
			tsnode.NewAuth(fixedWhoIs{login: "stranger@evil.com"}, s.allowed, map[string]bool{}),
			nil, nil,
			httpapi.Options{DefaultSSHUser: s.Opts.DefaultSSHUser, MaxUploadBytes: s.Opts.MaxUploadBytes,
				MaxAudioBytes: s.Opts.MaxAudioBytes, MaxSessions: s.Opts.MaxSessions, Hostname: "papaya"})
		srv = httptest.NewServer(api.Handler())
		s.T.Cleanup(srv.Close)
		s.servers["!denied"] = srv
	}
	s.mu.Unlock()
	return &Client{t: s.T, srv: srv, login: "stranger@evil.com"}
}

func writeMergedKnownHosts(t *testing.T, servers map[string]*SSHServer) string {
	t.Helper()
	var b strings.Builder
	for name, srv := range servers {
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(srv.Signer.PublicKey())))
		b.WriteString(name + " " + line + "\n")
	}

	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

// Client is a REST + WebSocket client for one identity.
type Client struct {
	t     *testing.T
	srv   *httptest.Server
	login string
}

// Session mirrors the API's session JSON.
type Session struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	User      string    `json:"user"`
	State     string    `json:"state"`
	Order     int       `json:"order"`
	CreatedAt time.Time `json:"createdAt"`
}

func (c *Client) do(method, path string, body any) (*http.Response, []byte) {
	c.t.Helper()

	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(c.t, err)
		r = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.srv.URL+path, r)
	require.NoError(c.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.srv.Client().Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	return resp, data
}

// CreateSession posts a new session and requires success.
func (c *Client) CreateSession(host, name string, cols, rows int) Session {
	c.t.Helper()
	resp, body := c.CreateSessionRaw(host, name, cols, rows)
	require.Equalf(c.t, http.StatusCreated, resp.StatusCode, "create failed: %s", body)

	var s Session
	require.NoError(c.t, json.Unmarshal(body, &s))
	return s
}

// CreateSessionRaw returns the raw response so tests can assert on 429.
func (c *Client) CreateSessionRaw(host, name string, cols, rows int) (*http.Response, []byte) {
	c.t.Helper()
	return c.do(http.MethodPost, "/api/sessions", map[string]any{
		"host": host, "name": name, "cols": cols, "rows": rows,
	})
}

func (c *Client) ListSessions() []Session {
	c.t.Helper()
	resp, body := c.do(http.MethodGet, "/api/sessions", nil)
	require.Equal(c.t, http.StatusOK, resp.StatusCode)

	var out []Session
	require.NoError(c.t, json.Unmarshal(body, &out))
	return out
}

func (c *Client) PatchSession(id string, patch map[string]any) (*http.Response, []byte) {
	c.t.Helper()
	return c.do(http.MethodPatch, "/api/sessions/"+id, patch)
}

func (c *Client) DeleteSession(id string) (*http.Response, []byte) {
	c.t.Helper()
	return c.do(http.MethodDelete, "/api/sessions/"+id, nil)
}

func (c *Client) Get(path string) (*http.Response, []byte) {
	c.t.Helper()
	return c.do(http.MethodGet, path, nil)
}

// Upload posts a multipart file to a session.
func (c *Client) Upload(sessionID, filename string, content []byte) (*http.Response, []byte) {
	c.t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	require.NoError(c.t, err)
	_, err = fw.Write(content)
	require.NoError(c.t, err)
	require.NoError(c.t, mw.Close())

	req, err := http.NewRequest(http.MethodPost, c.srv.URL+"/upload?session="+sessionID, &buf)
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.srv.Client().Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	return resp, data
}

// UploadStream posts a body of the given size without buffering it in the test,
// so memory assertions measure the server rather than the client.
func (c *Client) UploadStream(sessionID, filename string, size int64) (*http.Response, []byte) {
	c.t.Helper()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		chunk := bytes.Repeat([]byte("A"), 64<<10)
		for remaining := size; remaining > 0; {
			n := int64(len(chunk))
			if n > remaining {
				n = remaining
			}
			if _, err := fw.Write(chunk[:n]); err != nil {
				pw.CloseWithError(err)
				return
			}
			remaining -= n
		}
		mw.Close()
		pw.Close()
	}()

	req, err := http.NewRequest(http.MethodPost, c.srv.URL+"/upload?session="+sessionID, pr)
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.srv.Client().Do(req)
	if err != nil {
		// A rejected oversize body can surface as a transport error.
		return &http.Response{StatusCode: http.StatusRequestEntityTooLarge}, []byte(err.Error())
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

// DialWS opens a WebSocket for a session and requires success.
func (c *Client) DialWS(sessionID string) *WSClient {
	c.t.Helper()
	ws, resp, err := c.DialWSRaw(sessionID, nil)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		c.t.Fatalf("websocket dial failed (status %d): %v", code, err)
	}
	return ws
}

// DialWSRaw exposes the handshake result so tests can assert on 404 and origin
// rejection.
func (c *Client) DialWSRaw(sessionID string, header http.Header) (*WSClient, *http.Response, error) {
	c.t.Helper()

	u := "ws" + strings.TrimPrefix(c.srv.URL, "http") + "/ws?session=" + sessionID
	conn, resp, err := websocket.DefaultDialer.Dial(u, header)
	if err != nil {
		return nil, resp, err
	}

	w := newWSClient(c.t, conn)
	c.t.Cleanup(w.Close)
	return w, resp, nil
}

// transcriber builds a client pointed at the stub endpoint, or nil when the
// test did not ask for voice support.
func (s *Stack) transcriber() *transcribe.Client {
	switch {
	case s.Opts.OpenAI != nil:
		return transcribe.New(transcribe.Options{
			APIKey:  "sk-test-key",
			BaseURL: s.Opts.OpenAI.BaseURL(),
			Model:   "gpt-transcribe",
		})
	case s.Opts.OpenAIKey != "":
		return transcribe.New(transcribe.Options{
			APIKey: s.Opts.OpenAIKey,
			Model:  "gpt-transcribe",
		})
	default:
		return nil
	}
}

// hostNames lists the logical hosts the dialer knows, which the server passes
// to the transcriber as keywords.
func (s *Stack) hostNames() []string {
	out := make([]string, 0, len(s.Dialer.Rewrite))
	for name := range s.Dialer.Rewrite {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Capabilities fetches the feature flags the frontend uses.
func (c *Client) Capabilities() httpapi.Capabilities {
	c.t.Helper()
	resp, body := c.do(http.MethodGet, "/api/capabilities", nil)
	require.Equal(c.t, http.StatusOK, resp.StatusCode)

	var caps httpapi.Capabilities
	require.NoError(c.t, json.Unmarshal(body, &caps))
	return caps
}

// Transcribe posts a recording to a session.
func (c *Client) Transcribe(sessionID, filename string, audio []byte) (*http.Response, []byte) {
	c.t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	require.NoError(c.t, err)
	_, err = fw.Write(audio)
	require.NoError(c.t, err)
	require.NoError(c.t, mw.Close())

	req, err := http.NewRequest(http.MethodPost,
		c.srv.URL+"/api/transcribe?session="+sessionID, &buf)
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.srv.Client().Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	return resp, data
}

// TranscribeStream posts audio of the given size without buffering it locally,
// so size-limit and memory assertions measure the server.
func (c *Client) TranscribeStream(sessionID, filename string, size int64) (*http.Response, []byte) {
	c.t.Helper()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		chunk := bytes.Repeat([]byte("A"), 64<<10)
		for remaining := size; remaining > 0; {
			n := int64(len(chunk))
			if n > remaining {
				n = remaining
			}
			if _, err := fw.Write(chunk[:n]); err != nil {
				pw.CloseWithError(err)
				return
			}
			remaining -= n
		}
		mw.Close()
		pw.Close()
	}()

	req, err := http.NewRequest(http.MethodPost,
		c.srv.URL+"/api/transcribe?session="+sessionID, pr)
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.srv.Client().Do(req)
	if err != nil {
		return &http.Response{StatusCode: http.StatusRequestEntityTooLarge}, []byte(err.Error())
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

// policy builds a real config so the tests exercise the production allow-list
// logic rather than a copy of it. A reimplementation here would drift from the
// real rules and quietly stop testing them.
func (s *Stack) policy() *config.Config {
	users := map[string]bool{}
	for _, u := range s.Opts.AllowedSSHUsers {
		users[u] = true
	}
	return &config.Config{AllowedSSHUsers: users, AllowedHosts: s.Opts.AllowedHosts}
}

func (s *Stack) hostAllowed(host string) bool { return s.policy().HostAllowed(host) }

func (s *Stack) sshUserAllowed(user string) bool { return s.policy().SSHUserAllowed(user) }

// Do exposes the raw request helper so tests can send bodies the typed helpers
// do not model, such as an explicit remote user.
func (c *Client) Do(method, path string, body any) (*http.Response, []byte) {
	c.t.Helper()
	return c.do(method, path, body)
}

// URLFor is the base URL of the server bound to an identity, for tests that
// need to craft raw requests rather than use the typed client.
func (s *Stack) URLFor(login string) string { return s.serverFor(login).URL }

// Resumable lists remote workspaces the server is offering to reattach.
func (c *Client) Resumable() []map[string]any {
	c.t.Helper()
	resp, body := c.do(http.MethodGet, "/api/resumable", nil)
	require.Equalf(c.t, http.StatusOK, resp.StatusCode, "resumable failed: %s", body)

	var out []map[string]any
	require.NoError(c.t, json.Unmarshal(body, &out))
	return out
}

// ResumeRaw attaches a new tab to an existing remote workspace.
func (c *Client) ResumeRaw(host, name, workspace string, cols, rows int) (*http.Response, []byte) {
	c.t.Helper()
	return c.do(http.MethodPost, "/api/sessions/resume", map[string]any{
		"host": host, "name": name, "workspace": workspace, "cols": cols, "rows": rows,
	})
}

// Resume is ResumeRaw asserting success.
func (c *Client) Resume(host, name, workspace string, cols, rows int) Session {
	c.t.Helper()
	resp, body := c.ResumeRaw(host, name, workspace, cols, rows)
	require.Equalf(c.t, http.StatusCreated, resp.StatusCode, "resume failed: %s", body)

	var s Session
	require.NoError(c.t, json.Unmarshal(body, &s))
	return s
}

// ResumeAsRaw is ResumeRaw naming the remote account explicitly, for asserting
// that resuming is gated the same way creating is.
func (c *Client) ResumeAsRaw(host, sshUser, name, workspace string, cols, rows int) (*http.Response, []byte) {
	c.t.Helper()
	return c.do(http.MethodPost, "/api/sessions/resume", map[string]any{
		"host": host, "user": sshUser, "name": name,
		"workspace": workspace, "cols": cols, "rows": rows,
	})
}
