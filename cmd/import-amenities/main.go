// Command import-amenities loads the everyday places listings are scored
// against from an OpenStreetMap extract into the database, replacing the
// previous set:
//
//	import-amenities                            # downloads the region's extract
//	import-amenities thueringen-latest.osm.pbf  # uses a local copy
//	import-amenities -if-empty                  # only if nothing is imported yet
//
// -if-empty is for the first deploy: the worker runs it before starting, so
// a new cluster has places to score against without waiting for the weekly
// run, and later restarts skip it after one quick count.
//
// Like the other commands it reads DATABASE_URL and REGION_CONFIG, and it
// needs osmium on the PATH. It runs weekly to keep the places current.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MSJawad/apartment-finder/internal/amenity"
	"github.com/MSJawad/apartment-finder/internal/config"
	"github.com/MSJawad/apartment-finder/internal/db"
	"github.com/MSJawad/apartment-finder/internal/extract"
	"github.com/MSJawad/apartment-finder/internal/observability"
	"github.com/MSJawad/apartment-finder/internal/region"
)

// version is the git commit this binary was built from, set with
// -ldflags "-X main.version=<sha>".
var version = "dev"

// downloadTimeout bounds the whole extract download: about 160 MB for
// Thuringia, which takes seconds from a data centre.
const downloadTimeout = 10 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "import-amenities:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("import-amenities", flag.ContinueOnError)
	ifEmpty := flags.Bool("if-empty", false, "import only if no amenities are stored yet")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: import-amenities [-if-empty] [extract.osm.pbf]")
	}
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
	if *ifEmpty {
		n, err := amenity.Count(ctx, pool)
		if err != nil {
			return err
		}
		if n > 0 {
			logger.LogAttrs(ctx, slog.LevelInfo, "amenities already imported", slog.Int("total", n))
			return nil
		}
	}

	dir, err := os.MkdirTemp("", "amenities")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var extractPath string
	if flags.NArg() == 1 {
		extractPath = flags.Arg(0)
	} else {
		logger.LogAttrs(ctx, slog.LevelInfo, "downloading extract", slog.String("url", reg.OSMExtract))
		client := &http.Client{Timeout: downloadTimeout}
		if extractPath, err = extract.Download(ctx, client, reg.OSMExtract, dir); err != nil {
			return err
		}
	}
	path, err := amenity.Export(ctx, reg, extractPath, dir)
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
	attrs := []slog.Attr{slog.String("extract", extractPath), slog.Int("total", len(amenities))}
	for _, c := range reg.Categories {
		attrs = append(attrs, slog.Int(c.Key, perCategory[c.Key]))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "imported amenities", attrs...)
	return nil
}
