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
	return o.batched(ctx, mode, origin, destinations, false)
}

// TableTo returns the trip by mode from each origin to destination, in the
// same order. With it, trips to a personal place are always measured from
// the listing, whether the listing or the place was added last; on a bike,
// one-way streets can make the two directions differ.
func (o *OSRM) TableTo(ctx context.Context, mode region.Mode, origins []Point, destination Point) ([]Leg, error) {
	return o.batched(ctx, mode, destination, origins, true)
}

// batched routes between one fixed point and each of others, splitting the
// work into requests osrm-routed accepts. toFixed sets the direction.
func (o *OSRM) batched(ctx context.Context, mode region.Mode, fixed Point, others []Point, toFixed bool) ([]Leg, error) {
	server, ok := o.servers[mode]
	if !ok {
		return nil, fmt.Errorf("routing: no OSRM server for mode %q", mode)
	}
	legs := make([]Leg, 0, len(others))
	for start := 0; start < len(others); start += maxTableSize - 1 {
		end := min(start+maxTableSize-1, len(others))
		batch, err := o.table(ctx, server, mode, fixed, others[start:end], toFixed)
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

// table makes one request. The fixed point is location 0 and others follow.
// From the fixed point, the answer is one row of len(others)+1 columns,
// the first being the fixed point itself. To it, it is len(others) rows of
// one column.
func (o *OSRM) table(ctx context.Context, server string, mode region.Mode, fixed Point, others []Point, toFixed bool) ([]Leg, error) {
	coords := make([]string, 0, len(others)+1)
	for _, p := range append([]Point{fixed}, others...) {
		// OSRM wants longitude first.
		coords = append(coords, strconv.FormatFloat(p.Lon, 'f', -1, 64)+","+strconv.FormatFloat(p.Lat, 'f', -1, 64))
	}
	direction := "sources=0"
	if toFixed {
		sources := make([]string, len(others))
		for i := range others {
			sources[i] = strconv.Itoa(i + 1)
		}
		direction = "sources=" + strings.Join(sources, ";") + "&destinations=0"
	}
	// The profile segment is informational: each server only knows the
	// graph it loaded.
	url := fmt.Sprintf("%s/table/v1/%s/%s?%s&annotations=duration,distance",
		strings.TrimSuffix(server, "/"), mode, strings.Join(coords, ";"), direction)
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

	// cell returns the duration and distance of the trip between the fixed
	// point and others[i], in the requested direction.
	cell := func(m [][]*float64, i int) *float64 { return m[0][i+1] }
	rows, cols := 1, len(others)+1
	if toFixed {
		cell = func(m [][]*float64, i int) *float64 { return m[i][0] }
		rows, cols = len(others), 1
	}
	if !hasShape(table.Durations, rows, cols) || !hasShape(table.Distances, rows, cols) {
		return nil, fmt.Errorf("OSRM (%s) returned a table of the wrong size", mode)
	}

	legs := make([]Leg, len(others))
	for i := range others {
		duration, distance := cell(table.Durations, i), cell(table.Distances, i)
		if duration == nil || distance == nil {
			continue
		}
		legs[i] = Leg{Reachable: true, Minutes: *duration / 60, DistanceM: *distance}
	}
	return legs, nil
}

func hasShape(m [][]*float64, rows, cols int) bool {
	if len(m) != rows {
		return false
	}
	for _, row := range m {
		if len(row) != cols {
			return false
		}
	}
	return true
}
