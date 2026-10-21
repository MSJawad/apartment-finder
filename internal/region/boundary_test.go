package region

import (
	"strings"
	"testing"
)

// A 10×10 square with a 2×2 hole in the middle, and a separate 1×1 island.
const squareWithHole = `square
1
   0.0E+00   0.0E+00
   1.0E+01   0.0E+00
   1.0E+01   1.0E+01
   0.0E+00   1.0E+01
END
!hole
   4 4
   6 4
   6 6
   4 6
END
island

   20 20
   21 20
   21 21
END
END
`

func TestBoundaryContains(t *testing.T) {
	b, err := ParseBoundary(strings.NewReader(squareWithHole))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		lat, lon float64
		want     bool
	}{
		{"inside", 2, 2, true},
		{"in the hole", 5, 5, false},
		{"between the hole and the edge", 5, 8, true},
		{"on the island", 20.2, 20.5, true},
		{"outside", 15, 15, false},
		{"west of the square", 5, -1, false},
	}
	for _, tt := range tests {
		if got := b.Contains(tt.lat, tt.lon); got != tt.want {
			t.Errorf("%s: Contains(%v, %v) = %v, want %v", tt.name, tt.lat, tt.lon, got, tt.want)
		}
	}
	if got, want := b.BBox(), [4]float64{0, 0, 21, 21}; got != want {
		t.Errorf("BBox() = %v, want %v (holes don't widen it)", got, want)
	}
}

func TestParseBoundaryRejects(t *testing.T) {
	tests := []struct {
		name, poly, wantErr string
	}{
		{"empty", "", "empty file"},
		{"no final END", "name\n1\n0 0\n1 0\n1 1\nEND\n", "final END"},
		{"section without END", "name\n1\n0 0\n1 0\n", "has no END"},
		{"too few points", "name\n1\n0 0\n1 0\nEND\nEND\n", "fewer than 3"},
		{"not a number", "name\n1\n0 zero\n1 0\n1 1\nEND\nEND\n", "invalid syntax"},
		{"three columns", "name\n1\n0 0 0\n1 0\n1 1\nEND\nEND\n", `want "lon lat"`},
		{"out of range", "name\n1\n0 91\n1 0\n1 1\nEND\nEND\n", "not a longitude and latitude"},
		{"only a hole", "name\n!1\n0 0\n1 0\n1 1\nEND\nEND\n", "no outer ring"},
		{"unreadable line", "name\n1\n" + strings.Repeat("0", 70_000) + "\n", "token too long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseBoundary(strings.NewReader(tt.poly)); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseBoundary() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
