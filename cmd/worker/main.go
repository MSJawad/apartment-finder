// Command worker runs the background jobs that geocode listings and places
// and measure their surroundings, and cleans up data that has expired.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/config"
	"github.com/MSJawad/apartment-finder/internal/db"
	"github.com/MSJawad/apartment-finder/internal/enrich"
	"github.com/MSJawad/apartment-finder/internal/geocode"
	"github.com/MSJawad/apartment-finder/internal/listing"
	"github.com/MSJawad/apartment-finder/internal/observability"
	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/routing"
	"github.com/MSJawad/apartment-finder/internal/server"
	"github.com/MSJawad/apartment-finder/internal/upstream"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

// version is the git commit this binary was built from, set with
// -ldflags "-X main.version=<sha>".
var version = "dev"

const (
	workers      = 4
	pollInterval = time.Second
	// jobTimeout stays below the Kubernetes shutdown grace period, so jobs in
	// progress can finish on shutdown, and well below lease, so a slow job is
	// cancelled long before another worker could take it over.
	jobTimeout = 20 * time.Second
	lease      = 5 * time.Minute

	// Photon asks clients to be fair and gives no number, so the worker
	// keeps to one request a second, the limit of Nominatim, OpenStreetMap's
	// own geocoder. Addresses rarely move, so answers are cached for long;
	// "not found" answers are kept for less, in case the map gains them.
	photonInterval  = time.Second
	geocodeHitTTL   = 90 * 24 * time.Hour
	geocodeMissTTL  = 7 * 24 * time.Hour
	upstreamTimeout = 10 * time.Second

	// Clean-up runs at start and then every maintenanceEvery. Done jobs are
	// kept a week, dead ones a month so failures can be looked into.
	maintenanceEvery = time.Hour
	keepDoneJobs     = 7 * 24 * time.Hour
	keepDeadJobs     = 30 * 24 * time.Hour
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if err := cfg.RequireRouting(); err != nil {
		return err
	}
	reg, err := region.Load(cfg.RegionPath)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, cfg.LogLevel, "worker", version)
	registry := observability.NewRegistry(version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, logger); err != nil {
		return err
	}
	queued, err := listing.SyncExamples(ctx, pool, reg.Examples)
	if err != nil {
		return err
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "example listings synced", slog.Int("queued", queued))

	httpClient := &http.Client{Timeout: upstreamTimeout}
	photon := upstream.NewClient(pool, upstream.Options{
		Provider: "photon",
		// Public OpenStreetMap services ask for a User-Agent that says who
		// is calling and how to reach them.
		UserAgent: "apartment-finder/" + version + " (+https://github.com/MSJawad)",
		Interval:  photonInterval,
		HitTTL:    geocodeHitTTL,
		MissTTL:   geocodeMissTTL,
		HTTP:      httpClient,
	}, upstream.NewMetrics(registry))
	enricher := &enrich.Enricher{
		Pool:     pool,
		Region:   reg,
		Geocoder: geocode.NewPhoton(photon, cfg.PhotonURL, reg),
		Router:   routing.NewOSRM(map[region.Mode]string{region.Walk: cfg.OSRMWalkURL, region.Bike: cfg.OSRMBikeURL}, httpClient),
	}

	runner := &queue.Runner{
		Queue: queue.New(pool, lease),
		Handlers: map[string]queue.Handler{
			enrich.JobKind:      enricher.Handle,
			enrich.PlaceJobKind: enricher.HandlePlace,
		},
		Workers:      workers,
		PollInterval: pollInterval,
		JobTimeout:   jobTimeout,
		Logger:       logger,
		Metrics:      queue.NewMetrics(registry),
	}
	runCtx, stopRunner := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { runner.Run(runCtx) })
	wg.Go(func() { maintain(runCtx, pool, logger) })

	// The worker has no public port: only probes and metrics. If that server
	// fails, stop taking jobs too, so the process exits and gets restarted.
	checks := []server.Check{{Name: "database", Run: pool.Ping}}
	err = server.Serve(ctx, logger,
		server.NewServer(cfg.AdminAddr, server.AdminHandler(registry, logger, checks), logger),
	)
	stopRunner()
	wg.Wait()
	return err
}

// maintain deletes what is no longer needed, once at start and then every
// maintenanceEvery, until ctx ends. Every worker runs it; the deletes are
// safe to repeat, so overlapping runs are harmless.
func maintain(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	ticker := time.NewTicker(maintenanceEvery)
	defer ticker.Stop()
	for {
		cleanUp(ctx, pool, logger)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// cleanUp deletes visitors gone for visitor.Retention with everything they
// added, submissions too old to count, old finished jobs and expired cache
// entries. A failed step is logged and the rest still run.
func cleanUp(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	attrs := []slog.Attr{}
	warn := func(step string, err error) {
		logger.LogAttrs(ctx, slog.LevelWarn, "clean-up step failed", slog.String("step", step), slog.Any("error", err))
	}
	if n, err := visitor.DeleteInactive(ctx, pool); err != nil {
		warn("visitors", err)
	} else {
		attrs = append(attrs, slog.Int64("visitors", n))
	}
	if err := visitor.DeleteOldSubmissions(ctx, pool); err != nil {
		warn("submissions", err)
	}
	if n, err := queue.DeleteFinished(ctx, pool, keepDoneJobs, keepDeadJobs); err != nil {
		warn("jobs", err)
	} else {
		attrs = append(attrs, slog.Int64("jobs", n))
	}
	if n, err := upstream.DeleteExpired(ctx, pool); err != nil {
		warn("cache", err)
	} else {
		attrs = append(attrs, slog.Int64("cache_entries", n))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "clean-up done", attrs...)
}
