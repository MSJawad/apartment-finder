package scoring

import (
	"errors"
	"math"
	"strings"
	"testing"
)

const epsilon = 1e-9

func TestCriterionScore(t *testing.T) {
	walk := Criterion{Key: "category:supermarket", Best: 5, Worst: 20} // minutes, less is better
	size := Criterion{Key: "size_m2", Best: 70, Worst: 25}             // m², more is better
	tests := []struct {
		name      string
		criterion Criterion
		value     float64
		want      float64
	}{
		{"less is better: beyond best", walk, 0, 1},
		{"less is better: at best", walk, 5, 1},
		{"less is better: halfway", walk, 12.5, 0.5},
		{"less is better: at worst", walk, 20, 0},
		{"less is better: beyond worst", walk, 45, 0},
		{"more is better: beyond best", size, 90, 1},
		{"more is better: at best", size, 70, 1},
		{"more is better: halfway", size, 47.5, 0.5},
		{"more is better: at worst", size, 25, 0},
		{"more is better: beyond worst", size, 12, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.criterion.Score(tt.value); math.Abs(got-tt.want) > epsilon {
				t.Errorf("Score(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestScore(t *testing.T) {
	rent := Criterion{Key: "rent_per_m2", Best: 9, Worst: 18, Weight: 2}
	super := Criterion{Key: "category:supermarket", Best: 5, Worst: 20, Weight: 1}
	work := Criterion{Key: "place:1", Best: 10, Worst: 45, Weight: 1}

	type want struct {
		total    float64
		points   []float64 // per criterion, in order
		measured []bool
	}
	tests := []struct {
		name     string
		criteria []Criterion
		values   map[string]float64
		want     want
	}{
		{
			name:     "weighted average",
			criteria: []Criterion{rent, super},
			values:   map[string]float64{"rent_per_m2": 9, "category:supermarket": 20}, // 1 and 0
			want:     want{total: 200.0 / 3, points: []float64{200.0 / 3, 0}, measured: []bool{true, true}},
		},
		{
			name:     "everything perfect",
			criteria: []Criterion{rent, super, work},
			values:   map[string]float64{"rent_per_m2": 8, "category:supermarket": 3, "place:1": 10},
			want:     want{total: 100, points: []float64{50, 25, 25}, measured: []bool{true, true, true}},
		},
		{
			name:     "unmeasured criterion is left out and the rest rescale",
			criteria: []Criterion{rent, super, work},
			values:   map[string]float64{"rent_per_m2": 13.5, "category:supermarket": 5}, // 0.5 and 1; no route to work
			// rent now carries 2/3 of the weight and the supermarket 1/3.
			want: want{total: 0.5*200.0/3 + 100.0/3, points: []float64{100.0 / 3, 100.0 / 3, 0}, measured: []bool{true, true, false}},
		},
		{
			name:     "zero weight is scored but adds nothing",
			criteria: []Criterion{rent, {Key: "rooms", Best: 3, Worst: 1, Weight: 0}},
			values:   map[string]float64{"rent_per_m2": 9, "rooms": 2},
			want:     want{total: 100, points: []float64{100, 0}, measured: []bool{true, true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Score(tt.criteria, tt.values)
			if err != nil {
				t.Fatalf("Score() error = %v", err)
			}
			if math.Abs(got.Total-tt.want.total) > epsilon {
				t.Errorf("Total = %v, want %v", got.Total, tt.want.total)
			}
			var sum float64
			for i, comp := range got.Components {
				sum += comp.Points
				if comp.Key != tt.criteria[i].Key {
					t.Errorf("component %d is %s, want %s (criteria order)", i, comp.Key, tt.criteria[i].Key)
				}
				if comp.Measured != tt.want.measured[i] {
					t.Errorf("%s: Measured = %v, want %v", comp.Key, comp.Measured, tt.want.measured[i])
				}
				if math.Abs(comp.Points-tt.want.points[i]) > epsilon {
					t.Errorf("%s: Points = %v, want %v", comp.Key, comp.Points, tt.want.points[i])
				}
				if !comp.Measured && (comp.Share != 0 || comp.Score != 0) {
					t.Errorf("%s: unmeasured but Share = %v, Score = %v", comp.Key, comp.Share, comp.Score)
				}
			}
			if math.Abs(sum-got.Total) > epsilon {
				t.Errorf("points add up to %v, total is %v", sum, got.Total)
			}
		})
	}
}

func TestScoreErrors(t *testing.T) {
	ok := Criterion{Key: "rent_per_m2", Best: 9, Worst: 18, Weight: 1}
	tests := []struct {
		name     string
		criteria []Criterion
		values   map[string]float64
		wantErr  string
	}{
		{"all weights zero", []Criterion{{Key: "a", Best: 1, Worst: 0}}, map[string]float64{"a": 1}, ErrNoWeight.Error()},
		{"nothing measured", []Criterion{ok}, map[string]float64{}, ErrNoWeight.Error()},
		{"no criteria", nil, nil, ErrNoWeight.Error()},
		{"missing key", []Criterion{{Best: 1, Worst: 0, Weight: 1}}, nil, "no key"},
		{"best equals worst", []Criterion{{Key: "a", Best: 5, Worst: 5, Weight: 1}}, nil, "must be different"},
		{"best is NaN", []Criterion{{Key: "a", Best: math.NaN(), Worst: 5, Weight: 1}}, nil, "must be different"},
		{"negative weight", []Criterion{{Key: "a", Best: 1, Worst: 0, Weight: -1}}, nil, "zero or more"},
		{"infinite weight", []Criterion{{Key: "a", Best: 1, Worst: 0, Weight: math.Inf(1)}}, nil, "zero or more"},
		{"duplicate key", []Criterion{ok, ok}, nil, "appears twice"},
		{"value is NaN", []Criterion{ok}, map[string]float64{"rent_per_m2": math.NaN()}, "not a number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Score(tt.criteria, tt.values)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Score() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
	if _, err := Score([]Criterion{ok}, nil); !errors.Is(err, ErrNoWeight) {
		t.Errorf("errors.Is(err, ErrNoWeight) = false for %v", err)
	}
}

func TestWithWeights(t *testing.T) {
	defaults := []Criterion{
		{Key: "rent_per_m2", Best: 9, Worst: 18, Weight: 2},
		{Key: "category:gym", Best: 10, Worst: 30, Weight: 1},
	}
	tests := []struct {
		name        string
		weights     map[string]float64
		wantWeights []float64
		wantErr     string
	}{
		{name: "no overrides keeps defaults", weights: nil, wantWeights: []float64{2, 1}},
		{name: "overrides some", weights: map[string]float64{"category:gym": 5}, wantWeights: []float64{2, 5}},
		{name: "zero is allowed", weights: map[string]float64{"rent_per_m2": 0}, wantWeights: []float64{0, 1}},
		{name: "unknown key", weights: map[string]float64{"category:casino": 1}, wantErr: `no criterion "category:casino"`},
		{name: "negative", weights: map[string]float64{"rent_per_m2": -1}, wantErr: "zero or more"},
		{name: "NaN", weights: map[string]float64{"rent_per_m2": math.NaN()}, wantErr: "zero or more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WithWeights(defaults, tt.weights)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("WithWeights() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("WithWeights() error = %v", err)
			}
			for i, c := range got {
				if c.Weight != tt.wantWeights[i] {
					t.Errorf("%s weight = %v, want %v", c.Key, c.Weight, tt.wantWeights[i])
				}
			}
			if defaults[0].Weight != 2 || defaults[1].Weight != 1 {
				t.Error("WithWeights modified the defaults")
			}
		})
	}
}
