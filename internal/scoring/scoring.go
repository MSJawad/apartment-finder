// Package scoring turns a listing's measured values (rent per m², travel times
// and so on) into a score from 0 to 100 with a breakdown.
//
// Each Criterion maps one value onto 0 to 1 with fixed thresholds, so a
// listing's score depends only on that listing: adding another listing never
// changes it. The total is the weighted average of those scores.
package scoring

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// ErrNoWeight means none of the measured criteria has any weight, so there is
// nothing to average.
var ErrNoWeight = errors.New("scoring: no measured criterion has weight")

// Criterion is one thing a listing is scored on.
type Criterion struct {
	Key   string // stable identifier, e.g. "rent_per_m2" or "category:supermarket"
	Label string // for people, e.g. "Supermarket"
	Unit  string // of the measured value, e.g. "min" or "€/m²"

	// Best is the value that scores 1 and Worst the value that scores 0.
	// Between them the score is linear; beyond them it stays at 1 or 0. When
	// less is better, as for rent or travel time, Best is below Worst.
	Best, Worst float64

	// Weight is how much the criterion counts relative to the others.
	Weight float64
}

// Validate reports whether c can be used for scoring.
func (c Criterion) Validate() error {
	switch {
	case c.Key == "":
		return errors.New("criterion has no key")
	case !finite(c.Best) || !finite(c.Worst) || c.Best == c.Worst:
		return fmt.Errorf("criterion %s: best and worst must be different numbers", c.Key)
	case !finite(c.Weight) || c.Weight < 0:
		return fmt.Errorf("criterion %s: weight must be zero or more", c.Key)
	}
	return nil
}

// Validate reports the first problem with criteria: an invalid criterion, or
// two with the same key.
func Validate(criteria []Criterion) error {
	seen := make(map[string]bool, len(criteria))
	for _, c := range criteria {
		if err := c.Validate(); err != nil {
			return err
		}
		if seen[c.Key] {
			return fmt.Errorf("criterion %s appears twice", c.Key)
		}
		seen[c.Key] = true
	}
	return nil
}

// Score maps value onto 0 to 1.
func (c Criterion) Score(value float64) float64 {
	t := (value - c.Worst) / (c.Best - c.Worst)
	return min(max(t, 0), 1)
}

// Component is one criterion's part of a listing's result.
type Component struct {
	Criterion
	// Measured is false when the value is unknown, for example because
	// routing failed. The criterion is then left out of the total and the
	// other weights count proportionally more.
	Measured bool
	Value    float64
	Score    float64 // 0 to 1
	// Share is the fraction of the total this criterion's weight accounts
	// for, after unmeasured criteria are left out.
	Share float64
	// Points is the criterion's contribution to the total: Score × Share ×
	// 100. The points of all components add up to the total.
	Points float64
}

// Result is a listing's score with its breakdown, in the order the criteria
// were given.
type Result struct {
	Total      float64 // 0 to 100
	Components []Component
}

// Score scores the values, keyed by criterion key, against criteria. A
// criterion without a value counts as not measured.
func Score(criteria []Criterion, values map[string]float64) (Result, error) {
	if err := Validate(criteria); err != nil {
		return Result{}, err
	}
	components := make([]Component, len(criteria))
	var totalWeight float64
	for i, c := range criteria {
		value, measured := values[c.Key]
		if measured && !finite(value) {
			return Result{}, fmt.Errorf("criterion %s: value %v is not a number", c.Key, value)
		}
		components[i] = Component{Criterion: c, Measured: measured, Value: value}
		if measured {
			components[i].Score = c.Score(value)
			totalWeight += c.Weight
		}
	}
	if totalWeight == 0 {
		return Result{}, ErrNoWeight
	}

	var total float64
	for i := range components {
		comp := &components[i]
		if !comp.Measured {
			continue
		}
		comp.Share = comp.Weight / totalWeight
		comp.Points = comp.Score * comp.Share * 100
		total += comp.Points
	}
	return Result{Total: total, Components: components}, nil
}

// WithWeights returns a copy of criteria with the weights in weights, keyed
// by criterion key, in place of their defaults. It rejects keys that match no
// criterion, so a typo in a request can't be silently ignored.
func WithWeights(criteria []Criterion, weights map[string]float64) ([]Criterion, error) {
	index := make(map[string]int, len(criteria))
	for i, c := range criteria {
		index[c.Key] = i
	}
	out := slices.Clone(criteria)
	for key, weight := range weights {
		i, ok := index[key]
		if !ok {
			return nil, fmt.Errorf("no criterion %q", key)
		}
		if !finite(weight) || weight < 0 {
			return nil, fmt.Errorf("weight for %s must be zero or more", key)
		}
		out[i].Weight = weight
	}
	return out, nil
}

func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
