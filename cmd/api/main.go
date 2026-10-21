// Command api serves the public HTTP API and web page.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/MSJawad/apartment-finder/internal/api"
	"github.com/MSJawad/apartment-finder/internal/config"
	"github.com/MSJawad/apartment-finder/internal/db"
	"github.com/MSJawad/apartment-finder/internal/listing"
	"github.com/MSJawad/apartment-finder/internal/observability"
	"github.com/MSJawad/apartment-finder/internal/place"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/server"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

// version is the git commit this binary was built from, set with
// -ldflags "-X main.version=<sha>".
var version = "dev"

// limits cap how many listings and places visitors can add. Each one costs a
// request to Photon, a free public geocoder, so the site-wide cap keeps us a
// polite user of it.
var limits = visitor.Limits{PerHour: 10, PerDay: 30, SiteWidePerDay: 300}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, cfg.LogLevel, "api", version)
	registry := observability.NewRegistry(version)
	metrics := server.NewMetrics(registry)

	// Kubernetes sends SIGTERM before stopping a pod; cancelling ctx starts a
	// graceful shutdown so in-flight requests can finish.
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
	reg, err := region.Load(cfg.RegionPath)
	if err != nil {
		return err
	}
	checks := []server.Check{{Name: "database", Run: pool.Ping}}

	public := http.NewServeMux()
	a := &api.API{
		Pool:     pool,
		Region:   reg,
		Listings: &listing.Store{Pool: pool, Limits: limits},
		Places:   &place.Store{Pool: pool, Limits: limits},
		Source:   listing.Manual{},
		Logger:   logger,
	}
	a.Register(public)

	return server.Serve(ctx, logger,
		server.NewServer(cfg.Addr, server.Observe(public, metrics, logger), logger),
		server.NewServer(cfg.AdminAddr, server.AdminHandler(registry, logger, checks), logger),
	)
}
