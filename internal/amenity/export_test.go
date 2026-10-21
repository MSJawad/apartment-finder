package amenity

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
)

// requireOsmium skips the test when osmium isn't installed, except in CI
// (where GitHub sets CI=true), so the import is always tested there.
func requireOsmium(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("osmium"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("osmium is not installed")
		}
		t.Skip("osmium is not installed")
	}
}

func TestFilters(t *testing.T) {
	got := Filters(loadRegion(t))
	want := []string{
		"nwr/amenity=doctors", "nwr/amenity=pharmacy", "nwr/highway=bus_stop", "nwr/leisure=fitness_centre",
		"nwr/leisure=park", "nwr/railway=station", "nwr/railway=tram_stop", "nwr/shop=supermarket",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Filters() = %v, want %v", got, want)
	}
}

func TestExportSample(t *testing.T) {
	requireOsmium(t)
	reg := loadRegion(t)

	path, err := Export(t.Context(), reg, "testdata/erfurt-sample.osm", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	amenities, err := Parse(f, reg)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, a := range amenities {
		var geometry struct{ Type string }
		if err := json.Unmarshal(a.Geometry, &geometry); err != nil {
			t.Fatal(err)
		}
		got = append(got, a.Category+" "+a.Name+" ("+a.OSMType+", "+geometry.Type+")")
	}
	slices.Sort(got)
	want := []string{
		"gym FitX (node, Point)",
		"park Brühler Garten (way, MultiPolygon)",
		"park Nordpark (relation, MultiPolygon)",
		"supermarket Aldi (way, MultiPolygon)",
		"supermarket REWE (node, Point)",
		"train_station Erfurt Hbf (node, Point)",
		"transit_stop Anger (node, Point)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("exported:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestExportReportsOsmiumErrors(t *testing.T) {
	requireOsmium(t)
	_, err := Export(t.Context(), loadRegion(t), "testdata/no-such-file.osm.pbf", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "osmium tags-filter") {
		t.Fatalf("Export() error = %v, want an osmium tags-filter error", err)
	}
}

// TestImportSampleEndToEnd runs the whole import on the sample extract and
// asks the database for what's near the Anger.
func TestImportSampleEndToEnd(t *testing.T) {
	requireOsmium(t)
	reg := loadRegion(t)
	pool := dbtest.New(t)

	path, err := Export(t.Context(), reg, "testdata/erfurt-sample.osm", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	amenities, err := Parse(f, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Replace(t.Context(), pool, amenities); err != nil {
		t.Fatal(err)
	}

	const lat, lon = 50.9777, 11.0365 // the Anger
	tests := []struct {
		category string
		want     []string // nearest first
	}{
		{"supermarket", []string{"REWE", "Aldi"}},
		{"park", []string{"Brühler Garten", "Nordpark"}},
		{"gym", []string{"FitX"}},
		{"pharmacy", nil},
	}
	for _, tt := range tests {
		candidates, err := Nearest(t.Context(), pool, tt.category, lat, lon, 5000, 5)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range candidates {
			got = append(got, c.Name)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("nearest %s = %v, want %v", tt.category, got, tt.want)
		}
	}
}
