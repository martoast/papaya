package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/martoast/papaya/internal/themes"
)

// Omarchy's themes, from the app.
//
// Omarchy (github.com/omacom/omarchy, MIT) ships a library of beautiful
// themes. papaya-theme could already import one for an agent; this puts the
// whole library in the theme picker. The page may only talk to Papaya (its
// CSP), so Papaya fetches from GitHub: the list with each theme's colours for
// the previews (cached), and on a tap the colours plus the first wallpaper.

var (
	// OmarchyRaw and OmarchyAPI are where Omarchy's themes are read; tests
	// point them at a fake.
	OmarchyRaw = "https://raw.githubusercontent.com/omacom/omarchy/quattro/themes"
	OmarchyAPI = "https://api.github.com/repos/omacom/omarchy/contents/themes"
	omarchyRef = "quattro"
)

var omarchyName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

type omarchyTheme struct {
	Name      string            `json:"name"`
	Title     string            `json:"title"`
	Colors    map[string]string `json:"colors"`
	Installed bool              `json:"installed"`
}

type omarchyCache struct {
	mu     sync.Mutex
	at     time.Time
	themes []omarchyTheme
}

var omarchyHTTP = &http.Client{Timeout: 25 * time.Second}

func omarchyGet(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "papaya")
	res, err := omarchyHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("too large")
	}
	return b, nil
}

// omarchyDir lists a directory of Omarchy's repo through the contents API.
func omarchyDir(ctx context.Context, path string) ([]struct{ Name, Type string }, error) {
	url := OmarchyAPI
	if path != "" {
		url += "/" + path
	}
	b, err := omarchyGet(ctx, url+"?ref="+omarchyRef, 1<<20)
	if err != nil {
		return nil, err
	}
	var out []struct{ Name, Type string }
	return out, json.Unmarshal(b, &out)
}

func omarchyTitle(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func (s *Server) omarchyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/omarchy", s.listOmarchy)
	mux.HandleFunc("POST /api/omarchy/{name}", s.useOmarchy)
}

func (s *Server) listOmarchy(w http.ResponseWriter, r *http.Request) {
	if _, ok := owner(w, r); !ok {
		return
	}
	c := &s.omarchy
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.themes == nil || time.Since(c.at) > 6*time.Hour {
		list, err := fetchOmarchy(r.Context())
		if err != nil && c.themes == nil {
			writeError(w, http.StatusBadGateway, "couldn't reach Omarchy's themes on GitHub")
			return
		}
		if err == nil {
			c.themes, c.at = list, time.Now()
		}
	}
	out := make([]omarchyTheme, len(c.themes))
	copy(out, c.themes)
	for i := range out {
		_, err := s.themes.Get(out[i].Name)
		out[i].Installed = err == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"themes": out})
}

func fetchOmarchy(ctx context.Context) ([]omarchyTheme, error) {
	dir, err := omarchyDir(ctx, "")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range dir {
		if e.Type == "dir" && omarchyName.MatchString(e.Name) {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	out := make([]omarchyTheme, len(names))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = omarchyTheme{Name: n, Title: omarchyTitle(n)}
			b, err := omarchyGet(ctx, OmarchyRaw+"/"+n+"/colors.toml", 64<<10)
			if err != nil {
				return
			}
			if t, err := themes.FromOmarchy(n, out[i].Title, b); err == nil {
				out[i].Colors = t.Colors
			}
		}()
	}
	wg.Wait()
	// Only themes whose colours could be read are offered.
	kept := out[:0]
	for _, t := range out {
		if t.Colors != nil {
			kept = append(kept, t)
		}
	}
	return kept, nil
}

// useOmarchy imports one of Omarchy's themes (colours and its first
// wallpaper) and switches to it.
func (s *Server) useOmarchy(w http.ResponseWriter, r *http.Request) {
	user, ok := owner(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !omarchyName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "not an Omarchy theme name")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	toml, err := omarchyGet(ctx, OmarchyRaw+"/"+name+"/colors.toml", 64<<10)
	if err != nil {
		writeError(w, http.StatusBadGateway, "couldn't fetch "+name+" from Omarchy")
		return
	}
	t, err := themes.FromOmarchy(name, omarchyTitle(name), toml)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := s.themes.Put(t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// The wallpaper is a bonus: the colours stand on their own without it.
	if dir, err := omarchyDir(ctx, name+"/backgrounds"); err == nil {
		var pics []string
		for _, e := range dir {
			if e.Type == "file" {
				pics = append(pics, e.Name)
			}
		}
		sort.Strings(pics)
		if len(pics) > 0 {
			if img, err := omarchyGet(ctx, OmarchyRaw+"/"+name+"/backgrounds/"+pics[0], themes.MaxImageBytes); err == nil {
				_, _ = s.themes.PutImage(name, img)
			}
		}
	}
	if err := s.themes.SetActive(user, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.events.publish(user, map[string]any{"type": "theme"})
	writeJSON(w, http.StatusOK, viewOf(s.themes.Active(user)))
}
