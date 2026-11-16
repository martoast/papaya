// Package web embeds the built frontend into the binary.
package web

import (
	"embed"
	"io/fs"
)

// all: is required so the placeholder .gitkeep keeps the directory embeddable
// before the first frontend build.
//
//go:embed all:dist
var dist embed.FS

// Assets returns the built frontend, or nil when it has not been built yet.
// A nil result makes the server serve a "run the build" message rather than
// silently 404ing every page.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
