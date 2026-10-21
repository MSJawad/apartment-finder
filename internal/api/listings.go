package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/MSJawad/apartment-finder/internal/enrich"
	"github.com/MSJawad/apartment-finder/internal/input"
	"github.com/MSJawad/apartment-finder/internal/listing"
	"github.com/MSJawad/apartment-finder/internal/place"
	"github.com/MSJawad/apartment-finder/internal/scoring"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

// weightPrefix marks a weight in the query string:
// ?weight.rent_per_m2=5&weight.category:gym=0.
const weightPrefix = "weight."

// maxWeight is the highest weight a request may set.
const maxWeight = 10

type regionJSON struct {
	Name string     `json:"name"`
	BBox [4]float64 `json:"bbox"`
	// Outline is the covered area's outer boundary, as rings of lon, lat
	// pairs, so the page can show where addresses are accepted.
	Outline [][][2]float64 `json:"outline"`
	Map     struct {
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
		Zoom int     `json:"zoom"`
	} `json:"map"`
}

func (a *API) getRegion(w http.ResponseWriter, _ *http.Request) {
	resp := regionJSON{Name: a.Region.Name, BBox: a.Region.BBox, Outline: a.Region.Outline()}
	resp.Map.Lat, resp.Map.Lon, resp.Map.Zoom = a.Region.Map.Lat, a.Region.Map.Lon, a.Region.Map.Zoom
	writeJSON(w, http.StatusOK, resp)
}

type criterionJSON struct {
	Key           string  `json:"key"`
	Label         string  `json:"label"`
	Unit          string  `json:"unit"`
	Best          float64 `json:"best"`
	Worst         float64 `json:"worst"`
	DefaultWeight float64 `json:"default_weight"`
	Weight        float64 `json:"weight"`
}

type listingJSON struct {
	ID      int64  `json:"id"`
	Example bool   `json:"example"`
	Address string `json:"address"`
	// MatchedAddress is what the geocoder matched the address to, shown so
	// a wrong match is visible.
	MatchedAddress string         `json:"matched_address,omitempty"`
	Precision      string         `json:"precision,omitempty"`
	WarmRentEUR    float64        `json:"warm_rent_eur"`
	SizeM2         float64        `json:"size_m2"`
	Rooms          float64        `json:"rooms"`
	SourceURL      string         `json:"source_url,omitempty"`
	Status         string         `json:"status"`
	StatusDetail   string         `json:"status_detail,omitempty"`
	Location       *listing.Point `json:"location"`
	Score          *scoreJSON     `json:"score"`
	CreatedAt      time.Time      `json:"created_at"`
}

type scoreJSON struct {
	Total     float64         `json:"total"` // 0 to 100
	Breakdown []componentJSON `json:"breakdown"`
}

type componentJSON struct {
	Key string `json:"key"`
	// Outcome is measured, none_nearby (scores 0), unreachable (left out of
	// the score) or pending (not measured yet).
	Outcome   string   `json:"outcome"`
	Value     *float64 `json:"value"`
	Score     float64  `json:"score"`  // 0 to 1
	Points    float64  `json:"points"` // this criterion's part of the total
	Place     string   `json:"place,omitempty"`
	DistanceM *float64 `json:"distance_m,omitempty"`
}

type listingsJSON struct {
	Criteria []criterionJSON `json:"criteria"`
	Listings []listingJSON   `json:"listings"`
}

// listListings returns the visitor's listings and the examples, ranked under
// the weights in the query string.
func (a *API) listListings(w http.ResponseWriter, r *http.Request) {
	viewer, err := a.viewer(w, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defaults, criteria, err := a.criteria(r, viewer)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	listings, err := a.Listings.Visible(r.Context(), viewer)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ranked, err := listing.Rank(listings, criteria)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	resp := listingsJSON{Criteria: criteriaJSON(defaults, criteria), Listings: make([]listingJSON, len(ranked))}
	for i, s := range ranked {
		resp.Listings[i] = toListingJSON(s)
	}
	writeJSON(w, http.StatusOK, resp)
}

// getListing returns one listing, scored under the weights in the query
// string.
func (a *API) getListing(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.fail(w, r, listing.ErrNotFound)
		return
	}
	viewer, err := a.viewer(w, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	_, criteria, err := a.criteria(r, viewer)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	l, err := a.Listings.Get(r.Context(), viewer, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ranked, err := listing.Rank([]listing.Listing{l}, criteria)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toListingJSON(ranked[0]))
}

// addListing stores a listing from the form and queues its measurement. It
// answers 202 Accepted: the listing exists, but its score comes later.
func (a *API) addListing(w http.ResponseWriter, r *http.Request) {
	body, apiErr := readJSON(w, r)
	if apiErr != nil {
		a.fail(w, r, apiErr)
		return
	}
	draft, err := a.Source.Draft(r.Context(), body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	owner, err := a.owner(w, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, err := a.Listings.Add(r.Context(), owner, a.Source.Name(), draft)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	l, err := a.Listings.Get(r.Context(), owner, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/listings/"+strconv.FormatInt(id, 10))
	writeJSON(w, http.StatusAccepted, toListingJSON(listing.Scored{Listing: l}))
}

func (a *API) deleteListing(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.fail(w, r, listing.ErrNotFound)
		return
	}
	// Deleting never creates a visitor: a new one has nothing to delete.
	owner, ok := visitor.FromRequest(r)
	if !ok {
		a.fail(w, r, listing.ErrNotFound)
		return
	}
	if err := a.Listings.Delete(r.Context(), owner, id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// criteria returns what viewer's listings are scored on, the region's
// criteria plus one per place of theirs, both with their default weights
// and with the weights from the query string applied.
func (a *API) criteria(r *http.Request, viewer uuid.UUID) (defaults, weighted []scoring.Criterion, err error) {
	places, err := a.Places.List(r.Context(), viewer)
	if err != nil {
		return nil, nil, err
	}
	defaults = append(a.Region.Criteria(), place.Criteria(places)...)
	known := make(map[string]bool, len(defaults))
	for _, c := range defaults {
		known[c.Key] = true
	}
	weights := map[string]float64{}
	fields := input.FieldErrors{}
	for param, values := range r.URL.Query() {
		key, ok := strings.CutPrefix(param, weightPrefix)
		if !ok {
			continue
		}
		weight, err := strconv.ParseFloat(values[0], 64)
		switch {
		case !known[key]:
			fields[param] = "There's no such criterion."
		case err != nil || weight < 0 || weight > maxWeight:
			fields[param] = "Must be a number from 0 to " + strconv.Itoa(maxWeight) + "."
		default:
			weights[key] = weight
		}
	}
	if len(fields) > 0 {
		return nil, nil, fields
	}
	weighted, err = scoring.WithWeights(defaults, weights)
	if err != nil {
		return nil, nil, err
	}
	var total float64
	for _, c := range weighted {
		total += c.Weight
	}
	if total == 0 {
		return nil, nil, &apiError{status: http.StatusBadRequest, Message: "Give at least one criterion a weight above 0."}
	}
	return defaults, weighted, nil
}

// pathID returns the {id} in the request path, if it is a valid one.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func criteriaJSON(defaults, weighted []scoring.Criterion) []criterionJSON {
	out := make([]criterionJSON, len(weighted))
	for i, c := range weighted {
		out[i] = criterionJSON{
			Key: c.Key, Label: c.Label, Unit: c.Unit, Best: c.Best, Worst: c.Worst,
			DefaultWeight: defaults[i].Weight, Weight: c.Weight,
		}
	}
	return out
}

func toListingJSON(s listing.Scored) listingJSON {
	l := s.Listing
	out := listingJSON{
		ID: l.ID, Example: l.Example, Address: l.Address,
		MatchedAddress: l.MatchedAddress, Precision: l.Precision,
		WarmRentEUR: float64(l.WarmRentCents) / 100, SizeM2: l.SizeM2, Rooms: l.Rooms,
		SourceURL: l.SourceURL, Status: l.Status, StatusDetail: l.StatusDetail,
		Location: l.Location, CreatedAt: l.CreatedAt.UTC(),
	}
	if s.Result == nil {
		return out
	}
	out.Score = &scoreJSON{Total: s.Result.Total, Breakdown: make([]componentJSON, len(s.Result.Components))}
	for i, c := range s.Result.Components {
		comp := componentJSON{Key: c.Key, Outcome: "pending", Score: c.Score, Points: c.Points}
		if m, ok := l.Measurements[c.Key]; ok {
			comp.Outcome, comp.Place = m.Outcome, m.PlaceName
			if c.Measured && m.Outcome == enrich.Measured {
				comp.Value = &c.Value
			}
			if m.DistanceM > 0 {
				comp.DistanceM = &m.DistanceM
			}
		}
		out.Score.Breakdown[i] = comp
	}
	return out
}
