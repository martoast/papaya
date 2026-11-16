package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// assetCache serves the embedded frontend with an ETag and, where the browser
// accepts it, gzip.
//
// The files are embedded in the binary and cannot change while it runs, so
// each is read, hashed and compressed once, on first request, and kept. The
// bundle is 480 KB and gzips to about 125 KB, which is the difference a phone
// on a relayed tailnet path actually feels.
type assetCache struct {
	fsys fs.FS
	mu   sync.Mutex
	m    map[string]*asset // nil value: looked up, not a servable file
}

type asset struct {
	name  string
	ctype string
	raw   []byte
	gz    []byte // nil when compression would not help
	etag  string
}

func newAssetCache(fsys fs.FS) *assetCache {
	return &assetCache{fsys: fsys, m: map[string]*asset{}}
}

// compressible lists the types worth gzipping. Images and fonts are already
// compressed.
var compressible = map[string]bool{
	".js": true, ".css": true, ".html": true, ".json": true,
	".webmanifest": true, ".svg": true, ".txt": true, ".map": true,
}

// get returns the asset for a request path, or nil when the path is not a
// plain file (a directory other than the root, or nothing at all), in which
// case the caller falls back to http.FileServer and its usual answers.
func (c *assetCache) get(urlPath string) *asset {
	name := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" {
		name = "index.html"
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if a, seen := c.m[name]; seen {
		return a
	}

	var a *asset
	if st, err := fs.Stat(c.fsys, name); err == nil && !st.IsDir() {
		if raw, err := fs.ReadFile(c.fsys, name); err == nil {
			a = buildAsset(name, raw)
		}
	}
	c.m[name] = a
	return a
}

func buildAsset(name string, raw []byte) *asset {
	ext := path.Ext(name)
	ctype := mime.TypeByExtension(ext)
	if ext == ".webmanifest" {
		ctype = "application/manifest+json"
	}
	if ctype == "" {
		ctype = http.DetectContentType(raw)
	}

	sum := sha256.Sum256(raw)
	a := &asset{name: name, ctype: ctype, raw: raw, etag: hex.EncodeToString(sum[:8])}

	if compressible[ext] && len(raw) > 1024 {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		zw.Write(raw)
		zw.Close()
		if buf.Len() < len(raw)*9/10 {
			a.gz = buf.Bytes()
		}
	}
	return a
}

func (a *asset) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Add("Vary", "Accept-Encoding")

	body, tag := a.raw, a.etag
	if a.gz != nil && acceptsGzip(r) {
		body, tag = a.gz, a.etag+"-gz"
		h.Set("Content-Encoding", "gzip")
	}
	// ServeContent answers If-None-Match from this header with a 304.
	h.Set("ETag", `"`+tag+`"`)
	http.ServeContent(w, r, a.name, time.Time{}, bytes.NewReader(body))
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			continue
		}
		// "gzip;q=0" is an explicit refusal; any other weight is a yes.
		q := strings.ReplaceAll(strings.ToLower(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}
