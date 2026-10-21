// Command worker runs background jobs: geocoding, enrichment and scoring.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/MSJawad/apartment-finder/internal/config"
	"github.com/MSJawad/apartment-finder/internal/observability"
	"github.com/MSJawad/apartment-finder/internal/server"
)

// version is the git commit this binary was built from, set with
// -ldflags "-X main.version=<sha>".
var version = "dev"

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

	// The worker has no public port: only probes and metrics.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Serve(ctx, logger,
		server.NewServer(cfg.AdminAddr, server.AdminHandler(reg, logger, nil), logger),
	)
}
