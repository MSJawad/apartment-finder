// Package enrich measures listings and personal places: where they are,
// which everyday places are nearest a listing, and how long it takes to walk
// or cycle to them and to the visitor's own places. The results are stored
// as measurements; scoring itself happens when listings are read, so
// changing weights never needs a re-measure.
//
// Every job that calls the geocoder or the routers lives here.
package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/amenity"
	"github.com/MSJawad/apartment-finder/internal/geocode"
	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/routing"
)

// JobKind is the queue job kind that enriches one listing.
const JobKind = "enrich_listing"

// candidatesPerCategory is how many of the nearest places per category are
// routed to. The nearest in a straight line isn't always the nearest on foot,
// since rivers and railway lines force detours, so a few are compared.
const candidatesPerCategory = 5

// Payload is an enrich job's payload.
type Payload struct {
	ListingID int64 `json:"listing_id"`
}

// Outcomes of a measurement.
const (
	Measured    = "measured"
	NoneNearby  = "none_nearby"
	Unreachable = "unreachable"
)

// Geocoder places addresses; *geocode.Photon is one.
type Geocoder interface {
	Geocode(ctx context.Context, address string) (geocode.Result, error)
}

// Router calculates travel times; *routing.OSRM is one.
type Router interface {
	Table(ctx context.Context, mode region.Mode, origin routing.Point, destinations []routing.Point) ([]routing.Leg, error)
	TableTo(ctx context.Context, mode region.Mode, origins []routing.Point, destination routing.Point) ([]routing.Leg, error)
}

// Enricher handles enrich jobs.
type Enricher struct {
	Pool     *pgxpool.Pool
	Region   *region.Region
	Geocoder Geocoder
	Router   Router
}

// Handle is the queue.Handler for JobKind. On the job's last attempt, a
// failure marks the listing as failed, so the person who added it isn't left
// looking at "pending" forever.
func (e *Enricher) Handle(ctx context.Context, job queue.Job) error {
	var p Payload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode %s payload: %w", JobKind, err)
	}
	err := e.enrich(ctx, p.ListingID)
	if err != nil && job.Attempts >= job.MaxAttempts {
		if _, markErr := e.Pool.Exec(ctx,
			"UPDATE listings SET status = 'failed', status_detail = $2 WHERE id = $1",
			p.ListingID, "We couldn't measure this listing. Please try adding it again later."); markErr != nil {
			return errors.Join(err, markErr)
		}
	}
	return err
}

// measurement is what one criterion measured.
type measurement struct {
	criterion string
	outcome   string
	value     *float64
	placeName string
	distanceM *float64
}

func (e *Enricher) enrich(ctx context.Context, listingID int64) error {
	var address string
	var rentCents int
	var size, rooms float64
	err := e.Pool.QueryRow(ctx,
		"SELECT address, warm_rent_cents, size_m2, rooms FROM listings WHERE id = $1",
		listingID).Scan(&address, &rentCents, &size, &rooms)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted since the job was queued; nothing to do
	}
	if err != nil {
		return fmt.Errorf("load listing %d: %w", listingID, err)
	}

	place, err := e.Geocoder.Geocode(ctx, address)
	if err != nil {
		return err
	}
	if !place.Found {
		_, err := e.Pool.Exec(ctx,
			"UPDATE listings SET status = 'not_found', status_detail = $2 WHERE id = $1",
			listingID, fmt.Sprintf("We couldn't find this address in %s.", e.Region.Name))
		return err
	}

	rentPerM2, sizeM2, roomCount := float64(rentCents)/100/size, size, rooms
	measurements := []measurement{
		{criterion: "rent_per_m2", outcome: Measured, value: &rentPerM2},
		{criterion: "size_m2", outcome: Measured, value: &sizeM2},
		{criterion: "rooms", outcome: Measured, value: &roomCount},
	}
	travel, err := e.travelTimes(ctx, routing.Point{Lat: place.Lat, Lon: place.Lon})
	if err != nil {
		return err
	}
	measurements = append(measurements, travel...)
	if err := e.save(ctx, listingID, place, measurements); err != nil {
		return err
	}
	return e.measurePlacesFrom(ctx, listingID)
}

// travelTimes measures each category: the fastest trip to one of its nearest
// places, by the category's travel mode.
func (e *Enricher) travelTimes(ctx context.Context, origin routing.Point) ([]measurement, error) {
	type pending struct {
		category   region.Category
		candidates []amenity.Candidate
	}
	byMode := map[region.Mode][]pending{}
	var measurements []measurement
	for _, c := range e.Region.Categories {
		candidates, err := amenity.Nearest(ctx, e.Pool, c.Key, origin.Lat, origin.Lon, c.SearchRadius(), candidatesPerCategory)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			measurements = append(measurements, measurement{criterion: region.CategoryCriterion(c.Key), outcome: NoneNearby})
			continue
		}
		byMode[c.Mode] = append(byMode[c.Mode], pending{category: c, candidates: candidates})
	}

	// One routing request per mode covers every candidate of every category.
	for _, mode := range []region.Mode{region.Walk, region.Bike} {
		var destinations []routing.Point
		for _, p := range byMode[mode] {
			for _, c := range p.candidates {
				destinations = append(destinations, routing.Point{Lat: c.Lat, Lon: c.Lon})
			}
		}
		if len(destinations) == 0 {
			continue
		}
		legs, err := e.Router.Table(ctx, mode, origin, destinations)
		if err != nil {
			return nil, err
		}
		next := 0
		for _, p := range byMode[mode] {
			m := measurement{criterion: region.CategoryCriterion(p.category.Key), outcome: Unreachable}
			for _, c := range p.candidates {
				leg := legs[next]
				next++
				if leg.Reachable && (m.value == nil || leg.Minutes < *m.value) {
					minutes, distance := leg.Minutes, leg.DistanceM
					m.outcome, m.value, m.placeName, m.distanceM = Measured, &minutes, c.Name, &distance
				}
			}
			measurements = append(measurements, m)
		}
	}
	return measurements, nil
}

// save stores the geocoded location and the measurements, replacing any from
// an earlier run, and marks the listing scored, all in one transaction.
func (e *Enricher) save(ctx context.Context, listingID int64, place geocode.Result, measurements []measurement) error {
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE listings
			SET location = ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography,
			    geocoded_label = $4, geocode_precision = $5,
			    status = 'scored', status_detail = NULL, scored_at = now()
			WHERE id = $1`,
			listingID, place.Lon, place.Lat, place.Label, place.Precision); err != nil {
			return fmt.Errorf("save listing %d: %w", listingID, err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM listing_measurements WHERE listing_id = $1", listingID); err != nil {
			return fmt.Errorf("clear measurements of listing %d: %w", listingID, err)
		}
		rows := make([][]any, len(measurements))
		for i, m := range measurements {
			rows[i] = []any{listingID, m.criterion, m.outcome, m.value, nullIfEmpty(m.placeName), m.distanceM}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"listing_measurements"},
			[]string{"listing_id", "criterion", "outcome", "value", "place_name", "distance_m"},
			pgx.CopyFromRows(rows)); err != nil {
			return fmt.Errorf("save measurements of listing %d: %w", listingID, err)
		}
		return nil
	})
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
