package doc

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"leaflet/locales"
)

// A map block shows places on a map, and a route between them: its legs,
// each a way of going (on foot, a bus line, …) with the path it takes, its
// time and its length.

// How a route goes.
const (
	RouteWalk    = "walk"
	RouteCycle   = "cycle"
	RouteDrive   = "drive"
	RouteTransit = "transit"
)

// RouteModes are the ways a route can go, as find_route takes them.
var RouteModes = []string{RouteWalk, RouteCycle, RouteDrive, RouteTransit}

// legModes are how a leg goes: on foot, by bike or car, or by a kind of
// public transport, transit for any other.
var legModes = map[string]bool{
	"walk": true, "cycle": true, "drive": true, "bus": true, "coach": true, "tram": true, "subway": true,
	"rail": true, "ferry": true, "cable": true, "air": true, "transit": true,
}

// Where a route comes from, for the map to credit.
const (
	RouteFromTransitous = "transitous"
	RouteFromOSRM       = "osrm"
)

const (
	maxMapPlaces   = 25
	maxMapLegs     = 60
	maxMapPoints   = 40_000
	maxMapName     = 200
	maxMapLineName = 60
)

// MapPlace is a place on a map.
type MapPlace struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lng  float64 `json:"lng"`
}

// At is where the place is.
func (p MapPlace) At() LatLng { return LatLng{Lat: p.Lat, Lng: p.Lng} }

// MapLeg is a part of a route gone one way: its path an encoded polyline
// (EncodePath, PathPrecision), and for public transport its line, as
// "B8", the line's color and the stops it goes between.
type MapLeg struct {
	Mode      string `json:"mode"`
	Line      string `json:"line,omitempty"`
	Agency    string `json:"agency,omitempty"`
	Color     string `json:"color,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	DurationS int    `json:"duration_s"`
	DistanceM int    `json:"distance_m,omitempty"`
	Path      string `json:"path"`
}

// MapRoute is a way between two places: how it goes, where it was found,
// its time and length, and its legs.
type MapRoute struct {
	Mode      string   `json:"mode"`
	Source    string   `json:"source,omitempty"`
	DurationS int      `json:"duration_s"`
	DistanceM int      `json:"distance_m,omitempty"`
	Legs      []MapLeg `json:"legs"`
}

// Map is what a map block holds.
type Map struct {
	Title  string     `json:"title,omitempty"`
	Places []MapPlace `json:"places,omitempty"`
	Route  *MapRoute  `json:"route,omitempty"`
}

// MapOf reads a map block, leaving out what it cannot show: a place off
// the map or without its lat and lng, and a route that is none. It reads
// the block's data as it is, as often as a frame draws the map: its
// route's paths are read as they are drawn (Shape), which leaves out a
// leg's path that does not read, as MapData checked them as the map was
// written.
func MapOf(n *Node) Map {
	m := Map{Title: n.Str("title")}
	places, okPlaces := n.Data["places"].([]any)
	route, okRoute := n.Data["route"].(map[string]any)
	if !okPlaces && n.Data["places"] != nil || !okRoute && n.Data["route"] != nil {
		return mapOfJSON(n)
	}
	for _, v := range places {
		p := asMap(v)
		lat, latOK := mapNumber(p["lat"])
		lng, lngOK := mapNumber(p["lng"])
		name, _ := p["name"].(string)
		if place := (MapPlace{Name: name, Lat: lat, Lng: lng}); latOK && lngOK && place.At().Valid() {
			m.Places = append(m.Places, place)
		}
	}
	if route != nil {
		text := func(d map[string]any, key string) string {
			s, _ := d[key].(string)
			return s
		}
		number := func(d map[string]any, key string) int { return AsInt(d[key], 0) }
		r := &MapRoute{Mode: text(route, "mode"), Source: text(route, "source"), DurationS: number(route, "duration_s"), DistanceM: number(route, "distance_m")}
		for _, v := range listOf(route["legs"]) {
			l := asMap(v)
			r.Legs = append(r.Legs, MapLeg{Mode: text(l, "mode"), Line: text(l, "line"), Agency: text(l, "agency"), Color: text(l, "color"),
				From: text(l, "from"), To: text(l, "to"), DurationS: number(l, "duration_s"), DistanceM: number(l, "distance_m"), Path: text(l, "path")})
		}
		if checkRouteShape(r) == nil {
			m.Route = r
		}
	}
	return m
}

// mapOfJSON is MapOf for a block whose data holds its places or its route
// as other than JSON's lists and objects, read through JSON.
func mapOfJSON(n *Node) Map {
	var m Map
	raw, err := json.Marshal(n.Data)
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return Map{Title: n.Str("title")}
	}
	m.Places = slices.DeleteFunc(m.Places, func(p MapPlace) bool { return !p.At().Valid() })
	if len(m.Places) == 0 {
		m.Places = nil
	}
	if m.Route != nil && checkRouteShape(m.Route) != nil {
		m.Route = nil
	}
	return m
}

// mapNumber is v as a number of a map's, as JSON's are: false for none.
func mapNumber(v any) (float64, bool) {
	switch v.(type) {
	case float64, int, int64, json.Number:
		return AsFloat(v)
	}
	return 0, false
}

// Equal reports whether m and o are the same map.
func (m Map) Equal(o Map) bool {
	if m.Title != o.Title || !slices.Equal(m.Places, o.Places) || (m.Route == nil) != (o.Route == nil) {
		return false
	}
	r, s := m.Route, o.Route
	return r == nil || r.Mode == s.Mode && r.Source == s.Source && r.DurationS == s.DurationS && r.DistanceM == s.DistanceM &&
		slices.Equal(r.Legs, s.Legs)
}

// Data is the map as a block's data.
func (m Map) Data() map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// Paths are the points of each of the route's legs: none for a leg whose
// path does not read, or leaves the map.
func (m Map) Paths() [][]LatLng {
	if m.Route == nil {
		return nil
	}
	out := make([][]LatLng, len(m.Route.Legs))
	for i, l := range m.Route.Legs {
		path, err := DecodePath(l.Path, PathPrecision)
		if err == nil && !slices.ContainsFunc(path, func(p LatLng) bool { return !p.Valid() }) {
			out[i] = path
		}
	}
	return out
}

// Markers are the places a map marks: its places, or where its route
// starts and ends when it names none.
func (m Map) Markers() []MapPlace {
	if len(m.Places) > 0 || m.Route == nil {
		return m.Places
	}
	return m.markers(m.Paths())
}

// markers are the map's markers, paths its route's.
func (m Map) markers(paths [][]LatLng) []MapPlace {
	if len(m.Places) > 0 || m.Route == nil {
		return m.Places
	}
	var first, last *LatLng
	for _, p := range paths {
		if len(p) > 0 {
			if first == nil {
				first = &p[0]
			}
			last = &p[len(p)-1]
		}
	}
	if first == nil {
		return nil
	}
	return []MapPlace{{Lat: first.Lat, Lng: first.Lng}, {Lat: last.Lat, Lng: last.Lng}}
}

// Bounds is the box around the map's markers and route: south-west and
// north-east, or false when it has neither.
func (m Map) Bounds() (sw, ne LatLng, ok bool) {
	s := m.Shape()
	return s.SW, s.NE, s.OK
}

// MapShape is what a map draws, its route's paths read once: its legs,
// the points of each (Paths), its markers, and the box around them all,
// south-west and north-east, which OK reports it has.
type MapShape struct {
	Legs    []MapLeg
	Paths   [][]LatLng
	Markers []MapPlace
	SW, NE  LatLng
	OK      bool
}

// Shape is what m draws.
func (m Map) Shape() MapShape {
	s := MapShape{Paths: m.Paths(), SW: LatLng{Lat: 90, Lng: 180}, NE: LatLng{Lat: -90, Lng: -180}}
	if m.Route != nil {
		s.Legs = m.Route.Legs
	}
	s.Markers = m.markers(s.Paths)
	add := func(p LatLng) {
		s.SW.Lat, s.SW.Lng = math.Min(s.SW.Lat, p.Lat), math.Min(s.SW.Lng, p.Lng)
		s.NE.Lat, s.NE.Lng = math.Max(s.NE.Lat, p.Lat), math.Max(s.NE.Lng, p.Lng)
		s.OK = true
	}
	for _, p := range s.Markers {
		add(p.At())
	}
	for _, path := range s.Paths {
		for _, p := range path {
			add(p)
		}
	}
	return s
}

// View is the map as agents read it: its route's legs without their
// paths, in minutes and kilometers.
func (m Map) View() map[string]any {
	v := map[string]any{"places": m.Places}
	if m.Places == nil {
		v["places"] = []MapPlace{}
	}
	if m.Title != "" {
		v["title"] = m.Title
	}
	if r := m.Route; r != nil {
		legs := make([]map[string]any, len(r.Legs))
		for i, l := range r.Legs {
			leg := map[string]any{"mode": l.Mode, "duration_min": minutes(l.DurationS)}
			for k, s := range map[string]string{"line": l.Line, "agency": l.Agency, "from": l.From, "to": l.To} {
				if s != "" {
					leg[k] = s
				}
			}
			legs[i] = leg
		}
		v["route"] = map[string]any{"mode": r.Mode, "duration_min": minutes(r.DurationS), "distance_km": kilometers(r.DistanceM), "legs": legs}
	}
	return v
}

func minutes(s int) int { return int(math.Round(float64(s) / 60)) }

func kilometers(m int) float64 { return math.Round(float64(m)/100) / 10 }

// MapData checks a map an agent or a person made, as a spec's map, its
// route a route as find_route found it, and gives it as a block's data.
func MapData(spec map[string]any) (map[string]any, error) {
	if spec == nil {
		return nil, toolErr("A map block needs map: its places or a route.")
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, toolErr("map must be an object.")
	}
	var m Map
	// Each place's lat and lng are read again as given or not, for a place
	// without them to fail rather than be pinned at 0, 0.
	var given struct {
		Places []struct{ Lat, Lng *float64 } `json:"places"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || json.Unmarshal(raw, &given) != nil {
		return nil, toolErr("A map's places are {name, lat, lng}, in degrees, and its route is one find_route gave: %v", err)
	}
	m.Title = strings.TrimSpace(m.Title)
	if utf8.RuneCountInString(m.Title) > maxMapName {
		return nil, toolErr("map.title takes at most %d characters.", maxMapName)
	}
	if len(m.Places) > maxMapPlaces {
		return nil, toolErr("A map marks at most %d places.", maxMapPlaces)
	}
	for i, p := range m.Places {
		if at := given.Places[i]; at.Lat == nil || at.Lng == nil {
			return nil, toolErr("Place %d needs lat and lng, in degrees: find it with find_place.", i+1)
		}
		if !p.At().Valid() {
			return nil, toolErr("Place %d's lat and lng are not on a map: latitude -85 to 85, longitude -180 to 180.", i+1)
		}
		if m.Places[i].Name = strings.TrimSpace(p.Name); utf8.RuneCountInString(m.Places[i].Name) > maxMapName {
			return nil, toolErr("A place's name takes at most %d characters.", maxMapName)
		}
	}
	if m.Route != nil {
		if err := checkRoute(m.Route); err != nil {
			return nil, err
		}
	}
	if len(m.Places) == 0 && m.Route == nil {
		return nil, toolErr("A map needs places or a route.")
	}
	return m.Data(), nil
}

// checkRoute checks a route a map is written with: its shape, and each
// leg's path, read.
func checkRoute(r *MapRoute) error {
	if err := checkRouteShape(r); err != nil {
		return err
	}
	points := 0
	for i, l := range r.Legs {
		path, err := DecodePath(l.Path, PathPrecision)
		if err != nil || len(path) < 2 {
			return toolErr("Leg %d's path is not a route's: use the route find_route gave.", i+1)
		}
		for _, p := range path {
			if !p.Valid() {
				return toolErr("Leg %d's path leaves the map.", i+1)
			}
		}
		if points += len(path); points > maxMapPoints {
			return toolErr("A route takes at most %d points.", maxMapPoints)
		}
	}
	return nil
}

// checkRouteShape checks all of a route but its legs' paths, which take
// long to read: how it goes, its legs, their names, times and lengths,
// and keeps of a leg's color only one a map draws.
func checkRouteShape(r *MapRoute) error {
	if !slices.Contains(RouteModes, r.Mode) {
		return toolErr("A route goes by %s.", strings.Join(RouteModes, ", "))
	}
	if len(r.Legs) == 0 || len(r.Legs) > maxMapLegs {
		return toolErr("A route has 1 to %d legs.", maxMapLegs)
	}
	for i := range r.Legs {
		l := &r.Legs[i]
		if !legModes[l.Mode] {
			return toolErr("A leg goes by one of: walk, cycle, drive, bus, coach, tram, subway, rail, ferry, cable, air, transit.")
		}
		if l.Path == "" {
			return toolErr("Leg %d's path is not a route's: use the route find_route gave.", i+1)
		}
		l.Color = LineColor(*l)
		if utf8.RuneCountInString(l.Line) > maxMapLineName || utf8.RuneCountInString(l.Agency) > maxMapName ||
			utf8.RuneCountInString(l.From) > maxMapName || utf8.RuneCountInString(l.To) > maxMapName {
			return toolErr("Leg %d's names are too long.", i+1)
		}
		if l.DurationS < 0 || l.DistanceM < 0 {
			return toolErr("Leg %d's time and length cannot be less than nothing.", i+1)
		}
	}
	if r.DurationS < 0 || r.DistanceM < 0 {
		return toolErr("A route's time and length cannot be less than nothing.")
	}
	return nil
}

// LineColor is the color of a leg's line, as #rrggbb, or "" when it has
// none a map draws it in.
func LineColor(l MapLeg) string {
	if len(l.Color) == 7 && ValidColor(l.Color) {
		return l.Color
	}
	return ""
}

// RouteSummary is a route in a line: how it goes, its time and its
// length, as "Public transport · 43 min · 27 km".
func RouteSummary(msgs locales.Messages, r *MapRoute) string {
	parts := []string{routeModeName(msgs, r.Mode), Duration(msgs, r.DurationS)}
	if r.DistanceM > 0 {
		parts = append(parts, msgs.MapKilometers(trimFloat(kilometers(r.DistanceM))))
	}
	return strings.Join(parts, " · ")
}

// RouteSteps is a route's legs in a line, as "Walk 1 min → Bus B8 40 min".
func RouteSteps(msgs locales.Messages, r *MapRoute) string {
	steps := make([]string, len(r.Legs))
	for i, l := range r.Legs {
		steps[i] = LegName(msgs, l) + " " + Duration(msgs, l.DurationS)
	}
	return strings.Join(steps, " → ")
}

// LegName is how a leg goes, as "Walk" or "Bus B8".
func LegName(msgs locales.Messages, l MapLeg) string {
	name := map[string]func() string{
		"walk": msgs.MapLegWalk, "cycle": msgs.MapLegCycle, "drive": msgs.MapLegDrive, "bus": msgs.MapLegBus,
		"coach": msgs.MapLegBus, "tram": msgs.MapLegTram, "subway": msgs.MapLegSubway, "rail": msgs.MapLegRail,
		"ferry": msgs.MapLegFerry, "cable": msgs.MapLegCable, "air": msgs.MapLegAir,
	}[l.Mode]
	what := msgs.MapLegTransit()
	if name != nil {
		what = name()
	}
	if l.Line != "" {
		return what + " " + l.Line
	}
	return what
}

func routeModeName(msgs locales.Messages, mode string) string {
	switch mode {
	case RouteWalk:
		return msgs.MapModeWalk()
	case RouteCycle:
		return msgs.MapModeCycle()
	case RouteDrive:
		return msgs.MapModeDrive()
	}
	return msgs.MapModeTransit()
}

// Duration is a time in seconds as minutes, or hours and minutes.
func Duration(msgs locales.Messages, seconds int) string {
	m := max(minutes(seconds), 1)
	if m < 60 {
		return msgs.MapMinutes(m)
	}
	return msgs.MapHoursMinutes(m/60, m%60)
}

// PlaceLink is a link to a place on OpenStreetMap's map.
func PlaceLink(p MapPlace) string {
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f", p.Lat, p.Lng, p.Lat, p.Lng)
}

// mapSearchText is the map's words in any language: its title, its places
// and its legs' lines and stops.
func mapSearchText(m Map) string {
	lines := []string{m.Title}
	for _, p := range m.Places {
		lines = append(lines, p.Name)
	}
	if m.Route != nil {
		for _, l := range m.Route.Legs {
			lines = append(lines, strings.TrimSpace(strings.Join([]string{l.Line, l.Agency, l.From, l.To}, " ")))
		}
	}
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// mapMarkdown is a map as Markdown reads: its title, its places as links
// to them on OpenStreetMap, and its route in a line.
func mapMarkdown(msgs locales.Messages, m Map, link func(text, to string) string) string {
	var parts []string
	if m.Title != "" {
		parts = append(parts, "**"+escapeCommonMark(m.Title)+"**")
	}
	var places []string
	for i, p := range m.Places {
		name := p.Name
		if name == "" {
			name = fmt.Sprintf("%.5f, %.5f", p.Lat, p.Lng)
		}
		places = append(places, fmt.Sprintf("%d. %s", i+1, link(name, PlaceLink(p))))
	}
	if len(places) > 0 {
		parts = append(parts, strings.Join(places, "\n"))
	}
	if r := m.Route; r != nil {
		parts = append(parts, escapeCommonMark(RouteSummary(msgs, r)+": "+RouteSteps(msgs, r)))
	}
	return strings.Join(parts, "\n\n")
}

// mapFigure writes a map block as a web page and a PDF show it: its route
// and places drawn on a plain ground, and over them, in a web page that
// has one, a frame of the map on its tiles (opts.Map), which shows the
// drawing under it until its tiles load, or without scripts; in a printed
// page, the drawing with what its script draws the map on its tiles
// from, in its place (opts.MapData); then its title, its route in words,
// its places as links to them on OpenStreetMap, numbered as their pins,
// and whose data it shows.
func (w htmlWriter) mapFigure(n *Node) {
	m := MapOf(n)
	msgs := locales.In(w.opts.Lang)
	w.write("<figure", w.attrs(n, "map"), ">")
	src := ""
	if w.opts.Map != nil && !w.opts.Print {
		src = w.opts.Map(n)
	}
	data := ""
	if w.mapped {
		data = w.opts.MapData(n)
	}
	switch drawing := MapSVG(m); {
	case drawing == "":
	case src != "":
		title := m.Title
		if title == "" {
			title = msgs.HostSlashMap()
		}
		w.write(`<div class="mapframe">`, drawing, `<iframe src="`, html.EscapeString(src), `" title="`, html.EscapeString(title),
			`" loading="lazy" referrerpolicy="no-referrer"></iframe></div>`)
	case data != "":
		w.write(`<div class="mapshot" data-map="`, html.EscapeString(data), `">`, drawing, `</div>`)
	default:
		w.write(drawing)
	}
	if m.Title != "" || m.Route != nil {
		w.write("<figcaption>")
		if m.Title != "" {
			w.write("<strong>", html.EscapeString(m.Title), "</strong>")
		}
		if r := m.Route; r != nil {
			w.write(`<span class="route">`, html.EscapeString(RouteSummary(msgs, r)), "<br>", html.EscapeString(RouteSteps(msgs, r)), "</span>")
		}
		w.write("</figcaption>")
	}
	if len(m.Places) > 0 {
		w.write(`<ol class="places">`)
		for _, p := range m.Places {
			name := p.Name
			if name == "" {
				name = fmt.Sprintf("%.5f, %.5f", p.Lat, p.Lng)
			}
			w.write(`<li><a href="`, html.EscapeString(PlaceLink(p)), `" rel="noopener noreferrer">`, html.EscapeString(name), "</a></li>")
		}
		w.write("</ol>")
	}
	w.write(`<p class="map-credit"><a href="`, html.EscapeString(MapCreditLink(m)), `" rel="noopener noreferrer">`, html.EscapeString(MapCredit(msgs, m)), "</a></p></figure>\n")
}

// MapCreditLink is the page that credits a map's data: its router's list
// of sources, as Transitous asks for, or OpenStreetMap's copyright page.
func MapCreditLink(m Map) string {
	if m.Route != nil {
		switch m.Route.Source {
		case RouteFromTransitous:
			return "https://transitous.org/sources/"
		case RouteFromOSRM:
			return "https://routing.openstreetmap.de/about.html"
		}
	}
	return "https://www.openstreetmap.org/copyright"
}

// MapCredit names whose data a map shows, in msgs' language:
// OpenStreetMap's, and the route's router.
func MapCredit(msgs locales.Messages, m Map) string {
	by := "© OpenStreetMap contributors"
	if r := RouteCredit(msgs, m); r != "" {
		by += " · " + r
	}
	return by
}

// RouteCredit names the router a map's route comes from, as MapCredit
// does after OpenStreetMap, as "Routes: Transitous" in msgs' language: ""
// for a map without a route, or with one from a router it does not know.
func RouteCredit(msgs locales.Messages, m Map) string {
	if m.Route == nil {
		return ""
	}
	switch m.Route.Source {
	case RouteFromTransitous:
		return msgs.MapRoutesBy("Transitous")
	case RouteFromOSRM:
		return msgs.MapRoutesBy("FOSSGIS OSRM")
	}
	return ""
}

const (
	mapSVGWidth  = 640
	mapSVGHeight = 300
	mapSVGPad    = 28
	// mapSVGPoints is how many points of a leg the drawing keeps, at most.
	mapSVGPoints = 400
)

// mercator is where a point falls on Web Mercator's square, from 0 to 1.
func mercator(p LatLng) (x, y float64) {
	lat := p.Lat * math.Pi / 180
	return (p.Lng + 180) / 360, (1 - math.Log(math.Tan(lat)+1/math.Cos(lat))/math.Pi) / 2
}

// MapSVG draws a map's route and markers, fitted to its box, on a plain
// ground: as a web page and a PDF show a map, and as the Mac's map shows
// until its tiles load, or without a network.
func MapSVG(m Map) string { return m.Shape().SVG() }

// SVG is MapSVG of the map whose shape s is.
func (s MapShape) SVG() string {
	if !s.OK {
		return ""
	}
	x0, y1 := mercator(s.SW)
	x1, y0 := mercator(s.NE)
	// The box is drawn about its middle, which a box with no width or no
	// height, as a single place's, keeps in the drawing's.
	dx, dy := math.Max(x1-x0, 1e-7), math.Max(y1-y0, 1e-7)
	scale := math.Min((mapSVGWidth-2*mapSVGPad)/dx, (mapSVGHeight-2*mapSVGPad)/dy)
	cx, cy := (x0+x1)/2, (y0+y1)/2
	at := func(p LatLng) (float64, float64) {
		x, y := mercator(p)
		return mapSVGWidth/2 + (x-cx)*scale, mapSVGHeight/2 + (y-cy)*scale
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="mapsvg" viewBox="0 0 %d %d" role="img"><rect class="ground" width="%d" height="%d" rx="8"/>`,
		mapSVGWidth, mapSVGHeight, mapSVGWidth, mapSVGHeight)
	for i, path := range s.Paths {
		if len(path) < 2 {
			continue
		}
		leg := s.Legs[i]
		step := max(len(path)/mapSVGPoints, 1)
		var pts []string
		for k := 0; k < len(path); k += step {
			x, y := at(path[k])
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
		}
		x, y := at(path[len(path)-1])
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
		class, style := "leg", ""
		if leg.Mode == RouteWalk {
			class = "leg walk"
		} else if color := LineColor(leg); color != "" {
			style = ` style="stroke:` + color + `"`
		}
		fmt.Fprintf(&b, `<polyline class="%s"%s points="%s"/>`, class, style, strings.Join(pts, " "))
	}
	for i, p := range s.Markers {
		x, y := at(p.At())
		fmt.Fprintf(&b, `<circle class="pin" cx="%.1f" cy="%.1f" r="6"/><text class="pinno" x="%.1f" y="%.1f">%d</text>`, x, y, x, y+3.5, i+1)
	}
	b.WriteString("</svg>")
	return b.String()
}

// MapSVGCSS draws MapSVG's drawing in a page's colors: --code its ground,
// --link its route and pins, --muted its walks and --bg the pins' rims.
const MapSVGCSS = `.mapsvg{display:block;width:100%;height:auto}
.mapsvg .ground{fill:var(--code)}
.mapsvg .leg{fill:none;stroke:var(--link);stroke-width:4;stroke-linecap:round;stroke-linejoin:round}
.mapsvg .leg.walk{stroke:var(--muted);stroke-width:3;stroke-dasharray:1 7}
.mapsvg .pin{fill:var(--link);stroke:var(--bg);stroke-width:2}
.mapsvg .pinno{fill:var(--bg);font-size:9px;font-weight:700;text-anchor:middle}
`

const mapCSS = `figure.map{margin:1em 0}
` + MapSVGCSS + `figure.map .mapframe{position:relative;height:300px;border-radius:8px;overflow:hidden;background:var(--code)}
figure.map .mapframe .mapsvg,figure.map .mapframe iframe{position:absolute;inset:0;width:100%;height:100%}
figure.map .mapframe iframe{display:block;border:0}
figure.map .mapshot img{display:block;width:100%;height:auto;border-radius:8px}
.mapshot-draw{position:absolute;left:-10000px;top:0;overflow:hidden}
figure.map figcaption{display:flex;flex-direction:column;gap:2px}
figure.map .route{color:var(--muted);font-size:.9em}
figure.map .places{margin:.4em 0;padding-left:1.4em;list-style-type:decimal}
.map-credit{color:var(--muted);font-size:.75em;margin:.2em 0 0}
.map-credit a{color:inherit;text-decoration:none}
`

// EditFences is md with the body of each fenced block of language, as
// ```map, made what edit makes of it, the fences and the rest as they are.
// The lines of a fence of another language, or of a $$ formula, are its
// code, as a page reads them, and stay as they are, a fence written in
// them too. A fence left open runs to md's end, as in a reply still being
// written, and stays as it is.
func EditFences(md, language string, edit func(body string) string) string {
	lines := strings.SplitAfter(md, "\n")
	bare := func(line string) string { return strings.Trim(strings.TrimRight(line, "\r\n"), " \t") }
	var b strings.Builder
	for i := 0; i < len(lines); i++ {
		b.WriteString(lines[i])
		opening := bare(lines[i])
		lang, fenced := CodeFence(opening)
		if !fenced && opening != "$$" {
			continue
		}
		closes := func(line string) bool { return line == "$$" }
		if fenced {
			fence := opening[:len(opening)-len(strings.TrimLeft(opening, "`"))]
			closes = func(line string) bool { return strings.HasPrefix(line, fence) && strings.Trim(line, "` ") == "" }
		}
		end := i + 1
		for end < len(lines) && !closes(bare(lines[end])) {
			end++
		}
		if !fenced || lang != language || end == len(lines) {
			for _, l := range lines[i+1 : min(end+1, len(lines))] {
				b.WriteString(l)
			}
			i = end
			continue
		}
		if edited := edit(strings.TrimSuffix(strings.Join(lines[i+1:end], ""), "\n")); edited != "" {
			b.WriteString(edited + "\n")
		}
		b.WriteString(lines[end])
		i = end
	}
	return b.String()
}

// CompactMapFences is md with each ```map fence's route told in words,
// without its paths: as a model reads its earlier answers again, which
// the paths would fill with letters that say nothing to it.
func CompactMapFences(md string) string {
	return EditFences(md, "map", func(body string) string {
		var m Map
		if json.Unmarshal([]byte(body), &m) != nil || m.Route == nil {
			return body
		}
		raw, _ := json.Marshal(m.View())
		return string(raw)
	})
}

// KeptMapSpec is spec, a block spec that replaces the map prev, with what
// its map leaves out taken from prev: its title and places where the map
// has no such key, and its route where it has none or "", as a model
// renaming a map, or moving its places, leaves the rest as it was. A route
// of null takes it off. Any other spec is as it was.
func KeptMapSpec(prev *Node, spec map[string]any) map[string]any {
	if t, _ := spec["type"].(string); prev.Type != "map" || t != "map" {
		return spec
	}
	given, _ := spec["map"].(map[string]any)
	was := MapOf(prev)
	m := make(map[string]any, len(given)+3)
	for k, v := range given {
		m[k] = v
	}
	if _, ok := m["title"]; !ok && was.Title != "" {
		m["title"] = was.Title
	}
	if _, ok := m["places"]; !ok && len(was.Places) > 0 {
		m["places"] = was.Places
	}
	if v, ok := m["route"]; (!ok || v == "") && was.Route != nil {
		m["route"] = was.Route
	}
	out := make(map[string]any, len(spec))
	for k, v := range spec {
		out[k] = v
	}
	out["map"] = m
	return out
}
