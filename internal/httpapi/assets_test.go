package httpapi

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testAssets() *Server {
	big := strings.Repeat("console.log('papaya');\n", 400)
	fsys := fstest.MapFS{
		"index.html":             {Data: []byte("<html>" + strings.Repeat("x", 2000) + "</html>")},
		"assets/index-abc123.js": {Data: []byte(big)},
		"manifest.webmanifest":   {Data: []byte(`{"name":"papaya"}`)},
		"apple-touch-icon.png":   {Data: []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 2000))},
	}
	return &Server{static: fsys}
}

func get(t *testing.T, h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The bundle is named by its hash, so the browser keeps it for good and a
// phone reopening the app does not download it again.
func TestHashedAssetsAreCachedForGood(t *testing.T) {
	h := testAssets().staticHandler()
	rec := get(t, h, "/assets/index-abc123.js", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Fatalf("Cache-Control = %q", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

// index.html names the current hashes, so it must be revalidated every time —
// and revalidation must be cheap, which needs a validator.
func TestIndexRevalidatesWithAn304(t *testing.T) {
	h := testAssets().staticHandler()
	first := get(t, h, "/", map[string]string{"Accept-Encoding": "gzip"})
	if first.Code != 200 {
		t.Fatalf("status %d", first.Code)
	}
	if cc := first.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on index.html")
	}
	again := get(t, h, "/", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
	if again.Code != http.StatusNotModified {
		t.Fatalf("revalidation status %d, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Fatal("304 carried a body")
	}
}

func TestGzipOnlyWhenAccepted(t *testing.T) {
	h := testAssets().staticHandler()

	plain := get(t, h, "/assets/index-abc123.js", nil)
	if plain.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzipped for a client that did not ask")
	}

	refused := get(t, h, "/assets/index-abc123.js", map[string]string{"Accept-Encoding": "gzip;q=0, br"})
	if refused.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzipped for a client that refused it")
	}

	gz := get(t, h, "/assets/index-abc123.js", map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("not gzipped")
	}
	if gz.Body.Len() >= plain.Body.Len()/2 {
		t.Fatalf("gzip body %d not much smaller than %d", gz.Body.Len(), plain.Body.Len())
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	if !bytes.Equal(out, plain.Body.Bytes()) {
		t.Fatal("gzip body does not decode to the file")
	}
	// The two encodings must not share a validator, or a cache could answer
	// one with the other.
	if gz.Header().Get("ETag") == plain.Header().Get("ETag") {
		t.Fatal("gzip and identity share an ETag")
	}
	if !strings.Contains(gz.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatal("no Vary: Accept-Encoding")
	}
}

func TestManifestTypeAndImagesUncompressed(t *testing.T) {
	h := testAssets().staticHandler()
	m := get(t, h, "/manifest.webmanifest", map[string]string{"Accept-Encoding": "gzip"})
	if ct := m.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("manifest Content-Type = %q", ct)
	}
	png := get(t, h, "/apple-touch-icon.png", map[string]string{"Accept-Encoding": "gzip"})
	if png.Header().Get("Content-Encoding") != "" {
		t.Fatal("an image was gzipped")
	}
	if png.Code != 200 {
		t.Fatalf("png status %d", png.Code)
	}
}

func TestMissingFileStillNotFound(t *testing.T) {
	h := testAssets().staticHandler()
	if rec := get(t, h, "/nope.js", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}
