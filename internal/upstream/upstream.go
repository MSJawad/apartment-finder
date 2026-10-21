// Package upstream calls external services politely. Every response is
// cached in Postgres, "nothing found" answers included, and requests to each
// service are spaced out, as their usage policies ask.
package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// maxBody caps how much of a response is read, so a misbehaving upstream
// can't exhaust memory.
const maxBody = 10 << 20

// StatusError is a response other than 200 OK. Rate limiting (429) and
// server errors are usually temporary, so the job fails and the queue retries
// it later with backoff. These responses are never cached.
type StatusError struct {
	Provider string
	Code     int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.Provider, e.Code)
}

// Options configure a Client.
type Options struct {
	// Provider names the service, e.g. "photon"; it namespaces the cache.
	Provider string
	// UserAgent identifies this app to the service, with a way to contact
	// its operator, as public OpenStreetMap services ask.
	UserAgent string
	// Interval is the minimum time between requests.
	Interval time.Duration
	// HitTTL and MissTTL are how long answers and "nothing found" answers
	// stay cached.
	HitTTL, MissTTL time.Duration
	// HTTP is the client to send requests with; it should have a timeout.
	HTTP *http.Client
}

// Client fetches from one service through the cache.
type Client struct {
	opts    Options
	pool    *pgxpool.Pool
	limiter *Limiter
	metrics *Metrics
}

// NewClient returns a Client for the service described by opts.
func NewClient(pool *pgxpool.Pool, opts Options, metrics *Metrics) *Client {
	return &Client{opts: opts, pool: pool, limiter: NewLimiter(opts.Interval), metrics: metrics}
}

// Get returns the body at url, from the cache when a fresh copy is there.
// isMiss reports whether a successful body means "nothing found"; an error
// from it means the body is unusable, and it isn't cached.
func (c *Client) Get(ctx context.Context, url string, isMiss func(body []byte) (bool, error)) ([]byte, error) {
	sum := sha256.Sum256([]byte(url))
	key := hex.EncodeToString(sum[:])

	var cached []byte
	err := c.pool.QueryRow(ctx,
		"SELECT body FROM upstream_cache WHERE provider = $1 AND key = $2 AND expires_at > now()",
		c.opts.Provider, key).Scan(&cached)
	switch {
	case err == nil:
		c.metrics.requests.WithLabelValues(c.opts.Provider, "cache_hit").Inc()
		return cached, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("read %s cache: %w", c.opts.Provider, err)
	}

	body, err := c.fetch(ctx, url)
	if err != nil {
		c.metrics.requests.WithLabelValues(c.opts.Provider, "error").Inc()
		return nil, err
	}
	miss, err := isMiss(body)
	if err != nil {
		c.metrics.requests.WithLabelValues(c.opts.Provider, "error").Inc()
		return nil, fmt.Errorf("%s response: %w", c.opts.Provider, err)
	}
	ttl := c.opts.HitTTL
	if miss {
		ttl = c.opts.MissTTL
	}
	if _, err := c.pool.Exec(ctx, `
		INSERT INTO upstream_cache (provider, key, url, body, miss, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))
		ON CONFLICT (provider, key) DO UPDATE
		SET body = excluded.body, miss = excluded.miss, fetched_at = now(), expires_at = excluded.expires_at`,
		c.opts.Provider, key, url, body, miss, ttl.Seconds()); err != nil {
		return nil, fmt.Errorf("write %s cache: %w", c.opts.Provider, err)
	}
	c.metrics.requests.WithLabelValues(c.opts.Provider, "fetched").Inc()
	return body, nil
}

func (c *Client) fetch(ctx context.Context, url string) ([]byte, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", c.opts.Provider, err)
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.opts.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", c.opts.Provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	c.metrics.duration.WithLabelValues(c.opts.Provider).Observe(time.Since(start).Seconds())
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", c.opts.Provider, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Provider: c.opts.Provider, Code: resp.StatusCode}
	}
	return body, nil
}

// Metrics are the upstream request metrics.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics creates the upstream metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "upstream_requests_total",
			Help: "Requests for upstream data, by provider and result (cache_hit, fetched or error).",
		}, []string{"provider", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "upstream_request_duration_seconds",
			Help:    "Time taken by HTTP requests to upstream services, by provider.",
			Buckets: prometheus.DefBuckets,
		}, []string{"provider"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// DeleteExpired deletes cache entries past their expiry, which Get no longer
// serves, and returns how many it deleted.
func DeleteExpired(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, "DELETE FROM upstream_cache WHERE expires_at <= now()")
	if err != nil {
		return 0, fmt.Errorf("delete expired cache entries: %w", err)
	}
	return tag.RowsAffected(), nil
}
