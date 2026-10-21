// Command api serves the public HTTP API and web page.
package main

import (
	"context"
	"fmt"
	"net/http"
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
	reg := observability.NewRegistry(version)
	metrics := server.NewMetrics(reg)

	public := http.NewServeMux()

	// Kubernetes sends SIGTERM before stopping a pod; cancelling ctx starts a
	// graceful shutdown so in-flight requests can finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Serve(ctx, logger,
		server.NewServer(cfg.Addr, server.Observe(public, metrics, logger), logger),
		server.NewServer(cfg.AdminAddr, server.AdminHandler(reg, logger, nil), logger),
	)
}
