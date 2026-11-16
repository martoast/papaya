package transcribe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/transcribe"
)

// fakeAPI stands in for the OpenAI audio endpoint, recording exactly what was
// sent so the request shape can be asserted.
type fakeAPI struct {
	srv *httptest.Server

	mu        chan struct{}
	Auth      string
	Model     string
	Keywords  []string
	Languages []string
	Filename  string
	AudioLen  atomic.Int64

	Status int
	Body   string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{Status: http.StatusOK, mu: make(chan struct{}, 1)}
	f.mu <- struct{}{}

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-f.mu
		defer func() { f.mu <- struct{}{} }()

		f.Auth = r.Header.Get("Authorization")

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])

		f.Keywords = nil
		f.Languages = nil

		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			switch part.FormName() {
			case "model":
				b, _ := io.ReadAll(part)
				f.Model = string(b)
			case "keywords[]":
				b, _ := io.ReadAll(part)
				f.Keywords = append(f.Keywords, string(b))
			case "languages[]":
				b, _ := io.ReadAll(part)
				f.Languages = append(f.Languages, string(b))
			case "file":
				f.Filename = part.FileName()
				n, _ := io.Copy(io.Discard, part)
				f.AudioLen.Store(n)
			}
			part.Close()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.Status)
		if f.Body != "" {
			io.WriteString(w, f.Body)
			return
		}
		io.WriteString(w, `{"text":"run git status","languages":[{"code":"en"}]}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newClient(f *fakeAPI, opts transcribe.Options) *transcribe.Client {
	opts.APIKey = "sk-test-key"
	opts.BaseURL = f.srv.URL
	return transcribe.New(opts)
}

func TestTranscribeSendsExpectedRequest(t *testing.T) {
	f := newFakeAPI(t)
	c := newClient(f, transcribe.Options{
		Model:     "gpt-transcribe",
		Languages: []string{"en"},
		Keywords:  []string{"kubectl"},
	})

	audio := bytes.NewReader(bytes.Repeat([]byte("a"), 4096))
	res, err := c.Transcribe(context.Background(), "recording.webm", audio, []string{"devbox-1"})
	require.NoError(t, err)

	require.Equal(t, "run git status", res.Text)
	require.Equal(t, []string{"en"}, res.Languages)

	require.Equal(t, "Bearer sk-test-key", f.Auth)
	require.Equal(t, "gpt-transcribe", f.Model)
	require.Equal(t, int64(4096), f.AudioLen.Load())

	// keywords[] is the encoding the API actually honours; plain "keywords" is
	// silently ignored, which shows up as "dev box one" instead of "devbox-1".
	require.Contains(t, f.Keywords, "kubectl")
	require.Contains(t, f.Keywords, "devbox-1")
	require.Equal(t, []string{"en"}, f.Languages)
}

func TestTranscribeKeepsAudioExtension(t *testing.T) {
	cases := map[string]string{
		"recording.webm": "audio.webm",
		"clip.wav":       "audio.wav",
		"voice.m4a":      "audio.m4a",
		"note.mp3":       "audio.mp3",
		// The API detects format from the extension, so an unknown one falls
		// back to what browsers actually produce.
		"weird.xyz":            "audio.webm",
		"":                     "audio.webm",
		"../../etc/passwd":     "audio.webm",
		"a.wav; rm -rf /":      "audio.webm",
	}

	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			f := newFakeAPI(t)
			c := newClient(f, transcribe.Options{})

			_, err := c.Transcribe(context.Background(), in, strings.NewReader("audio"), nil)
			require.NoError(t, err)
			require.Equal(t, want, f.Filename)
		})
	}
}

func TestTranscribeCleansKeywords(t *testing.T) {
	f := newFakeAPI(t)
	c := newClient(f, transcribe.Options{
		// The API rejects the entire request on angle brackets or newlines.
		Keywords: []string{"good", "ba<d>", "with\nnewline", "with\rcr", "  ", "dup", "dup"},
	})

	_, err := c.Transcribe(context.Background(), "a.webm", strings.NewReader("x"), nil)
	require.NoError(t, err)

	for _, kw := range f.Keywords {
		require.NotContains(t, kw, "<")
		require.NotContains(t, kw, ">")
		require.NotContains(t, kw, "\n")
		require.NotContains(t, kw, "\r")
		require.NotEmpty(t, strings.TrimSpace(kw))
	}

	seen := map[string]int{}
	for _, kw := range f.Keywords {
		seen[kw]++
	}
	require.Equal(t, 1, seen["dup"], "duplicates must be collapsed")
	require.Contains(t, f.Keywords, "good")
}

func TestTranscribeCapsKeywordCount(t *testing.T) {
	f := newFakeAPI(t)

	many := make([]string, 500)
	for i := range many {
		many[i] = fmt.Sprintf("kw%d", i)
	}
	c := newClient(f, transcribe.Options{Keywords: many})

	_, err := c.Transcribe(context.Background(), "a.webm", strings.NewReader("x"), nil)
	require.NoError(t, err)
	require.LessOrEqual(t, len(f.Keywords), 100,
		"an unbounded keyword list would fail the whole request on length")
}

func TestTranscribeErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized,
			`{"error":{"message":"Incorrect API key provided: sk-abc123"}}`,
			"API key was rejected"},
		{"rate limited", http.StatusTooManyRequests,
			`{"error":{"message":"Rate limit reached"}}`,
			"rate limit"},
		{"server error", http.StatusInternalServerError, `{"error":{"message":"boom"}}`, "boom"},
		{"unparseable", http.StatusBadGateway, `not json`, "502"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.Status = tc.status
			f.Body = tc.body

			c := newClient(f, transcribe.Options{})
			_, err := c.Transcribe(context.Background(), "a.webm", strings.NewReader("x"), nil)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// The upstream message may echo back a key fragment; that must not survive into
// anything shown to a user.
func TestTranscribeErrorDoesNotLeakKey(t *testing.T) {
	f := newFakeAPI(t)
	f.Status = http.StatusUnauthorized
	f.Body = `{"error":{"message":"Incorrect API key provided: sk-test-key"}}`

	c := newClient(f, transcribe.Options{})
	_, err := c.Transcribe(context.Background(), "a.webm", strings.NewReader("x"), nil)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "sk-test-key")
}

func TestTranscribeNotConfigured(t *testing.T) {
	c := transcribe.New(transcribe.Options{})
	require.False(t, c.Enabled())

	_, err := c.Transcribe(context.Background(), "a.webm", strings.NewReader("x"), nil)
	require.ErrorIs(t, err, transcribe.ErrNotConfigured)
}

func TestTranscribeRespectsContext(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // outlive the context on purpose
	}))
	t.Cleanup(slow.Close)

	c := transcribe.New(transcribe.Options{APIKey: "k", BaseURL: slow.URL})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Transcribe(ctx, "a.webm", strings.NewReader("x"), nil)
	require.Error(t, err)
}

// CleanText guards what reaches a shell: a newline would submit the line before
// the user had read it.
func TestCleanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"run git status", "run git status"},
		{"  padded  ", "padded"},
		{"line\nbreak", "line break"},
		{"tab\there", "tab here"},
		{"carriage\rreturn", "carriage return"},
		// Whitespace controls become separators; the rest are dropped outright,
		// since a bell or an escape is not a word boundary.
		{"bell\x07and\x1bescape", "bellandescape"},
		{"", ""},
		{"\n\n\n", ""},
		{"collapse   many   spaces", "collapse many spaces"},
		{"日本語 テスト", "日本語 テスト"},
	}

	for _, tc := range cases {
		got := transcribe.CleanText(tc.in)
		require.Equalf(t, tc.want, got, "input %q", tc.in)
		require.NotContains(t, got, "\n", "a newline would submit the command")
		require.NotContains(t, got, "\r")
	}
}

func FuzzCleanText(f *testing.F) {
	for _, s := range []string{"hello", "a\nb", "\x00\x1b[31m", "日本語", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := transcribe.CleanText(in)
		for _, r := range out {
			if r == '\n' || r == '\r' {
				t.Fatalf("newline survived from %q", in)
			}
			if r < 0x20 || r == 0x7f {
				t.Fatalf("control character U+%04X survived from %q", r, in)
			}
		}
		if strings.HasPrefix(out, " ") || strings.HasSuffix(out, " ") {
			t.Fatalf("not trimmed: %q", out)
		}
	})
}

// A transcript arrives as JSON; make sure a hostile one cannot smuggle control
// characters through.
func TestTranscribeSanitizesUpstreamText(t *testing.T) {
	f := newFakeAPI(t)
	payload, _ := json.Marshal(map[string]any{
		"text": "rm -rf /\nsudo shutdown\r\n\x1b[31m",
	})
	f.Body = string(payload)

	c := newClient(f, transcribe.Options{})
	res, err := c.Transcribe(context.Background(), "a.webm", strings.NewReader("x"), nil)
	require.NoError(t, err)

	require.NotContains(t, res.Text, "\n", "a newline would execute the command")
	require.NotContains(t, res.Text, "\r")
	require.NotContains(t, res.Text, "\x1b")
}
