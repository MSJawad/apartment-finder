// Package listing holds apartment listings: where they come from, how they
// are checked and stored, and how they rank.
package listing

import (
	"context"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/MSJawad/apartment-finder/internal/input"
)

// Draft is a submitted listing that passed validation but isn't stored yet.
type Draft struct {
	Address       string
	WarmRentCents int
	SizeM2        float64
	Rooms         float64
	// SourceURL links to the original advert. It is only ever shown as a
	// link: the service never fetches it.
	SourceURL string
}

// Source turns what someone submits into a Draft. Every way of adding a
// listing goes through one, and each stored listing records which.
//
// Manual entry is the only source. Scraping listing sites such as
// ImmoScout24 or Kleinanzeigen is against their terms, so it is ruled out. A
// new input, say a partner API that permits it, would be a new Source;
// nothing after the Draft changes.
type Source interface {
	// Name is stored with each listing, e.g. "manual".
	Name() string
	Draft(ctx context.Context, data []byte) (Draft, error)
}

// Manual is the add-listing form: the visitor types in every field.
type Manual struct{}

// Name implements Source.
func (Manual) Name() string { return "manual" }

// manualInput is the form as JSON. Numbers are pointers so a missing field
// can be told apart from a zero.
type manualInput struct {
	Address     string   `json:"address"`
	WarmRentEUR *float64 `json:"warm_rent_eur"`
	SizeM2      *float64 `json:"size_m2"`
	Rooms       *float64 `json:"rooms"`
	SourceURL   string   `json:"source_url"`
}

// Draft implements Source. It reports every invalid field at once, so the
// form can mark them all.
func (Manual) Draft(_ context.Context, data []byte) (Draft, error) {
	var in manualInput
	if err := input.Decode(data, &in); err != nil {
		return Draft{}, err
	}

	d := Draft{Address: input.Text(in.Address), SourceURL: strings.TrimSpace(in.SourceURL)}
	errs := input.FieldErrors{}
	if n := utf8.RuneCountInString(d.Address); n < 3 || n > 200 {
		errs["address"] = "Enter the address, e.g. “Johannesstraße 10, Erfurt”."
	}
	switch {
	case in.WarmRentEUR == nil:
		errs["warm_rent_eur"] = "Enter the warm rent."
	case *in.WarmRentEUR < 50 || *in.WarmRentEUR > 10_000:
		errs["warm_rent_eur"] = "Enter a warm rent between 50 and 10,000 €."
	default:
		d.WarmRentCents = int(math.Round(*in.WarmRentEUR * 100))
	}
	switch {
	case in.SizeM2 == nil:
		errs["size_m2"] = "Enter the size."
	case *in.SizeM2 < 5 || *in.SizeM2 > 1000:
		errs["size_m2"] = "Enter a size between 5 and 1,000 m²."
	default:
		d.SizeM2 = math.Round(*in.SizeM2*100) / 100
	}
	switch {
	case in.Rooms == nil:
		errs["rooms"] = "Enter the number of rooms."
	// German listings count half rooms, e.g. 2.5 Zimmer.
	case *in.Rooms < 1 || *in.Rooms > 20 || *in.Rooms*2 != math.Trunc(*in.Rooms*2):
		errs["rooms"] = "Enter 1 to 20 rooms, in steps of 0.5."
	default:
		d.Rooms = *in.Rooms
	}
	if d.SourceURL != "" && !validLink(d.SourceURL) {
		errs["source_url"] = "Enter a link starting with http:// or https://, or leave it empty."
	}
	if len(errs) > 0 {
		return Draft{}, errs
	}
	return d, nil
}

// validLink reports whether s is a web link that is safe to put in an href.
// Allowing only http and https rules out javascript: links.
func validLink(s string) bool {
	if len(s) > 2000 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
