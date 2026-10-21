// Package geocode turns addresses into coordinates with Photon, komoot's
// OpenStreetMap geocoder.
package geocode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/upstream"
)

// Result is where an address was placed.
type Result struct {
	Found    bool
	Lat, Lon float64
	// Label is the address as Photon matched it. It is shown next to the
	// listing, so a wrong match (say, the main station for a bare city name)
	// is visible instead of silently scored.
	Label string
	// Precision is "house" when a building matched and "street" when only
	// the street did. German listings often leave out the house number.
	Precision string
}

// Photon geocodes addresses within one region.
type Photon struct {
	client  *upstream.Client
	baseURL string
	region  *region.Region
}

// NewPhoton returns a geocoder that calls the Photon server at baseURL, e.g.
// https://photon.komoot.io, through client.
func NewPhoton(client *upstream.Client, baseURL string, r *region.Region) *Photon {
	return &Photon{client: client, baseURL: strings.TrimSuffix(baseURL, "/"), region: r}
}

// Geocode places address within the region. When nothing matches, or the
// match lies outside the region where there's no routing data, the result
// has Found false.
func (p *Photon) Geocode(ctx context.Context, address string) (Result, error) {
	query := normalize(address)
	if query == "" {
		return Result{}, errors.New("geocode: empty address")
	}
	if !houseNumber.MatchString(query) {
		// Without a house number, the person means a street. Asked for
		// buildings too, Photon can prefer a landmark on or named after it,
		// such as a tram stop at its far end. So streets are searched first,
		// and a street only counts if the address names it: otherwise
		// "Erfurt" alone matches an "Erfurter Straße" in another town.
		street, name, err := p.search(ctx, query, "street")
		if err != nil {
			return Result{}, err
		}
		if street.Found && strings.Contains(spellOut(query), spellOut(name)) {
			return street, nil
		}
		// Not a street, so perhaps a landmark such as "Universität Erfurt".
	}
	result, _, err := p.search(ctx, query, "house", "street")
	return result, err
}

// houseNumber matches a house number such as "1", "10a" or "1-3", but not a
// five-digit postcode.
var houseNumber = regexp.MustCompile(`(^|[\s,])\d{1,4}\s?[a-z]?([-/]\d{1,4}[a-z]?)?($|[\s,])`)

// spellOut writes the usual abbreviations of "straße" in full, so
// "Johannesstr." names the street "Johannesstraße".
func spellOut(s string) string {
	s = strings.ToLower(s)
	for _, short := range []string{"strasse", "str."} {
		s = strings.ReplaceAll(s, short, "straße")
	}
	return s
}

// search asks Photon for the best match in the given layers and returns it
// with the matched feature's name.
func (p *Photon) search(ctx context.Context, query string, layers ...string) (Result, string, error) {
	bbox := make([]string, len(p.region.BBox))
	for i, v := range p.region.BBox {
		bbox[i] = strconv.FormatFloat(v, 'f', -1, 64)
	}
	params := url.Values{
		"q":     {query},
		"limit": {"1"},
		"lang":  {p.region.Language},
		// Never districts or cities: those would put the listing at the
		// city center.
		"layer": layers,
		"bbox":  {strings.Join(bbox, ",")},
	}
	// Encode sorts the parameters, so the same query always gives the same
	// URL, and so the same cache entry.
	body, err := p.client.Get(ctx, p.baseURL+"/api?"+params.Encode(), isMiss)
	if err != nil {
		return Result{}, "", err
	}

	resp, err := parse(body)
	if err != nil {
		return Result{}, "", err
	}
	if len(resp.Features) == 0 {
		return Result{}, "", nil
	}
	f := resp.Features[0]
	lon, lat := f.Geometry.Coordinates[0], f.Geometry.Coordinates[1]
	if !p.region.Contains(lat, lon) {
		return Result{}, "", nil
	}
	return Result{Found: true, Lat: lat, Lon: lon, Label: f.Properties.label(), Precision: f.Properties.Type}, f.Properties.Name, nil
}

// normalize trims and lowercases address and collapses its whitespace, so
// trivially different spellings share a cache entry.
func normalize(address string) string {
	return strings.ToLower(strings.Join(strings.Fields(address), " "))
}

type response struct {
	Features []struct {
		Properties properties `json:"properties"`
		Geometry   struct {
			Coordinates []float64 `json:"coordinates"` // [lon, lat]
		} `json:"geometry"`
	} `json:"features"`
}

type properties struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Street      string `json:"street"`
	HouseNumber string `json:"housenumber"`
	Postcode    string `json:"postcode"`
	City        string `json:"city"`
}

// label formats the match like a postal address: "Anger 1, 99084 Erfurt",
// with the place's name first when it's a landmark rather than a plain
// address.
func (p properties) label() string {
	street := strings.TrimSpace(p.Street + " " + p.HouseNumber)
	var parts []string
	if p.Name != "" && p.Name != street {
		parts = append(parts, p.Name)
	}
	if street != "" {
		parts = append(parts, street)
	}
	if place := strings.TrimSpace(p.Postcode + " " + p.City); place != "" {
		parts = append(parts, place)
	}
	return strings.Join(parts, ", ")
}

func parse(body []byte) (response, error) {
	var resp response
	if err := json.Unmarshal(body, &resp); err != nil {
		return response{}, fmt.Errorf("decode photon response: %w", err)
	}
	for _, f := range resp.Features {
		if len(f.Geometry.Coordinates) != 2 {
			return response{}, errors.New("photon response: a feature has no point coordinates")
		}
	}
	return resp, nil
}

func isMiss(body []byte) (bool, error) {
	resp, err := parse(body)
	if err != nil {
		return false, err
	}
	return len(resp.Features) == 0, nil
}
