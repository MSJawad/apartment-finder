package listing_test

import (
	"errors"
	"slices"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/enrich"
	"github.com/MSJawad/apartment-finder/internal/listing"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

var draft = listing.Draft{Address: "Anger 1, Erfurt", WarmRentCents: 85000, SizeM2: 62.5, Rooms: 2.5}

func setup(t *testing.T, limits visitor.Limits) (*pgxpool.Pool, *listing.Store) {
	t.Helper()
	pool := dbtest.New(t)
	return pool, &listing.Store{Pool: pool, Limits: limits}
}

func newVisitor(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id, err := visitor.Ensure(t.Context(), pool, uuid.UUID{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func count(t *testing.T, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAddQueuesTheMeasurement(t *testing.T) {
	t.Parallel()
	pool, store := setup(t, visitor.Limits{PerHour: 1, PerDay: 1, SiteWidePerDay: 1})
	alice := newVisitor(t, pool)

	d := draft
	d.SourceURL = "https://example.com/ad/1"
	id, err := store.Add(t.Context(), alice, "manual", d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), alice, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != d.Address || got.WarmRentCents != d.WarmRentCents || got.SizeM2 != d.SizeM2 ||
		got.Rooms != d.Rooms || got.SourceURL != d.SourceURL || got.Source != "manual" ||
		got.Status != "pending" || got.Example || got.Location != nil {
		t.Errorf("stored listing = %+v", got)
	}
	if n := count(t, pool, "SELECT count(*) FROM jobs WHERE kind = '"+enrich.JobKind+"'"); n != 1 {
		t.Errorf("%d enrich jobs queued, want 1", n)
	}

	// Over the limit, nothing is stored or queued.
	_, err = store.Add(t.Context(), alice, "manual", draft)
	if _, limited := errors.AsType[*visitor.LimitError](err); !limited {
		t.Fatalf("second Add() = %v, want a LimitError", err)
	}
	if n := count(t, pool, "SELECT count(*) FROM listings"); n != 1 {
		t.Errorf("%d listings after a refused Add, want 1", n)
	}
	if n := count(t, pool, "SELECT count(*) FROM jobs"); n != 1 {
		t.Errorf("%d jobs after a refused Add, want 1", n)
	}
}

// addExample adds an example listing, as SyncExamples would.
func addExample(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO listings (visitor_id, source, address, warm_rent_cents, size_m2, rooms)
		VALUES (NULL, 'example', 'Johannesstraße, Erfurt', 70000, 55, 2) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestVisitorsSeeExamplesAndTheirOwn(t *testing.T) {
	t.Parallel()
	pool, store := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	alice, bob := newVisitor(t, pool), newVisitor(t, pool)

	example := addExample(t, pool)
	alicesListing, err := store.Add(t.Context(), alice, "manual", draft)
	if err != nil {
		t.Fatal(err)
	}
	bobsListing, err := store.Add(t.Context(), bob, "manual", draft)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name   string
		viewer uuid.UUID
		want   []int64 // newest first
	}{
		{"alice", alice, []int64{alicesListing, example}},
		{"bob", bob, []int64{bobsListing, example}},
		{"someone new", uuid.Nil(), []int64{example}},
	} {
		listings, err := store.Visible(t.Context(), tt.viewer)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, l := range listings {
			got = append(got, l.ID)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s sees %v, want %v", tt.name, got, tt.want)
		}
	}

	if _, err := store.Get(t.Context(), alice, bobsListing); !errors.Is(err, listing.ErrNotFound) {
		t.Errorf("alice getting bob's listing: %v, want ErrNotFound", err)
	}
	if l, err := store.Get(t.Context(), alice, example); err != nil || !l.Example {
		t.Errorf("alice getting the example: %+v, %v", l, err)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	pool, store := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	alice, bob := newVisitor(t, pool), newVisitor(t, pool)
	id, err := store.Add(t.Context(), alice, "manual", draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO listing_measurements (listing_id, criterion, outcome, value)
		VALUES ($1, 'rooms', 'measured', 2.5)`, id); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(t.Context(), bob, id); !errors.Is(err, listing.ErrNotFound) {
		t.Errorf("bob deleting alice's listing: %v, want ErrNotFound", err)
	}
	if err := store.Delete(t.Context(), alice, id); err != nil {
		t.Fatalf("alice deleting their listing: %v", err)
	}
	if n := count(t, pool, "SELECT count(*) FROM listing_measurements"); n != 0 {
		t.Errorf("%d measurements left after deleting their listing", n)
	}
	// The slot it used still counts towards their limits.
	if n := count(t, pool, "SELECT count(*) FROM submissions"); n != 1 {
		t.Errorf("%d submissions after deleting, want 1", n)
	}
}

func TestDeletingAnExampleHidesIt(t *testing.T) {
	t.Parallel()
	pool, store := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	alice, bob := newVisitor(t, pool), newVisitor(t, pool)
	example := addExample(t, pool)
	alicesListing, err := store.Add(t.Context(), alice, "manual", draft)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(t.Context(), alice, example); err != nil {
		t.Fatalf("alice deleting the example: %v", err)
	}
	listings, err := store.Visible(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(listings) != 1 || listings[0].ID != alicesListing {
		t.Errorf("alice sees %+v after deleting the example, want only their own listing", listings)
	}
	if _, err := store.Get(t.Context(), alice, example); !errors.Is(err, listing.ErrNotFound) {
		t.Errorf("alice getting the deleted example: %v, want ErrNotFound", err)
	}
	if err := store.Delete(t.Context(), alice, example); !errors.Is(err, listing.ErrNotFound) {
		t.Errorf("alice deleting it again: %v, want ErrNotFound", err)
	}
	// Only for alice: the example itself stays.
	if l, err := store.Get(t.Context(), bob, example); err != nil || !l.Example {
		t.Errorf("bob getting the example: %+v, %v", l, err)
	}

	for _, tt := range []struct {
		id   int64
		want bool
	}{{example, true}, {alicesListing, false}, {example + alicesListing + 1, false}} {
		if got, err := store.IsExample(t.Context(), tt.id); err != nil || got != tt.want {
			t.Errorf("IsExample(%d) = %v, %v; want %v", tt.id, got, err, tt.want)
		}
	}
}

func TestMeasurementsAreLoaded(t *testing.T) {
	t.Parallel()
	pool, store := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	alice := newVisitor(t, pool)
	id, err := store.Add(t.Context(), alice, "manual", draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE listings
		SET status = 'scored', location = ST_SetSRID(ST_MakePoint(11.0365, 50.9777), 4326)::geography,
		    geocoded_label = 'Anger 1, 99084 Erfurt', geocode_precision = 'house'
		WHERE id = $1;`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO listing_measurements (listing_id, criterion, outcome, value, place_name, distance_m)
		VALUES ($1, 'category:supermarket', 'measured', 4.5, 'Netto', 310),
		       ($1, 'category:gym', 'unreachable', NULL, NULL, NULL)`, id); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(t.Context(), alice, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Location == nil || got.Location.Lat != 50.9777 || got.Location.Lon != 11.0365 ||
		got.MatchedAddress != "Anger 1, 99084 Erfurt" || got.Precision != "house" {
		t.Errorf("located listing = %+v", got)
	}
	want := map[string]listing.Measurement{
		"category:supermarket": {Outcome: enrich.Measured, Value: 4.5, PlaceName: "Netto", DistanceM: 310},
		"category:gym":         {Outcome: enrich.Unreachable},
	}
	if len(got.Measurements) != len(want) {
		t.Errorf("measurements = %+v, want %+v", got.Measurements, want)
	}
	for k, w := range want {
		if got.Measurements[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got.Measurements[k], w)
		}
	}
}
