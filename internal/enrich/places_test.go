package enrich

import (
	"encoding/json"
	"math"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/region"
)

func addVisitor(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(t.Context(), "INSERT INTO visitors (id) VALUES (gen_random_uuid()) RETURNING id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// addOwnListing adds a listing at the Anger for owner, or an example listing
// for a nil owner, and runs its enrich job.
func addOwnListing(t *testing.T, pool *pgxpool.Pool, enricher *Enricher, owner *uuid.UUID) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO listings (visitor_id, address, warm_rent_cents, size_m2, rooms)
		VALUES ($1, 'Anger 1, Erfurt', 85000, 62.5, 2.5) RETURNING id`, owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := enricher.Handle(t.Context(), job(id, 1)); err != nil {
		t.Fatal(err)
	}
	return id
}

func addPlace(t *testing.T, pool *pgxpool.Pool, owner uuid.UUID, address string, mode region.Mode) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO places (visitor_id, name, address, mode) VALUES ($1, 'Somewhere', $2, $3) RETURNING id`,
		owner, address, mode).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func placeJob(placeID int64, attempt int) queue.Job {
	payload, _ := json.Marshal(PlacePayload{PlaceID: placeID})
	return queue.Job{Kind: PlaceJobKind, Payload: payload, Attempts: attempt, MaxAttempts: 8}
}

// trips returns the measured minutes from each listing to placeID; a NULL
// (no route) is -1.
func trips(t *testing.T, pool *pgxpool.Pool, where string, arg int64) map[[2]int64]float64 {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT listing_id, place_id, coalesce(minutes, -1) FROM place_measurements WHERE "+where, arg)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[[2]int64]float64{}
	for rows.Next() {
		var listingID, placeID int64
		var minutes float64
		if err := rows.Scan(&listingID, &placeID, &minutes); err != nil {
			t.Fatal(err)
		}
		got[[2]int64{listingID, placeID}] = minutes
	}
	return got
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestPlaceTripsComeFromEveryListingItsVisitorSees(t *testing.T) {
	t.Parallel()
	pool, enricher, router := setup(t)
	alice, bob := addVisitor(t, pool), addVisitor(t, pool)
	alicesListing := addOwnListing(t, pool, enricher, &alice)
	example := addOwnListing(t, pool, enricher, nil)
	bobsListing := addOwnListing(t, pool, enricher, &bob)

	uni := addPlace(t, pool, alice, "Nordhäuser Straße 63, Erfurt", region.Bike)
	// Running twice must give the same result: jobs can run more than once.
	for range 2 {
		if err := enricher.HandlePlace(t.Context(), placeJob(uni, 1)); err != nil {
			t.Fatal(err)
		}
	}

	var status, label string
	if err := pool.QueryRow(t.Context(), "SELECT status, geocoded_label FROM places WHERE id = $1", uni).Scan(&status, &label); err != nil {
		t.Fatal(err)
	}
	if status != "located" || label != "Universität Erfurt" {
		t.Errorf("place = %s, %q; want located at the university", status, label)
	}

	got := trips(t, pool, "place_id = $1", uni)
	wantMinutes := 0.012 * 111_200 / 250 // cycling ~1.3 km
	if len(got) != 2 || !near(got[[2]int64{alicesListing, uni}], wantMinutes) || !near(got[[2]int64{example, uni}], wantMinutes) {
		t.Errorf("trips = %v, want ~%.1f min from alice's listing %d and the example %d only (not bob's %d)",
			got, wantMinutes, alicesListing, example, bobsListing)
	}
	if router.toCalls[region.Bike] != 2 {
		t.Errorf("%d TableTo calls over two runs, want one per run", router.toCalls[region.Bike])
	}
}

func TestNewListingsMeasureTripsToPlaces(t *testing.T) {
	t.Parallel()
	pool, enricher, _ := setup(t)
	alice, bob := addVisitor(t, pool), addVisitor(t, pool)
	alicesStation := addPlace(t, pool, alice, "Willy-Brandt-Platz 12, Erfurt", region.Walk)
	bobsUni := addPlace(t, pool, bob, "Nordhäuser Straße 63, Erfurt", region.Bike)
	for _, id := range []int64{alicesStation, bobsUni} {
		// With no listings yet, there is nothing to measure.
		if err := enricher.HandlePlace(t.Context(), placeJob(id, 1)); err != nil {
			t.Fatal(err)
		}
	}

	// Alice's listing measures the trip to her place only.
	alicesListing := addOwnListing(t, pool, enricher, &alice)
	got := trips(t, pool, "listing_id = $1", alicesListing)
	if len(got) != 1 || !near(got[[2]int64{alicesListing, alicesStation}], 0.005*111_200/80) {
		t.Errorf("trips from alice's listing = %v, want only the walk to her station", got)
	}

	// An example listing measures the trip to everyone's places.
	example := addOwnListing(t, pool, enricher, nil)
	got = trips(t, pool, "listing_id = $1", example)
	if len(got) != 2 || !near(got[[2]int64{example, alicesStation}], 0.005*111_200/80) ||
		!near(got[[2]int64{example, bobsUni}], 0.012*111_200/250) {
		t.Errorf("trips from the example = %v, want alice's walk and bob's ride", got)
	}
}

func TestDeletedExamplesAreNotMeasured(t *testing.T) {
	t.Parallel()
	pool, enricher, _ := setup(t)
	alice, bob := addVisitor(t, pool), addVisitor(t, pool)
	example := addOwnListing(t, pool, enricher, nil)
	if _, err := pool.Exec(t.Context(), "INSERT INTO hidden_examples (visitor_id, listing_id) VALUES ($1, $2)", alice, example); err != nil {
		t.Fatal(err)
	}

	// A place of alice's isn't measured from the example alice deleted.
	alicesStation := addPlace(t, pool, alice, "Willy-Brandt-Platz 12, Erfurt", region.Walk)
	bobsUni := addPlace(t, pool, bob, "Nordhäuser Straße 63, Erfurt", region.Bike)
	for _, id := range []int64{alicesStation, bobsUni} {
		if err := enricher.HandlePlace(t.Context(), placeJob(id, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if got := trips(t, pool, "place_id = $1", alicesStation); len(got) != 0 {
		t.Errorf("trips to alice's place = %v, want none from the deleted example", got)
	}

	// Nor when the example is measured again, say after its rent changed,
	// while bob's place still is.
	if _, err := pool.Exec(t.Context(), "DELETE FROM place_measurements"); err != nil {
		t.Fatal(err)
	}
	if err := enricher.Handle(t.Context(), job(example, 1)); err != nil {
		t.Fatal(err)
	}
	if got := trips(t, pool, "listing_id = $1", example); len(got) != 1 || !near(got[[2]int64{example, bobsUni}], 0.012*111_200/250) {
		t.Errorf("trips from the example = %v, want only bob's ride", got)
	}
}

func TestPlaceWithoutRoute(t *testing.T) {
	t.Parallel()
	pool, enricher, router := setup(t)
	alice := addVisitor(t, pool)
	listing := addOwnListing(t, pool, enricher, &alice)
	router.overrides["-0.0050"] = -1 // nothing routes to the station
	station := addPlace(t, pool, alice, "Willy-Brandt-Platz 12, Erfurt", region.Walk)
	if err := enricher.HandlePlace(t.Context(), placeJob(station, 1)); err != nil {
		t.Fatal(err)
	}
	if got := trips(t, pool, "place_id = $1", station); len(got) != 1 || got[[2]int64{listing, station}] != -1 {
		t.Errorf("trips = %v, want one without a route", got)
	}
}

func TestPlaceNotFoundOrFailing(t *testing.T) {
	t.Parallel()
	pool, enricher, _ := setup(t)
	alice := addVisitor(t, pool)

	nowhere := addPlace(t, pool, alice, "Nirgendwo 1", region.Walk)
	if err := enricher.HandlePlace(t.Context(), placeJob(nowhere, 1)); err != nil {
		t.Fatal(err)
	}
	down := addPlace(t, pool, alice, "Photon is down", region.Walk)
	for _, attempt := range []int{1, 8} {
		if err := enricher.HandlePlace(t.Context(), placeJob(down, attempt)); err == nil {
			t.Fatalf("attempt %d succeeded, want the geocoding error", attempt)
		}
	}

	for id, want := range map[int64]string{nowhere: "not_found", down: "failed"} {
		var status, detail string
		if err := pool.QueryRow(t.Context(), "SELECT status, status_detail FROM places WHERE id = $1", id).Scan(&status, &detail); err != nil {
			t.Fatal(err)
		}
		if status != want || detail == "" {
			t.Errorf("place %d = %s (%q), want %s with an explanation", id, status, detail, want)
		}
	}

	// A place deleted before its job ran is nothing to do.
	if err := enricher.HandlePlace(t.Context(), placeJob(999_999, 1)); err != nil {
		t.Errorf("HandlePlace() for a deleted place = %v", err)
	}
	if err := enricher.HandlePlace(t.Context(), queue.Job{Payload: []byte("not json")}); err == nil {
		t.Error("HandlePlace() with a broken payload succeeded")
	}
}
