package doc

import (
	"errors"
	"math"
	"strings"
)

// LatLng is a point on the Earth, in degrees.
type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Valid reports a latitude and a longitude a map can show.
func (p LatLng) Valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) && p.Lat >= -85.06 && p.Lat <= 85.06 && p.Lng >= -180 && p.Lng <= 180
}

// PathPrecision is how many decimal places of a degree a map's stored
// paths keep: 6, a tenth of a meter.
const PathPrecision = 6

var errPolyline = errors.New("not an encoded polyline")

// EncodePath writes points as an encoded polyline, Google's format, with
// precision decimal places.
func EncodePath(points []LatLng, precision int) string {
	scale := math.Pow10(precision)
	var b strings.Builder
	var lastLat, lastLng int64
	for _, p := range points {
		lat, lng := int64(math.Round(p.Lat*scale)), int64(math.Round(p.Lng*scale))
		encodeDelta(&b, lat-lastLat)
		encodeDelta(&b, lng-lastLng)
		lastLat, lastLng = lat, lng
	}
	return b.String()
}

func encodeDelta(b *strings.Builder, v int64) {
	u := uint64(v) << 1
	if v < 0 {
		u = ^u
	}
	for u >= 0x20 {
		b.WriteByte(byte((0x20 | (u & 0x1f)) + 63))
		u >>= 5
	}
	b.WriteByte(byte(u + 63))
}

// maxPathPrecision is the most decimal places of a degree a path is read
// with, far finer than any router's: a precision past it is no path's, and
// one past 308 would put every point at 0, 0.
const maxPathPrecision = 10

// DecodePath reads an encoded polyline of precision decimal places, 0 to
// maxPathPrecision.
func DecodePath(s string, precision int) ([]LatLng, error) {
	if precision < 0 || precision > maxPathPrecision {
		return nil, errPolyline
	}
	scale := math.Pow10(precision)
	var out []LatLng
	var lat, lng int64
	for i := 0; i < len(s); {
		var deltas [2]int64
		for k := range deltas {
			var result uint64
			shift := uint(0)
			for {
				if i >= len(s) || shift > 60 {
					return nil, errPolyline
				}
				c := int64(s[i]) - 63
				i++
				if c < 0 || c > 0x3f {
					return nil, errPolyline
				}
				result |= uint64(c&0x1f) << shift
				shift += 5
				if c < 0x20 {
					break
				}
			}
			d := int64(result >> 1)
			if result&1 != 0 {
				d = ^d
			}
			deltas[k] = d
		}
		lat += deltas[0]
		lng += deltas[1]
		out = append(out, LatLng{Lat: float64(lat) / scale, Lng: float64(lng) / scale})
	}
	return out, nil
}
