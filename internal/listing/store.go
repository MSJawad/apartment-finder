package listing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/enrich"
	"github.com/MSJawad/apartment-finder/internal/place"
	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

// ErrNotFound means no listing with that id is visible to the visitor.
var ErrNotFound = errors.New("listing not found")

// Listing is a stored listing with what was measured about it.
type Listing struct {
	ID int64
	// Example listings have no owner: every visitor sees them.
	Example       bool
	Source        string
	Address       string
	WarmRentCents int
	SizeM2        float64
	Rooms         float64
	SourceURL     string
	// Status is pending, scored, not_found or failed; StatusDetail explains
	// the last two.
	Status       string
	StatusDetail string
	// MatchedAddress and Precision say what the geocoder matched, and
	// Location where; all are empty until the listing is located.
	MatchedAddress string
	Precision      string
	Location       *Point
	CreatedAt      time.Time
	// Measurements are keyed by scoring criterion.
	Measurements map[string]Measurement
}

// Point is a location.
type Point struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Measurement is what was measured for one criterion; see package enrich.
type Measurement struct {
	Outcome   string // enrich.Measured, NoneNearby or Unreachable
	Value     float64
	PlaceName string // for travel times: the place the time is to
	DistanceM float64
}

// Store reads and writes listings.
type Store struct {
	Pool   *pgxpool.Pool
	Limits visitor.Limits
}

// Add stores d as owner's listing and queues the job that measures it. It
// first takes a slot under the limits, and does all three in one
// transaction: no listing without its job, and no slot used up by a listing
// that wasn't stored.
func (s *Store) Add(ctx context.Context, owner uuid.UUID, source string, d Draft) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.Limits.Reserve(ctx, tx, owner); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO listings (visitor_id, source, address, warm_rent_cents, size_m2, rooms, source_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			owner, source, d.Address, d.WarmRentCents, d.SizeM2, d.Rooms, nullIfEmpty(d.SourceURL),
		).Scan(&id); err != nil {
			return fmt.Errorf("store listing: %w", err)
		}
		return queue.Enqueue(ctx, tx, enrich.JobKind, enrich.Payload{ListingID: id})
	})
	return id, err
}

// Visible returns viewer's own listings and the examples they haven't
// deleted, newest first. uuid.Nil() as viewer means a visitor without
// listings of their own.
func (s *Store) Visible(ctx context.Context, viewer uuid.UUID) ([]Listing, error) {
	return s.query(ctx, viewer, "")
}

// Get returns one listing if viewer may see it, and ErrNotFound otherwise.
// Another visitor's listing is "not found" rather than "forbidden", so ids
// reveal nothing about other visitors.
func (s *Store) Get(ctx context.Context, viewer uuid.UUID, id int64) (Listing, error) {
	listings, err := s.query(ctx, viewer, "AND id = $2", id)
	if err != nil {
		return Listing{}, err
	}
	if len(listings) == 0 {
		return Listing{}, ErrNotFound
	}
	return listings[0], nil
}

// Delete deletes one of owner's listings with its measurements. An example
// belongs to no one, so deleting it hides it from owner instead, and other
// visitors still see it.
func (s *Store) Delete(ctx context.Context, owner uuid.UUID, id int64) error {
	tag, err := s.Pool.Exec(ctx, "DELETE FROM listings WHERE id = $1 AND visitor_id = $2", id, owner)
	if err != nil {
		return fmt.Errorf("delete listing %d: %w", id, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	tag, err = s.Pool.Exec(ctx, `
		INSERT INTO hidden_examples (visitor_id, listing_id)
		SELECT $2, id FROM listings WHERE id = $1 AND visitor_id IS NULL
		ON CONFLICT DO NOTHING`, id, owner)
	if err != nil {
		return fmt.Errorf("hide example %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IsExample reports whether id is an example listing.
func (s *Store) IsExample(ctx context.Context, id int64) (bool, error) {
	var example bool
	err := s.Pool.QueryRow(ctx, "SELECT visitor_id IS NULL FROM listings WHERE id = $1", id).Scan(&example)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load listing %d: %w", id, err)
	}
	return example, nil
}

// query loads the listings viewer may see that match filter, then their
// measurements, including the trips to viewer's own places. In filter, $1 is
// the viewer and args start at $2.
func (s *Store) query(ctx context.Context, viewer uuid.UUID, filter string, args ...any) ([]Listing, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, visitor_id IS NULL, source, address, warm_rent_cents, size_m2, rooms,
		       coalesce(source_url, ''), status, coalesce(status_detail, ''),
		       coalesce(geocoded_label, ''), coalesce(geocode_precision, ''),
		       ST_Y(location::geometry), ST_X(location::geometry), created_at
		FROM listings
		WHERE (visitor_id = $1 OR visitor_id IS NULL AND NOT EXISTS (
		           SELECT 1 FROM hidden_examples h WHERE h.listing_id = listings.id AND h.visitor_id = $1)) `+filter+`
		ORDER BY created_at DESC, id DESC`, append([]any{viewer}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("load listings: %w", err)
	}
	listings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Listing, error) {
		var l Listing
		var lat, lon *float64
		err := row.Scan(&l.ID, &l.Example, &l.Source, &l.Address, &l.WarmRentCents, &l.SizeM2, &l.Rooms,
			&l.SourceURL, &l.Status, &l.StatusDetail, &l.MatchedAddress, &l.Precision, &lat, &lon, &l.CreatedAt)
		if lat != nil && lon != nil {
			l.Location = &Point{Lat: *lat, Lon: *lon}
		}
		l.Measurements = map[string]Measurement{}
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("load listings: %w", err)
	}
	if len(listings) == 0 {
		return listings, nil
	}

	byID := make(map[int64]*Listing, len(listings))
	ids := make([]int64, len(listings))
	for i := range listings {
		byID[listings[i].ID] = &listings[i]
		ids[i] = listings[i].ID
	}
	rows, err = s.Pool.Query(ctx, `
		SELECT listing_id, criterion, outcome, coalesce(value, 0), coalesce(place_name, ''), coalesce(distance_m, 0)
		FROM listing_measurements
		WHERE listing_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("load measurements: %w", err)
	}
	var listingID int64
	var criterion string
	var m Measurement
	_, err = pgx.ForEachRow(rows, []any{&listingID, &criterion, &m.Outcome, &m.Value, &m.PlaceName, &m.DistanceM}, func() error {
		byID[listingID].Measurements[criterion] = m
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load measurements: %w", err)
	}

	// Trips to places are keyed like any other criterion. Only the viewer's
	// places count: an example listing has trips to every visitor's places.
	rows, err = s.Pool.Query(ctx, `
		SELECT pm.listing_id, pm.place_id, pm.minutes, pm.distance_m
		FROM place_measurements pm
		JOIN places p ON p.id = pm.place_id
		WHERE pm.listing_id = ANY($1) AND p.visitor_id = $2`, ids, viewer)
	if err != nil {
		return nil, fmt.Errorf("load trips to places: %w", err)
	}
	var placeID int64
	var minutes, distance *float64
	_, err = pgx.ForEachRow(rows, []any{&listingID, &placeID, &minutes, &distance}, func() error {
		m := Measurement{Outcome: enrich.Unreachable}
		if minutes != nil && distance != nil {
			m = Measurement{Outcome: enrich.Measured, Value: *minutes, DistanceM: *distance}
		}
		byID[listingID].Measurements[place.CriterionKey(placeID)] = m
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load trips to places: %w", err)
	}
	return listings, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
