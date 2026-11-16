package test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/test/harness"
)

func voiceStack(t *testing.T, opts harness.StackOptions) (*harness.Stack, *harness.FakeOpenAI) {
	t.Helper()
	fake := harness.NewFakeOpenAI(t)
	opts.OpenAI = fake
	st, _ := newStack(t, opts)
	return st, fake
}

func TestVoiceTranscribeInjectsAtCursor(t *testing.T) {
	st, fake := voiceStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)
	ws.RunCommand("echo ready", 5*time.Second)

	fake.Text = "git status --short"

	resp, body := c.Transcribe(sess.ID, "recording.webm", []byte("fake audio bytes"))
	require.Equalf(t, http.StatusOK, resp.StatusCode, "transcribe failed: %s", body)

	var out struct {
		Text      string   `json:"text"`
		Languages []string `json:"languages"`
		Injected  bool     `json:"injected"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Equal(t, "git status --short", out.Text)
	require.True(t, out.Injected)

	// The text lands at the shell cursor and is echoed back by the PTY.
	ws.WaitForOutput("git status --short", 10*time.Second)

	// Nothing ran: no newline was injected, so the command is still pending.
	require.NotContains(t, ws.Output(), "GOT:git status --short")
}

// The whole point of the keyword list: papaya knows the host names, and without
// them the model mishears "devbox-1" as "dev box one".
func TestVoiceSendsHostNamesAsKeywords(t *testing.T) {
	st, fake := voiceStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	resp, _ := c.Transcribe(sess.ID, "recording.webm", []byte("audio"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Contains(t, fake.Keywords(), "devbox-1",
		"the session's host must be passed as a transcription hint")
	require.Equal(t, "gpt-transcribe", fake.Model())
}

func TestVoiceRequiresOwnership(t *testing.T) {
	st, _ := voiceStack(t, harness.StackOptions{})

	a := st.Client(alice)
	sess := a.CreateSession("devbox-1", "", 80, 24)

	b := st.Client("other@example.com")
	resp, _ := b.Transcribe(sess.ID, "r.webm", []byte("audio"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"another owner's session must 404, not 403")
}

func TestVoiceRequiresAuth(t *testing.T) {
	st, _ := voiceStack(t, harness.StackOptions{})

	sess := st.Client(alice).CreateSession("devbox-1", "", 80, 24)

	resp, _ := st.DeniedClient().Transcribe(sess.ID, "r.webm", []byte("audio"))
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestVoiceUnknownSessionIs404(t *testing.T) {
	st, _ := voiceStack(t, harness.StackOptions{})

	resp, _ := st.Client(alice).Transcribe(
		"00000000-0000-4000-8000-000000000000", "r.webm", []byte("audio"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestVoiceDisabledWithoutAPIKey(t *testing.T) {
	// No OpenAI option, so transcription is unconfigured.
	st, _ := newStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)

	resp, body := c.Transcribe(sess.ID, "r.webm", []byte("audio"))
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Contains(t, string(body), "not configured")

	// And the frontend is told, so it can hide the control.
	caps := c.Capabilities()
	require.False(t, caps.Transcription)
}

func TestVoiceCapabilitiesReportEnabled(t *testing.T) {
	st, _ := voiceStack(t, harness.StackOptions{MaxSessions: 7})
	caps := st.Client(alice).Capabilities()

	require.True(t, caps.Transcription)
	require.Equal(t, 7, caps.MaxSessions)
	require.Greater(t, caps.MaxAudioMB, int64(0))
}

func TestVoiceOversizedAudioRejected(t *testing.T) {
	st, _ := voiceStack(t, harness.StackOptions{MaxAudioBytes: 64 << 10})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	resp, _ := c.TranscribeStream(sess.ID, "big.webm", 1<<20)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

// A transcript is remote-controlled text written straight into a shell. A
// newline in it would run the command before the user ever saw it.
func TestVoiceTranscriptCannotSubmitACommand(t *testing.T) {
	st, fake := voiceStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)
	ws.RunCommand("echo ready", 5*time.Second)

	fake.Text = "rm -rf /tmp/x\nsudo shutdown now\r\n"

	before := ws.Output()
	resp, body := c.Transcribe(sess.ID, "r.webm", []byte("audio"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct{ Text string }
	require.NoError(t, json.Unmarshal(body, &out))
	require.NotContains(t, out.Text, "\n")
	require.NotContains(t, out.Text, "\r")

	time.Sleep(500 * time.Millisecond) // prove absence of execution

	delta := ws.Output()[len(before):]
	require.NotContains(t, delta, "GOT:", "the injected transcript must not run")
}

func TestVoiceEmptyTranscriptIsHarmless(t *testing.T) {
	st, fake := voiceStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	fake.Text = "   "

	resp, body := c.Transcribe(sess.ID, "r.webm", []byte("audio"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Text     string `json:"text"`
		Injected bool   `json:"injected"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Empty(t, out.Text)
	require.False(t, out.Injected)
}

func TestVoiceUpstreamFailureIsReportedCleanly(t *testing.T) {
	st, fake := voiceStack(t, harness.StackOptions{})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	fake.Status = http.StatusUnauthorized
	fake.Body = `{"error":{"message":"Incorrect API key provided: sk-secret-abc123"}}`

	resp, body := c.Transcribe(sess.ID, "r.webm", []byte("audio"))
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)

	// The upstream message echoes the key back; it must not reach the browser.
	require.NotContains(t, string(body), "sk-secret-abc123")
	require.NotContains(t, string(body), "sk-")
	require.Contains(t, string(body), "API key was rejected")
}

func TestVoiceStreamsWithoutBuffering(t *testing.T) {
	st, _ := voiceStack(t, harness.StackOptions{MaxAudioBytes: 25 << 20})
	c := st.Client(alice)

	sess := c.CreateSession("devbox-1", "", 80, 24)
	ws := c.DialWS(sess.ID)
	ws.WaitForState("connected", 10*time.Second)

	const size = 16 << 20
	resp, _ := c.TranscribeStream(sess.ID, "long.webm", size)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
