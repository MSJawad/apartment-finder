package api

import (
	"net/http"
	"time"

	"github.com/MSJawad/apartment-finder/internal/listing"
	"github.com/MSJawad/apartment-finder/internal/place"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

type placeJSON struct {
	ID int64 `json:"id"`
	// Criterion is the key the place scores under, for weight.<criterion>.
	Criterion      string         `json:"criterion"`
	Name           string         `json:"name"`
	Address        string         `json:"address"`
	Mode           string         `json:"mode"`
	Status         string         `json:"status"`
	StatusDetail   string         `json:"status_detail,omitempty"`
	MatchedAddress string         `json:"matched_address,omitempty"`
	Precision      string         `json:"precision,omitempty"`
	Location       *listing.Point `json:"location"`
	CreatedAt      time.Time      `json:"created_at"`
}

type placesJSON struct {
	Places []placeJSON `json:"places"`
}

func (a *API) listPlaces(w http.ResponseWriter, r *http.Request) {
	viewer, err := a.viewer(w, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	places, err := a.Places.List(r.Context(), viewer)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	resp := placesJSON{Places: make([]placeJSON, len(places))}
	for i, p := range places {
		resp.Places[i] = toPlaceJSON(p)
	}
	writeJSON(w, http.StatusOK, resp)
}

// addPlace stores a place and queues the job that locates it and measures
// the trips to it. Like a new listing, it answers 202 Accepted.
func (a *API) addPlace(w http.ResponseWriter, r *http.Request) {
	body, apiErr := readJSON(w, r)
	if apiErr != nil {
		a.fail(w, r, apiErr)
		return
	}
	draft, err := place.Parse(body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	owner, err := a.owner(w, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, err := a.Places.Add(r.Context(), owner, draft)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	places, err := a.Places.List(r.Context(), owner)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, p := range places {
		if p.ID == id {
			writeJSON(w, http.StatusAccepted, toPlaceJSON(p))
			return
		}
	}
	a.fail(w, r, place.ErrNotFound)
}

func (a *API) deletePlace(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		a.fail(w, r, place.ErrNotFound)
		return
	}
	owner, ok := visitor.FromRequest(r)
	if !ok {
		a.fail(w, r, place.ErrNotFound)
		return
	}
	if err := a.Places.Delete(r.Context(), owner, id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toPlaceJSON(p place.Place) placeJSON {
	out := placeJSON{
		ID: p.ID, Criterion: place.CriterionKey(p.ID), Name: p.Name, Address: p.Address, Mode: string(p.Mode),
		Status: p.Status, StatusDetail: p.StatusDetail, MatchedAddress: p.MatchedAddress, Precision: p.Precision,
		CreatedAt: p.CreatedAt.UTC(),
	}
	if p.Location != nil {
		out.Location = &listing.Point{Lat: p.Location.Lat, Lon: p.Location.Lon}
	}
	return out
}
