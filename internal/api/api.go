// Package api serves the public JSON API that the page uses.
//
// Requests are tied to a visitor by a cookie (see package visitor). Reads
// never create a visitor; the first listing or place someone adds does.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/input"
	"github.com/MSJawad/apartment-finder/internal/listing"
	"github.com/MSJawad/apartment-finder/internal/place"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

// maxBody caps request bodies. A listing is a few hundred bytes.
const maxBody = 16 << 10

// API holds the handlers' dependencies.
type API struct {
	Pool     *pgxpool.Pool
	Region   *region.Region
	Listings *listing.Store
	Places   *place.Store
	// Source reads the add-listing form; see listing.Source.
	Source listing.Source
	Logger *slog.Logger
	// Version is the git commit the server was built from. After a deploy,
	// CI polls GET /api/version until it shows the new commit.
	Version string
}

// Register adds the API's routes to mux.
//
// Every route rejects non-GET requests from other sites:
// http.CrossOriginProtection checks the Sec-Fetch-Site or Origin header that
// browsers send. That stops another site from making a visitor's browser add
// or delete listings with their cookie (cross-site request forgery). The
// cookie's SameSite setting is a second line of defence. The check runs
// after routing, so rejected requests are still logged under their route.
func (a *API) Register(mux *http.ServeMux) {
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, apiError{Message: "Requests from other sites aren't allowed."})
	}))
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, protection.Handler(h))
	}
	handle("GET /api/version", a.getVersion)
	handle("GET /api/region", a.getRegion)
	handle("GET /api/listings", a.listListings)
	handle("POST /api/listings", a.addListing)
	handle("GET /api/listings/{id}", a.getListing)
	handle("DELETE /api/listings/{id}", a.deleteListing)
	handle("GET /api/places", a.listPlaces)
	handle("POST /api/places", a.addPlace)
	handle("DELETE /api/places/{id}", a.deletePlace)
}

// viewer returns the visitor making a read request, or uuid.Nil() for
// someone who hasn't added anything (or whose data was deleted). A known
// visitor's visit is recorded and their cookie renewed.
func (a *API) viewer(w http.ResponseWriter, r *http.Request) (uuid.UUID, error) {
	id, ok := visitor.FromRequest(r)
	if !ok {
		return uuid.Nil(), nil
	}
	exists, err := visitor.Touch(r.Context(), a.Pool, id)
	if err != nil || !exists {
		return uuid.Nil(), err
	}
	visitor.SetCookie(w, r, id)
	return id, nil
}

// owner returns the visitor making a write request, creating one if needed.
func (a *API) owner(w http.ResponseWriter, r *http.Request) (uuid.UUID, error) {
	cookie, ok := visitor.FromRequest(r)
	id, err := visitor.Ensure(r.Context(), a.Pool, cookie, ok)
	if err != nil {
		return uuid.UUID{}, err
	}
	visitor.SetCookie(w, r, id)
	return id, nil
}

// readJSON returns the request body, which must be JSON and at most maxBody.
// Requiring the JSON content type also matters for cross-site request
// forgery: a plain HTML form on another site can't send it.
func readJSON(w http.ResponseWriter, r *http.Request) ([]byte, *apiError) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return nil, &apiError{status: http.StatusUnsupportedMediaType, Message: "Send the request body as application/json."}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
		return nil, &apiError{status: http.StatusRequestEntityTooLarge, Message: "The request body is too large."}
	}
	if err != nil {
		return nil, &apiError{status: http.StatusBadRequest, Message: "The request body couldn't be read."}
	}
	return body, nil
}

// apiError is an error response. Its JSON is what the client gets.
type apiError struct {
	status  int
	Message string            `json:"error"`
	Fields  map[string]string `json:"fields,omitempty"`
	// retryAfter is set for 429 responses.
	retryAfter int
}

// fail responds with err, mapping the errors the packages below return to
// status codes. Anything unexpected is logged and answered with a generic
// 500, so internal details never reach the client.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	resp, ok := errors.AsType[*apiError](err)
	if !ok {
		resp = toAPIError(err)
	}
	if resp == nil {
		a.Logger.LogAttrs(r.Context(), slog.LevelError, "request failed",
			slog.String("path", r.URL.Path), slog.Any("error", err))
		resp = &apiError{status: http.StatusInternalServerError, Message: "Something went wrong on our side. Please try again."}
	}
	if resp.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(resp.retryAfter))
	}
	writeJSON(w, resp.status, resp)
}

func (e *apiError) Error() string { return e.Message }

// toAPIError maps known errors to responses, and returns nil for others.
func toAPIError(err error) *apiError {
	if fields, ok := errors.AsType[input.FieldErrors](err); ok {
		return &apiError{status: http.StatusBadRequest, Message: "Some fields need fixing.", Fields: fields}
	}
	if limit, ok := errors.AsType[*visitor.LimitError](err); ok {
		return &apiError{status: http.StatusTooManyRequests, Message: limit.Reason, retryAfter: int(limit.RetryAfter.Seconds())}
	}
	switch {
	case errors.Is(err, input.ErrMalformed):
		return &apiError{status: http.StatusBadRequest, Message: "The request body must be a JSON object with the listing's fields."}
	case errors.Is(err, listing.ErrNotFound):
		return &apiError{status: http.StatusNotFound, Message: "There's no such listing."}
	case errors.Is(err, place.ErrNotFound):
		return &apiError{status: http.StatusNotFound, Message: "There's no such place."}
	case errors.Is(err, place.ErrTooMany):
		return &apiError{status: http.StatusConflict,
			Message: fmt.Sprintf("You can have up to %d places. Delete one to add another.", place.MaxPerVisitor)}
	}
	return nil
}

func (a *API) getVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": a.Version})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	// Answers depend on the visitor's cookie, so shared caches must not
	// keep them.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// The status is already sent; an encode error means the client went away.
	_ = json.NewEncoder(w).Encode(body)
}
