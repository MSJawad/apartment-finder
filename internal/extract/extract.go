// Package extract downloads a region's OpenStreetMap extract and checks it
// against the checksum Geofabrik publishes next to it.
package extract

import (
	"context"
	"crypto/md5" //nolint:gosec // Geofabrik publishes MD5 sums; they check integrity, HTTPS checks authenticity
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var md5Sum = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Download fetches the extract at rawURL into dir, checks it, and returns
// its path.
//
// Geofabrik's "latest" URLs redirect to a dated file, and the file behind
// them changes daily, so two requests can get different days. The redirect
// is resolved first, and both the extract and its .md5 come from the same
// dated URL.
func Download(ctx context.Context, client *http.Client, rawURL, dir string) (string, error) {
	dated, err := resolve(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	sumBody, err := get(ctx, client, dated+".md5", 1<<10)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(sumBody))
	if len(fields) == 0 || !md5Sum.MatchString(fields[0]) {
		return "", fmt.Errorf("extract: %s.md5 holds no MD5 sum", dated)
	}
	want := fields[0]

	u, err := url.Parse(dated)
	if err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	dest := filepath.Join(dir, path.Base(u.Path))
	got, err := save(ctx, client, dated, dest)
	if err != nil {
		return "", err
	}
	if got != want {
		_ = os.Remove(dest)
		return "", fmt.Errorf("extract: %s has MD5 %s, but its .md5 says %s", dated, got, want)
	}
	return dest, nil
}

// resolve follows redirects from rawURL and returns where they end.
func resolve(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	current := rawURL
	for range 5 {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, current, nil)
		if err != nil {
			return "", fmt.Errorf("extract: %w", err)
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			return "", fmt.Errorf("extract: %w", err)
		}
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			next, err := resp.Location()
			if err != nil {
				return "", fmt.Errorf("extract: redirect from %s: %w", current, err)
			}
			current = next.String()
		case resp.StatusCode == http.StatusOK:
			return current, nil
		default:
			return "", fmt.Errorf("extract: %s returned HTTP %d", current, resp.StatusCode)
		}
	}
	return "", fmt.Errorf("extract: too many redirects from %s", rawURL)
}

func get(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	resp, err := request(ctx, client, rawURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// save streams rawURL to dest and returns the MD5 of what it wrote.
func save(ctx context.Context, client *http.Client, rawURL, dest string) (sum string, err error) {
	resp, err := request(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	f, err := os.Create(dest) //nolint:gosec // dest is in a directory the caller chose
	if err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("extract: %w", closeErr)
		}
	}()
	hash := md5.New() //nolint:gosec // see the import
	if _, err := io.Copy(io.MultiWriter(f, hash), resp.Body); err != nil {
		return "", fmt.Errorf("extract: download %s: %w", rawURL, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func request(ctx context.Context, client *http.Client, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("extract: %s returned HTTP %d", rawURL, resp.StatusCode)
	}
	return resp, nil
}
