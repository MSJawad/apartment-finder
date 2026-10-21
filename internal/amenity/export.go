package amenity

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MSJawad/apartment-finder/internal/region"
)

// Filters returns the osmium tags-filter expressions that keep every object
// with any of the region's category tags, e.g. "nwr/shop=supermarket" (nwr:
// nodes, ways and relations). A category that needs several tags at once
// gets one expression per tag; that keeps a few objects too many, and
// Parse's exact matching drops them.
func Filters(reg *region.Region) []string {
	var filters []string
	for _, c := range reg.Categories {
		for _, tags := range c.OSM {
			for k, v := range tags {
				filters = append(filters, fmt.Sprintf("nwr/%s=%s", k, v))
			}
		}
	}
	slices.Sort(filters)
	return slices.Compact(filters)
}

// Export filters the OpenStreetMap extract at pbfPath down to the region's
// categories and writes them, one GeoJSON feature per line, to a file in dir,
// whose path it returns. It runs osmium (https://osmcode.org/osmium-tool/),
// which must be installed.
func Export(ctx context.Context, reg *region.Region, pbfPath, dir string) (string, error) {
	filtered := filepath.Join(dir, "amenities.osm.pbf")
	out := filepath.Join(dir, "amenities.geojsonseq")

	// tags-filter also keeps the nodes and ways that matching ways and
	// relations are made of, which export needs to build their shapes.
	args := append([]string{"tags-filter", pbfPath}, Filters(reg)...)
	if err := osmium(ctx, append(args, "--overwrite", "--output", filtered)...); err != nil {
		return "", err
	}
	// Points for places mapped as a single node, polygons for areas. Lines
	// are left out: osmium exports a closed way twice, as its outline and as
	// the area inside it, and only the area is wanted.
	if err := osmium(ctx, "export", filtered, "--overwrite", "--output", out,
		"--output-format", "geojsonseq", "--attributes", "type,id", "--geometry-types", "point,polygon"); err != nil {
		return "", err
	}
	return out, nil
}

func osmium(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "osmium", args...) //nolint:gosec // a fixed program; the arguments come from our own config
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("osmium %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
