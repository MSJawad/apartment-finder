package upstream_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/upstream"
)

const userAgent = "apartment-finder/test (+https://github.com/MSJawad)"

// fakeService answers every request with status and body, and counts calls.
type fakeService struct {
	*httptest.Server
	calls atomic.Int32
}

func newFakeService(t *testing.T, status int, body string) *fakeService {
	t.Helper()
	f := &fakeService{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q, want %q", got, userAgent)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.Close)
	return f
}

func newClient(pool *pgxpool.Pool) *upstream.Client {
	return upstream.NewClient(pool, upstream.Options{
		Provider:  "test",
		UserAgent: userAgent,
		HitTTL:    time.Hour,
		MissTTL:   time.Minute,
		HTTP:      &http.Client{Timeout: 5 * time.Second},
	}, upstream.NewMetrics(prometheus.NewRegistry()))
}

func isEmpty(body []byte) (bool, error) {
	if strings.HasPrefix(string(body), "garbage") {
		return false, errors.New("not JSON")
	}
	return string(body) == "[]", nil
}

func TestGetServesRepeatsFromTheCache(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	svc := newFakeService(t, http.StatusOK, `["Anger 1"]`)
	client := newClient(pool)

	for range 3 {
		body, err := client.Get(t.Context(), svc.URL+"/api?q=anger", isEmpty)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `["Anger 1"]` {
			t.Errorf("body = %s", body)
		}
	}
	if n := svc.calls.Load(); n != 1 {
		t.Errorf("upstream called %d times, want 1", n)
	}
	if _, err := client.Get(t.Context(), svc.URL+"/api?q=other", isEmpty); err != nil {
		t.Fatal(err)
	}
	if n := svc.calls.Load(); n != 2 {
		t.Errorf("a different request: upstream called %d times in total, want 2", n)
	}
}

func TestGetCachesMissesForTheShorterTime(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	svc := newFakeService(t, http.StatusOK, `[]`)
	client := newClient(pool)

	for range 2 {
		if _, err := client.Get(t.Context(), svc.URL+"/api?q=nowhere", isEmpty); err != nil {
			t.Fatal(err)
		}
	}
	if n := svc.calls.Load(); n != 1 {
		t.Errorf("upstream called %d times, want 1: the miss should be cached", n)
	}

	var miss bool
	var ttl float64
	if err := pool.QueryRow(t.Context(),
		"SELECT miss, extract(epoch FROM expires_at - fetched_at) FROM upstream_cache").Scan(&miss, &ttl); err != nil {
		t.Fatal(err)
	}
	if !miss || ttl < 59 || ttl > 61 {
		t.Errorf("cached miss=%v for %.0fs, want miss=true for the 60s MissTTL", miss, ttl)
	}
}

func TestGetRefetchesExpiredEntries(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	svc := newFakeService(t, http.StatusOK, `["Anger 1"]`)
	client := newClient(pool)

	if _, err := client.Get(t.Context(), svc.URL+"/api?q=anger", isEmpty); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE upstream_cache SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(t.Context(), svc.URL+"/api?q=anger", isEmpty); err != nil {
		t.Fatal(err)
	}
	if n := svc.calls.Load(); n != 2 {
		t.Errorf("upstream called %d times, want 2 (once more after expiry)", n)
	}
}

func TestGetNeverCachesErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"rate limited", http.StatusTooManyRequests, "slow down"},
		{"server error", http.StatusBadGateway, "at capacity"},
		{"unparseable body", http.StatusOK, "garbage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := dbtest.New(t)
			svc := newFakeService(t, tt.status, tt.body)
			client := newClient(pool)

			for range 2 {
				_, err := client.Get(t.Context(), svc.URL+"/api?q=x", isEmpty)
				if err == nil {
					t.Fatal("Get() succeeded, want an error")
				}
				var statusErr *upstream.StatusError
				if tt.status != http.StatusOK && (!errors.As(err, &statusErr) || statusErr.Code != tt.status) {
					t.Errorf("Get() error = %v, want StatusError %d", err, tt.status)
				}
			}
			if n := svc.calls.Load(); n != 2 {
				t.Errorf("upstream called %d times, want 2: errors must not be cached", n)
			}
		})
	}
}
