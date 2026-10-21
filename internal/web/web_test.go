package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve requests path, as a revalidation if etag is set.
func serve(t *testing.T, mux *http.ServeMux, path string, etag ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if len(etag) > 0 {
		req.Header.Set("If-None-Match", etag[0])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func newMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if err := Register(mux); err != nil {
		t.Fatal(err)
	}
	return mux
}

func TestPage(t *testing.T) {
	t.Parallel()
	rec := serve(t, newMux(t), "/")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET / = %d, %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "https://tile.openstreetmap.org", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy %q lacks %q", csp, want)
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no X-Content-Type-Options: nosniff")
	}
	body := rec.Body.String()
	// Everything the page loads comes from this server: no CDN.
	for _, want := range []string{`src="/static/vendor/leaflet/leaflet.js"`, `src="/static/app.js"`, `href="/static/app.css"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page doesn't load %s", want)
		}
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "style=") {
		t.Error("the page has inline script or style, which the policy blocks")
	}
}

func TestStaticFiles(t *testing.T) {
	t.Parallel()
	mux := newMux(t)
	tests := []struct {
		path, wantType string
	}{
		{"/static/app.js", "text/javascript"},
		{"/static/app.css", "text/css"},
		{"/static/favicon.svg", "image/svg+xml"},
		{"/static/vendor/leaflet/leaflet.js", "text/javascript"},
		{"/static/vendor/leaflet/images/layers.png", "image/png"},
	}
	for _, tt := range tests {
		rec := serve(t, mux, tt.path)
		etag := rec.Header().Get("ETag")
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), tt.wantType) || etag == "" {
			t.Errorf("GET %s = %d, %q, ETag %q", tt.path, rec.Code, rec.Header().Get("Content-Type"), etag)
			continue
		}
		// A browser revalidating its copy gets a body-less 304.
		if again := serve(t, mux, tt.path, etag); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
			t.Errorf("GET %s with its ETag = %d and %d bytes, want 304 and none", tt.path, again.Code, again.Body.Len())
		}
	}
}

func TestNotServed(t *testing.T) {
	t.Parallel()
	mux := newMux(t)
	for _, path := range []string{
		"/static/", // no directory listings
		"/static/vendor/leaflet/",
		"/static/index.html", // the page lives at /
		"/static/missing.js",
	} {
		if rec := serve(t, mux, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	// A path climbing out of /static/ is cleaned by the router, which
	// redirects to /web.go; nothing is served from outside the folder.
	rec := serve(t, mux, "/static/../web.go")
	if rec.Code == http.StatusOK {
		t.Fatal("GET /static/../web.go served a file")
	}
	if location := rec.Header().Get("Location"); location != "" {
		if again := serve(t, mux, location); again.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", location, again.Code)
		}
	}
}
