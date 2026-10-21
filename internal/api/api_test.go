package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/listing"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

const validListing = `{"address": "Anger 1, Erfurt", "warm_rent_eur": 850, "size_m2": 62.5, "rooms": 2.5}`

// browser is an HTTP client with its own cookies, like one person's browser.
type browser struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func setup(t *testing.T, limits visitor.Limits) (*pgxpool.Pool, *httptest.Server) {
	t.Helper()
	pool := dbtest.New(t)
	reg, err := region.Load("../../config/thuringia.json")
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		Pool: pool, Region: reg, Source: listing.Manual{},
		Listings: &listing.Store{Pool: pool, Limits: limits},
		Logger:   slog.New(slog.DiscardHandler),
	}
	mux := http.NewServeMux()
	a.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return pool, server
}

func newBrowser(t *testing.T, server *httptest.Server) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, server: server, client: &http.Client{Jar: jar}}
}

func (b *browser) do(method, path, contentType, body string, headers ...string) response {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), method, b.server.URL+path, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data}
}

func (b *browser) addListing(body string) response {
	b.t.Helper()
	return b.do(http.MethodPost, "/api/listings", "application/json", body)
}

func (b *browser) listings(query string) listingsJSON {
	b.t.Helper()
	resp := b.do(http.MethodGet, "/api/listings"+query, "", "")
	if resp.status != http.StatusOK {
		b.t.Fatalf("GET /api/listings%s = %d %s", query, resp.status, resp.body)
	}
	var out listingsJSON
	decode(b.t, resp.body, &out)
	return out
}

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func TestAddListing(t *testing.T) {
	t.Parallel()
	pool, server := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	alice := newBrowser(t, server)

	resp := alice.addListing(validListing)
	if resp.status != http.StatusAccepted {
		t.Fatalf("status = %d %s, want 202", resp.status, resp.body)
	}
	var added listingJSON
	decode(t, resp.body, &added)
	if added.Status != "pending" || added.Address != "Anger 1, Erfurt" || added.WarmRentEUR != 850 || added.Score != nil {
		t.Errorf("added listing = %+v", added)
	}
	if loc := resp.header.Get("Location"); loc != "/api/listings/"+strconv.FormatInt(added.ID, 10) {
		t.Errorf("Location = %q", loc)
	}
	if cookie := resp.header.Get("Set-Cookie"); !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Errorf("Set-Cookie = %q", cookie)
	}

	// Alice sees her listing; someone else doesn't.
	if got := alice.listings("").Listings; len(got) != 1 || got[0].ID != added.ID {
		t.Errorf("alice's listings = %+v", got)
	}
	if got := newBrowser(t, server).listings("").Listings; len(got) != 0 {
		t.Errorf("a new visitor sees %+v", got)
	}

	var jobs int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Errorf("%d jobs queued, want 1", jobs)
	}
}

func TestAddListingRejects(t *testing.T) {
	t.Parallel()
	_, server := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	tests := []struct {
		name        string
		contentType string
		body        string
		headers     []string
		wantStatus  int
		wantFields  []string
	}{
		{name: "invalid fields", contentType: "application/json",
			body:       `{"address": "Anger 1", "warm_rent_eur": 20, "size_m2": 50}`,
			wantStatus: http.StatusBadRequest, wantFields: []string{"warm_rent_eur", "rooms"}},
		{name: "not JSON", contentType: "application/json", body: `address=Anger`,
			wantStatus: http.StatusBadRequest},
		{name: "a form post", contentType: "application/x-www-form-urlencoded", body: `address=Anger`,
			wantStatus: http.StatusUnsupportedMediaType},
		{name: "too large", contentType: "application/json", body: `{"address": "` + strings.Repeat("a", maxBody) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge},
		{name: "from another site", contentType: "application/json", body: validListing,
			headers: []string{"Sec-Fetch-Site", "cross-site"}, wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := newBrowser(t, server).do(http.MethodPost, "/api/listings", tt.contentType, tt.body, tt.headers...)
			if resp.status != tt.wantStatus {
				t.Fatalf("status = %d %s, want %d", resp.status, resp.body, tt.wantStatus)
			}
			var body apiError
			decode(t, resp.body, &body)
			if body.Message == "" {
				t.Error("no error message")
			}
			for _, field := range tt.wantFields {
				if body.Fields[field] == "" {
					t.Errorf("no error for %s in %v", field, body.Fields)
				}
			}
		})
	}
}

func TestAddListingOverTheLimit(t *testing.T) {
	t.Parallel()
	_, server := setup(t, visitor.Limits{PerHour: 1, PerDay: 10, SiteWidePerDay: 10})
	alice := newBrowser(t, server)
	if resp := alice.addListing(validListing); resp.status != http.StatusAccepted {
		t.Fatalf("first listing: %d %s", resp.status, resp.body)
	}
	resp := alice.addListing(validListing)
	if resp.status != http.StatusTooManyRequests {
		t.Fatalf("status = %d %s, want 429", resp.status, resp.body)
	}
	if retry, err := strconv.Atoi(resp.header.Get("Retry-After")); err != nil || retry < 1 || retry > 3600 {
		t.Errorf("Retry-After = %q, want 1 to 3600 seconds", resp.header.Get("Retry-After"))
	}
	// Someone else still can.
	if resp := newBrowser(t, server).addListing(validListing); resp.status != http.StatusAccepted {
		t.Errorf("another visitor: %d %s", resp.status, resp.body)
	}
}

func TestReadingCreatesNoVisitor(t *testing.T) {
	t.Parallel()
	pool, server := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	resp := newBrowser(t, server).do(http.MethodGet, "/api/listings", "", "")
	if resp.status != http.StatusOK || resp.header.Get("Set-Cookie") != "" {
		t.Errorf("GET = %d with Set-Cookie %q, want 200 and no cookie", resp.status, resp.header.Get("Set-Cookie"))
	}
	var visitors int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM visitors").Scan(&visitors); err != nil {
		t.Fatal(err)
	}
	if visitors != 0 {
		t.Errorf("%d visitors after a read, want 0", visitors)
	}
}

// score marks a listing scored with the given measurements, as the enrich
// job would.
func score(t *testing.T, pool *pgxpool.Pool, id int64, values map[string]float64) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		UPDATE listings SET status = 'scored', location = ST_SetSRID(ST_MakePoint(11.03, 50.97), 4326)::geography
		WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	for criterion, value := range values {
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO listing_measurements (listing_id, criterion, outcome, value, place_name)
			VALUES ($1, $2, 'measured', $3, 'Somewhere')`, id, criterion, value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRankingFollowsTheWeights(t *testing.T) {
	t.Parallel()
	pool, server := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	alice := newBrowser(t, server)
	var cheap, central listingJSON
	decode(t, alice.addListing(validListing).body, &cheap)
	decode(t, alice.addListing(validListing).body, &central)
	// Rent 9 €/m² scores 1 and 16 scores 0; a supermarket 5 minutes away
	// scores 1, 12.5 minutes 0.5. Both criteria have weight 3 by default.
	score(t, pool, cheap.ID, map[string]float64{"rent_per_m2": 9, "category:supermarket": 12.5})
	score(t, pool, central.ID, map[string]float64{"rent_per_m2": 16, "category:supermarket": 5})

	tests := []struct {
		query     string
		wantOrder []int64
		wantTotal []float64
	}{
		{"", []int64{cheap.ID, central.ID}, []float64{75, 50}},
		{"?weight.rent_per_m2=0", []int64{central.ID, cheap.ID}, []float64{100, 50}},
		{"?weight.rent_per_m2=0&weight.category:supermarket=0&weight.size_m2=1", nil, nil},
	}
	for _, tt := range tests {
		got := alice.listings(tt.query)
		for i, id := range tt.wantOrder {
			l := got.Listings[i]
			if l.ID != id || l.Score == nil || l.Score.Total != tt.wantTotal[i] {
				t.Errorf("%q: position %d = listing %d scoring %+v, want %d scoring %v", tt.query, i, l.ID, l.Score, id, tt.wantTotal[i])
			}
		}
		if tt.wantOrder == nil {
			// Only size has weight, and it wasn't measured: no scores.
			for _, l := range got.Listings {
				if l.Score != nil {
					t.Errorf("%q: listing %d scored %v, want no score", tt.query, l.ID, l.Score.Total)
				}
			}
		}
	}

	got := alice.listings("?weight.rent_per_m2=0")
	if c := got.Criteria[0]; c.Key != "rent_per_m2" || c.Weight != 0 || c.DefaultWeight != 3 {
		t.Errorf("rent criterion = %+v, want weight 0 and default 3", c)
	}
	// The breakdown explains the score.
	breakdown := got.Listings[0].Score.Breakdown
	var supermarket componentJSON
	for _, c := range breakdown {
		if c.Key == "category:supermarket" {
			supermarket = c
		}
	}
	if supermarket.Outcome != "measured" || supermarket.Value == nil || *supermarket.Value != 5 ||
		supermarket.Points != 100 || supermarket.Place != "Somewhere" {
		t.Errorf("supermarket component = %+v", supermarket)
	}

	for _, bad := range []string{
		"?weight.rent_per_m2=-1", "?weight.rent_per_m2=11", "?weight.rent_per_m2=lots", "?weight.view=3",
		"?weight.rent_per_m2=0&weight.size_m2=0&weight.rooms=0&weight.category:supermarket=0&weight.category:transit_stop=0" +
			"&weight.category:pharmacy=0&weight.category:doctor=0&weight.category:park=0&weight.category:train_station=0&weight.category:gym=0",
	} {
		if resp := alice.do(http.MethodGet, "/api/listings"+bad, "", ""); resp.status != http.StatusBadRequest {
			t.Errorf("%s: status %d %s, want 400", bad, resp.status, resp.body)
		}
	}
}

func TestGetAndDelete(t *testing.T) {
	t.Parallel()
	_, server := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	alice, bob := newBrowser(t, server), newBrowser(t, server)
	var added listingJSON
	decode(t, alice.addListing(validListing).body, &added)
	path := "/api/listings/" + strconv.FormatInt(added.ID, 10)

	if resp := alice.do(http.MethodGet, path, "", ""); resp.status != http.StatusOK {
		t.Errorf("alice GET = %d", resp.status)
	}
	for _, tt := range []struct {
		name string
		b    *browser
		path string
	}{
		{"bob reading alice's listing", bob, path},
		{"bob deleting alice's listing", bob, path},
		{"a malformed id", alice, "/api/listings/abc"},
	} {
		method := http.MethodGet
		if strings.Contains(tt.name, "deleting") {
			method = http.MethodDelete
		}
		if resp := tt.b.do(method, tt.path, "", ""); resp.status != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", tt.name, resp.status)
		}
	}

	if resp := alice.do(http.MethodDelete, path, "", ""); resp.status != http.StatusNoContent {
		t.Fatalf("alice DELETE = %d %s", resp.status, resp.body)
	}
	if resp := alice.do(http.MethodGet, path, "", ""); resp.status != http.StatusNotFound {
		t.Errorf("GET after DELETE = %d, want 404", resp.status)
	}
}

func TestRegion(t *testing.T) {
	t.Parallel()
	_, server := setup(t, visitor.Limits{PerHour: 10, PerDay: 10, SiteWidePerDay: 10})
	resp := newBrowser(t, server).do(http.MethodGet, "/api/region", "", "")
	var got regionJSON
	decode(t, resp.body, &got)
	if resp.status != http.StatusOK || got.Name != "Thuringia" || got.Map.Zoom != 13 {
		t.Errorf("region = %d %+v", resp.status, got)
	}
}
