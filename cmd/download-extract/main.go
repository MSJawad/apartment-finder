// Command download-extract downloads the region's OpenStreetMap extract to a
// file, checked against Geofabrik's checksum:
//
//	download-extract /data/region.osm.pbf
//
// The routers use it to fetch the map data they prepare at first start. It
// reads REGION_CONFIG for the extract's URL.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/MSJawad/apartment-finder/internal/extract"
	"github.com/MSJawad/apartment-finder/internal/region"
)

// timeout bounds the whole download: about 160 MB for Thuringia.
const timeout = 10 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "download-extract:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: download-extract <destination file>")
	}
	dest := os.Args[1]
	path := os.Getenv("REGION_CONFIG")
	if path == "" {
		return errors.New("REGION_CONFIG is required")
	}
	reg, err := region.Load(path)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Downloaded next to the destination, so the final rename stays on one
	// filesystem and the destination only ever holds a checked file.
	dir, err := os.MkdirTemp(filepath.Dir(dest), ".download-") //nolint:gosec // dest is the operator's argument
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }() //nolint:gosec // the temporary directory made just above
	downloaded, err := extract.Download(ctx, &http.Client{Timeout: timeout}, reg.OSMExtract, dir)
	if err != nil {
		return err
	}
	if err := os.Rename(downloaded, dest); err != nil { //nolint:gosec // as above
		return err
	}
	fmt.Println(dest)
	return nil
}
