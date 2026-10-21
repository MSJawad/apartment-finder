package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/routing"
)

// PlaceJobKind is the queue job kind that locates a visitor's personal place
// and measures the trip to it from every listing the visitor sees.
const PlaceJobKind = "locate_place"

// PlacePayload is a locate-place job's payload.
type PlacePayload struct {
	PlaceID int64 `json:"place_id"`
}

// HandlePlace is the queue.Handler for PlaceJobKind. Like Handle, it marks
// the place failed on the job's last attempt.
func (e *Enricher) HandlePlace(ctx context.Context, job queue.Job) error {
	var p PlacePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode %s payload: %w", PlaceJobKind, err)
	}
	err := e.locatePlace(ctx, p.PlaceID)
	if err != nil && job.Attempts >= job.MaxAttempts {
		if _, markErr := e.Pool.Exec(ctx,
			"UPDATE places SET status = 'failed', status_detail = $2 WHERE id = $1",
			p.PlaceID, "We couldn't measure trips to this place. Please try adding it again later."); markErr != nil {
			return errors.Join(err, markErr)
		}
	}
	return err
}

func (e *Enricher) locatePlace(ctx context.Context, placeID int64) error {
	var address string
	err := e.Pool.QueryRow(ctx, "SELECT address FROM places WHERE id = $1", placeID).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted since the job was queued
	}
	if err != nil {
		return fmt.Errorf("load place %d: %w", placeID, err)
	}

	found, err := e.Geocoder.Geocode(ctx, address)
	if err != nil {
		return err
	}
	if !found.Found {
		_, err := e.Pool.Exec(ctx,
			"UPDATE places SET status = 'not_found', status_detail = $2 WHERE id = $1",
			placeID, fmt.Sprintf("We couldn't find this address in %s.", e.Region.Name))
		return err
	}
	// The location is committed before listings are read, and the enrich
	// job does the same the other way round. So when a listing and a place
	// are added at the same moment, at least one of the two jobs sees the
	// other's location and measures the trip.
	if _, err := e.Pool.Exec(ctx, `
		UPDATE places
		SET location = ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography,
		    geocoded_label = $4, geocode_precision = $5, status = 'located', status_detail = NULL
		WHERE id = $1`,
		placeID, found.Lon, found.Lat, found.Label, found.Precision); err != nil {
		return fmt.Errorf("save place %d: %w", placeID, err)
	}
	return e.measureToPlace(ctx, placeID)
}

// measureToPlace measures the trip to a place from every located listing its
// visitor sees: their own and the examples.
func (e *Enricher) measureToPlace(ctx context.Context, placeID int64) error {
	var mode region.Mode
	var dest routing.Point
	var owner uuid.UUID
	err := e.Pool.QueryRow(ctx, `
		SELECT mode, ST_Y(location::geometry), ST_X(location::geometry), visitor_id
		FROM places WHERE id = $1 AND location IS NOT NULL`, placeID,
	).Scan(&mode, &dest.Lat, &dest.Lon, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load place %d: %w", placeID, err)
	}

	rows, err := e.Pool.Query(ctx, `
		SELECT id, ST_Y(location::geometry), ST_X(location::geometry)
		FROM listings
		WHERE location IS NOT NULL AND (visitor_id IS NULL OR visitor_id = $1)`, owner)
	if err != nil {
		return fmt.Errorf("load listings for place %d: %w", placeID, err)
	}
	var listingIDs []int64
	var origins []routing.Point
	var id int64
	var p routing.Point
	if _, err := pgx.ForEachRow(rows, []any{&id, &p.Lat, &p.Lon}, func() error {
		listingIDs, origins = append(listingIDs, id), append(origins, p)
		return nil
	}); err != nil {
		return fmt.Errorf("load listings for place %d: %w", placeID, err)
	}
	if len(origins) == 0 {
		return nil
	}

	legs, err := e.Router.TableTo(ctx, mode, origins, dest)
	if err != nil {
		return err
	}
	trips := make([]trip, len(legs))
	for i, leg := range legs {
		trips[i] = trip{listingID: listingIDs[i], placeID: placeID, leg: leg}
	}
	return e.saveTrips(ctx, trips)
}

// measurePlacesFrom measures the trip from a listing to every located place
// that sees it: its visitor's places, or for an example listing, everyone's.
func (e *Enricher) measurePlacesFrom(ctx context.Context, listingID int64) error {
	var origin routing.Point
	var owner *uuid.UUID
	err := e.Pool.QueryRow(ctx, `
		SELECT ST_Y(location::geometry), ST_X(location::geometry), visitor_id
		FROM listings WHERE id = $1 AND location IS NOT NULL`, listingID,
	).Scan(&origin.Lat, &origin.Lon, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load listing %d: %w", listingID, err)
	}

	rows, err := e.Pool.Query(ctx, `
		SELECT id, mode, ST_Y(location::geometry), ST_X(location::geometry)
		FROM places
		WHERE location IS NOT NULL AND ($1::uuid IS NULL OR visitor_id = $1)`, owner)
	if err != nil {
		return fmt.Errorf("load places for listing %d: %w", listingID, err)
	}
	placeIDs := map[region.Mode][]int64{}
	destinations := map[region.Mode][]routing.Point{}
	var id int64
	var mode region.Mode
	var p routing.Point
	if _, err := pgx.ForEachRow(rows, []any{&id, &mode, &p.Lat, &p.Lon}, func() error {
		placeIDs[mode], destinations[mode] = append(placeIDs[mode], id), append(destinations[mode], p)
		return nil
	}); err != nil {
		return fmt.Errorf("load places for listing %d: %w", listingID, err)
	}

	var trips []trip
	for _, mode := range []region.Mode{region.Walk, region.Bike} {
		if len(destinations[mode]) == 0 {
			continue
		}
		legs, err := e.Router.Table(ctx, mode, origin, destinations[mode])
		if err != nil {
			return err
		}
		for i, leg := range legs {
			trips = append(trips, trip{listingID: listingID, placeID: placeIDs[mode][i], leg: leg})
		}
	}
	return e.saveTrips(ctx, trips)
}

// trip is a measured trip from a listing to a place.
type trip struct {
	listingID, placeID int64
	leg                routing.Leg
}

// saveTrips stores trips, replacing earlier measurements of the same pairs.
// The two jobs above can measure the same pair at the same time, so this
// is an upsert rather than delete-then-insert, which would collide.
func (e *Enricher) saveTrips(ctx context.Context, trips []trip) error {
	if len(trips) == 0 {
		return nil
	}
	listingIDs := make([]int64, len(trips))
	placeIDs := make([]int64, len(trips))
	minutes := make([]*float64, len(trips))
	distances := make([]*float64, len(trips))
	for i, t := range trips {
		listingIDs[i], placeIDs[i] = t.listingID, t.placeID
		if t.leg.Reachable {
			m, d := t.leg.Minutes, t.leg.DistanceM
			minutes[i], distances[i] = &m, &d
		}
	}
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO place_measurements (listing_id, place_id, minutes, distance_m)
		SELECT * FROM unnest($1::bigint[], $2::bigint[], $3::double precision[], $4::double precision[])
		ON CONFLICT (listing_id, place_id)
		DO UPDATE SET minutes = excluded.minutes, distance_m = excluded.distance_m`,
		listingIDs, placeIDs, minutes, distances); err != nil {
		return fmt.Errorf("save trips: %w", err)
	}
	return nil
}
