package api

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/MSJawad/apartment-finder/internal/place"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

const validPlace = `{"name": "Work", "address": "Nordhäuser Straße 63, Erfurt", "mode": "bike"}`

func (b *browser) addPlace(body string) response {
	b.t.Helper()
	return b.do(http.MethodPost, "/api/places", "application/json", body)
}

func TestPlaces(t *testing.T) {
	t.Parallel()
	_, server := setup(t, visitor.Limits{PerHour: 100, PerDay: 100, SiteWidePerDay: 100})
	alice, bob := newBrowser(t, server), newBrowser(t, server)

	resp := alice.addPlace(validPlace)
	if resp.status != http.StatusAccepted {
		t.Fatalf("POST = %d %s, want 202", resp.status, resp.body)
	}
	var work placeJSON
	decode(t, resp.body, &work)
	if work.Name != "Work" || work.Mode != "bike" || work.Status != "pending" || work.Criterion != "place:"+strconv.FormatInt(work.ID, 10) {
		t.Errorf("added place = %+v", work)
	}

	var places placesJSON
	decode(t, alice.do(http.MethodGet, "/api/places", "", "").body, &places)
	if len(places.Places) != 1 || places.Places[0].ID != work.ID {
		t.Errorf("alice's places = %+v", places.Places)
	}
	decode(t, bob.do(http.MethodGet, "/api/places", "", "").body, &places)
	if len(places.Places) != 0 {
		t.Errorf("bob sees %+v", places.Places)
	}

	// The place is one more criterion for alice, and only for alice.
	criteria := alice.listings("?weight." + work.Criterion + "=5").Criteria
	last := criteria[len(criteria)-1]
	if last.Key != work.Criterion || last.Label != "Work" || last.DefaultWeight != 3 || last.Weight != 5 {
		t.Errorf("alice's last criterion = %+v", last)
	}
	if resp := bob.do(http.MethodGet, "/api/listings?weight."+work.Criterion+"=5", "", ""); resp.status != http.StatusBadRequest {
		t.Errorf("bob weighting alice's place: %d, want 400", resp.status)
	}

	path := "/api/places/" + strconv.FormatInt(work.ID, 10)
	if resp := bob.do(http.MethodDelete, path, "", ""); resp.status != http.StatusNotFound {
		t.Errorf("bob deleting alice's place: %d, want 404", resp.status)
	}
	if resp := alice.do(http.MethodDelete, path, "", ""); resp.status != http.StatusNoContent {
		t.Errorf("alice deleting their place: %d %s", resp.status, resp.body)
	}
}

func TestAddPlaceRejects(t *testing.T) {
	t.Parallel()
	_, server := setup(t, visitor.Limits{PerHour: 100, PerDay: 100, SiteWidePerDay: 100})
	alice := newBrowser(t, server)

	resp := alice.addPlace(`{"name": "", "address": "Anger 1", "mode": "car"}`)
	var body apiError
	decode(t, resp.body, &body)
	if resp.status != http.StatusBadRequest || body.Fields["name"] == "" || body.Fields["mode"] == "" {
		t.Errorf("invalid place: %d %+v", resp.status, body)
	}

	for range place.MaxPerVisitor {
		if resp := alice.addPlace(validPlace); resp.status != http.StatusAccepted {
			t.Fatalf("POST = %d %s", resp.status, resp.body)
		}
	}
	if resp := alice.addPlace(validPlace); resp.status != http.StatusConflict {
		t.Errorf("one place too many: %d %s, want 409", resp.status, resp.body)
	}
}

func TestPlaceTripsCountInTheScore(t *testing.T) {
	t.Parallel()
	pool, server := setup(t, visitor.Limits{PerHour: 100, PerDay: 100, SiteWidePerDay: 100})
	alice := newBrowser(t, server)
	var near, far listingJSON
	decode(t, alice.addListing(validListing).body, &near)
	decode(t, alice.addListing(validListing).body, &far)
	var work placeJSON
	decode(t, alice.addPlace(validPlace).body, &work)

	// Same rent, but one is 10 minutes from work (full marks) and the other
	// 45 (none).
	score(t, pool, near.ID, map[string]float64{"rent_per_m2": 12})
	score(t, pool, far.ID, map[string]float64{"rent_per_m2": 12})
	for id, minutes := range map[int64]float64{near.ID: 10, far.ID: 45} {
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO place_measurements (listing_id, place_id, minutes, distance_m) VALUES ($1, $2, $3, 1000)`,
			id, work.ID, minutes); err != nil {
			t.Fatal(err)
		}
	}

	got := alice.listings("?weight.rent_per_m2=1&weight." + work.Criterion + "=1")
	if got.Listings[0].ID != near.ID || got.Listings[1].ID != far.ID {
		t.Fatalf("order = %d, %d; want the one near work first", got.Listings[0].ID, got.Listings[1].ID)
	}
	var trip componentJSON
	for _, c := range got.Listings[0].Score.Breakdown {
		if c.Key == work.Criterion {
			trip = c
		}
	}
	if trip.Outcome != "measured" || trip.Value == nil || *trip.Value != 10 || trip.Score != 1 {
		t.Errorf("trip to work = %+v", trip)
	}
}

func TestPlaceThatCouldNotBeLocatedIsNoCriterion(t *testing.T) {
	t.Parallel()
	pool, server := setup(t, visitor.Limits{PerHour: 100, PerDay: 100, SiteWidePerDay: 100})
	alice := newBrowser(t, server)
	var home listingJSON
	decode(t, alice.addListing(validListing).body, &home)
	measured := map[string]float64{}
	for _, c := range alice.listings("").Criteria {
		measured[c.Key] = 5
	}
	score(t, pool, home.ID, measured)

	for _, status := range []string{"not_found", "failed"} {
		var work placeJSON
		decode(t, alice.addPlace(validPlace).body, &work)
		// As the worker leaves it.
		if _, err := pool.Exec(t.Context(),
			"UPDATE places SET status = $2, status_detail = 'We couldn''t find this address in Thuringia.' WHERE id = $1",
			work.ID, status); err != nil {
			t.Fatal(err)
		}

		// A weight the page saved while the place was being located is
		// still accepted, but the place is no criterion and nothing waits
		// for its trips, so the page stops polling.
		got := alice.listings("?weight." + work.Criterion + "=5")
		for _, c := range got.Criteria {
			if c.Key == work.Criterion {
				t.Errorf("%s place: still a criterion: %+v", status, c)
			}
		}
		for _, c := range got.Listings[0].Score.Breakdown {
			if c.Key == work.Criterion || c.Outcome == "pending" {
				t.Errorf("%s place: breakdown has %+v", status, c)
			}
		}

		// The place itself says what went wrong.
		var places placesJSON
		decode(t, alice.do(http.MethodGet, "/api/places", "", "").body, &places)
		last := places.Places[len(places.Places)-1]
		if last.ID != work.ID || last.Status != status || last.StatusDetail == "" {
			t.Errorf("%s place: listed as %+v", status, last)
		}
	}
}
