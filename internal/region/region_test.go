package region

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadShippedConfig(t *testing.T) {
	r, err := Load("../../config/thuringia.json")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := len(r.Criteria()); got != 3+len(r.Categories) {
		t.Errorf("%d criteria, want 3 listing attributes + %d categories", got, len(r.Categories))
	}
	for _, tt := range []struct {
		place    string
		lat, lon float64
		want     bool
	}{
		{"Erfurt, Anger", 50.9777, 11.0365, true},
		{"Eisenach, near the western border", 50.9747, 10.3194, true},
		{"Gera, in the east", 50.8806, 12.0817, true},
		// Inside the box around Thuringia, but in neighbouring states.
		{"Halle (Saale)", 51.4804, 11.9718, false},
		{"Leipzig", 51.3397, 12.3731, false},
		{"Hof, Bavaria", 50.3135, 11.9128, false},
		{"Berlin", 52.52, 13.405, false},
	} {
		if got := r.Contains(tt.lat, tt.lon); got != tt.want {
			t.Errorf("Contains(%s) = %v, want %v", tt.place, got, tt.want)
		}
	}
	box := r.BBox
	if !(box[0] > 9.8 && box[0] < 9.9 && box[1] > 50.1 && box[1] < 50.3 && box[2] > 12.6 && box[2] < 12.7 && box[3] > 51.6 && box[3] < 51.7) {
		t.Errorf("BBox = %v, want the box around Thuringia", box)
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
		{"no boundary", func(r map[string]any) { r["boundary"] = "" }, "boundary is missing"},
		{"boundary file missing", func(r map[string]any) { r["boundary"] = "nowhere.poly" }, "nowhere.poly"},
		{"broken boundary", func(r map[string]any) { r["boundary"] = "broken.poly" }, "boundary:"},
		{"map outside the boundary", func(r map[string]any) { r["map"] = map[string]any{"lat": 52.5, "lon": 13.4, "zoom": 13} }, "map center"},
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
			if _, err := Parse(data, testFiles); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
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
		"boundary":    "test.poly",
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

// testFiles holds the boundaries the test configs refer to: a square from
// 10°E 50°N to 12°E 52°N.
var testFiles = fstest.MapFS{
	"test.poly":   {Data: []byte("test\n1\n 10 50\n 12 50\n 12 52\n 10 52\nEND\nEND\n")},
	"broken.poly": {Data: []byte("test\n1\n 10 50\nEND\nEND\n")},
}

func TestValidateNeedsTheBoundary(t *testing.T) {
	r := &Region{Name: "Test", Language: "de"}
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "boundary is not loaded") {
		t.Errorf("Validate() = %v, want a missing boundary error", err)
	}
}

func TestParseValidConfig(t *testing.T) {
	data, err := json.Marshal(validConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(data, testFiles)
	if err != nil {
		t.Fatal(err)
	}
	if r.BBox != [4]float64{10, 50, 12, 52} {
		t.Errorf("BBox = %v, want the square's corners", r.BBox)
	}
}

func threshold(best, worst float64) map[string]any {
	return map[string]any{"best": best, "worst": worst, "weight": 1}
}

func listing(r map[string]any) map[string]any { return r["listing"].(map[string]any) }

func category(r map[string]any) map[string]any { return r["categories"].([]any)[0].(map[string]any) }
