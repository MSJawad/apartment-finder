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

	// The fake Photon picks a recorded response by query, and checks the
	// parameters every request must carry.
	fixtures := map[string]string{
		"anger 1, erfurt":        "photon_house.json",
		"johannesstraße, erfurt": "photon_street.json",
		"erfurt":                 "photon_landmark.json",
		"alexanderplatz 1":       "photon_outside.json",
	}
	var calls atomic.Int32
	photon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		q := req.URL.Query()
		if q.Get("lang") != "de" || q.Get("limit") != "1" || strings.Join(q["layer"], ",") != "house,street" ||
			q.Get("bbox") != "9.8632,50.1997,12.6611,51.6524" || req.URL.Path != "/api" {
			t.Errorf("unexpected request %s", req.URL)
		}
		fixture, ok := fixtures[q.Get("q")]
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
	}{
		{"Anger 1, Erfurt", Result{Found: true, Lat: 50.9776985, Lon: 11.0364678, Label: "Anger 1, 99084 Erfurt", Precision: "house"}},
		{"  ANGER 1,   Erfurt ", Result{Found: true, Lat: 50.9776985, Lon: 11.0364678, Label: "Anger 1, 99084 Erfurt", Precision: "house"}},
		{"Johannesstraße, Erfurt", Result{Found: true, Lat: 50.9806, Lon: 11.0302, Label: "Johannesstraße, 99084 Erfurt", Precision: "street"}},
		{"Erfurt", Result{Found: true, Lat: 50.9725, Lon: 11.0385, Label: "Erfurt Hauptbahnhof, Willy-Brandt-Platz 12, 99084 Erfurt", Precision: "house"}},
		{"Alexanderplatz 1", Result{}}, // in Berlin, outside the region
		{"Nirgendwo 99", Result{}},
	}
	for _, tt := range tests {
		got, err := geocoder.Geocode(t.Context(), tt.address)
		if err != nil {
			t.Fatalf("Geocode(%q) error = %v", tt.address, err)
		}
		if got.Found != tt.want.Found || got.Label != tt.want.Label || got.Precision != tt.want.Precision ||
			math.Abs(got.Lat-tt.want.Lat) > 1e-9 || math.Abs(got.Lon-tt.want.Lon) > 1e-9 {
			t.Errorf("Geocode(%q) = %+v, want %+v", tt.address, got, tt.want)
		}
	}
	// "Anger 1, Erfurt" and its messier spelling share one cache entry.
	if n, want := int(calls.Load()), len(tests)-1; n != want {
		t.Errorf("Photon called %d times, want %d", n, want)
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
