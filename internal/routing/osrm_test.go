package routing

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MSJawad/apartment-finder/internal/region"
)

var anger = Point{Lat: 50.9777, Lon: 11.0365}

// fakeOSRM answers table requests like osrm-routed. A trip between
// locations i and j takes (i+j) minutes and (i+j)×100 m, so with the fixed
// point at 0 each other location takes its index in minutes, in either
// direction. Locations with longitude 0 are unreachable. It records the
// requests.
type fakeOSRM struct {
	*httptest.Server
	requests  atomic.Int32
	lastPath  atomic.Value
	lastQuery atomic.Value
}

func newFakeOSRM(t *testing.T) *fakeOSRM {
	t.Helper()
	f := &fakeOSRM{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.lastPath.Store(r.URL.Path)
		f.lastQuery.Store(r.URL.RawQuery)
		// OSRM separates list items with ";", which url.ParseQuery rejects,
		// so the query is split by hand.
		q := map[string]string{}
		for _, pair := range strings.Split(r.URL.RawQuery, "&") {
			k, v, _ := strings.Cut(pair, "=")
			q[k] = v
		}
		if q["annotations"] != "duration,distance" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		coords := strings.Split(strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "/"):], "/"), ";")
		if len(coords) > maxTableSize {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"TooBig","message":"Too many table coordinates"}`))
			return
		}
		indexes := func(param string) []int {
			var out []int
			if q[param] == "" {
				for i := range coords {
					out = append(out, i)
				}
				return out
			}
			for _, s := range strings.Split(q[param], ";") {
				i, err := strconv.Atoi(s)
				if err != nil {
					t.Errorf("bad %s %q", param, q[param])
				}
				out = append(out, i)
			}
			return out
		}
		var durations, distances [][]*float64
		for _, src := range indexes("sources") {
			var durationRow, distanceRow []*float64
			for _, dst := range indexes("destinations") {
				if strings.HasPrefix(coords[src], "0,") || strings.HasPrefix(coords[dst], "0,") {
					durationRow, distanceRow = append(durationRow, nil), append(distanceRow, nil)
					continue
				}
				d, m := float64((src+dst)*60), float64((src+dst)*100)
				durationRow, distanceRow = append(durationRow, &d), append(distanceRow, &m)
			}
			durations, distances = append(durations, durationRow), append(distances, distanceRow)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "durations": durations, "distances": distances})
	}))
	t.Cleanup(f.Close)
	return f
}

func newClient(walkURL string) *OSRM {
	return NewOSRM(map[region.Mode]string{region.Walk: walkURL}, &http.Client{Timeout: 5 * time.Second})
}

func TestTable(t *testing.T) {
	osrm := newFakeOSRM(t)
	client := newClient(osrm.URL + "/")

	destinations := []Point{
		{Lat: 50.9786, Lon: 11.0365},
		{Lat: 50.9, Lon: 0}, // unreachable
		{Lat: 50.9725, Lon: 11.0385},
	}
	legs, err := client.Table(t.Context(), region.Walk, anger, destinations)
	if err != nil {
		t.Fatal(err)
	}
	want := []Leg{
		{Reachable: true, Minutes: 1, DistanceM: 100},
		{Reachable: false},
		{Reachable: true, Minutes: 3, DistanceM: 300},
	}
	if len(legs) != len(want) {
		t.Fatalf("got %d legs, want %d", len(legs), len(want))
	}
	for i := range want {
		if legs[i].Reachable != want[i].Reachable || math.Abs(legs[i].Minutes-want[i].Minutes) > 1e-9 ||
			math.Abs(legs[i].DistanceM-want[i].DistanceM) > 1e-9 {
			t.Errorf("leg %d = %+v, want %+v", i, legs[i], want[i])
		}
	}
	wantPath := "/table/v1/walk/11.0365,50.9777;11.0365,50.9786;0,50.9;11.0385,50.9725"
	if got := osrm.lastPath.Load(); got != wantPath {
		t.Errorf("path = %s, want %s (longitude first, origin first)", got, wantPath)
	}
	if got := osrm.lastQuery.Load(); got != "sources=0&annotations=duration,distance" {
		t.Errorf("query = %s, want only the origin as source", got)
	}
}

func TestTableTo(t *testing.T) {
	osrm := newFakeOSRM(t)
	client := newClient(osrm.URL)

	origins := []Point{
		{Lat: 50.9786, Lon: 11.0365},
		{Lat: 50.9, Lon: 0}, // unreachable
		{Lat: 50.9725, Lon: 11.0385},
	}
	legs, err := client.TableTo(t.Context(), region.Walk, origins, anger)
	if err != nil {
		t.Fatal(err)
	}
	want := []Leg{
		{Reachable: true, Minutes: 1, DistanceM: 100},
		{Reachable: false},
		{Reachable: true, Minutes: 3, DistanceM: 300},
	}
	if len(legs) != len(want) {
		t.Fatalf("got %d legs, want %d", len(legs), len(want))
	}
	for i := range want {
		if legs[i] != want[i] {
			t.Errorf("leg %d = %+v, want %+v", i, legs[i], want[i])
		}
	}
	// The destination is location 0, and every origin is a source.
	wantPath := "/table/v1/walk/11.0365,50.9777;11.0365,50.9786;0,50.9;11.0385,50.9725"
	if got := osrm.lastPath.Load(); got != wantPath {
		t.Errorf("path = %s, want %s", got, wantPath)
	}
	if got := osrm.lastQuery.Load(); got != "sources=1;2;3&destinations=0&annotations=duration,distance" {
		t.Errorf("query = %s, want the origins as sources and the destination alone", got)
	}
}

func TestTableToSplitsLargeRequests(t *testing.T) {
	osrm := newFakeOSRM(t)
	origins := make([]Point, 150)
	for i := range origins {
		origins[i] = Point{Lat: 50.9, Lon: 11 + float64(i)/1000}
	}
	legs, err := newClient(osrm.URL).TableTo(t.Context(), region.Walk, origins, anger)
	if err != nil {
		t.Fatal(err)
	}
	if n := osrm.requests.Load(); n != 2 || len(legs) != 150 {
		t.Fatalf("%d requests and %d legs, want 2 and 150", n, len(legs))
	}
	if legs[98].Minutes != 99 || legs[99].Minutes != 1 {
		t.Errorf("legs 98 and 99 took %v and %v min, want 99 and 1: results out of order", legs[98].Minutes, legs[99].Minutes)
	}
}

func TestTableSplitsLargeRequests(t *testing.T) {
	osrm := newFakeOSRM(t)
	client := newClient(osrm.URL)

	destinations := make([]Point, 150)
	for i := range destinations {
		destinations[i] = Point{Lat: 50.9, Lon: 11 + float64(i)/1000}
	}
	legs, err := client.Table(t.Context(), region.Walk, anger, destinations)
	if err != nil {
		t.Fatal(err)
	}
	if n := osrm.requests.Load(); n != 2 {
		t.Errorf("%d requests, want 2 (99 + 51 destinations)", n)
	}
	if len(legs) != 150 {
		t.Fatalf("got %d legs, want 150", len(legs))
	}
	// Each batch restarts its own index, so destination 99 is the first of
	// the second batch: 1 minute.
	if legs[98].Minutes != 99 || legs[99].Minutes != 1 {
		t.Errorf("legs 98 and 99 took %v and %v min, want 99 and 1: results out of order", legs[98].Minutes, legs[99].Minutes)
	}
}

func TestTableErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		mode    region.Mode
		wantErr string
	}{
		{"no server for the mode", http.StatusOK, "", region.Bike, `no OSRM server for mode "bike"`},
		{"OSRM error code", http.StatusBadRequest, `{"code":"InvalidQuery","message":"Query string malformed"}`, region.Walk, "InvalidQuery"},
		{"not JSON", http.StatusBadGateway, "<html>bad gateway</html>", region.Walk, "HTTP 502"},
		{"wrong size", http.StatusOK, `{"code":"Ok","durations":[[0]],"distances":[[0]]}`, region.Walk, "wrong size"},
		{"ragged rows", http.StatusOK, `{"code":"Ok","durations":[[0,1]],"distances":[[0]]}`, region.Walk, "wrong size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			_, err := newClient(server.URL).Table(t.Context(), tt.mode, anger, []Point{{Lat: 50.98, Lon: 11.03}})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Table() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestTableUnreachableServer(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close() // nothing listens there any more
	if _, err := newClient(server.URL).Table(t.Context(), region.Walk, anger, []Point{anger}); err == nil {
		t.Fatal("Table() succeeded against a closed server")
	}
}
