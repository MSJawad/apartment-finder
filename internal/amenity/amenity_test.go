package amenity_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/MSJawad/apartment-finder/internal/amenity"
	"github.com/MSJawad/apartment-finder/internal/dbtest"
)

// A listing on Erfurt's Anger. Near 51°N, 0.001° of latitude is about 111 m.
const lat, lon = 50.9777, 11.0365

func point(id int64, category string, dLat, dLon float64) amenity.Amenity {
	return amenity.Amenity{
		Category: category, OSMType: "node", OSMID: id, Name: fmt.Sprintf("%s %d", category, id),
		Geometry: json.RawMessage(fmt.Sprintf(`{"type":"Point","coordinates":[%f,%f]}`, lon+dLon, lat+dLat)),
	}
}

// park is a square from 0.002° to 0.006° north of the listing: its nearest
// edge is about 222 m away, its middle about 444 m.
var park = amenity.Amenity{
	Category: "park", OSMType: "way", OSMID: 100, Name: "Brühler Garten",
	Geometry: json.RawMessage(fmt.Sprintf(`{"type":"Polygon","coordinates":[[[%[1]f,%[3]f],[%[2]f,%[3]f],[%[2]f,%[4]f],[%[1]f,%[4]f],[%[1]f,%[3]f]]]}`,
		lon-0.002, lon+0.002, lat+0.002, lat+0.006)),
}

func TestNearest(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	err := amenity.Replace(t.Context(), pool, []amenity.Amenity{
		point(1, "supermarket", 0.0009, 0), // ~100 m
		point(2, "supermarket", 0.0027, 0), // ~300 m
		point(3, "supermarket", 0.027, 0),  // ~3 km
		point(4, "transit_stop", 0.0001, 0),
		park,
	})
	if err != nil {
		t.Fatal(err)
	}

	type want struct {
		name      string
		distanceM float64
	}
	tests := []struct {
		name     string
		category string
		lat      float64
		radiusM  float64
		limit    int
		want     []want
	}{
		{"closest first, within the radius", "supermarket", lat, 1000, 5,
			[]want{{"supermarket 1", 100}, {"supermarket 2", 300}}},
		{"limit", "supermarket", lat, 1000, 1, []want{{"supermarket 1", 100}}},
		{"other categories don't count", "pharmacy", lat, 5000, 5, nil},
		{"an area counts from its nearest edge", "park", lat, 1000, 5, []want{{"Brühler Garten", 222}}},
		{"inside an area is distance 0", "park", lat + 0.004, 1000, 5, []want{{"Brühler Garten", 0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := amenity.Nearest(t.Context(), pool, tt.category, tt.lat, lon, tt.radiusM, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %d candidates", got, len(tt.want))
			}
			for i, w := range tt.want {
				if got[i].Name != w.name || math.Abs(got[i].DistanceM-w.distanceM) > 2 {
					t.Errorf("candidate %d = %s at %.0f m, want %s at %.0f m", i, got[i].Name, got[i].DistanceM, w.name, w.distanceM)
				}
			}
		})
	}

	// Routes go to the park's nearest edge, straight north of the listing.
	got, err := amenity.Nearest(t.Context(), pool, "park", lat, lon, 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got[0].Lat-(lat+0.002)) > 1e-6 || math.Abs(got[0].Lon-lon) > 1e-6 {
		t.Errorf("closest point = %.6f,%.6f, want %.6f,%.6f", got[0].Lat, got[0].Lon, lat+0.002, lon)
	}
}

func TestReplaceSwapsTheWholeSet(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	ctx := t.Context()

	if err := amenity.Replace(ctx, pool, []amenity.Amenity{point(1, "gym", 0.001, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := amenity.Replace(ctx, pool, []amenity.Amenity{point(2, "gym", 0.002, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := amenity.Replace(ctx, pool, nil); err == nil {
		t.Error("Replace with no amenities succeeded, want it refused")
	}

	got, err := amenity.Nearest(ctx, pool, "gym", lat, lon, 5000, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "gym 2" {
		t.Errorf("after replacing: %+v, want only gym 2", got)
	}
}
