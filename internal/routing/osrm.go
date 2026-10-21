// Package routing asks the self-hosted OSRM routers how long it takes to walk
// or cycle from a listing to the places around it.
package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/MSJawad/apartment-finder/internal/region"
)

// maxTableSize is the most locations, origin included, one OSRM table
// request may hold; it is osrm-routed's default --max-table-size.
const maxTableSize = 100

// Point is a location.
type Point struct {
	Lat, Lon float64
}

// Leg is the trip from the origin to one destination.
type Leg struct {
	// Reachable is false when OSRM found no route, for example to a place
	// on an island or behind a fence the map doesn't show a way through.
	Reachable bool
	Minutes   float64
	DistanceM float64
}

// OSRM is a client for one osrm-routed server per travel mode. Each server
// loads the routing graph built with that mode's profile (foot or bicycle).
type OSRM struct {
	servers map[region.Mode]string
	http    *http.Client
}

// NewOSRM returns a client for the given servers, e.g.
// {region.Walk: "http://osrm-foot:5000", region.Bike: "http://osrm-bike:5000"}.
func NewOSRM(servers map[region.Mode]string, client *http.Client) *OSRM {
	return &OSRM{servers: servers, http: client}
}

// Table returns the trip by mode from origin to each destination, in the
// same order. The servers are our own, so unlike public services they need
// no cache or rate limit.
func (o *OSRM) Table(ctx context.Context, mode region.Mode, origin Point, destinations []Point) ([]Leg, error) {
	server, ok := o.servers[mode]
	if !ok {
		return nil, fmt.Errorf("routing: no OSRM server for mode %q", mode)
	}
	legs := make([]Leg, 0, len(destinations))
	for start := 0; start < len(destinations); start += maxTableSize - 1 {
		end := min(start+maxTableSize-1, len(destinations))
		batch, err := o.table(ctx, server, mode, origin, destinations[start:end])
		if err != nil {
			return nil, err
		}
		legs = append(legs, batch...)
	}
	return legs, nil
}

type tableResponse struct {
	Code      string       `json:"code"`
	Message   string       `json:"message"`
	Durations [][]*float64 `json:"durations"` // seconds; null when unreachable
	Distances [][]*float64 `json:"distances"` // metres; null when unreachable
}

func (o *OSRM) table(ctx context.Context, server string, mode region.Mode, origin Point, destinations []Point) ([]Leg, error) {
	coords := make([]string, 0, len(destinations)+1)
	for _, p := range append([]Point{origin}, destinations...) {
		// OSRM wants longitude first.
		coords = append(coords, strconv.FormatFloat(p.Lon, 'f', -1, 64)+","+strconv.FormatFloat(p.Lat, 'f', -1, 64))
	}
	// The profile segment is informational: each server only knows the
	// graph it loaded.
	url := fmt.Sprintf("%s/table/v1/%s/%s?sources=0&annotations=duration,distance",
		strings.TrimSuffix(server, "/"), mode, strings.Join(coords, ";"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build OSRM request: %w", err)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call OSRM (%s): %w", mode, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read OSRM response: %w", err)
	}

	var table tableResponse
	if err := json.Unmarshal(body, &table); err != nil {
		return nil, fmt.Errorf("OSRM (%s) returned HTTP %d: %.200s", mode, resp.StatusCode, body)
	}
	if resp.StatusCode != http.StatusOK || table.Code != "Ok" {
		return nil, fmt.Errorf("OSRM (%s) returned HTTP %d, %s: %s", mode, resp.StatusCode, table.Code, table.Message)
	}
	if len(table.Durations) != 1 || len(table.Durations[0]) != len(destinations)+1 ||
		len(table.Distances) != 1 || len(table.Distances[0]) != len(destinations)+1 {
		return nil, fmt.Errorf("OSRM (%s) returned a table of the wrong size", mode)
	}

	legs := make([]Leg, len(destinations))
	for i := range destinations {
		// Column 0 is the origin itself.
		duration, distance := table.Durations[0][i+1], table.Distances[0][i+1]
		if duration == nil || distance == nil {
			continue
		}
		legs[i] = Leg{Reachable: true, Minutes: *duration / 60, DistanceM: *distance}
	}
	return legs, nil
}
