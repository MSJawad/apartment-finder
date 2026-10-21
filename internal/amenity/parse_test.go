package amenity

import (
	"strings"
	"testing"

	"github.com/MSJawad/apartment-finder/internal/region"
)

func loadRegion(t *testing.T) *region.Region {
	t.Helper()
	r, err := region.Load("../../config/thuringia.json")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParse(t *testing.T) {
	reg := loadRegion(t)
	const rs = "\x1e"
	input := strings.Join([]string{
		// osmium writes a record separator before each feature.
		rs + `{"type":"Feature","geometry":{"type":"Point","coordinates":[11.03,50.97]},"properties":{"@type":"node","@id":1,"shop":"supermarket","name":"REWE"}}`,
		``, // blank lines are ignored
		`{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[11,50],[11.1,50],[11.1,50.1],[11,50]]]},"properties":{"@type":"way","@id":2,"leisure":"park"}}`,
		// No category: skipped.
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[11,50]},"properties":{"@type":"node","@id":3,"amenity":"bench"}}`,
		// Two categories: one amenity each.
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[11,50]},"properties":{"@type":"node","@id":4,"highway":"bus_stop","railway":"station","name":"Hbf"}}`,
	}, "\n")

	got, err := Parse(strings.NewReader(input), reg)
	if err != nil {
		t.Fatal(err)
	}
	want := []Amenity{
		{Category: "supermarket", OSMType: "node", OSMID: 1, Name: "REWE"},
		{Category: "park", OSMType: "way", OSMID: 2},
		{Category: "transit_stop", OSMType: "node", OSMID: 4, Name: "Hbf"},
		{Category: "train_station", OSMType: "node", OSMID: 4, Name: "Hbf"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d amenities, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g := got[i]
		if g.Category != want[i].Category || g.OSMType != want[i].OSMType || g.OSMID != want[i].OSMID || g.Name != want[i].Name {
			t.Errorf("amenity %d = %+v, want %+v", i, g, want[i])
		}
		if len(g.Geometry) == 0 {
			t.Errorf("amenity %d has no geometry", i)
		}
	}
}

func TestParseErrors(t *testing.T) {
	reg := loadRegion(t)
	tests := []struct {
		name, input, wantErr string
	}{
		{"not JSON", `{"geometry":{"type":"Point","coordinates":[11,50]},"properties":{"@type":"node","@id":1}}` + "\nnot json", "line 2"},
		{"no @id", `{"geometry":{"type":"Point","coordinates":[11,50]},"properties":{"@type":"node","shop":"supermarket"}}`, "lacks @type, @id"},
		{"no geometry", `{"properties":{"@type":"node","@id":1,"shop":"supermarket"}}`, "lacks @type, @id"},
		{"line too long", `{"x":"` + strings.Repeat("a", maxFeature) + `"}`, "read features"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(tt.input), reg); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
