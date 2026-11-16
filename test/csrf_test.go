package test

import (
	"bytes"
	"strings"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

// Can a page the user merely visits drive their terminal? Auth is ambient — it
// comes from the tailnet source address, not a cookie or token — so the browser
// attaches it to any request, including one a hostile page makes.
//
// multipart/form-data is a CORS-safelisted content type: no preflight, so the
// request is *sent* even though the attacker cannot read the reply. The side
// effect is what matters here.
func TestCSRFUploadFromHostilePage(t *testing.T) {
	st, _ := uploadStack(t)
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "from-evil.png")
	fw.Write([]byte("payload planted by a page the user merely visited"))
	mw.Close()

	req, err := http.NewRequest(http.MethodPost,
		st.URLFor(alice)+"/upload?session="+sess.ID, &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// Exactly what a browser sends for a cross-origin form post.
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equalf(t, http.StatusForbidden, resp.StatusCode,
		"a cross-origin upload must be refused; got %d", resp.StatusCode)
}

func TestCSRFTranscribeFromHostilePage(t *testing.T) {
	st, _ := voiceStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "evil.webm")
	fw.Write([]byte("audio"))
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost,
		st.URLFor(alice)+"/api/transcribe?session="+sess.ID, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "https://evil.example.com")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a cross-origin transcription must be refused")
}

// Clickjacking: without a frame policy a hostile page can embed the terminal
// invisibly and collect keystrokes the user believes are going elsewhere.
func TestFramingIsRefused(t *testing.T) {
	st, _ := newStack(t, harness.StackOptions{})
	resp, _ := st.Client(alice).Get("/")

	xfo := resp.Header.Get("X-Frame-Options")
	csp := resp.Header.Get("Content-Security-Policy")
	require.Truef(t,
		xfo == "DENY" || strings.Contains(csp, "frame-ancestors 'none'"),
		"the app must refuse to be framed; X-Frame-Options=%q CSP=%q", xfo, csp)
}
