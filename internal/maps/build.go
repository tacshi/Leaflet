package maps

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/locales"
)

// MaxPlaces is how many places a map a person makes names.
const MaxPlaces = 25

// ErrNoPlaces is the error of a map that names no place.
var ErrNoPlaces = errors.New("a map needs a place")

// NotFoundError is the error of a place no search found.
type NotFoundError struct{ Name string }

func (e *NotFoundError) Error() string { return fmt.Sprintf("no place named %q was found", e.Name) }

// PlaceLine reads a line of a map's places, as a person writes them: a
// name or an address to find, "lat, lng", or "Name | lat, lng", the name
// all before the line's last "|", which may hold one.
func PlaceLine(line string) (name string, at *doc.LatLng) {
	line = strings.TrimSpace(line)
	coords := line
	if i := strings.LastIndex(line, "|"); i >= 0 {
		name, coords = line[:i], line[i+1:]
	}
	if p, ok := parseLatLng(coords); ok {
		return strings.TrimSpace(name), &p
	}
	return line, nil
}

func parseLatLng(s string) (doc.LatLng, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), ",")
	if !ok {
		return doc.LatLng{}, false
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	p := doc.LatLng{Lat: lat, Lng: lng}
	return p, err1 == nil && err2 == nil && p.Valid()
}

// PlacesText is a map's places as a person edits them, a line each, as
// "Name | lat, lng".
func PlacesText(places []doc.MapPlace) string {
	lines := make([]string, len(places))
	for i, p := range places {
		at := strconv.FormatFloat(roundTo(p.Lat, 6), 'f', -1, 64) + ", " + strconv.FormatFloat(roundTo(p.Lng, 6), 'f', -1, 64)
		lines[i] = at
		if p.Name != "" {
			lines[i] = p.Name + " | " + at
		}
	}
	return strings.Join(lines, "\n")
}

func roundTo(f float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(f*p) / p
}

// Build makes the map a person asks for: titled title, of the places
// lines names, a line each as PlaceLine reads it, those without their lat
// and lng found by name, and by mode a route from the first place to the
// last, or none when mode is "". It keeps was's route when that goes by
// mode between the same places, and finds no place it already has.
func Build(ctx context.Context, title, lines, mode string, was doc.Map) (doc.Map, error) {
	m := doc.Map{Title: strings.TrimSpace(title)}
	known := map[string]doc.MapPlace{}
	for _, p := range was.Places {
		known[strings.TrimSpace(p.Name)] = p
	}
	for _, line := range strings.Split(lines, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(m.Places) == MaxPlaces {
			break
		}
		name, at := PlaceLine(line)
		if at != nil {
			m.Places = append(m.Places, doc.MapPlace{Name: name, Lat: at.Lat, Lng: at.Lng})
			continue
		}
		if p, ok := known[name]; ok {
			m.Places = append(m.Places, p)
			continue
		}
		var near *doc.LatLng
		if len(m.Places) > 0 {
			last := m.Places[len(m.Places)-1].At()
			near = &last
		}
		found, err := FindPlaces(ctx, name, near, 1)
		if err != nil {
			return doc.Map{}, err
		}
		if len(found) == 0 {
			return doc.Map{}, &NotFoundError{Name: name}
		}
		m.Places = append(m.Places, doc.MapPlace{Name: name, Lat: found[0].Lat, Lng: found[0].Lng})
	}
	if len(m.Places) == 0 {
		return doc.Map{}, ErrNoPlaces
	}
	if mode == "" || len(m.Places) < 2 {
		return m, nil
	}
	from, to := m.Places[0].At(), m.Places[len(m.Places)-1].At()
	if r := was.Route; r != nil && r.Mode == mode && sameEnds(was, from, to) {
		m.Route = r
		return m, nil
	}
	routes, err := FindRoutes(ctx, from, to, mode, time.Time{})
	if err != nil {
		return doc.Map{}, err
	}
	m.Route = &routes[0]
	return m, nil
}

// sameEnds reports that m's places begin and end where from and to are.
func sameEnds(m doc.Map, from, to doc.LatLng) bool {
	if len(m.Places) < 2 {
		return false
	}
	near := func(a, b doc.LatLng) bool { return math.Abs(a.Lat-b.Lat) < 1e-6 && math.Abs(a.Lng-b.Lng) < 1e-6 }
	return near(m.Places[0].At(), from) && near(m.Places[len(m.Places)-1].At(), to)
}

// RouteMode is the mode of was's route, as a person's map keeps it: "" for
// none.
func RouteMode(m doc.Map) string {
	if m.Route == nil {
		return ""
	}
	return m.Route.Mode
}

// AppleMapsURL opens m in Apple's Maps: its route's ends, as directions
// by its way of going, or its first place.
func AppleMapsURL(m doc.Map) string {
	if len(m.Places) == 0 {
		markers := m.Markers()
		if len(markers) == 0 {
			return ""
		}
		m.Places = markers
	}
	first, last := m.Places[0], m.Places[len(m.Places)-1]
	q := url.Values{}
	if m.Route == nil || len(m.Places) < 2 {
		q.Set("ll", fmt.Sprintf("%.6f,%.6f", first.Lat, first.Lng))
		if first.Name != "" {
			q.Set("q", first.Name)
		}
		return "https://maps.apple.com/?" + q.Encode()
	}
	q.Set("saddr", fmt.Sprintf("%.6f,%.6f", first.Lat, first.Lng))
	q.Set("daddr", fmt.Sprintf("%.6f,%.6f", last.Lat, last.Lng))
	if flag, ok := map[string]string{doc.RouteDrive: "d", doc.RouteWalk: "w", doc.RouteTransit: "r"}[m.Route.Mode]; ok {
		q.Set("dirflg", flag)
	}
	return "https://maps.apple.com/?" + q.Encode()
}

// OSMURL opens m on openstreetmap.org: its route's ends, as directions
// by the FOSSGIS router of its way of going, or its first place.
func OSMURL(m doc.Map) string {
	markers := m.Places
	if len(markers) == 0 {
		markers = m.Markers()
	}
	if len(markers) == 0 {
		return ""
	}
	first, last := markers[0], markers[len(markers)-1]
	engine, ok := map[string]string{doc.RouteDrive: "fossgis_osrm_car", doc.RouteCycle: "fossgis_osrm_bike", doc.RouteWalk: "fossgis_osrm_foot"}[RouteMode(m)]
	if !ok || len(markers) < 2 {
		return doc.PlaceLink(first)
	}
	return fmt.Sprintf("https://www.openstreetmap.org/directions?engine=%s&route=%.6f%%2C%.6f%%3B%.6f%%2C%.6f", engine, first.Lat, first.Lng, last.Lat, last.Lng)
}

// Message is why a map could not be made, for a person to read in msgs'
// language.
func Message(msgs locales.Messages, err error) string {
	var missing *NotFoundError
	switch {
	case errors.As(err, &missing):
		return msgs.DialogMapNotFound(missing.Name)
	case errors.Is(err, ErrNoPlaces):
		return msgs.DialogMapEmpty()
	case errors.Is(err, ErrNoRoute):
		return msgs.DialogMapNoRouteFound()
	}
	return msgs.DialogMapFailed(err.Error())
}
