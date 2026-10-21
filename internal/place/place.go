// Package place holds visitors' personal places, such as work or a
// university. Each place is an extra scoring criterion for its visitor: the
// trip to it from each listing, by walking or cycling.
package place

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/enrich"
	"github.com/MSJawad/apartment-finder/internal/input"
	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/routing"
	"github.com/MSJawad/apartment-finder/internal/scoring"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

// How places score. A trip of BestMinutes or less scores 1 and one of
// WorstMinutes or more scores 0. The default weight matches the region's
// most important everyday places, since a commute matters at least as much.
const (
	BestMinutes   = 10
	WorstMinutes  = 45
	DefaultWeight = 3
)

// MaxPerVisitor is how many places one visitor can have.
const MaxPerVisitor = 10

var (
	// ErrNotFound means the visitor has no place with that id.
	ErrNotFound = errors.New("place not found")
	// ErrTooMany means the visitor already has MaxPerVisitor places.
	ErrTooMany = fmt.Errorf("a visitor can have at most %d places", MaxPerVisitor)
)

// Draft is a submitted place that passed validation.
type Draft struct {
	Name    string
	Address string
	Mode    region.Mode
}

// Parse reads the add-place form, reporting every invalid field at once.
func Parse(data []byte) (Draft, error) {
	var in struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Mode    string `json:"mode"`
	}
	if err := input.Decode(data, &in); err != nil {
		return Draft{}, err
	}
	d := Draft{Name: input.Text(in.Name), Address: input.Text(in.Address), Mode: region.Mode(in.Mode)}
	errs := input.FieldErrors{}
	if n := utf8.RuneCountInString(d.Name); n < 1 || n > 40 {
		errs["name"] = "Name the place, e.g. “Work”, in up to 40 characters."
	}
	if n := utf8.RuneCountInString(d.Address); n < 3 || n > 200 {
		errs["address"] = "Enter the address, e.g. “Nordhäuser Straße 63, Erfurt”."
	}
	if d.Mode != region.Walk && d.Mode != region.Bike {
		errs["mode"] = "Choose “walk” or “bike”."
	}
	if len(errs) > 0 {
		return Draft{}, errs
	}
	return d, nil
}

// Place is a stored place.
type Place struct {
	ID      int64
	Name    string
	Address string
	Mode    region.Mode
	// Status is pending, located, not_found or failed; StatusDetail
	// explains the last two.
	Status       string
	StatusDetail string
	// MatchedAddress and Precision say what the geocoder matched, and
	// Location where; all are empty until the place is located.
	MatchedAddress string
	Precision      string
	Location       *routing.Point
	CreatedAt      time.Time
}

// CriterionKey is the scoring key for the trip to a place.
func CriterionKey(id int64) string {
	return "place:" + strconv.FormatInt(id, 10)
}

// Criteria returns a scoring criterion for each place.
func Criteria(places []Place) []scoring.Criterion {
	criteria := make([]scoring.Criterion, len(places))
	for i, p := range places {
		criteria[i] = scoring.Criterion{
			Key: CriterionKey(p.ID), Label: p.Name, Unit: p.Mode.Unit(),
			Best: BestMinutes, Worst: WorstMinutes, Weight: DefaultWeight,
		}
	}
	return criteria
}

// Store reads and writes places.
type Store struct {
	Pool   *pgxpool.Pool
	Limits visitor.Limits
}

// Add stores d as one of owner's places and queues the job that locates it.
// A place costs a geocoding request like a listing, so it takes a slot under
// the same limits.
func (s *Store) Add(ctx context.Context, owner uuid.UUID, d Draft) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Reserve takes the lock that also makes this count exact.
		if err := s.Limits.Reserve(ctx, tx, owner); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM places WHERE visitor_id = $1", owner).Scan(&count); err != nil {
			return fmt.Errorf("count places: %w", err)
		}
		if count >= MaxPerVisitor {
			return ErrTooMany
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO places (visitor_id, name, address, mode) VALUES ($1, $2, $3, $4) RETURNING id`,
			owner, d.Name, d.Address, d.Mode,
		).Scan(&id); err != nil {
			return fmt.Errorf("store place: %w", err)
		}
		return queue.Enqueue(ctx, tx, enrich.PlaceJobKind, enrich.PlacePayload{PlaceID: id})
	})
	return id, err
}

// List returns owner's places, oldest first, so they keep their order in
// the page. uuid.Nil() has none.
func (s *Store) List(ctx context.Context, owner uuid.UUID) ([]Place, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, address, mode, status, coalesce(status_detail, ''),
		       coalesce(geocoded_label, ''), coalesce(geocode_precision, ''),
		       ST_Y(location::geometry), ST_X(location::geometry), created_at
		FROM places
		WHERE visitor_id = $1
		ORDER BY created_at, id`, owner)
	if err != nil {
		return nil, fmt.Errorf("load places: %w", err)
	}
	places, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Place, error) {
		var p Place
		var lat, lon *float64
		err := row.Scan(&p.ID, &p.Name, &p.Address, &p.Mode, &p.Status, &p.StatusDetail,
			&p.MatchedAddress, &p.Precision, &lat, &lon, &p.CreatedAt)
		if lat != nil && lon != nil {
			p.Location = &routing.Point{Lat: *lat, Lon: *lon}
		}
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("load places: %w", err)
	}
	return places, nil
}

// Delete deletes one of owner's places with its measured trips.
func (s *Store) Delete(ctx context.Context, owner uuid.UUID, id int64) error {
	tag, err := s.Pool.Exec(ctx, "DELETE FROM places WHERE id = $1 AND visitor_id = $2", id, owner)
	if err != nil {
		return fmt.Errorf("delete place %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
