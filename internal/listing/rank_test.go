package listing

import (
	"math"
	"testing"

	"github.com/MSJawad/apartment-finder/internal/enrich"
	"github.com/MSJawad/apartment-finder/internal/scoring"
)

func TestRank(t *testing.T) {
	t.Parallel()
	criteria := []scoring.Criterion{
		{Key: "rent_per_m2", Best: 9, Worst: 16, Weight: 1},
		{Key: "category:gym", Best: 5, Worst: 20, Weight: 1},
	}
	measured := func(v float64) Measurement { return Measurement{Outcome: enrich.Measured, Value: v} }
	listings := []Listing{
		{ID: 1, Status: "pending"},
		{ID: 2, Status: "scored", Measurements: map[string]Measurement{
			// 0.5 and 0: 25 points
			"rent_per_m2": measured(12.5), "category:gym": {Outcome: enrich.NoneNearby},
		}},
		{ID: 3, Status: "not_found"},
		{ID: 4, Status: "scored", Measurements: map[string]Measurement{
			// 1, and the gym is left out: 100 points
			"rent_per_m2": measured(9), "category:gym": {Outcome: enrich.Unreachable},
		}},
		{ID: 5, Status: "scored", Measurements: map[string]Measurement{
			// 0.5 and 1: 75 points
			"rent_per_m2": measured(12.5), "category:gym": measured(5),
		}},
	}

	ranked, err := Rank(listings, criteria)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []int64{4, 5, 2, 1, 3} // best first, then unscored in the given order
	wantTotal := map[int64]float64{4: 100, 5: 75, 2: 25}
	for i, s := range ranked {
		if s.ID != wantOrder[i] {
			t.Errorf("position %d: listing %d, want %d", i, s.ID, wantOrder[i])
		}
		want, scored := wantTotal[s.ID]
		switch {
		case scored && s.Result == nil:
			t.Errorf("listing %d has no score, want %v", s.ID, want)
		case scored && math.Abs(s.Result.Total-want) > 1e-9:
			t.Errorf("listing %d scores %v, want %v", s.ID, s.Result.Total, want)
		case !scored && s.Result != nil:
			t.Errorf("listing %d (%s) has a score", s.ID, s.Status)
		}
	}
}

func TestRankWithoutWeightedMeasurements(t *testing.T) {
	t.Parallel()
	// The only weighted criterion couldn't be measured, so there is nothing
	// to average: the listing has no score rather than a made-up one.
	criteria := []scoring.Criterion{
		{Key: "rent_per_m2", Best: 9, Worst: 16, Weight: 0},
		{Key: "category:gym", Best: 5, Worst: 20, Weight: 1},
	}
	listings := []Listing{{ID: 1, Status: "scored", Measurements: map[string]Measurement{
		"rent_per_m2":  {Outcome: enrich.Measured, Value: 10},
		"category:gym": {Outcome: enrich.Unreachable},
	}}}
	ranked, err := Rank(listings, criteria)
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].Result != nil {
		t.Errorf("score = %+v, want none", ranked[0].Result)
	}

	if _, err := Rank(listings, []scoring.Criterion{{Key: "broken"}}); err == nil {
		t.Error("Rank with an invalid criterion succeeded")
	}
}
