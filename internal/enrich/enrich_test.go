package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/amenity"
	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/geocode"
	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/routing"
)

// The listing is on Erfurt's Anger. Near 51°N, 0.001° latitude is ~111 m.
const lat, lon = 50.9777, 11.0365

type fakeGeocoder struct{}

func (fakeGeocoder) Geocode(_ context.Context, address string) (geocode.Result, error) {
	switch address {
	case "Anger 1, Erfurt":
		return geocode.Result{Found: true, Lat: lat, Lon: lon, Label: "Anger 1, 99084 Erfurt", Precision: "house"}, nil
	case "Photon is down":
		return geocode.Result{}, errors.New("photon returned HTTP 502")
	default:
		return geocode.Result{}, nil
	}
}

// fakeRouter takes a minute per 80 m walked or 250 m cycled, in a straight
// line, unless overrides says otherwise for a destination (keyed by its
// latitude offset from the listing); a negative override means no route.
type fakeRouter struct {
	overrides map[string]float64
	calls     map[region.Mode]int
}

func (f *fakeRouter) Table(_ context.Context, mode region.Mode, origin routing.Point, destinations []routing.Point) ([]routing.Leg, error) {
	f.calls[mode]++
	speed := map[region.Mode]float64{region.Walk: 80, region.Bike: 250}[mode]
	legs := make([]routing.Leg, len(destinations))
	for i, d := range destinations {
		metres := math.Abs(d.Lat-origin.Lat) * 111_200
		minutes := metres / speed
		if override, ok := f.overrides[fmt.Sprintf("%.4f", d.Lat-origin.Lat)]; ok {
			minutes = override
		}
		if minutes >= 0 {
			legs[i] = routing.Leg{Reachable: true, Minutes: minutes, DistanceM: metres}
		}
	}
	return legs, nil
}

func place(id int64, category, name string, dLat float64) amenity.Amenity {
	return amenity.Amenity{
		Category: category, OSMType: "node", OSMID: id, Name: name,
		Geometry: json.RawMessage(fmt.Sprintf(`{"type":"Point","coordinates":[%f,%f]}`, lon, lat+dLat)),
	}
}

func setup(t *testing.T) (*pgxpool.Pool, *Enricher, *fakeRouter) {
	t.Helper()
	pool := dbtest.New(t)
	reg, err := region.Load("../../config/thuringia.json")
	if err != nil {
		t.Fatal(err)
	}
	err = amenity.Replace(t.Context(), pool, []amenity.Amenity{
		place(1, "supermarket", "REWE", 0.0009),    // nearest in a straight line...
		place(2, "supermarket", "Netto", 0.0027),   // ...but this one is quicker to reach
		place(3, "transit_stop", "Anger", 0.0004),  // ~45 m
		place(4, "gym", "FitX", 0.0090),            // ~1 km, but no route
		place(5, "park", "Brühler Garten", 0.0020), // ~222 m
		place(6, "pharmacy", "Far away", 0.2),      // ~22 km: outside the search radius
	})
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeRouter{
		overrides: map[string]float64{"0.0009": 10, "0.0027": 4, "0.0090": -1},
		calls:     map[region.Mode]int{},
	}
	return pool, &Enricher{
		Pool: pool, Region: reg, Geocoder: fakeGeocoder{}, Router: router,
	}, router
}

func addListing(t *testing.T, pool *pgxpool.Pool, address string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(t.Context(), `
		INSERT INTO listings (address, warm_rent_cents, size_m2, rooms)
		VALUES ($1, 85000, 62.5, 2.5) RETURNING id`, address).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func job(listingID int64, attempt int) queue.Job {
	payload, _ := json.Marshal(Payload{ListingID: listingID})
	return queue.Job{Kind: JobKind, Payload: payload, Attempts: attempt, MaxAttempts: 8}
}

type row struct {
	outcome   string
	value     *float64
	placeName *string
}

func measurements(t *testing.T, pool *pgxpool.Pool, listingID int64) map[string]row {
	t.Helper()
	rows, err := pool.Query(t.Context(),
		"SELECT criterion, outcome, value, place_name FROM listing_measurements WHERE listing_id = $1", listingID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]row{}
	for rows.Next() {
		var criterion string
		var r row
		if err := rows.Scan(&criterion, &r.outcome, &r.value, &r.placeName); err != nil {
			t.Fatal(err)
		}
		got[criterion] = r
	}
	return got
}

func TestEnrichMeasuresAListing(t *testing.T) {
	t.Parallel()
	pool, enricher, router := setup(t)
	id := addListing(t, pool, "Anger 1, Erfurt")

	// Running twice must give the same result: jobs can run more than once.
	for range 2 {
		if err := enricher.Handle(t.Context(), job(id, 1)); err != nil {
			t.Fatal(err)
		}
	}

	var status, label, precision string
	var hasLocation bool
	if err := pool.QueryRow(t.Context(), `
		SELECT status, geocoded_label, geocode_precision, location IS NOT NULL FROM listings WHERE id = $1`,
		id).Scan(&status, &label, &precision, &hasLocation); err != nil {
		t.Fatal(err)
	}
	if status != "scored" || label != "Anger 1, 99084 Erfurt" || precision != "house" || !hasLocation {
		t.Errorf("listing = %s, %q, %s, located=%v", status, label, precision, hasLocation)
	}

	got := measurements(t, pool, id)
	want := map[string]struct {
		outcome   string
		value     float64 // when measured
		placeName string
	}{
		"rent_per_m2":            {Measured, 13.6, ""}, // 850 € / 62.5 m²
		"size_m2":                {Measured, 62.5, ""},
		"rooms":                  {Measured, 2.5, ""},
		"category:supermarket":   {Measured, 4, "Netto"}, // the faster one, not the nearer
		"category:transit_stop":  {Measured, 0.0004 * 111_200 / 80, "Anger"},
		"category:park":          {Measured, 0.0020 * 111_200 / 80, "Brühler Garten"},
		"category:gym":           {Unreachable, 0, ""},
		"category:pharmacy":      {NoneNearby, 0, ""},
		"category:doctor":        {NoneNearby, 0, ""},
		"category:train_station": {NoneNearby, 0, ""},
	}
	if len(got) != len(want) {
		t.Errorf("%d measurements, want %d: %v", len(got), len(want), got)
	}
	for criterion, w := range want {
		g, ok := got[criterion]
		switch {
		case !ok:
			t.Errorf("%s: not measured", criterion)
		case g.outcome != w.outcome:
			t.Errorf("%s: outcome %s, want %s", criterion, g.outcome, w.outcome)
		case w.outcome == Measured && math.Abs(*g.value-w.value) > 0.01:
			t.Errorf("%s: value %v, want %v", criterion, *g.value, w.value)
		case w.placeName != "" && (g.placeName == nil || *g.placeName != w.placeName):
			t.Errorf("%s: place %v, want %s", criterion, g.placeName, w.placeName)
		}
	}

	// One routing request per mode per run: walk for the supermarket, stop
	// and park, bike for the gym.
	if router.calls[region.Walk] != 2 || router.calls[region.Bike] != 2 {
		t.Errorf("router calls = %v, want 2 per mode over two runs", router.calls)
	}
}

func TestEnrichAddressNotFound(t *testing.T) {
	t.Parallel()
	pool, enricher, _ := setup(t)
	id := addListing(t, pool, "Nirgendwo 1")

	if err := enricher.Handle(t.Context(), job(id, 1)); err != nil {
		t.Fatal(err)
	}
	var status, detail string
	if err := pool.QueryRow(t.Context(), "SELECT status, status_detail FROM listings WHERE id = $1", id).Scan(&status, &detail); err != nil {
		t.Fatal(err)
	}
	if status != "not_found" || detail != "We couldn't find this address in Thuringia." {
		t.Errorf("listing = %s (%q), want not_found with an explanation", status, detail)
	}
	if n := len(measurements(t, pool, id)); n != 0 {
		t.Errorf("%d measurements for an address that wasn't found", n)
	}
}

func TestEnrichFailureMarksTheListingOnlyOnTheLastAttempt(t *testing.T) {
	t.Parallel()
	pool, enricher, _ := setup(t)
	id := addListing(t, pool, "Photon is down")

	for _, tt := range []struct {
		attempt    int
		wantStatus string
	}{{1, "pending"}, {8, "failed"}} {
		if err := enricher.Handle(t.Context(), job(id, tt.attempt)); err == nil {
			t.Fatalf("attempt %d: Handle() succeeded, want the geocoding error", tt.attempt)
		}
		var status string
		if err := pool.QueryRow(t.Context(), "SELECT status FROM listings WHERE id = $1", id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != tt.wantStatus {
			t.Errorf("after attempt %d: status %s, want %s", tt.attempt, status, tt.wantStatus)
		}
	}
}

func TestEnrichDeletedListing(t *testing.T) {
	t.Parallel()
	_, enricher, _ := setup(t)
	if err := enricher.Handle(t.Context(), job(999_999, 1)); err != nil {
		t.Errorf("Handle() for a deleted listing = %v, want nil", err)
	}
	if err := enricher.Handle(t.Context(), queue.Job{Payload: []byte("not json")}); err == nil {
		t.Error("Handle() with a broken payload succeeded")
	}
}
