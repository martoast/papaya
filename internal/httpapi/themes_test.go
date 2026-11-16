package httpapi_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/httpapi"
	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/internal/themes"
	"github.com/martoast/papaya/internal/tsnode"
	"github.com/martoast/papaya/test/harness"
)

func newThemeServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dialer := harness.NewCountingDialer(map[string]string{})
	h := hub.New(sshx.NewPool(dialer, sshx.Config{}), hub.Options{UploadDir: filepath.Join(dir, "up")})
	t.Cleanup(func() { h.CloseAll("test over") })
	api := httpapi.New(h,
		tsnode.NewAuth(whoIs{"alice@example.com"}, map[string]bool{"alice@example.com": true}, nil),
		&peers{}, nil, httpapi.Options{Hostname: "papaya"})
	store, err := themes.Open(filepath.Join(dir, "themes"))
	require.NoError(t, err)
	api.EnableThemes(store)
	srv := newServer(t, api.Handler())
	return srv
}

func newServer(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func do(t *testing.T, method, url, ctype, body string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

const koi = `{"name":"koi","description":"pond","opacity":0.7,"colors":{"accent":"#ff8a3d","term-bg":"#0b2a2a"}}`

func TestAThemePushedAndSwitchedToIsWhatBrowsersGet(t *testing.T) {
	url := newThemeServer(t)

	code, _, _ := do(t, "GET", url+"/api/theme", "", "")
	require.Equal(t, 200, code)

	code, body, _ := do(t, "PUT", url+"/api/themes/koi", "application/json", koi)
	require.Equal(t, 200, code, body)
	code, body, _ = do(t, "PUT", url+"/api/themes/koi/scene", "text/javascript", "papaya.scene({ draw(s) {} })")
	require.Equal(t, 200, code, body)
	code, body, _ = do(t, "POST", url+"/api/themes/active", "application/json", `{"name":"koi"}`)
	require.Equal(t, 200, code, body)

	_, body, _ = do(t, "GET", url+"/api/theme", "", "")
	require.Contains(t, body, `"name":"koi"`)
	require.Contains(t, body, `"accent":"#ff8a3d"`)
	require.Contains(t, body, `"sceneUrl":"/theme/scene/koi?v=`)
	require.NotContains(t, body, "papaya.scene", "the scene source is not handed to the page")

	// Pushing theme.json again keeps the scene already saved.
	do(t, "PUT", url+"/api/themes/koi", "application/json", koi)
	_, body, _ = do(t, "GET", url+"/api/themes/koi", "", "")
	require.Contains(t, body, "papaya.scene")

	_, body, _ = do(t, "GET", url+"/api/themes?format=text", "", "")
	require.Contains(t, body, "on: koi")
	require.Contains(t, body, "* koi")
	require.Contains(t, body, "orchard")
}

func TestBadThemesAreRefusedWithAReason(t *testing.T) {
	url := newThemeServer(t)
	code, body, _ := do(t, "PUT", url+"/api/themes/koi", "application/json", `{"name":"koi","colors":{"accent":"red;x:url(y)"}}`)
	require.Equal(t, 422, code)
	require.Contains(t, body, "colors.accent")

	code, body, _ = do(t, "PUT", url+"/api/themes/koi", "application/json", `{"name":"other"}`)
	require.Equal(t, 400, code)
	require.Contains(t, body, "pushed as")

	code, body, _ = do(t, "PUT", url+"/api/themes/koi/scene", "text/javascript", "const x = '</script>'")
	require.Equal(t, 422, code)
	require.Contains(t, body, "line 1")

	code, _, _ = do(t, "PUT", url+"/api/themes/orchard", "application/json", `{"name":"orchard"}`)
	require.Equal(t, 422, code)
}

// The scene document is the one place in Papaya where code someone else wrote
// runs. Its policy must take away the network, and only Papaya may frame it.
func TestTheSceneDocumentIsLockedDown(t *testing.T) {
	url := newThemeServer(t)
	code, body, h := do(t, "GET", url+"/theme/scene/orchard", "", "")
	require.Equal(t, 200, code)
	csp := h.Get("Content-Security-Policy")
	require.Contains(t, csp, "default-src 'none'")
	require.Contains(t, csp, "connect-src 'none'")
	require.Contains(t, csp, "frame-ancestors 'self'")
	require.NotContains(t, csp, "unsafe-eval")
	require.Equal(t, "SAMEORIGIN", h.Get("X-Frame-Options"))
	nonce := strings.TrimSuffix(strings.SplitN(strings.SplitN(csp, "'nonce-", 2)[1], "'", 2)[0], "'")
	require.Equal(t, 3, strings.Count(body, `<script nonce="`+nonce+`">`))

	// Everything else keeps the page policy.
	_, _, h = do(t, "GET", url+"/api/theme", "", "")
	require.Equal(t, "DENY", h.Get("X-Frame-Options"))
}

func TestBrowsersHearThemeChangesAndCues(t *testing.T) {
	url := newThemeServer(t)
	do(t, "PUT", url+"/api/themes/koi", "application/json", koi)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/api/events", nil)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	lines := bufio.NewScanner(res.Body)
	next := func() string {
		for lines.Scan() {
			if l := lines.Text(); strings.HasPrefix(l, "data: ") {
				return strings.TrimPrefix(l, "data: ")
			}
		}
		return ""
	}

	go do(t, "POST", url+"/api/themes/active", "application/json", `{"name":"koi"}`)
	require.JSONEq(t, `{"type":"theme"}`, next())

	go do(t, "POST", url+"/api/themes/cue", "application/json", `{"name":"merged"}`)
	require.JSONEq(t, `{"type":"cue","name":"merged"}`, next())

	// Saving the theme that is on reloads it everywhere.
	go do(t, "PUT", url+"/api/themes/koi/scene", "text/javascript", "papaya.scene({})")
	require.JSONEq(t, `{"type":"theme"}`, next())
}

func TestTheAgentCanReadWhatTheBrowserSaw(t *testing.T) {
	url := newThemeServer(t)
	do(t, "PUT", url+"/api/themes/koi", "application/json", koi)
	do(t, "PUT", url+"/api/themes/koi/scene", "text/javascript", "papaya.scene({\n  draw(s) {\n    boom()\n  }\n})")
	do(t, "POST", url+"/api/themes/active", "application/json", `{"name":"koi"}`)

	_, body, _ := do(t, "GET", url+"/api/themes/koi/status?format=text", "", "")
	require.Contains(t, body, "scene:   waiting")

	_, view, _ := do(t, "GET", url+"/api/theme", "", "")
	version := strings.SplitN(strings.SplitN(view, `"version":"`, 2)[1], `"`, 2)[0]

	code, _, _ := do(t, "POST", url+"/api/themes/koi/status", "application/json",
		`{"version":"`+version+`","state":"error","error":"ReferenceError: boom is not defined"}`)
	require.Equal(t, 204, code)
	_, body, _ = do(t, "GET", url+"/api/themes/koi/status?format=text", "", "")
	require.Contains(t, body, "scene:   ERROR")
	require.Contains(t, body, "boom is not defined")

	// A report about an older version of the scene is not shown as current.
	do(t, "PUT", url+"/api/themes/koi/scene", "text/javascript", "papaya.scene({ draw() {} })")
	_, body, _ = do(t, "GET", url+"/api/themes/koi/status?format=text", "", "")
	require.NotContains(t, body, "ERROR")
}

// Cross-origin pages cannot change a theme: it is a state change made with the
// caller's ambient tailnet identity.
func TestThemeWritesAreCSRFGuarded(t *testing.T) {
	url := newThemeServer(t)
	req, _ := http.NewRequest("POST", url+"/api/themes/active", strings.NewReader(`{"name":"orchard"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, 403, res.StatusCode)
}

func TestOmarchyImportAndWallpaper(t *testing.T) {
	url := newThemeServer(t)
	toml := "mode = \"dark\"\naccent = \"#ff9f0a\"\nbackground = \"#1c1c1e\"\nforeground = \"#f5f5f7\"\nred = \"#ff5f4a\"\nyellow = \"#ffc27a\"\ngreen = \"#ff9f0a\"\ncyan = \"#a6dcef\"\nblue = \"#8fb8de\"\nmagenta = \"#ff8fa3\"\n"
	code, body, _ := do(t, "POST", url+"/api/themes/starship/omarchy?title=Starship", "text/plain", toml)
	require.Equal(t, 200, code, body)

	jpeg := "\xff\xd8\xff\xe0" + strings.Repeat("x", 64)
	code, body, _ = do(t, "PUT", url+"/api/themes/starship/image", "image/jpeg", jpeg)
	require.Equal(t, 200, code, body)
	require.Contains(t, body, `"hasImage":true`)

	do(t, "POST", url+"/api/themes/active", "application/json", `{"name":"starship"}`)
	_, view, _ := do(t, "GET", url+"/api/theme", "", "")
	require.Contains(t, view, `"accent":"#ff9f0a"`)
	sceneURL := strings.SplitN(strings.SplitN(view, `"sceneUrl":"`, 2)[1], `"`, 2)[0]

	code, doc, h := do(t, "GET", url+sceneURL, "", "")
	require.Equal(t, 200, code)
	require.Contains(t, doc, "data:image/jpeg;base64,")
	require.Contains(t, h.Get("Cache-Control"), "immutable", "a versioned scene URL may be cached")
	_, _, h = do(t, "GET", url+"/theme/scene/starship", "", "")
	require.Equal(t, "no-store", h.Get("Cache-Control"))

	// Re-importing the palette keeps the wallpaper; theme.json cannot fake one.
	do(t, "POST", url+"/api/themes/starship/omarchy", "text/plain", toml)
	_, body, _ = do(t, "GET", url+"/api/themes/starship", "", "")
	require.Contains(t, body, `"imageType":"image/jpeg"`)
	do(t, "PUT", url+"/api/themes/fake", "application/json", `{"name":"fake","imageType":"image/png","imageSum":"abc"}`)
	_, body, _ = do(t, "GET", url+"/api/themes/fake", "", "")
	require.NotContains(t, body, "imageType")

	code, body, _ = do(t, "PUT", url+"/api/themes/starship/image", "image/svg+xml", "<svg/>")
	require.Equal(t, 422, code, body)
}
