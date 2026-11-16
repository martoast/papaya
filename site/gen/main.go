// Command gen writes the landing page's live theme previews: one document per
// theme in site/themes/, built by the same code Papaya serves to its sandboxed
// scene frame, plus themes.json with each theme's colours.
//
//	go run ./site/gen
package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/martoast/papaya/internal/themes"
)

func main() {
	out := filepath.Join("site", "themes")
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	store, err := themes.Open(filepath.Join(os.TempDir(), "papaya-site-gen"))
	if err != nil {
		log.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		log.Fatal(err)
	}
	var all []*themes.Theme
	for _, s := range list {
		t, err := store.Get(s.Name)
		if err != nil {
			log.Fatal(err)
		}
		all = append(all, t)
	}
	// Example themes made by an agent, shipped in the repo.
	examples, _ := filepath.Glob(filepath.Join("examples", "themes", "*", "theme.json"))
	for _, f := range examples {
		b, err := os.ReadFile(f)
		if err != nil {
			log.Fatal(err)
		}
		var t themes.Theme
		if err := json.Unmarshal(b, &t); err != nil {
			log.Fatal(err)
		}
		if scene, err := os.ReadFile(filepath.Join(filepath.Dir(f), "scene.js")); err == nil {
			t.Scene = string(scene)
		}
		if err := t.Validate(); err != nil {
			log.Fatalf("%s: %v", f, err)
		}
		all = append(all, &t)
	}

	type entry struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Builtin     bool              `json:"builtin"`
		Opacity     float64           `json:"opacity"`
		Colors      map[string]string `json:"colors"`
	}
	var manifest []entry
	for _, t := range all {
		if t.Scene == "" {
			continue
		}
		doc := themes.SceneDocument(t, "preview", nil)
		if err := os.WriteFile(filepath.Join(out, t.Name+".html"), doc, 0o644); err != nil {
			log.Fatal(err)
		}
		op := t.Opacity
		if op == 0 {
			op = themes.DefaultOpacity
		}
		manifest = append(manifest, entry{t.Name, t.Description, t.Builtin, op, t.Colors})
	}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "themes.json"), b, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %d theme previews to %s", len(manifest), out)
}
