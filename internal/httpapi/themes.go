package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/martoast/papaya/internal/themes"
)

// Themes are written by agents with papaya-theme and applied live to every
// browser the same person has open. The flow:
//
//	PUT  /api/themes/{name}        save theme.json (colours, opacity, description)
//	PUT  /api/themes/{name}/scene  save scene.js (raw JavaScript)
//	POST /api/themes/active        {"name": "..."}: switch to it; browsers follow
//	GET  /api/themes/{name}/status what the browsers saw: running at N fps, or the error
//	GET  /api/events               server-sent events: theme changes, cues, messages
//	GET  /theme/scene/{name}       the scene, as a document for a sandboxed frame
//
// Colours land on the page as CSS custom properties, after themes.Validate has
// checked each value. Scenes never run in the page: see SceneCSP.

// EnableThemes turns on themes, kept in dir.
func (s *Server) EnableThemes(store *themes.Store) {
	s.themes = store
	if s.events == nil {
		s.events = newBroker()
	}
}

func (s *Server) themeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/events", s.serveEvents)
	if s.themes == nil {
		return
	}
	mux.HandleFunc("GET /api/themes", s.listThemes)
	mux.HandleFunc("GET /api/theme", s.activeTheme)
	mux.HandleFunc("POST /api/themes/active", s.setActiveTheme)
	mux.HandleFunc("POST /api/themes/cue", s.cueTheme)
	mux.HandleFunc("GET /api/themes/{name}", s.getTheme)
	mux.HandleFunc("PUT /api/themes/{name}", s.putTheme)
	mux.HandleFunc("PUT /api/themes/{name}/scene", s.putScene)
	mux.HandleFunc("PUT /api/themes/{name}/image", s.putImage)
	mux.HandleFunc("POST /api/themes/{name}/omarchy", s.importOmarchy)
	mux.HandleFunc("DELETE /api/themes/{name}", s.deleteTheme)
	mux.HandleFunc("GET /api/themes/{name}/status", s.themeStatus)
	mux.HandleFunc("POST /api/themes/{name}/status", s.reportThemeStatus)
	mux.HandleFunc("GET /theme/scene/{name}", s.sceneDocument)
}

// activeView is what a browser needs to apply a theme. The scene source stays
// out: the browser loads it into the sandboxed frame by URL.
type activeView struct {
	Name     string            `json:"name"`
	Version  string            `json:"version,omitempty"`
	Colors   map[string]string `json:"colors,omitempty"`
	Opacity  float64           `json:"opacity,omitempty"`
	SceneURL string            `json:"sceneUrl,omitempty"`
}

func viewOf(t *themes.Theme) activeView {
	if t == nil {
		return activeView{Name: "default"}
	}
	v := activeView{Name: t.Name, Version: t.Version(), Colors: t.Colors, Opacity: t.Opacity}
	if v.Opacity == 0 {
		v.Opacity = themes.DefaultOpacity
	}
	if t.HasBackdrop() {
		v.SceneURL = "/theme/scene/" + t.Name + "?v=" + v.Version
	}
	return v
}

func (s *Server) listThemes(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	list, err := s.themes.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	active := "default"
	if t := s.themes.Active(user); t != nil {
		active = t.Name
	}
	if r.URL.Query().Get("format") == "text" {
		var b strings.Builder
		fmt.Fprintf(&b, "on: %s\n", active)
		for _, t := range list {
			mark, kind, origin := " ", "colours", ""
			if t.Name == active {
				mark = "*"
			}
			if t.HasScene {
				kind = "scene"
			}
			if t.Builtin {
				origin = "built-in"
			}
			fmt.Fprintf(&b, "%s %-20s %-8s %-9s %s\n", mark, t.Name, kind, origin, t.Description)
		}
		mark := " "
		if active == "default" {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %-20s %-8s %-9s %s\n", mark, "default", "colours", "built-in", "Papaya's plain look")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, b.String())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "themes": list})
}

func (s *Server) activeTheme(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, viewOf(s.themes.Active(user)))
}

func (s *Server) getTheme(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	t, err := s.themes.Get(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no theme named "+r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// putTheme saves theme.json. A scene already saved for the theme is kept
// unless the body carries one, so theme.json and scene.js can be pushed
// separately in either order.
func (s *Server) putTheme(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	var t themes.Theme
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, themes.MaxSceneBytes+64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, "theme.json: "+err.Error())
		return
	}
	name := r.PathValue("name")
	if t.Name == "" {
		t.Name = name
	}
	if t.Name != name {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("theme.json says name %q but was pushed as %q", t.Name, name))
		return
	}
	t.ImageType, t.ImageSum = "", ""
	if old, err := s.themes.Get(name); err == nil && !old.Builtin {
		if t.Scene == "" {
			t.Scene = old.Scene
		}
		// The wallpaper is managed by its own endpoint; theme.json cannot claim one.
		t.ImageType, t.ImageSum = old.ImageType, old.ImageSum
	}
	if err := s.themes.Put(&t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.themeChanged(user, name)
	writeJSON(w, http.StatusOK, t.Summary())
}

// putScene saves scene.js on its own, as raw JavaScript.
func (s *Server) putScene(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, themes.MaxSceneBytes+1))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("scene.js is over the %d KB limit", themes.MaxSceneBytes>>10))
		return
	}
	t, err := s.themes.Get(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		t = &themes.Theme{Name: name}
	}
	t.Scene = string(body)
	if err := s.themes.Put(t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.themeChanged(user, name)
	writeJSON(w, http.StatusOK, t.Summary())
}

func (s *Server) deleteTheme(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	wasActive := false
	if t := s.themes.Active(user); t != nil && t.Name == r.PathValue("name") {
		wasActive = true
	}
	if err := s.themes.Delete(r.PathValue("name")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "no theme named "+r.PathValue("name"))
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if wasActive {
		s.events.publish(user, map[string]any{"type": "theme"})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setActiveTheme(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.themes.SetActive(user, strings.TrimSpace(req.Name)); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.events.publish(user, map[string]any{"type": "theme"})
	writeJSON(w, http.StatusOK, viewOf(s.themes.Active(user)))
}

// themeChanged tells browsers to reload a theme that was just saved, if it is
// the one this person has on.
func (s *Server) themeChanged(user, name string) {
	if t := s.themes.Active(user); t != nil && t.Name == name {
		s.events.publish(user, map[string]any{"type": "theme"})
	}
}

// cueTheme fires a named moment scenes can react to: an agent runs
// `papaya-theme cue merged` after merging, and the scene sets off fireworks.
func (s *Server) cueTheme(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	name := cleanLabel(req.Name, 40)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name the cue, e.g. merged")
		return
	}
	s.events.publish(user, map[string]any{"type": "cue", "name": name})
	w.WriteHeader(http.StatusNoContent)
}

// sceneDocument serves a theme's scene as the document of the sandboxed frame.
func (s *Server) sceneDocument(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	t, err := s.themes.Get(r.PathValue("name"))
	if err != nil || !t.HasBackdrop() {
		http.NotFound(w, r)
		return
	}
	var image []byte
	if t.ImageType != "" {
		image, _ = s.themes.Image(t.Name)
	}
	nonce := themes.NewNonce()
	h := w.Header()
	// Replaces the page policy set by securityHeaders: this document may be
	// framed by Papaya itself, and may do nothing but run its own scripts.
	h.Set("Content-Security-Policy", themes.SceneCSP(nonce))
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Type", "text/html; charset=utf-8")
	// A versioned URL never changes content, so the browser may keep it; that
	// matters once a wallpaper makes the document a few megabytes. The nonce
	// is then reused from the cache, which is fine: the policy and the scripts
	// it authorises are cached together.
	if r.URL.Query().Get("v") == t.Version() {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-store")
	}
	w.Write(themes.SceneDocument(t, nonce, image))
}

// putImage saves a theme's wallpaper (raw WebP, PNG or JPEG); an empty body
// removes it.
func (s *Server) putImage(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, themes.MaxImageBytes+1))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("wallpaper is over the %d MB limit", themes.MaxImageBytes>>20))
		return
	}
	t, err := s.themes.PutImage(r.PathValue("name"), body)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.themeChanged(user, t.Name)
	writeJSON(w, http.StatusOK, t.Summary())
}

// importOmarchy turns an Omarchy colors.toml (the request body) into the
// theme's colours. A wallpaper and scene already saved for it are kept.
func (s *Server) importOmarchy(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "colors.toml is too large")
		return
	}
	name := r.PathValue("name")
	t, err := themes.FromOmarchy(name, cleanLabel(r.URL.Query().Get("title"), 60), body)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if old, err := s.themes.Get(name); err == nil && !old.Builtin {
		t.Scene, t.ImageType, t.ImageSum, t.Opacity = old.Scene, old.ImageType, old.ImageSum, old.Opacity
	}
	if err := s.themes.Put(t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.themeChanged(user, name)
	writeJSON(w, http.StatusOK, t.Summary())
}

// sceneStatus is what browsers report about a scene they ran.
type sceneStatus struct {
	Version string    `json:"version"`
	State   string    `json:"state"` // running | error
	FPS     int       `json:"fps,omitempty"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"`
}

func (s *Server) reportThemeStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	var st sceneStatus
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&st); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if st.State != "running" && st.State != "error" {
		writeError(w, http.StatusBadRequest, "state is running or error")
		return
	}
	st.Version = cleanLabel(st.Version, 20)
	st.Error = strings.ToValidUTF8(st.Error, "")
	if t, err := s.themes.Get(r.PathValue("name")); err == nil && st.Error != "" {
		st.Error = themes.SceneError(t, st.Error)
	}
	if len(st.Error) > 2000 {
		st.Error = st.Error[:2000]
	}
	if st.FPS < 0 || st.FPS > 1000 {
		st.FPS = 0
	}
	st.At = time.Now().UTC()
	s.statusMu.Lock()
	if s.sceneStatus == nil {
		s.sceneStatus = map[string]sceneStatus{}
	}
	s.sceneStatus[user+"\x00"+r.PathValue("name")] = st
	s.statusMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) themeStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	t, err := s.themes.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "no theme named "+name)
		return
	}
	out := map[string]any{
		"name":     name,
		"version":  t.Version(),
		"hasScene": t.HasBackdrop(),
		"active":   false,
		"viewers":  s.events.count(user),
	}
	if a := s.themes.Active(user); a != nil && a.Name == name {
		out["active"] = true
	}
	s.statusMu.Lock()
	st, seen := s.sceneStatus[user+"\x00"+name]
	s.statusMu.Unlock()
	if seen && st.Version == t.Version() {
		out["scene"] = st
	}
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, statusText(out, st, seen && st.Version == t.Version()))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// statusText is the status as papaya-theme prints it. The scene line's first
// word is what the command keys on: running, ERROR, waiting or none.
func statusText(out map[string]any, st sceneStatus, fresh bool) string {
	var b strings.Builder
	on := "not on"
	if out["active"] == true {
		on = "on"
	}
	fmt.Fprintf(&b, "theme:   %s (%s)\n", out["name"], on)
	fmt.Fprintf(&b, "viewers: %d open Papaya window(s)\n", out["viewers"])
	switch {
	case out["hasScene"] != true:
		b.WriteString("scene:   none (colours only)\n")
	case fresh && st.State == "running":
		fmt.Fprintf(&b, "scene:   running at %d fps\n", st.FPS)
	case fresh && st.State == "error":
		b.WriteString("scene:   ERROR, the browser stopped it:\n")
		for _, l := range strings.Split(strings.TrimSpace(st.Error), "\n") {
			b.WriteString("         " + l + "\n")
		}
	case out["viewers"] == 0:
		b.WriteString("scene:   waiting: no Papaya window is open to run it\n")
	default:
		b.WriteString("scene:   waiting: the browser reports about 3 s after the scene starts\n")
	}
	return b.String()
}

// cleanLabel keeps a short label printable.
func cleanLabel(v string, max int) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(v) {
		if r >= 0x20 && r != 0x7f && b.Len() < max {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- events ----

// broker fans server events out to each person's open browsers.
type broker struct {
	mu   sync.Mutex
	subs map[string]map[chan []byte]struct{}
}

func newBroker() *broker { return &broker{subs: map[string]map[chan []byte]struct{}{}} }

func (b *broker) subscribe(user string) (chan []byte, func()) {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	if b.subs[user] == nil {
		b.subs[user] = map[chan []byte]struct{}{}
	}
	b.subs[user][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[user], ch)
		if len(b.subs[user]) == 0 {
			delete(b.subs, user)
		}
		b.mu.Unlock()
	}
}

// publish never blocks: a browser too slow to take an event misses it, which
// for a theme change or a firework is the right trade.
func (b *broker) publish(user string, ev any) {
	if b == nil {
		return
	}
	msg, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[user] {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *broker) count(user string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[user])
}

// serveEvents streams a person's events as server-sent events.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	if s.events == nil {
		s.events = newBroker()
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": papaya events\n\n")
	if err := rc.Flush(); err != nil {
		return
	}
	ch, cancel := s.events.subscribe(user)
	defer cancel()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
