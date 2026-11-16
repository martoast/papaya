package harness

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// FakeOpenAI stands in for the OpenAI audio endpoint so the transcription path
// is testable without a network call or an API key.
//
// It records the request so tests can assert on the shape papaya sends,
// particularly the keywords[] encoding the real API requires.
type FakeOpenAI struct {
	// Text is returned as the transcription. Set before the request.
	Text string
	// Status and Body override the response when simulating upstream failures.
	Status int
	Body   string

	srv *httptest.Server

	mu        sync.Mutex
	auth      string
	model     string
	keywords  []string
	languages []string
	filename  string
	audioLen  int64
	calls     int
}

// NewFakeOpenAI starts the stub and registers cleanup.
func NewFakeOpenAI(t *testing.T) *FakeOpenAI {
	t.Helper()

	f := &FakeOpenAI{Text: "hello from the fake transcriber"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// BaseURL is what papaya should be pointed at.
func (f *FakeOpenAI) BaseURL() string { return f.srv.URL }

func (f *FakeOpenAI) handle(w http.ResponseWriter, r *http.Request) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "bad content type", http.StatusBadRequest)
		return
	}

	auth := r.Header.Get("Authorization")
	var (
		model, filename string
		keywords, langs []string
		audioLen        int64
	)

	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		switch part.FormName() {
		case "model":
			b, _ := io.ReadAll(part)
			model = string(b)
		case "keywords[]":
			b, _ := io.ReadAll(part)
			keywords = append(keywords, string(b))
		case "languages[]":
			b, _ := io.ReadAll(part)
			langs = append(langs, string(b))
		case "file":
			filename = part.FileName()
			audioLen, _ = io.Copy(io.Discard, part)
		}
		part.Close()
	}

	f.mu.Lock()
	f.auth, f.model, f.filename = auth, model, filename
	f.keywords, f.languages, f.audioLen = keywords, langs, audioLen
	f.calls++
	status, body, text := f.Status, f.Body, f.Text
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
	}
	if body != "" {
		io.WriteString(w, body)
		return
	}

	enc, _ := json.Marshal(map[string]any{
		"text":      text,
		"languages": []map[string]string{{"code": "en"}},
	})
	w.Write(enc)
}

func (f *FakeOpenAI) Model() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.model
}

func (f *FakeOpenAI) Keywords() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keywords...)
}

func (f *FakeOpenAI) Languages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.languages...)
}

func (f *FakeOpenAI) Filename() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.filename
}

func (f *FakeOpenAI) AudioLen() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.audioLen
}

func (f *FakeOpenAI) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *FakeOpenAI) Auth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth
}
