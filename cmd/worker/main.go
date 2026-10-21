// Command worker runs background jobs: geocoding, enrichment and scoring.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/MSJawad/apartment-finder/internal/config"
	"github.com/MSJawad/apartment-finder/internal/db"
	"github.com/MSJawad/apartment-finder/internal/observability"
	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/server"
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
	logger := observability.NewLogger(os.Stdout, cfg.LogLevel, "worker", version)
	reg := observability.NewRegistry(version)

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

	runner := &queue.Runner{
		Queue:        queue.New(pool, lease),
		Handlers:     map[string]queue.Handler{},
		Workers:      workers,
		PollInterval: pollInterval,
		JobTimeout:   jobTimeout,
		Logger:       logger,
		Metrics:      queue.NewMetrics(reg),
	}
	runCtx, stopRunner := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { runner.Run(runCtx) })

	// The worker has no public port: only probes and metrics. If that server
	// fails, stop taking jobs too, so the process exits and gets restarted.
	checks := []server.Check{{Name: "database", Run: pool.Ping}}
	err = server.Serve(ctx, logger,
		server.NewServer(cfg.AdminAddr, server.AdminHandler(reg, logger, checks), logger),
	)
	stopRunner()
	wg.Wait()
	return err
}
