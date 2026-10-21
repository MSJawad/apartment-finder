package region

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Boundary is the exact area a region covers. It comes from the .poly file
// Geofabrik publishes with each extract, so it follows the same line the
// routing and amenity data were cut along. A rectangle around the region
// would also take in parts of its neighbours, where there is no data.
type Boundary struct {
	rings []ring
}

type ring struct {
	hole   bool
	points [][2]float64 // lon, lat
}

// ParseBoundary reads a boundary in the Osmosis polygon format: a name line,
// then sections of "lon lat" lines that each end with END, and a final END.
// A section whose name starts with "!" is a hole.
func ParseBoundary(r io.Reader) (*Boundary, error) {
	scanner := bufio.NewScanner(r)
	next := func() (string, bool) {
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); line != "" {
				return line, true
			}
		}
		return "", false
	}
	// failed explains why next found no more lines: a read error, or the
	// file ending too soon.
	failed := func(early string) error {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("boundary: %w", err)
		}
		return errors.New("boundary: " + early)
	}

	if _, ok := next(); !ok {
		return nil, failed("empty file")
	}
	b := &Boundary{}
	for {
		name, ok := next()
		if !ok {
			return nil, failed("missing the final END")
		}
		if name == "END" {
			break
		}
		rg := ring{hole: strings.HasPrefix(name, "!")}
		for {
			line, ok := next()
			if !ok {
				return nil, failed("section " + name + " has no END")
			}
			if line == "END" {
				break
			}
			point, err := parsePoint(line)
			if err != nil {
				return nil, fmt.Errorf("boundary: section %s: %w", name, err)
			}
			rg.points = append(rg.points, point)
		}
		if len(rg.points) < 3 {
			return nil, fmt.Errorf("boundary: section %s has fewer than 3 points", name)
		}
		b.rings = append(b.rings, rg)
	}
	for _, rg := range b.rings {
		if !rg.hole {
			return b, nil
		}
	}
	return nil, errors.New("boundary: no outer ring, only holes")
}

func parsePoint(line string) ([2]float64, error) {
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return [2]float64{}, fmt.Errorf("want \"lon lat\", got %q", line)
	}
	lon, err1 := strconv.ParseFloat(fields[0], 64)
	lat, err2 := strconv.ParseFloat(fields[1], 64)
	if err := errors.Join(err1, err2); err != nil {
		return [2]float64{}, fmt.Errorf("%q: %w", line, err)
	}
	if !(lon >= -180 && lon <= 180 && lat >= -90 && lat <= 90) {
		return [2]float64{}, fmt.Errorf("%q is not a longitude and latitude", line)
	}
	return [2]float64{lon, lat}, nil
}

// Contains reports whether the point is inside the boundary: inside an
// outer ring and not inside a hole.
func (b *Boundary) Contains(lat, lon float64) bool {
	inside := false
	for _, rg := range b.rings {
		if rg.contains(lon, lat) {
			if rg.hole {
				return false
			}
			inside = true
		}
	}
	return inside
}

// contains casts a ray from the point eastwards and counts the edges it
// crosses: an odd count means inside.
func (rg ring) contains(x, y float64) bool {
	inside := false
	pts := rg.points
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		xi, yi, xj, yj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}
	return inside
}

// BBox returns the smallest box around the outer rings: min lon, min lat,
// max lon, max lat.
func (b *Boundary) BBox() [4]float64 {
	box := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, rg := range b.rings {
		if rg.hole {
			continue
		}
		for _, p := range rg.points {
			box[0], box[1] = min(box[0], p[0]), min(box[1], p[1])
			box[2], box[3] = max(box[2], p[0]), max(box[3], p[1])
		}
	}
	return box
}
