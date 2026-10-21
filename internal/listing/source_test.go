package listing

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/MSJawad/apartment-finder/internal/input"
)

func TestManualDraft(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		want       Draft
		wantFields []string // fields with errors; nil means valid
		wantBad    bool     // the input as a whole is unreadable
	}{
		{
			name:  "valid, with cleanup",
			input: `{"address": "  Johannesstraße 10,\n Erfurt ", "warm_rent_eur": 850.5, "size_m2": 62.456, "rooms": 2.5, "source_url": " https://example.com/ad/1 "}`,
			want:  Draft{Address: "Johannesstraße 10, Erfurt", WarmRentCents: 85050, SizeM2: 62.46, Rooms: 2.5, SourceURL: "https://example.com/ad/1"},
		},
		{
			name:  "source link is optional",
			input: `{"address": "Anger 1, Erfurt", "warm_rent_eur": 600, "size_m2": 40, "rooms": 1}`,
			want:  Draft{Address: "Anger 1, Erfurt", WarmRentCents: 60000, SizeM2: 40, Rooms: 1},
		},
		{
			name:       "everything missing",
			input:      `{}`,
			wantFields: []string{"address", "rooms", "size_m2", "warm_rent_eur"},
		},
		{
			name:       "null is an empty form",
			input:      `null`,
			wantFields: []string{"address", "rooms", "size_m2", "warm_rent_eur"},
		},
		{
			name:       "below the bounds",
			input:      `{"address": "ab", "warm_rent_eur": 49.99, "size_m2": 4.9, "rooms": 0.5}`,
			wantFields: []string{"address", "rooms", "size_m2", "warm_rent_eur"},
		},
		{
			name:       "above the bounds",
			input:      `{"address": "Anger 1", "warm_rent_eur": 10000.01, "size_m2": 1000.5, "rooms": 21}`,
			wantFields: []string{"rooms", "size_m2", "warm_rent_eur"},
		},
		{
			name:       "rooms come in halves",
			input:      `{"address": "Anger 1", "warm_rent_eur": 700, "size_m2": 50, "rooms": 2.25}`,
			wantFields: []string{"rooms"},
		},
		{
			name:       "a javascript link can't become an href",
			input:      `{"address": "Anger 1", "warm_rent_eur": 700, "size_m2": 50, "rooms": 2, "source_url": "javascript:alert(1)"}`,
			wantFields: []string{"source_url"},
		},
		{
			name:       "a link needs a host",
			input:      `{"address": "Anger 1", "warm_rent_eur": 700, "size_m2": 50, "rooms": 2, "source_url": "https://"}`,
			wantFields: []string{"source_url"},
		},
		{
			name:       "a number given as text",
			input:      `{"address": "Anger 1", "warm_rent_eur": 700, "size_m2": 50, "rooms": "two"}`,
			wantFields: []string{"rooms"},
		},
		{name: "not JSON", input: `address=Anger`, wantBad: true},
		{name: "not an object", input: `["Anger 1"]`, wantBad: true},
		{name: "an unknown field", input: `{"street": "Anger 1"}`, wantBad: true},
		{name: "data after the object", input: `{"address": "Anger 1"} {}`, wantBad: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Manual{}.Draft(t.Context(), []byte(tt.input))
			fields, isFieldErr := errors.AsType[input.FieldErrors](err)
			switch {
			case tt.wantBad:
				if !errors.Is(err, input.ErrMalformed) {
					t.Errorf("err = %v, want ErrMalformed", err)
				}
			case tt.wantFields != nil:
				if !isFieldErr {
					t.Fatalf("err = %v, want field errors for %v", err, tt.wantFields)
				}
				if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, tt.wantFields) {
					t.Errorf("fields with errors = %v, want %v", got, tt.wantFields)
				}
			case err != nil:
				t.Fatalf("err = %v, want none", err)
			case got != tt.want:
				t.Errorf("draft = %+v, want %+v", got, tt.want)
			}
		})
	}
}
