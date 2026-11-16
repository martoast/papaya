package httpapi

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/transcribe"
)

// Capabilities tells the frontend which optional features are configured, so it
// can hide a control rather than offer one that always fails.
type Capabilities struct {
	Transcription bool   `json:"transcription"`
	MaxUploadMB   int64  `json:"maxUploadMB"`
	MaxAudioMB    int64  `json:"maxAudioMB"`
	MaxSessions   int    `json:"maxSessions"`
	DefaultUser   string `json:"defaultUser"`
	// VoiceSettable says the voice key can be pasted (or removed) in the app.
	VoiceSettable bool `json:"voiceSettable"`
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, Capabilities{
		Transcription: s.transcriber.Enabled(),
		MaxUploadMB:   s.opts.MaxUploadBytes >> 20,
		MaxAudioMB:    s.opts.MaxAudioBytes >> 20,
		MaxSessions:   s.opts.MaxSessions,
		DefaultUser:   s.opts.DefaultSSHUser,
		VoiceSettable: s.transcriber != nil && s.opts.VoiceKeyPath != "" && !s.opts.VoiceKeyFromEnv,
	})
}

// transcribeAudio converts a recording to text and injects it at the shell
// cursor, the same way an uploaded image's path is injected.
//
// The audio streams through to the upstream API; it is never buffered here.
func (s *Server) transcribeAudio(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}

	if !s.transcriber.Enabled() {
		writeError(w, http.StatusServiceUnavailable,
			"voice transcription is not configured (set PAPAYA_OPENAI_API_KEY)")
		return
	}

	if !s.transcribeLimit.allow(user, time.Now()) {
		log.Printf("rate limited: %s exceeded the transcription budget", user)
		writeError(w, http.StatusTooManyRequests,
			"too many transcription requests, try again shortly")
		return
	}

	// A session is required: the transcript is injected into its stdin, and
	// ownership must be checked before anything is sent upstream.
	sess, err := s.hub.Get(user, r.URL.Query().Get("session"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxAudioBytes)

	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart body")
		return
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "malformed multipart body")
			return
		}
		if part.FileName() == "" {
			part.Close()
			continue
		}

		// Host names are the terms most often misheard, and papaya already
		// knows them, so they are passed as keywords for this request.
		res, err := s.transcriber.Transcribe(r.Context(), part.FileName(), part, s.transcriptionKeywords(sess))
		part.Close()

		if err != nil {
			switch {
			case errors.Is(err, transcribe.ErrNotConfigured):
				writeError(w, http.StatusServiceUnavailable, "voice transcription is not configured")
			case isTooLarge(err):
				writeError(w, http.StatusRequestEntityTooLarge, "recording exceeds the audio size limit")
			default:
				log.Printf("transcription for session %s failed: %v", sess.ID, err)
				writeError(w, http.StatusBadGateway, sanitizeUpstream(err))
			}
			return
		}

		if res.Text == "" {
			writeJSON(w, http.StatusOK, map[string]any{"text": "", "injected": false})
			return
		}

		// Injected without a trailing newline: the user reads it and decides.
		injected := true
		if err := sess.InjectText(res.Text); err != nil {
			if errors.Is(err, hub.ErrNotConnected) {
				injected = false
			} else {
				log.Printf("injecting transcript into session %s: %v", sess.ID, err)
				injected = false
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"text":      res.Text,
			"languages": res.Languages,
			"injected":  injected,
		})
		return
	}

	writeError(w, http.StatusBadRequest, "no audio part in request")
}

// transcriptionKeywords builds the per-request hint list: the session's own
// host, plus every configured host name.
func (s *Server) transcriptionKeywords(sess *hub.Session) []string {
	view := sess.View()

	words := make([]string, 0, len(s.opts.StaticHosts)+4)
	words = append(words, view.Host, view.User)
	words = append(words, s.opts.StaticHosts...)

	// Short forms too: "devbox-1.tailnet.ts.net" is never spoken in full.
	for _, h := range s.opts.StaticHosts {
		if i := strings.Index(h, "."); i > 0 {
			words = append(words, h[:i])
		}
	}
	return words
}

// sanitizeUpstream keeps upstream detail useful without leaking configuration.
func sanitizeUpstream(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "API key was rejected"):
		return "the transcription API key was rejected"
	case strings.Contains(msg, "rate limit"):
		return "transcription rate limit reached, try again shortly"
	case strings.Contains(msg, "context deadline"), strings.Contains(msg, "timeout"):
		return "transcription timed out"
	default:
		return "transcription failed"
	}
}
