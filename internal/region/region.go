// Package region describes the area the service covers and what listings in
// it are scored on. It is loaded from a JSON file, so covering another city
// is a config change, not a code change.
package region

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/MSJawad/apartment-finder/internal/scoring"
)

// Mode is how a category's travel time is measured.
type Mode string

// Travel modes.
const (
	Walk Mode = "walk"
	Bike Mode = "bike"
)

// metresPerMinute are the speeds search radii are derived from. They are
// faster than anyone really walks (6 km/h) or cycles (20 km/h), so a
// straight-line radius never cuts off a place that is reachable in time.
var metresPerMinute = map[Mode]float64{Walk: 100, Bike: 333}

var (
	categoryKey  = regexp.MustCompile(`^[a-z][a-z_]*$`)
	languageCode = regexp.MustCompile(`^[a-z]{2}$`)
)

// Region is the covered area and its scoring rules.
type Region struct {
	Name string `json:"name"`
	// Language is the two-letter code addresses are shown in, e.g. "de".
	Language string `json:"language"`
	// BoundaryFile is the region's .poly file, the one Geofabrik publishes
	// with its extract, relative to the config file. See Boundary.
	BoundaryFile string `json:"boundary"`
	// BBox is the smallest box around the boundary: min lon, min lat, max
	// lon, max lat. Geocoder searches are limited to it.
	BBox     [4]float64 `json:"-"`
	boundary *Boundary
	// Map is where the page's map opens.
	Map struct {
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
		Zoom int     `json:"zoom"`
	} `json:"map"`
	// OSMExtract is the OpenStreetMap download for this region, used to
	// build the routing data.
	OSMExtract string `json:"osm_extract"`
	Listing    struct {
		RentPerM2 Threshold `json:"rent_per_m2"`
		SizeM2    Threshold `json:"size_m2"`
		Rooms     Threshold `json:"rooms"`
	} `json:"listing"`
	Categories []Category `json:"categories"`
}

// Threshold is how a listing attribute scores; see scoring.Criterion.
type Threshold struct {
	Best   float64 `json:"best"`
	Worst  float64 `json:"worst"`
	Weight float64 `json:"weight"`
}

// Category is a kind of everyday place, scored by the travel time to the
// nearest one.
type Category struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Mode     Mode    `json:"mode"`
	BestMin  float64 `json:"best_min"`
	WorstMin float64 `json:"worst_min"`
	Weight   float64 `json:"weight"`
	// OSM lists the OpenStreetMap tags that make a place one of these. A
	// place matches if it has all the tags of any one entry.
	OSM []map[string]string `json:"osm"`
}

// Load reads and validates the region file at path, and the boundary file
// next to it.
func Load(path string) (*Region, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path comes from our own deployment config
	if err != nil {
		return nil, fmt.Errorf("read region config: %w", err)
	}
	return Parse(data, os.DirFS(filepath.Dir(path)))
}

// Parse decodes and validates a region config, reading its boundary file
// from files. Unknown fields are errors, so a misspelt setting can't be
// silently ignored.
func Parse(data []byte, files fs.FS) (*Region, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Region
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parse region config: %w", err)
	}
	if r.BoundaryFile == "" {
		return nil, errors.New("region config: boundary is missing")
	}
	f, err := files.Open(r.BoundaryFile)
	if err != nil {
		return nil, fmt.Errorf("region config: %w", err)
	}
	defer func() { _ = f.Close() }()
	if r.boundary, err = ParseBoundary(f); err != nil {
		return nil, fmt.Errorf("region config: %w", err)
	}
	r.BBox = r.boundary.BBox()
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("region config: %w", err)
	}
	return &r, nil
}

// Validate reports the first problem with r, whose boundary must be loaded.
func (r *Region) Validate() error {
	switch {
	case r.Name == "":
		return errors.New("name is missing")
	case !languageCode.MatchString(r.Language):
		return errors.New(`language must be a two-letter code such as "de"`)
	case r.boundary == nil:
		return errors.New("boundary is not loaded")
	case !r.Contains(r.Map.Lat, r.Map.Lon):
		return errors.New("map center is outside the boundary")
	case r.Map.Zoom < 1 || r.Map.Zoom > 19:
		return errors.New("map zoom must be between 1 and 19")
	case r.OSMExtract == "":
		return errors.New("osm_extract is missing")
	case r.Listing.RentPerM2.Best >= r.Listing.RentPerM2.Worst:
		return errors.New("rent_per_m2: best must be below worst, since cheaper is better")
	case r.Listing.SizeM2.Best <= r.Listing.SizeM2.Worst:
		return errors.New("size_m2: best must be above worst, since bigger is better")
	case r.Listing.Rooms.Best <= r.Listing.Rooms.Worst:
		return errors.New("rooms: best must be above worst, since more is better")
	case len(r.Categories) == 0:
		return errors.New("no categories")
	}
	for _, c := range r.Categories {
		if err := c.validate(); err != nil {
			return err
		}
	}
	// Catches duplicate category keys, negative weights and non-numbers.
	return scoring.Validate(r.Criteria())
}

func (c Category) validate() error {
	switch {
	case !categoryKey.MatchString(c.Key):
		return fmt.Errorf("category key %q must be lower case letters and underscores", c.Key)
	case c.Label == "":
		return fmt.Errorf("category %s: label is missing", c.Key)
	case c.Mode != Walk && c.Mode != Bike:
		return fmt.Errorf("category %s: mode must be %q or %q", c.Key, Walk, Bike)
	case c.BestMin < 0 || c.BestMin >= c.WorstMin:
		return fmt.Errorf("category %s: best_min must be at least 0 and below worst_min", c.Key)
	case len(c.OSM) == 0:
		return fmt.Errorf("category %s: no osm tags", c.Key)
	}
	for _, tags := range c.OSM {
		if len(tags) == 0 {
			return fmt.Errorf("category %s: empty osm tag set", c.Key)
		}
	}
	return nil
}

// Contains reports whether the point is inside the region's boundary, where
// there is routing and amenity data.
func (r *Region) Contains(lat, lon float64) bool {
	return r.boundary.Contains(lat, lon)
}

// SearchRadius is how far, in metres in a straight line, to look for places
// of this category. Anything further away can't be reached within WorstMin,
// so it would score 0 anyway.
func (c Category) SearchRadius() float64 {
	return c.WorstMin * metresPerMinute[c.Mode]
}

// Matches reports whether a place with these OSM tags belongs to c.
func (c Category) Matches(tags map[string]string) bool {
	for _, want := range c.OSM {
		if hasAll(tags, want) {
			return true
		}
	}
	return false
}

func hasAll(tags, want map[string]string) bool {
	for k, v := range want {
		if tags[k] != v {
			return false
		}
	}
	return true
}

// Criteria returns what every listing in the region is scored on: rent per
// m², size, rooms and the travel time to each category.
func (r *Region) Criteria() []scoring.Criterion {
	l := r.Listing
	criteria := []scoring.Criterion{
		{Key: "rent_per_m2", Label: "Warm rent per m²", Unit: "€/m²", Best: l.RentPerM2.Best, Worst: l.RentPerM2.Worst, Weight: l.RentPerM2.Weight},
		{Key: "size_m2", Label: "Size", Unit: "m²", Best: l.SizeM2.Best, Worst: l.SizeM2.Worst, Weight: l.SizeM2.Weight},
		{Key: "rooms", Label: "Rooms", Unit: "rooms", Best: l.Rooms.Best, Worst: l.Rooms.Worst, Weight: l.Rooms.Weight},
	}
	for _, c := range r.Categories {
		criteria = append(criteria, scoring.Criterion{
			Key: CategoryCriterion(c.Key), Label: c.Label, Unit: "min",
			Best: c.BestMin, Worst: c.WorstMin, Weight: c.Weight,
		})
	}
	return criteria
}

// CategoryCriterion is the scoring key for a category's travel time.
func CategoryCriterion(key string) string {
	return "category:" + key
}
