package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martoast/papaya/internal/transcribe"
)

// Turning voice on from the app.
//
// Voice input needs an OpenAI key, and setting an environment variable on a
// server is exactly the kind of step that loses people. So the mic is always
// there; tapping it before voice is on asks for the key, Papaya checks it with
// OpenAI, and keeps it in its state directory (mode 0600). The key never goes
// back to any browser: no endpoint returns it.

// LoadVoiceKey reads a key saved from the app, if there is one.
func LoadVoiceKey(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *Server) voiceKeySettable(w http.ResponseWriter) bool {
	switch {
	case s.transcriber == nil || s.opts.VoiceKeyPath == "":
		writeError(w, http.StatusNotFound, "voice can't be set up from the app on this server")
		return false
	case s.opts.VoiceKeyFromEnv:
		writeError(w, http.StatusConflict, "this server's voice key comes from its configuration (PAPAYA_OPENAI_API_KEY)")
		return false
	}
	return true
}

func (s *Server) setVoiceKey(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok || !s.voiceKeySettable(w) {
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	key := strings.TrimSpace(req.Key)
	if len(key) < 20 || len(key) > 512 || strings.ContainsAny(key, " \t\r\n") {
		writeError(w, http.StatusBadRequest, "that doesn't look like an OpenAI API key (they start with sk-)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.transcriber.CheckKey(ctx, key); err != nil {
		if errors.Is(err, transcribe.ErrKeyRejected) {
			writeError(w, http.StatusBadRequest, "OpenAI didn't accept that key. Check it was copied whole.")
			return
		}
		writeError(w, http.StatusBadGateway, "couldn't check the key with OpenAI; try again in a moment")
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.opts.VoiceKeyPath), 0o700); err == nil {
		tmp := s.opts.VoiceKeyPath + ".tmp"
		if err = os.WriteFile(tmp, []byte(key+"\n"), 0o600); err == nil {
			err = os.Rename(tmp, s.opts.VoiceKeyPath)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "couldn't save the key")
			return
		}
	}
	s.transcriber.SetAPIKey(key)
	log.Printf("voice: turned on from the app by %s", user)
	writeJSON(w, http.StatusOK, map[string]bool{"transcription": true})
}

func (s *Server) deleteVoiceKey(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok || !s.voiceKeySettable(w) {
		return
	}
	if err := os.Remove(s.opts.VoiceKeyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, "couldn't remove the key")
		return
	}
	s.transcriber.SetAPIKey("")
	log.Printf("voice: turned off from the app by %s", user)
	writeJSON(w, http.StatusOK, map[string]bool{"transcription": false})
}
