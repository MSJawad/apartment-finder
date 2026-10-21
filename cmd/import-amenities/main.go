// Command import-amenities loads the everyday places listings are scored
// against from an OpenStreetMap extract into the database, replacing the
// previous set:
//
//	import-amenities thueringen-latest.osm.pbf
//
// Like the other commands it reads DATABASE_URL and REGION_CONFIG, and it
// needs osmium on the PATH. It runs weekly to keep the places current.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/MSJawad/apartment-finder/internal/amenity"
	"github.com/MSJawad/apartment-finder/internal/config"
	"github.com/MSJawad/apartment-finder/internal/db"
	"github.com/MSJawad/apartment-finder/internal/observability"
	"github.com/MSJawad/apartment-finder/internal/region"
)

// version is the git commit this binary was built from, set with
// -ldflags "-X main.version=<sha>".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "import-amenities:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: import-amenities <extract.osm.pbf>")
	}
	extract := os.Args[1]
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, cfg.LogLevel, "import-amenities", version)
	reg, err := region.Load(cfg.RegionPath)
	if err != nil {
		return err
	}

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

	dir, err := os.MkdirTemp("", "amenities")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path, err := amenity.Export(ctx, reg, extract, dir)
	if err != nil {
		return err
	}
	f, err := os.Open(path) //nolint:gosec // a file we just wrote to our own temp directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	amenities, err := amenity.Parse(f, reg)
	if err != nil {
		return err
	}
	if err := amenity.Replace(ctx, pool, amenities); err != nil {
		return err
	}

	perCategory := map[string]int{}
	for _, a := range amenities {
		perCategory[a.Category]++
	}
	attrs := []slog.Attr{slog.String("extract", extract), slog.Int("total", len(amenities))}
	for _, c := range reg.Categories {
		attrs = append(attrs, slog.Int(c.Key, perCategory[c.Key]))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "imported amenities", attrs...)
	return nil
}
