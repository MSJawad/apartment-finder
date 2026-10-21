package geocode

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/upstream"
)

func TestGeocode(t *testing.T) {
	t.Parallel()
	r, err := region.Load("../../config/thuringia.json")
	if err != nil {
		t.Fatal(err)
	}

	// The fake Photon picks a recorded response by query and layers, and
	// checks the parameters every request must carry.
	fixtures := map[string]string{
		"anger 1, erfurt|house,street":           "photon_house.json",
		"johannesstraße, erfurt|street":          "photon_street.json",
		"johannesstr., erfurt|street":            "photon_street.json",
		"magdeburger allee, erfurt|street":       "photon_street_only.json",
		"magdeburger allee, erfurt|house,street": "photon_poi_on_street.json", // a tram line: wrong
		"universität erfurt|street":              "photon_no_street.json",
		"universität erfurt|house,street":        "photon_university.json",
		"erfurt|street":                          "photon_city_street.json", // Erfurter Straße, near Weimar
		"erfurt|house,street":                    "photon_landmark.json",
		"alexanderplatz 1|house,street":          "photon_outside.json",
	}
	var calls atomic.Int32
	photon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		q := req.URL.Query()
		if q.Get("lang") != "de" || q.Get("limit") != "1" ||
			q.Get("bbox") != "9.863176,50.19971,12.66106,51.65237" || req.URL.Path != "/api" {
			t.Errorf("unexpected request %s", req.URL)
		}
		fixture, ok := fixtures[q.Get("q")+"|"+strings.Join(q["layer"], ",")]
		if !ok {
			fixture = "photon_empty.json"
		}
		body, err := os.ReadFile("testdata/" + fixture)
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(photon.Close)

	client := upstream.NewClient(dbtest.New(t), upstream.Options{
		Provider: "photon", UserAgent: "apartment-finder/test", HitTTL: time.Hour, MissTTL: time.Hour,
		HTTP: &http.Client{Timeout: 5 * time.Second},
	}, upstream.NewMetrics(prometheus.NewRegistry()))
	geocoder := NewPhoton(client, photon.URL+"/", r)

	tests := []struct {
		address string
		want    Result
		calls   int // requests to Photon
	}{
		{"Anger 1, Erfurt", Result{Found: true, Lat: 50.9776985, Lon: 11.0364678, Label: "Anger 1, 99084 Erfurt", Precision: "house"}, 1},
		// A messier spelling of the same address shares its cache entry.
		{"  ANGER 1,   Erfurt ", Result{Found: true, Lat: 50.9776985, Lon: 11.0364678, Label: "Anger 1, 99084 Erfurt", Precision: "house"}, 0},
		{"Johannesstraße, Erfurt", Result{Found: true, Lat: 50.9806, Lon: 11.0302, Label: "Johannesstraße, 99084 Erfurt", Precision: "street"}, 1},
		{"Johannesstr., Erfurt", Result{Found: true, Lat: 50.9806, Lon: 11.0302, Label: "Johannesstraße, 99084 Erfurt", Precision: "street"}, 1},
		// Without a house number, the street wins over a landmark on it.
		{"Magdeburger Allee, Erfurt", Result{Found: true, Lat: 50.9939995, Lon: 11.0256813, Label: "Magdeburger Allee, 99086 Erfurt", Precision: "street"}, 1},
		// No street by that name: a landmark.
		{"Universität Erfurt", Result{Found: true, Lat: 50.9909272, Lon: 11.0095474, Label: "Universität Erfurt, Nordhäuser Straße 63, 99089 Erfurt", Precision: "house"}, 2},
		// A street the address doesn't name doesn't count.
		{"Erfurt", Result{Found: true, Lat: 50.9725, Lon: 11.0385, Label: "Erfurt Hauptbahnhof, Willy-Brandt-Platz 12, 99084 Erfurt", Precision: "house"}, 2},
		{"Alexanderplatz 1", Result{}, 1}, // in Berlin, outside the region
		{"Nirgendwo 99", Result{}, 1},
	}
	for _, tt := range tests {
		before := calls.Load()
		got, err := geocoder.Geocode(t.Context(), tt.address)
		if err != nil {
			t.Fatalf("Geocode(%q) error = %v", tt.address, err)
		}
		if got.Found != tt.want.Found || got.Label != tt.want.Label || got.Precision != tt.want.Precision ||
			math.Abs(got.Lat-tt.want.Lat) > 1e-9 || math.Abs(got.Lon-tt.want.Lon) > 1e-9 {
			t.Errorf("Geocode(%q) = %+v, want %+v", tt.address, got, tt.want)
		}
		if n := int(calls.Load() - before); n != tt.calls {
			t.Errorf("Geocode(%q) called Photon %d times, want %d", tt.address, n, tt.calls)
		}
	}

	if _, err := geocoder.Geocode(t.Context(), "   "); err == nil {
		t.Error("Geocode of a blank address succeeded, want an error")
	}
}

func TestParseRejectsMalformedResponses(t *testing.T) {
	for _, body := range []string{`not json`, `{"features":[{"geometry":{"coordinates":[11.0]}}]}`} {
		if _, err := isMiss([]byte(body)); err == nil {
			t.Errorf("isMiss(%s) succeeded, want an error", body)
		}
	}
}

func TestHouseNumber(t *testing.T) {
	for query, want := range map[string]bool{
		"anger 1, erfurt":              true,
		"anger 1 erfurt":               true,
		"johannesstraße 10a, erfurt":   true,
		"johannesstraße 10 a, erfurt":  true,
		"anger 1-3, erfurt":            true,
		"12 anger":                     true,
		"johannesstraße, 99084 erfurt": false, // a postcode
		"johannesstraße, erfurt":       false,
		"straße des 17. juni, berlin":  false,
	} {
		if got := houseNumber.MatchString(query); got != want {
			t.Errorf("houseNumber(%q) = %v, want %v", query, got, want)
		}
	}
}
