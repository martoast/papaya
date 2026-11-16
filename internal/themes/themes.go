// Package themes stores Papaya's themes: colours for the chrome and the
// terminal, and an optional animated backdrop ("scene") drawn behind them.
//
// Themes are meant to be written by coding agents. A person says "make me a
// koi pond theme"; their agent writes theme.json and scene.js and pushes them
// with papaya-theme, and every open Papaya tab switches live.
//
// A scene is arbitrary JavaScript, so it is never run in Papaya's own page.
// The browser runs it in a sandboxed frame with an opaque origin and a policy
// that allows no network, no navigation and no focus (see scene.go). The
// colours are validated here, value by value, because they are applied to the
// page itself as CSS custom properties.
package themes

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// MaxSceneBytes bounds a scene. Generous for hand- or agent-written canvas
// code, small enough that a theme is never a way to fill the disk.
const MaxSceneBytes = 256 << 10

// MaxThemes bounds the library.
const MaxThemes = 200

// DefaultOpacity is how opaque terminal backgrounds are while a scene runs,
// when the theme does not say: enough for text to stay readable, low enough
// for the scene to show through.
const DefaultOpacity = 0.82

// Theme is one theme as stored and as served.
type Theme struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Colors maps token names (see Tokens) to CSS colours.
	Colors map[string]string `json:"colors,omitempty"`
	// Opacity of terminal backgrounds while the scene runs, 0.2 to 1.
	// Zero means DefaultOpacity.
	Opacity float64 `json:"opacity,omitempty"`
	// Scene is the backdrop's JavaScript source. Empty means colours only.
	Scene string `json:"scene,omitempty"`
	// ImageType is set when the theme carries a wallpaper (kept beside the
	// theme as <name>.image); ImageSum identifies its content.
	ImageType string    `json:"imageType,omitempty"`
	ImageSum  string    `json:"imageSum,omitempty"`
	Updated   time.Time `json:"updated"`
	Builtin   bool      `json:"builtin,omitempty"`
}

// MaxImageBytes bounds a wallpaper.
const MaxImageBytes = 8 << 20

// HasBackdrop reports whether anything is drawn behind the terminals.
func (t *Theme) HasBackdrop() bool { return t.Scene != "" || t.ImageType != "" }

// Version identifies a theme's content, so browsers can tell a changed theme
// from the one they already show and scene URLs can be cache-busted.
func (t *Theme) Version() string {
	h := sha256.New()
	b, _ := json.Marshal(struct {
		C map[string]string
		O float64
		S string
		I string
	}{t.Colors, t.Opacity, t.Scene, t.ImageSum})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Summary is a theme without its scene source, for listings.
type Summary struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	HasScene    bool      `json:"hasScene"`
	HasImage    bool      `json:"hasImage,omitempty"`
	Builtin     bool      `json:"builtin,omitempty"`
	Updated     time.Time `json:"updated"`
	Version     string    `json:"version"`
}

func (t *Theme) Summary() Summary {
	return Summary{t.Name, t.Description, t.HasBackdrop(), t.ImageType != "", t.Builtin, t.Updated, t.Version()}
}

// Tokens are the colours a theme may set. Each is a CSS custom property
// --color-<token> on Papaya's page; nothing else on the page can be changed.
var Tokens = []string{
	"surface-0", "surface-1", "surface-2", "surface-3", "text-hi", "text-lo", "accent",
	"state-ok", "state-warn", "state-err",
	"term-bg", "term-fg", "term-cursor", "term-sel",
	"ansi-black", "ansi-red", "ansi-green", "ansi-yellow", "ansi-blue", "ansi-magenta", "ansi-cyan", "ansi-white",
	"ansi-bright-black", "ansi-bright-red", "ansi-bright-green", "ansi-bright-yellow",
	"ansi-bright-blue", "ansi-bright-magenta", "ansi-bright-cyan", "ansi-bright-white",
}

var tokenSet = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range Tokens {
		m[t] = true
	}
	return m
}()

var (
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	// A colour is a hex value or a colour function with plain numeric
	// arguments. Nothing that could close the declaration or load a URL.
	colorRE = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|(rgb|rgba|hsl|hsla|oklch|oklab|lab|lch)\([0-9a-z.,%/ +-]{1,80}\))$`)
	// The scene is inlined into a <script> element. These would end the
	// element or change how the HTML parser reads its contents.
	sceneBadRE = regexp.MustCompile(`(?i)</script|<script|<!--`)
)

// Validate checks a theme and normalises it. It reports problems in words an
// agent can act on without reading this file.
func (t *Theme) Validate() error {
	t.Name = strings.TrimSpace(t.Name)
	if !nameRE.MatchString(t.Name) {
		return fmt.Errorf("theme name %q: use lowercase letters, digits and dashes, up to 40 characters (e.g. koi-pond)", t.Name)
	}
	if t.Name == "default" {
		return errors.New(`"default" is Papaya's own look and cannot be replaced; pick another name`)
	}
	t.Description = strings.TrimSpace(t.Description)
	if len(t.Description) > 300 {
		return errors.New("description: keep it under 300 characters")
	}
	for k, v := range t.Colors {
		if !tokenSet[k] {
			return fmt.Errorf("colors.%s is not a theme colour; allowed: %s", k, strings.Join(Tokens, ", "))
		}
		v = strings.TrimSpace(v)
		if !colorRE.MatchString(v) {
			return fmt.Errorf("colors.%s = %q: use #rgb, #rrggbb, #rrggbbaa or rgb()/hsl()/oklch() with numbers only", k, v)
		}
		t.Colors[k] = v
	}
	if t.Opacity != 0 && (t.Opacity < 0.2 || t.Opacity > 1) {
		return fmt.Errorf("opacity %.2f: use a number from 0.2 (scene shows strongly) to 1 (terminals opaque)", t.Opacity)
	}
	if len(t.Scene) > MaxSceneBytes {
		return fmt.Errorf("scene is %d KB; the limit is %d KB", len(t.Scene)>>10, MaxSceneBytes>>10)
	}
	if !utf8.ValidString(t.Scene) {
		return errors.New("scene is not valid UTF-8")
	}
	if loc := sceneBadRE.FindStringIndex(t.Scene); loc != nil {
		line := strings.Count(t.Scene[:loc[0]], "\n") + 1
		return fmt.Errorf("scene line %d contains %q, which would break out of the page's <script> element; split the string (e.g. '<'+'/script')",
			line, t.Scene[loc[0]:loc[1]])
	}
	return nil
}

//go:embed builtin
var builtinFS embed.FS

// Store is the theme library in a directory, plus each person's choice.
type Store struct {
	dir     string
	mu      sync.Mutex
	builtin map[string]*Theme
}

// Open uses dir (created if absent) for the library.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, builtin: map[string]*Theme{}}
	if err := s.loadBuiltins(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadBuiltins() error {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			return err
		}
		var t Theme
		if err := json.Unmarshal(b, &t); err != nil {
			return fmt.Errorf("builtin %s: %w", e.Name(), err)
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		if scene, err := builtinFS.ReadFile("builtin/" + base + ".js"); err == nil {
			t.Scene = string(scene)
		}
		if err := t.Validate(); err != nil {
			return fmt.Errorf("builtin %s: %w", e.Name(), err)
		}
		t.Builtin = true
		s.builtin[t.Name] = &t
	}
	return nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name+".json") }

func (s *Store) imagePath(name string) string { return filepath.Join(s.dir, name+".image") }

// Image returns a theme's wallpaper, if it has one.
func (s *Store) Image(name string) ([]byte, error) {
	if !nameRE.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	return os.ReadFile(s.imagePath(name))
}

// PutImage stores a wallpaper for a theme that already exists. Empty data
// removes it. Only WebP, PNG and JPEG are accepted, recognised by content.
func (s *Store) PutImage(name string, data []byte) (*Theme, error) {
	t, err := s.Get(name)
	if err != nil {
		return nil, fmt.Errorf("no theme named %q yet; push its theme.json first", name)
	}
	if t.Builtin {
		return nil, fmt.Errorf("%q is a built-in theme; save your version under another name", name)
	}
	if len(data) == 0 {
		_ = os.Remove(s.imagePath(name))
		t.ImageType, t.ImageSum = "", ""
		return t, s.Put(t)
	}
	if len(data) > MaxImageBytes {
		return nil, fmt.Errorf("wallpaper is %d MB; the limit is %d MB", len(data)>>20, MaxImageBytes>>20)
	}
	kind := imageKind(data)
	if kind == "" {
		return nil, errors.New("wallpaper must be a WebP, PNG or JPEG image")
	}
	tmp := s.imagePath(name) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, s.imagePath(name)); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	t.ImageType, t.ImageSum = kind, hex.EncodeToString(sum[:])[:12]
	if t.Opacity == 0 {
		t.Opacity = 0.74 // a photo shows through a little more than a scene
	}
	return t, s.Put(t)
}

func imageKind(b []byte) string {
	switch {
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	case len(b) > 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	}
	return ""
}

// Get returns a theme by name.
func (s *Store) Get(name string) (*Theme, error) {
	if t, ok := s.builtin[name]; ok {
		c := *t
		return &c, nil
	}
	if !nameRE.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		return nil, err
	}
	var t Theme
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("theme %s is unreadable: %w", name, err)
	}
	return &t, nil
}

// List returns every theme, built-ins first.
func (s *Store) List() ([]Summary, error) {
	var out []Summary
	for _, t := range s.builtin {
		out = append(out, t.Summary())
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if name == e.Name() || name == "active" || strings.Contains(name, ".") || s.builtin[name] != nil {
			continue
		}
		if t, err := s.Get(name); err == nil {
			out = append(out, t.Summary())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return out[i].Builtin
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Put validates and saves a theme.
func (s *Store) Put(t *Theme) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if s.builtin[t.Name] != nil {
		return fmt.Errorf("%q is a built-in theme; save your version under another name (e.g. %s-mine)", t.Name, t.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path(t.Name)); errors.Is(err, fs.ErrNotExist) {
		if list, _ := s.List(); len(list)-len(s.builtin) >= MaxThemes {
			return fmt.Errorf("the library holds %d themes; remove some with papaya-theme rm", MaxThemes)
		}
	}
	t.Builtin = false
	t.Updated = time.Now().UTC().Truncate(time.Second)
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(t.Name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(t.Name))
}

// Delete removes a theme, and clears it as anyone's choice.
func (s *Store) Delete(name string) error {
	if s.builtin[name] != nil {
		return fmt.Errorf("%q is built in and cannot be removed", name)
	}
	if !nameRE.MatchString(name) {
		return fs.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(name)); err != nil {
		return err
	}
	_ = os.Remove(s.imagePath(name))
	act := s.readActive()
	changed := false
	for who, n := range act {
		if n == name {
			delete(act, who)
			changed = true
		}
	}
	if changed {
		return s.writeActive(act)
	}
	return nil
}

func (s *Store) readActive() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(s.dir, "active.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *Store) writeActive(m map[string]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := filepath.Join(s.dir, "active.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "active.json"))
}

// Active returns the theme a person has chosen, or nil for Papaya's default.
func (s *Store) Active(owner string) *Theme {
	s.mu.Lock()
	name := s.readActive()[owner]
	s.mu.Unlock()
	if name == "" {
		return nil
	}
	t, err := s.Get(name)
	if err != nil {
		return nil
	}
	return t
}

// SetActive records a person's choice. An empty name or "default" goes back
// to Papaya's own look.
func (s *Store) SetActive(owner, name string) error {
	if name == "default" {
		name = ""
	}
	if name != "" {
		if _, err := s.Get(name); err != nil {
			return fmt.Errorf("no theme named %q", name)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readActive()
	if name == "" {
		delete(m, owner)
	} else {
		m[owner] = name
	}
	return s.writeActive(m)
}
