package place_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/input"
	"github.com/MSJawad/apartment-finder/internal/place"
	"github.com/MSJawad/apartment-finder/internal/region"
	"github.com/MSJawad/apartment-finder/internal/visitor"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		data       string
		want       place.Draft
		wantFields []string
	}{
		{name: "valid", data: `{"name": " Work ", "address": "Nordhäuser  Straße 63, Erfurt", "mode": "bike"}`,
			want: place.Draft{Name: "Work", Address: "Nordhäuser Straße 63, Erfurt", Mode: region.Bike}},
		{name: "empty", data: `{}`, wantFields: []string{"address", "mode", "name"}},
		{name: "name too long, unknown mode", data: `{"name": "` + strings.Repeat("a", 41) + `", "address": "Anger 1", "mode": "car"}`,
			wantFields: []string{"mode", "name"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := place.Parse([]byte(tt.data))
			if tt.wantFields == nil {
				if err != nil || got != tt.want {
					t.Errorf("Parse() = %+v, %v; want %+v", got, err, tt.want)
				}
				return
			}
			fields, ok := errors.AsType[input.FieldErrors](err)
			if !ok {
				t.Fatalf("err = %v, want field errors", err)
			}
			if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, tt.wantFields) {
				t.Errorf("fields with errors = %v, want %v", got, tt.wantFields)
			}
		})
	}
	if _, err := place.Parse([]byte(`not json`)); !errors.Is(err, input.ErrMalformed) {
		t.Errorf("Parse(not json) = %v, want ErrMalformed", err)
	}
}

func TestCriteria(t *testing.T) {
	t.Parallel()
	got := place.Criteria([]place.Place{{ID: 7, Name: "Work"}})
	if len(got) != 1 || got[0].Key != "place:7" || got[0].Label != "Work" || got[0].Best != 10 || got[0].Worst != 45 || got[0].Weight != 3 {
		t.Errorf("Criteria() = %+v", got)
	}
}

func TestStore(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	store := &place.Store{Pool: pool, Limits: visitor.Limits{PerHour: 100, PerDay: 100, SiteWidePerDay: 100}}
	alice, err := visitor.Ensure(t.Context(), pool, uuid.UUID{}, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := visitor.Ensure(t.Context(), pool, uuid.UUID{}, false)
	if err != nil {
		t.Fatal(err)
	}

	var ids []int64
	for i := range place.MaxPerVisitor {
		id, err := store.Add(t.Context(), alice, place.Draft{Name: "Place", Address: "Anger 1, Erfurt", Mode: region.Walk})
		if err != nil {
			t.Fatalf("place %d: %v", i+1, err)
		}
		ids = append(ids, id)
	}
	if _, err := store.Add(t.Context(), alice, place.Draft{Name: "One more", Address: "Anger 1", Mode: region.Walk}); !errors.Is(err, place.ErrTooMany) {
		t.Fatalf("place %d: %v, want ErrTooMany", place.MaxPerVisitor+1, err)
	}

	places, err := store.List(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, p := range places {
		got = append(got, p.ID)
	}
	if !slices.Equal(got, ids) || places[0].Status != "pending" || places[0].Location != nil {
		t.Errorf("alice's places = %v (first %+v), want %v, oldest first and pending", got, places[0], ids)
	}

	// Each place queued its job; the refused one queued nothing.
	var jobs int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'locate_place'").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != place.MaxPerVisitor {
		t.Errorf("%d jobs, want %d", jobs, place.MaxPerVisitor)
	}

	if err := store.Delete(t.Context(), bob, ids[0]); !errors.Is(err, place.ErrNotFound) {
		t.Errorf("bob deleting alice's place: %v, want ErrNotFound", err)
	}
	if err := store.Delete(t.Context(), alice, ids[0]); err != nil {
		t.Fatal(err)
	}
	// Deleting frees a place: the cap is on how many she has, not on how
	// many she has added.
	if _, err := store.Add(t.Context(), alice, place.Draft{Name: "Gym", Address: "Anger 1", Mode: region.Bike}); err != nil {
		t.Errorf("adding after deleting one: %v", err)
	}
	if bobs, err := store.List(t.Context(), bob); err != nil || len(bobs) != 0 {
		t.Errorf("bob's places = %v, %v", bobs, err)
	}
}
