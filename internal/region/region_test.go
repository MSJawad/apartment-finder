package region

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadShippedConfig(t *testing.T) {
	r, err := Load("../../config/thuringia.json")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := len(r.Criteria()); got != 3+len(r.Categories) {
		t.Errorf("%d criteria, want 3 listing attributes + %d categories", got, len(r.Categories))
	}
	if !r.Contains(50.9777, 11.0365) { // Erfurt, Anger
		t.Error("Erfurt is outside the Thuringia bbox")
	}
	if r.Contains(52.52, 13.405) { // Berlin
		t.Error("Berlin is inside the Thuringia bbox")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("no/such/region.json"); err == nil || !strings.Contains(err.Error(), "read region config") {
		t.Fatalf("Load() error = %v, want a read error", err)
	}
}

func TestParseRejectsBadConfigs(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(r map[string]any)
		wantErr string
	}{
		{"unknown field", func(r map[string]any) { r["colour"] = "blue" }, "unknown field"},
		{"no name", func(r map[string]any) { r["name"] = "" }, "name is missing"},
		{"bad language", func(r map[string]any) { r["language"] = "German" }, "two-letter code"},
		{"bbox corners swapped", func(r map[string]any) { r["bbox"] = []float64{12, 51, 10, 50} }, "bbox must be"},
		{"map outside bbox", func(r map[string]any) { r["map"] = map[string]any{"lat": 52.5, "lon": 13.4, "zoom": 13} }, "map center"},
		{"map zoom", func(r map[string]any) { r["map"] = map[string]any{"lat": 51, "lon": 11, "zoom": 0} }, "zoom"},
		{"no extract", func(r map[string]any) { r["osm_extract"] = "" }, "osm_extract"},
		{"rent thresholds swapped", func(r map[string]any) { listing(r)["rent_per_m2"] = threshold(16, 9) }, "cheaper is better"},
		{"size thresholds swapped", func(r map[string]any) { listing(r)["size_m2"] = threshold(30, 70) }, "bigger is better"},
		{"rooms thresholds swapped", func(r map[string]any) { listing(r)["rooms"] = threshold(1, 3) }, "more is better"},
		{"negative weight", func(r map[string]any) { listing(r)["rooms"] = map[string]any{"best": 3, "worst": 1, "weight": -1} }, "zero or more"},
		{"no categories", func(r map[string]any) { r["categories"] = []any{} }, "no categories"},
		{"bad key", func(r map[string]any) { category(r)["key"] = "Super Market" }, "lower case"},
		{"no label", func(r map[string]any) { category(r)["label"] = "" }, "label is missing"},
		{"bad mode", func(r map[string]any) { category(r)["mode"] = "drive" }, "mode must be"},
		{"minutes swapped", func(r map[string]any) { category(r)["best_min"] = 30 }, "below worst_min"},
		{"no tags", func(r map[string]any) { category(r)["osm"] = []any{} }, "no osm tags"},
		{"empty tag set", func(r map[string]any) { category(r)["osm"] = []any{map[string]any{}} }, "empty osm tag set"},
		{"duplicate category", func(r map[string]any) {
			cats := r["categories"].([]any)
			r["categories"] = append(cats, cats[0])
		}, "appears twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validConfig(t)
			tt.edit(r)
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestCategoryMatches(t *testing.T) {
	stop := Category{OSM: []map[string]string{{"highway": "bus_stop"}, {"railway": "tram_stop"}}}
	station := Category{OSM: []map[string]string{{"railway": "station", "train": "yes"}}}
	tests := []struct {
		name     string
		category Category
		tags     map[string]string
		want     bool
	}{
		{"first alternative", stop, map[string]string{"highway": "bus_stop", "name": "Anger"}, true},
		{"second alternative", stop, map[string]string{"railway": "tram_stop"}, true},
		{"neither", stop, map[string]string{"amenity": "bench"}, false},
		{"all tags of an entry", station, map[string]string{"railway": "station", "train": "yes"}, true},
		{"only some tags of an entry", station, map[string]string{"railway": "station"}, false},
		{"no tags", stop, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.category.Matches(tt.tags); got != tt.want {
				t.Errorf("Matches(%v) = %v, want %v", tt.tags, got, tt.want)
			}
		})
	}
}

func TestSearchRadius(t *testing.T) {
	walk := Category{Mode: Walk, WorstMin: 20}
	bike := Category{Mode: Bike, WorstMin: 20}
	if got := walk.SearchRadius(); got != 2000 {
		t.Errorf("walk radius = %v m, want 2000", got)
	}
	if got := bike.SearchRadius(); got != 6660 {
		t.Errorf("bike radius = %v m, want 6660", got)
	}
}

func validConfig(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"name":        "Test",
		"language":    "de",
		"bbox":        []float64{10, 50, 12, 52},
		"map":         map[string]any{"lat": 51, "lon": 11, "zoom": 13},
		"osm_extract": "https://example.com/test.osm.pbf",
		"listing": map[string]any{
			"rent_per_m2": threshold(9, 16),
			"size_m2":     threshold(70, 30),
			"rooms":       threshold(3, 1),
		},
		"categories": []any{map[string]any{
			"key": "supermarket", "label": "Supermarket", "mode": "walk",
			"best_min": 5, "worst_min": 20, "weight": 1,
			"osm": []any{map[string]any{"shop": "supermarket"}},
		}},
	}
}

func threshold(best, worst float64) map[string]any {
	return map[string]any{"best": best, "worst": worst, "weight": 1}
}

func listing(r map[string]any) map[string]any { return r["listing"].(map[string]any) }

func category(r map[string]any) map[string]any { return r["categories"].([]any)[0].(map[string]any) }
