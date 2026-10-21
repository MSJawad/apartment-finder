package visitor_test

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

func TestFromRequest(t *testing.T) {
	t.Parallel()
	id := uuid.NewV4()
	tests := []struct {
		name   string
		cookie string // the Cookie header
		wantOK bool
	}{
		{name: "no cookie"},
		{name: "not a uuid", cookie: visitor.CookieName + "=admin"},
		{name: "another cookie", cookie: "session=" + id.String()},
		{name: "valid", cookie: visitor.CookieName + "=" + id.String(), wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.cookie != "" {
				r.Header.Set("Cookie", tt.cookie)
			}
			got, ok := visitor.FromRequest(r)
			if ok != tt.wantOK || (ok && got != id) {
				t.Errorf("FromRequest() = %v, %v; want %v, %v", got, ok, id, tt.wantOK)
			}
		})
	}
}

func TestSetCookie(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		tls        bool
		forwarded  string
		wantSecure bool
	}{
		{name: "plain HTTP, locally"},
		{name: "HTTPS", tls: true, wantSecure: true},
		{name: "HTTPS ended at the ingress", forwarded: "https", wantSecure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tt.forwarded != "" {
				r.Header.Set("X-Forwarded-Proto", tt.forwarded)
			}
			rec := httptest.NewRecorder()
			id := uuid.NewV4()
			visitor.SetCookie(rec, r, id)

			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("%d cookies, want 1", len(cookies))
			}
			c := cookies[0]
			if c.Name != visitor.CookieName || c.Value != id.String() || c.Path != "/" ||
				!c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 30*24*60*60 {
				t.Errorf("cookie = %+v", c)
			}
			if c.Secure != tt.wantSecure {
				t.Errorf("Secure = %v, want %v", c.Secure, tt.wantSecure)
			}
		})
	}
}

func TestEnsureAndTouch(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	ctx := t.Context()

	// Without a cookie, a new visitor is created.
	first, err := visitor.Ensure(ctx, pool, uuid.UUID{}, false)
	if err != nil {
		t.Fatal(err)
	}
	// With a live cookie, it is that visitor.
	if again, err := visitor.Ensure(ctx, pool, first, true); err != nil || again != first {
		t.Errorf("Ensure(existing) = %v, %v; want %v", again, err, first)
	}
	// With the cookie of a deleted visitor, or an id the server never
	// handed out, a new random id is chosen.
	madeUp := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	if fresh, err := visitor.Ensure(ctx, pool, madeUp, true); err != nil || fresh == madeUp || fresh == first {
		t.Errorf("Ensure(unknown) = %v, %v; want a new id", fresh, err)
	}

	if exists, err := visitor.Touch(ctx, pool, first); err != nil || !exists {
		t.Errorf("Touch(existing) = %v, %v", exists, err)
	}
	if exists, err := visitor.Touch(ctx, pool, madeUp); err != nil || exists {
		t.Errorf("Touch(unknown) = %v, %v", exists, err)
	}
}

func TestDeleteInactive(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	ctx := t.Context()
	recent, stale := addVisitor(t, pool), addVisitor(t, pool)
	if _, err := pool.Exec(ctx, "UPDATE visitors SET last_seen_at = now() - interval '31 days' WHERE id = $1", stale); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO listings (visitor_id, address, warm_rent_cents, size_m2, rooms)
		VALUES ($1, 'Anger 1, Erfurt', 80000, 50, 2)`, stale); err != nil {
		t.Fatal(err)
	}

	deleted, err := visitor.DeleteInactive(ctx, pool)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteInactive() = %d, %v; want 1", deleted, err)
	}
	var visitors, listings int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM visitors), (SELECT count(*) FROM listings)").Scan(&visitors, &listings); err != nil {
		t.Fatal(err)
	}
	if visitors != 1 || listings != 0 {
		t.Errorf("%d visitors and %d listings left, want only the recent visitor", visitors, listings)
	}
	if exists, _ := visitor.Touch(ctx, pool, recent); !exists {
		t.Error("the recent visitor was deleted")
	}
}

var limits = visitor.Limits{PerHour: 3, PerDay: 5, SiteWidePerDay: 8}

func reserve(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) error {
	t.Helper()
	return pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		return limits.Reserve(t.Context(), tx, id)
	})
}

// backdate makes n of the visitor's submissions look older.
func backdate(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, age time.Duration) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), "UPDATE submissions SET created_at = created_at - make_interval(secs => $2) WHERE visitor_id = $1",
		id, age.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func TestLimits(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	alice := addVisitor(t, pool)

	for i := range 3 {
		if err := reserve(t, pool, alice); err != nil {
			t.Fatalf("submission %d: %v", i+1, err)
		}
	}
	hourly := wantLimit(t, reserve(t, pool, alice), "You can add 3 an hour", time.Hour)

	// An hour and a half later, the hour has room again, but the day only
	// has room for two more.
	backdate(t, pool, alice, 90*time.Minute)
	for i := range 2 {
		if err := reserve(t, pool, alice); err != nil {
			t.Fatalf("submission %d after an hour: %v", i+4, err)
		}
	}
	daily := wantLimit(t, reserve(t, pool, alice), "You can add 5 a day", 23*time.Hour)
	if daily <= hourly {
		t.Errorf("the daily limit frees up in %v, before the hourly one did (%v)", daily, hourly)
	}

	// Submissions older than a day count for nothing.
	backdate(t, pool, alice, 24*time.Hour)
	if err := reserve(t, pool, alice); err != nil {
		t.Errorf("a day later: %v", err)
	}
	if err := visitor.DeleteOldSubmissions(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM submissions").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Errorf("%d submissions left after cleanup, want the 1 from today", left)
	}
}

func TestSiteWideLimit(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	// Eight visitors with one submission each fill the site-wide limit.
	for range limits.SiteWidePerDay {
		if err := reserve(t, pool, addVisitor(t, pool)); err != nil {
			t.Fatal(err)
		}
	}
	wantLimit(t, reserve(t, pool, addVisitor(t, pool)), "The site has reached its limit", 24*time.Hour)
}

func TestConcurrentSubmissionsDontOvershoot(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	bob := addVisitor(t, pool)

	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for range 10 {
		wg.Go(func() {
			err := reserve(t, pool, bob)
			if _, limited := errors.AsType[*visitor.LimitError](err); err != nil && !limited {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted++
			}
		})
	}
	wg.Wait()
	if accepted != limits.PerHour {
		t.Errorf("%d of 10 concurrent submissions accepted, want exactly %d", accepted, limits.PerHour)
	}
}

// wantLimit checks err is a LimitError whose message starts with reason and
// whose RetryAfter is within maxWait, and returns the RetryAfter.
func wantLimit(t *testing.T, err error, reason string, maxWait time.Duration) time.Duration {
	t.Helper()
	limit, ok := errors.AsType[*visitor.LimitError](err)
	if !ok {
		t.Fatalf("err = %v, want a LimitError", err)
	}
	if len(limit.Reason) < len(reason) || limit.Reason[:len(reason)] != reason {
		t.Errorf("reason = %q, want it to start with %q", limit.Reason, reason)
	}
	if limit.RetryAfter <= 0 || limit.RetryAfter > maxWait {
		t.Errorf("RetryAfter = %v, want between 0 and %v", limit.RetryAfter, maxWait)
	}
	return limit.RetryAfter
}

func addVisitor(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id, err := visitor.Ensure(t.Context(), pool, uuid.UUID{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
