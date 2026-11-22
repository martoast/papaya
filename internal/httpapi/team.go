package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/martoast/papaya/internal/setup"
)

// The Team panel: the admin adds a teammate by tailnet login (or tag) and ticks
// the machines they may use. Only an admin can read or change it.

// TeamView is the team plus the machines that can be given out.
type TeamView struct {
	Members  []setup.Member `json:"members"`
	Machines []string       `json:"machines"`
}

func (s *Server) teamRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/team", s.adminOnly(s.getTeam))
	mux.HandleFunc("PUT /api/team", s.adminOnly(s.putTeam))
}

func (s *Server) teamView() TeamView {
	st := s.store()
	v := TeamView{Members: st.Team(), Machines: st.Hosts()}
	if v.Members == nil {
		v.Members = []setup.Member{}
	}
	return v
}

func (s *Server) getTeam(w http.ResponseWriter, r *http.Request) {
	if s.store() == nil {
		writeError(w, http.StatusNotFound, "setup is not available")
		return
	}
	writeJSON(w, http.StatusOK, s.teamView())
}

func (s *Server) putTeam(w http.ResponseWriter, r *http.Request) {
	st := s.store()
	if st == nil {
		writeError(w, http.StatusNotFound, "setup is not available")
		return
	}
	var req struct {
		Members []setup.Member `json:"members"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Members) > 100 {
		writeError(w, http.StatusBadRequest, "too many members")
		return
	}
	if err := st.SetTeam(req.Members, s.auth.IsAdmin); err != nil {
		if errors.Is(err, setup.ErrInvalidTeam) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not save: "+err.Error())
		return
	}
	user, _ := owner(w, r)
	log.Printf("team: %s saved the team (%d member(s))", user, len(req.Members))
	writeJSON(w, http.StatusOK, s.teamView())
}

// adminOnly wraps a handler so that only admins reach it.
func (s *Server) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		h(w, r)
	}
}
