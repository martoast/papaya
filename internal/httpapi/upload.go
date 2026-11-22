package httpapi

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/martoast/papaya/internal/hub"
)

// upload streams a file to the session's remote host and injects its path at
// the shell cursor.
//
// The body is read with MultipartReader rather than ParseMultipartForm: the
// latter buffers the whole upload to disk before the handler sees any of it,
// which defeats both the size limit and streaming.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeOf(w, r)
	if !ok {
		return
	}

	sess, err := s.sessionFor(sc, r.URL.Query().Get("session"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes)

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

		filename := part.FileName()
		if filename == "" {
			part.Close() // a plain form field, not the file
			continue
		}

		remote, err := sess.Upload(filename, part)
		part.Close()

		if err != nil {
			switch {
			case errors.Is(err, hub.ErrNotConnected):
				writeError(w, http.StatusConflict, "session is not connected")
			case isTooLarge(err):
				writeError(w, http.StatusRequestEntityTooLarge, "file exceeds the upload size limit")
			default:
				log.Printf("upload to session %s failed: %v", sess.ID, err)
				writeError(w, http.StatusBadGateway, "upload failed")
			}
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"path": remote, "name": filename})
		return
	}

	writeError(w, http.StatusBadRequest, "no file part in request")
}

func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}
