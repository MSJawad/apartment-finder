package extract

import (
	"crypto/md5" //nolint:gosec // matching what Geofabrik publishes
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGeofabrik serves a "latest" URL that redirects to a dated extract, and
// the dated extract's .md5.
func fakeGeofabrik(t *testing.T, data, sum string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thueringen-latest.osm.pbf":
			http.Redirect(w, r, "/thueringen-261020.osm.pbf", http.StatusTemporaryRedirect)
		case "/thueringen-261020.osm.pbf":
			_, _ = w.Write([]byte(data))
		case "/thueringen-261020.osm.pbf.md5":
			_, _ = w.Write([]byte(sum + "  thueringen-261020.osm.pbf\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // see the import
	return hex.EncodeToString(sum[:])
}

func TestDownload(t *testing.T) {
	t.Parallel()
	const data = "pretend this is a protobuf extract"
	server := fakeGeofabrik(t, data, md5Hex(data))
	dir := t.TempDir()

	path, err := Download(t.Context(), server.Client(), server.URL+"/thueringen-latest.osm.pbf", dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "thueringen-261020.osm.pbf") {
		t.Errorf("path = %s, want the dated file's name", path)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != data { //nolint:gosec // the file the test just downloaded
		t.Errorf("downloaded %q, %v", got, err)
	}
}

func TestDownloadRejects(t *testing.T) {
	t.Parallel()
	const data = "pretend this is a protobuf extract"
	tests := []struct {
		name, sum, path, wantErr string
	}{
		{"a corrupt download", md5Hex("something else"), "/thueringen-latest.osm.pbf", "but its .md5 says"},
		{"a broken checksum file", "not-a-checksum", "/thueringen-latest.osm.pbf", "holds no MD5 sum"},
		{"a missing extract", md5Hex(data), "/berlin-latest.osm.pbf", "HTTP 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := fakeGeofabrik(t, data, tt.sum)
			dir := t.TempDir()
			_, err := Download(t.Context(), server.Client(), server.URL+tt.path, dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("left %d files behind after a failed download", len(entries))
			}
		})
	}
}

func TestDownloadGivesUpOnRedirectLoops(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound) //nolint:gosec // a deliberate loop, to the same test server
	}))
	t.Cleanup(server.Close)
	if _, err := Download(t.Context(), server.Client(), server.URL+"/loop", t.TempDir()); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("err = %v, want too many redirects", err)
	}
}
