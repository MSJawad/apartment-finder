package input

import (
	"errors"
	"testing"
)

func TestDecode(t *testing.T) {
	t.Parallel()
	type form struct {
		Name  string   `json:"name"`
		Rooms *float64 `json:"rooms"`
	}
	tests := []struct {
		name      string
		data      string
		wantField string // a FieldErrors for this field
		wantErr   bool   // ErrMalformed
	}{
		{name: "valid", data: `{"name": "Work", "rooms": 2}`},
		{name: "null", data: `null`},
		{name: "wrong type", data: `{"rooms": "two"}`, wantField: "rooms"},
		{name: "not JSON", data: `name=Work`, wantErr: true},
		{name: "not an object", data: `["Work"]`, wantErr: true},
		{name: "unknown field", data: `{"title": "Work"}`, wantErr: true},
		{name: "trailing data", data: `{"name": "Work"} {}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var f form
			err := Decode([]byte(tt.data), &f)
			fields, isFields := errors.AsType[FieldErrors](err)
			switch {
			case tt.wantField != "":
				if !isFields || fields[tt.wantField] == "" {
					t.Errorf("err = %v, want a field error for %s", err, tt.wantField)
				}
			case tt.wantErr:
				if !errors.Is(err, ErrMalformed) {
					t.Errorf("err = %v, want ErrMalformed", err)
				}
			case err != nil:
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestFieldErrorsMessage(t *testing.T) {
	t.Parallel()
	err := FieldErrors{"rooms": "Enter the rooms.", "address": "Enter the address."}
	if got, want := err.Error(), "invalid fields: address: Enter the address.; rooms: Enter the rooms."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestText(t *testing.T) {
	t.Parallel()
	if got := Text("  Anger \n\t 1,  Erfurt "); got != "Anger 1, Erfurt" {
		t.Errorf("Text() = %q", got)
	}
}
