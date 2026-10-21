package listing

import (
	"cmp"
	"errors"
	"slices"

	"github.com/MSJawad/apartment-finder/internal/enrich"
	"github.com/MSJawad/apartment-finder/internal/scoring"
)

// Scored is a listing with its score. Result is nil while the listing has
// none: it is still pending, wasn't found, failed, or has no measured
// criterion with any weight.
type Scored struct {
	Listing
	Result *scoring.Result
}

// Rank scores listings against criteria and sorts them best first. Listings
// without a score come last, in the order they were given.
//
// Scores are worked out here, on every read, from the stored measurements.
// Moving a weight slider is just another read: nothing is re-measured or
// stored.
func Rank(listings []Listing, criteria []scoring.Criterion) ([]Scored, error) {
	ranked := make([]Scored, len(listings))
	for i, l := range listings {
		ranked[i].Listing = l
		if l.Status != "scored" {
			continue
		}
		result, err := scoring.Score(criteria, values(l, criteria))
		if errors.Is(err, scoring.ErrNoWeight) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ranked[i].Result = &result
	}
	slices.SortStableFunc(ranked, func(a, b Scored) int {
		switch {
		case a.Result != nil && b.Result != nil:
			return cmp.Compare(b.Result.Total, a.Result.Total)
		case a.Result != nil:
			return -1
		case b.Result != nil:
			return 1
		}
		return 0
	})
	return ranked, nil
}

// values turns a listing's measurements into scoring values. A category with
// nothing within reach gets the criterion's worst value, so it scores 0. One
// whose places couldn't be routed to gets no value, which leaves it out of
// the score: that's a gap in the map data, not a fact about the listing.
func values(l Listing, criteria []scoring.Criterion) map[string]float64 {
	values := make(map[string]float64, len(criteria))
	for _, c := range criteria {
		m := l.Measurements[c.Key]
		switch m.Outcome {
		case enrich.Measured:
			values[c.Key] = m.Value
		case enrich.NoneNearby:
			values[c.Key] = c.Worst
		}
	}
	return values
}
