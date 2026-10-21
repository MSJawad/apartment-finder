// Package web serves the page: one HTML file, its script and styles, and a
// copy of Leaflet, all built into the binary. The page loads no code from
// other sites, so a compromised CDN can't change what it runs.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var files embed.FS

// contentSecurityPolicy lets the page load only its own scripts, styles and
// API, plus map tiles from OpenStreetMap. Even if some text from a visitor
// slipped into the page as markup, the browser wouldn't run a script from
// it: inline scripts and styles aren't allowed.
var contentSecurityPolicy = strings.Join([]string{
	"default-src 'none'",
	"script-src 'self'",
	"style-src 'self'",
	"img-src 'self' data: https://tile.openstreetmap.org",
	"connect-src 'self'",
	"base-uri 'none'",
	"form-action 'self'",
	"frame-ancestors 'none'",
}, "; ")

// Register adds the page at / and its files under /static/ to mux.
func Register(mux *http.ServeMux) error {
	static, err := fs.Sub(files, "static")
	if err != nil {
		return err
	}
	etags, err := hashFiles(static)
	if err != nil {
		return err
	}
	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		return err
	}

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		secure(w.Header())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})

	fileServer := http.FileServerFS(static)
	mux.HandleFunc("GET /static/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		etag, ok := etags[path]
		if !ok || path == "index.html" {
			// Unknown files and directories, which would otherwise be
			// listed, and the page itself, which lives at /.
			http.NotFound(w, r)
			return
		}
		secure(w.Header())
		// The browser keeps its copy but checks it on each load; the ETag
		// makes that check a cheap 304 until the file changes in a deploy.
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/" + path
		fileServer.ServeHTTP(w, r)
	})
	return nil
}

func secure(h http.Header) {
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	// Tile servers want to know which site their tiles appear on, so the
	// origin is sent with cross-origin requests, but never the full URL.
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

// hashFiles returns an ETag for every file, from a hash of its contents.
// The embedded files have no modification times to validate against.
func hashFiles(fsys fs.FS) (map[string]string, error) {
	etags := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		etags[path] = fmt.Sprintf("%q", hex.EncodeToString(sum[:8]))
		return nil
	})
	return etags, err
}
