// Package amenity stores the everyday places listings are scored against
// (supermarkets, stops, parks and so on) and finds the nearest ones.
//
// The places come from the same OpenStreetMap extract the router uses: the
// extract is filtered down to the region's categories and exported as
// GeoJSON, which Parse reads and Replace loads. Finding the nearest places is
// then an indexed PostGIS query on our own database, with no external API
// involved.
package amenity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/region"
)

// maxFeature caps the size of one GeoJSON line. Large parks and forests are
// polygons with many thousands of points.
const maxFeature = 64 << 20

// Amenity is one place in one category.
type Amenity struct {
	Category string
	OSMType  string // node, way or relation
	OSMID    int64
	Name     string
	Geometry json.RawMessage // GeoJSON
}

type feature struct {
	Geometry   json.RawMessage `json:"geometry"`
	Properties map[string]any  `json:"properties"`
}

// Parse reads GeoJSON features, one per line, as osmium's geojsonseq format
// writes them, with or without the RFC 8142 record separator before each.
// Each feature's properties are its OSM tags plus "@type" and "@id". It
// returns one Amenity per feature and category the feature belongs to, and
// skips features that belong to none.
func Parse(r io.Reader, reg *region.Region) ([]Amenity, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), maxFeature)
	var amenities []Amenity
	for line := 1; scanner.Scan(); line++ {
		data := bytes.TrimSpace(bytes.TrimPrefix(scanner.Bytes(), []byte{0x1e}))
		if len(data) == 0 {
			continue
		}
		var f feature
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		osmType, _ := f.Properties["@type"].(string)
		osmID, _ := f.Properties["@id"].(float64)
		if osmType == "" || osmID == 0 || len(f.Geometry) == 0 {
			return nil, fmt.Errorf("line %d: feature lacks @type, @id or a geometry", line)
		}
		tags := make(map[string]string, len(f.Properties))
		for k, v := range f.Properties {
			if s, ok := v.(string); ok {
				tags[k] = s
			}
		}
		for _, c := range reg.Categories {
			if c.Matches(tags) {
				amenities = append(amenities, Amenity{
					Category: c.Key, OSMType: osmType, OSMID: int64(osmID), Name: tags["name"], Geometry: f.Geometry,
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read features: %w", err)
	}
	return amenities, nil
}

// Replace swaps all stored amenities for these in one transaction, so lookups
// see the old set or the new one, never a half-loaded table. An empty set is
// refused: it almost certainly means the export failed, and keeping
// yesterday's places beats scoring everything as having none nearby.
func Replace(ctx context.Context, pool *pgxpool.Pool, amenities []Amenity) error {
	if len(amenities) == 0 {
		return errors.New("refusing to replace amenities with an empty set")
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			CREATE TEMP TABLE amenity_import (
				category text, osm_type text, osm_id bigint, name text, geojson text
			) ON COMMIT DROP`); err != nil {
			return fmt.Errorf("create import table: %w", err)
		}
		rows := make([][]any, len(amenities))
		for i, a := range amenities {
			rows[i] = []any{a.Category, a.OSMType, a.OSMID, a.Name, string(a.Geometry)}
		}
		// COPY is Postgres' bulk load; far faster than one INSERT per row.
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"amenity_import"},
			[]string{"category", "osm_type", "osm_id", "name", "geojson"}, pgx.CopyFromRows(rows)); err != nil {
			return fmt.Errorf("copy amenities: %w", err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM amenities"); err != nil {
			return fmt.Errorf("clear amenities: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO amenities (category, osm_type, osm_id, name, geom)
			SELECT category, osm_type, osm_id, nullif(name, ''), ST_GeomFromGeoJSON(geojson)::geography
			FROM amenity_import
			ON CONFLICT DO NOTHING`); err != nil {
			return fmt.Errorf("insert amenities: %w", err)
		}
		return nil
	})
}

// Candidate is a place near a listing.
type Candidate struct {
	Name string
	// Lat and Lon are the point of the place closest to the listing: the
	// place itself, or the nearest edge of an area such as a park. Routes
	// are calculated to this point.
	Lat, Lon float64
	// DistanceM is the straight-line distance in metres.
	DistanceM float64
}

// Querier runs a query; *pgxpool.Pool and pgx.Tx both qualify.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Nearest returns up to limit places of category within radiusM metres of
// the point, closest first. Areas count from their nearest edge, so a large
// park next door isn't penalized for having its middle far away.
func Nearest(ctx context.Context, db Querier, category string, lat, lon, radiusM float64, limit int) ([]Candidate, error) {
	rows, err := db.Query(ctx, `
		WITH here AS (SELECT ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography AS p)
		SELECT coalesce(a.name, ''), ST_Y(c.pt), ST_X(c.pt), ST_Distance(a.geom, here.p)
		FROM amenities a
		CROSS JOIN here
		CROSS JOIN LATERAL (SELECT ST_ClosestPoint(a.geom::geometry, here.p::geometry) AS pt) c
		WHERE a.category = $1 AND ST_DWithin(a.geom, here.p, $4)
		ORDER BY a.geom <-> here.p
		LIMIT $5`,
		category, lon, lat, radiusM, limit)
	if err != nil {
		return nil, fmt.Errorf("find nearest %s: %w", category, err)
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Candidate, error) {
		var c Candidate
		err := row.Scan(&c.Name, &c.Lat, &c.Lon, &c.DistanceM)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("find nearest %s: %w", category, err)
	}
	return candidates, nil
}
