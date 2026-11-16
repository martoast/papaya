// Package transcribe converts recorded speech to text via the OpenAI audio API.
//
// The API key lives here and never reaches the browser: the frontend posts audio
// to papaya, papaya posts it to OpenAI. That is the same rule the SSH private key
// follows, and it is why this is a server-side proxy rather than a direct call
// from the page.
package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// ErrNotConfigured is returned when no API key is set.
var ErrNotConfigured = errors.New("transcription is not configured")

// ShellVocabulary is sent with every request unless the caller opts out.
//
// General speech models transcribe what a person plausibly said, and in ordinary
// English "git status" is "get status" and "kubectl" is nothing at all. These
// hints are the difference between a usable terminal dictation feature and one
// that needs every line retyped. Kept short: the list costs request budget, and
// hints can pull the transcript toward words that were never spoken.
var ShellVocabulary = []string{
	"git", "git status", "git commit", "git push", "git pull", "git diff",
	"git checkout", "git rebase", "git log", "branch", "stash",
	"ssh", "scp", "rsync", "sudo", "chmod", "chown", "mkdir", "grep", "sed", "awk",
	"curl", "npm", "npm run build", "npm install", "pnpm", "yarn", "node",
	"python", "pip", "cargo", "make", "docker", "docker compose", "kubectl",
	"systemctl", "journalctl", "tmux", "vim", "nano", "htop", "tail", "cat",
	"ls", "cd", "ps aux", "kill", "stdout", "stderr", "localhost", "sudo apt",
}

// Options configures a Client.
type Options struct {
	APIKey  string
	BaseURL string
	Model   string
	// Languages hints the expected input languages. For gpt-transcribe this
	// replaces the singular language field; sending both is an error.
	Languages []string
	// Keywords are literal terms expected in the audio. They matter for a
	// terminal: without them "devbox-1" comes back as "dev box one".
	Keywords []string
	// NoShellVocabulary suppresses the built-in command hints.
	NoShellVocabulary bool
	Timeout           time.Duration
}

// Client talks to the OpenAI transcription endpoint.
type Client struct {
	opts Options
	http *http.Client
}

func New(opts Options) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.openai.com/v1"
	}
	if opts.Model == "" {
		opts.Model = "gpt-transcribe"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	return &Client{opts: opts, http: &http.Client{Timeout: opts.Timeout}}
}

// keywordsFor combines the per-request hints with the configured and built-in
// ones. Caller-supplied terms come first so they survive the length cap.
func (c *Client) keywordsFor(extra []string) []string {
	out := make([]string, 0, len(extra)+len(c.opts.Keywords)+len(ShellVocabulary))
	out = append(out, extra...)
	out = append(out, c.opts.Keywords...)
	if !c.opts.NoShellVocabulary {
		out = append(out, ShellVocabulary...)
	}
	return out
}

// Enabled reports whether an API key is configured.
func (c *Client) Enabled() bool { return c != nil && c.opts.APIKey != "" }

// Result is a completed transcription.
type Result struct {
	Text      string   `json:"text"`
	Languages []string `json:"languages,omitempty"`
}

// Transcribe streams audio to the API and returns the recognised text.
//
// The audio is piped straight through rather than buffered: a 25 MB recording
// must not become 25 MB of server memory.
func (c *Client) Transcribe(ctx context.Context, filename string, audio io.Reader, extraKeywords []string) (*Result, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		// Any error here closes the pipe, which fails the request rather than
		// letting it hang.
		writeErr := func() error {
			if err := mw.WriteField("model", c.opts.Model); err != nil {
				return err
			}
			for _, lang := range c.opts.Languages {
				if err := mw.WriteField("languages[]", lang); err != nil {
					return err
				}
			}
			// The API rejects the whole request if a keyword contains a newline
			// or angle bracket, so they are cleaned before being sent.
			for _, kw := range cleanKeywords(c.keywordsFor(extraKeywords)) {
				if err := mw.WriteField("keywords[]", kw); err != nil {
					return err
				}
			}

			part, err := mw.CreateFormFile("file", safeAudioName(filename))
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, audio); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(writeErr)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(c.opts.BaseURL, "/")+"/audio/transcriptions", pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		// Never wrap the raw error: the request URL is safe, but keep the shape
		// predictable for the caller's sanitising.
		return nil, fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading transcription response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("transcription failed: %s", apiError(resp.StatusCode, body))
	}

	var out struct {
		Text      string `json:"text"`
		Languages []struct {
			Code string `json:"code"`
		} `json:"languages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("malformed transcription response")
	}

	res := &Result{Text: CleanText(out.Text)}
	for _, l := range out.Languages {
		if l.Code != "" {
			res.Languages = append(res.Languages, l.Code)
		}
	}
	return res, nil
}

// apiError extracts a useful message without echoing anything sensitive.
func apiError(status int, body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		switch status {
		case http.StatusUnauthorized:
			return "the transcription API key was rejected"
		case http.StatusTooManyRequests:
			return "transcription rate limit reached, try again shortly"
		}
		return e.Error.Message
	}
	return fmt.Sprintf("upstream returned %d", status)
}

// CleanText makes a transcription safe to write into a terminal's stdin.
//
// Recognised speech should be plain text, but it arrives from a remote service
// and is written straight to a shell, so control characters are stripped rather
// than trusted. A newline in particular would submit the line before the user
// has read it.
func CleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\v', '\f', '\r':
			return ' '
		}
		if r == 0x7f || (unicode.IsControl(r) && r != ' ') {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// cleanKeywords drops characters the API rejects and removes duplicates.
func cleanKeywords(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))

	for _, kw := range in {
		kw = strings.Map(func(r rune) rune {
			if r == '<' || r == '>' || r == '\r' || r == '\n' {
				return -1
			}
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, kw)

		kw = strings.TrimSpace(kw)
		if kw == "" || seen[kw] {
			continue
		}
		seen[kw] = true
		out = append(out, kw)

		// The API has a length limit on the whole request; a runaway keyword
		// list is never worth failing the transcription over.
		if len(out) >= 100 {
			break
		}
	}
	return out
}

// safeAudioName keeps the extension, which the API uses to detect the format,
// and discards everything else about the client-supplied name.
func safeAudioName(name string) string {
	ext := strings.ToLower(filepath.Ext(filepath.Base(name)))
	switch ext {
	case ".mp3", ".mp4", ".mpeg", ".mpga", ".m4a", ".wav", ".webm", ".ogg", ".flac":
		return "audio" + ext
	default:
		// MediaRecorder's default in Chrome and Firefox.
		return "audio.webm"
	}
}
