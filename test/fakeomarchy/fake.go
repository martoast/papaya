// Package fakeomarchy serves a tiny stand-in for Omarchy's GitHub repo (the
// contents API and raw files), so tests never depend on GitHub.
package fakeomarchy

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
)

const colors = `accent = "#7aa2f7"
background = "#1a1b26"
foreground = "#a9b1d6"
cursor = "#c0caf5"
selection_background = "#33467c"
color0 = "#32344a"
red = "#f7768e"
green = "#9ece6a"
yellow = "#e0af68"
blue = "#7aa2f7"
magenta = "#ad8ee6"
cyan = "#449dab"
`

// New starts the fake and returns its raw and API base URLs.
func New() (srv *httptest.Server, raw, api string) {
	var pic bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{122, 162, 247, 255})
	_ = png.Encode(&pic, img)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/themes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"name":"tokyo-night","type":"dir"},{"name":"starship","type":"dir"},{"name":"README.md","type":"file"}]`))
	})
	mux.HandleFunc("/api/themes/tokyo-night/backgrounds", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"name":"1-tokyo.png","type":"file"}]`))
	})
	mux.HandleFunc("/api/themes/starship/backgrounds", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	for _, n := range []string{"tokyo-night", "starship"} {
		mux.HandleFunc("/raw/"+n+"/colors.toml", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(colors)) })
	}
	mux.HandleFunc("/raw/tokyo-night/backgrounds/1-tokyo.png", func(w http.ResponseWriter, r *http.Request) { w.Write(pic.Bytes()) })
	srv = httptest.NewServer(mux)
	return srv, srv.URL + "/raw", srv.URL + "/api/themes"
}
