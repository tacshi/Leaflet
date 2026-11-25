package doc

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"leaflet/locales"
)

func TestPathRoundTrip(t *testing.T) {
	path := []LatLng{{38.5, -120.2}, {40.7, -120.95}, {43.252, -126.453}}
	if got := EncodePath(path, 5); got != "_p~iF~ps|U_ulLnnqC_mqNvxq`@" {
		t.Errorf("Google's example encodes as %q", got)
	}
	for _, precision := range []int{5, 6, 7} {
		back, err := DecodePath(EncodePath(path, precision), precision)
		if err != nil || len(back) != len(path) {
			t.Fatalf("precision %d: %v, %v", precision, back, err)
		}
		for i := range path {
			if math.Abs(back[i].Lat-path[i].Lat) > 1e-5 || math.Abs(back[i].Lng-path[i].Lng) > 1e-5 {
				t.Errorf("precision %d: %v, want %v", precision, back[i], path[i])
			}
		}
	}
	for _, bad := range []string{"_p~iF~ps|U_ulL", "\x01\x02", "~~~~~~~~~~~~~~~~"} {
		if _, err := DecodePath(bad, 5); err == nil {
			t.Errorf("%q decoded", bad)
		}
	}
}

func busRoute() *MapRoute {
	walk := EncodePath([]LatLng{{22.3825, 114.1873}, {22.3784, 114.1918}}, PathPrecision)
	bus := EncodePath([]LatLng{{22.3784, 114.1918}, {22.45, 114.17}, {22.55206, 114.15382}}, PathPrecision)
	return &MapRoute{Mode: RouteTransit, Source: RouteFromTransitous, DurationS: 3240, DistanceM: 27_400, Legs: []MapLeg{
		{Mode: "walk", DurationS: 780, DistanceM: 868, Path: walk},
		{Mode: "bus", Line: "B8", Agency: "Citybus", Color: "#0053b9", From: "Jat Min Chuen", To: "Heung Yuen Wai Port", DurationS: 2400, Path: bus},
	}}
}

func TestSpecMap(t *testing.T) {
	spec := map[string]any{"title": "To the border", "places": []any{
		map[string]any{"name": "Sha Tin", "lat": 22.3825, "lng": 114.1873},
		map[string]any{"name": "Heung Yuen Wai", "lat": 22.55206, "lng": 114.15382},
	}, "route": busRoute()}
	raw, _ := json.Marshal(spec)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	n, err := SpecToNode(map[string]any{"type": "map", "map": m}, "", "Leaflet")
	if err != nil {
		t.Fatal(err)
	}
	got := MapOf(n)
	if got.Title != "To the border" || len(got.Places) != 2 || got.Route == nil || got.Route.Legs[1].Line != "B8" {
		t.Fatalf("map %+v", got)
	}
	view := BlockViews(&Content{Children: []*Node{n}})[0]["map"].(map[string]any)
	route := view["route"].(map[string]any)
	if route["duration_min"] != 54 || route["distance_km"] != 27.4 || len(route["legs"].([]map[string]any)) != 2 {
		t.Errorf("view %+v", view)
	}
	if b, _ := json.Marshal(view); strings.Contains(string(b), "path") {
		t.Errorf("the view gives the paths: %s", b)
	}
	sw, ne, ok := got.Bounds()
	if !ok || sw.Lat > 22.3785 || ne.Lat < 22.552 {
		t.Errorf("bounds %v %v", sw, ne)
	}
	for _, bad := range []map[string]any{
		{},
		{"places": []any{map[string]any{"name": "x", "lat": 95.0, "lng": 0.0}}},
		{"places": "Sha Tin"},
		{"route": map[string]any{"mode": "teleport", "legs": []any{}}},
		{"route": map[string]any{"mode": "walk", "legs": []any{map[string]any{"mode": "walk", "path": "not a path"}}}},
	} {
		if _, err := SpecToNode(map[string]any{"type": "map", "map": bad}, "", ""); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

func TestMapMarkersFallBackToTheRoutesEnds(t *testing.T) {
	m := Map{Route: busRoute()}
	markers := m.Markers()
	if len(markers) != 2 || markers[0].Lat != 22.3825 || markers[1].Lat != 22.55206 {
		t.Errorf("markers %+v", markers)
	}
}

func TestMapOutputs(t *testing.T) {
	data := Map{Title: "To the border", Places: []MapPlace{{Name: "Sha Tin", Lat: 22.3825, Lng: 114.1873}}, Route: busRoute()}.Data()
	n := &Node{ID: "m1", Type: "map", Data: data}
	md := PageMarkdown("", &Content{Children: []*Node{n}}, MarkdownOptions{})
	for _, want := range []string{
		"**To the border**",
		"1. [Sha Tin](https://www.openstreetmap.org/?mlat=22.382500&mlon=114.187300#map=17/22.382500/114.187300)",
		"Public transport · 54 min · 27.4 km: Walk 13 min → Bus B8 40 min",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	out := PageHTML("T", &Content{Children: []*Node{n}}, HTMLOptions{})
	for _, want := range []string{`class="map"`, `<svg class="mapsvg"`, `class="leg walk"`, `style="stroke:#0053b9"`, "Routes: Transitous", ".mapsvg .leg{"} {
		if !strings.Contains(out, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	if got := PlainCopy([]*Node{n}, nil); !strings.Contains(got, "Sha Tin") || !strings.Contains(got, "B8 Citybus Jat Min Chuen Heung Yuen Wai Port") {
		t.Errorf("plain copy %q", got)
	}
}

func TestMapFrameShowsOverItsDrawing(t *testing.T) {
	m := Map{Title: "南京", Places: []MapPlace{{Name: "① 南京博物院", Lat: 32.0423, Lng: 118.8199}, {Name: "Off the map", Lat: 95, Lng: 0},
		{Name: "新街口", Lat: 32.0461, Lng: 118.7922}, {Lat: 32.0602, Lng: 118.8485}}}
	n := &Node{ID: "m1", Type: "map", Data: m.Data()}
	asked := 0
	frame := func(n *Node) string { asked++; return `/tok/m/` + n.ID + `?"><b>` }
	page := &Content{Children: []*Node{n}}
	out := PageHTML("T", page, HTMLOptions{Map: frame})
	figure := out[strings.Index(out, `<figure id="b-m1"`):]
	svg, iframe := strings.Index(figure, `<svg class="mapsvg"`), strings.Index(figure, `<iframe src="/tok/m/m1?&#34;&gt;&lt;b&gt;" title="南京"`)
	if !strings.HasPrefix(figure[strings.Index(figure, ">")+1:], `<div class="mapframe"><svg`) || svg < 0 || iframe < svg ||
		!strings.Contains(figure, `loading="lazy" referrerpolicy="no-referrer"></iframe></div><figcaption><strong>南京</strong>`) {
		t.Errorf("the frame is not over the map's drawing, before its caption: %s", figure)
	}
	if strings.Contains(out, `<b>`) || !strings.Contains(out, "figure.map .mapframe iframe{") {
		t.Errorf("the frame's address or look: %s", out)
	}
	// The list numbers the places as the drawing's pins and the map's are.
	pins := regexp.MustCompile(`<text class="pinno"[^>]*>(\d+)</text>`).FindAllStringSubmatch(figure, -1)
	items := regexp.MustCompile(`<li><a href="[^"]*" rel="noopener noreferrer">([^<]*)</a></li>`).FindAllStringSubmatch(figure, -1)
	markers := MapOf(n).Markers()
	if len(pins) != 3 || len(items) != 3 || len(markers) != 3 || !strings.Contains(out, "figure.map .places{margin:.4em 0;padding-left:1.4em;list-style-type:decimal}") {
		t.Fatalf("pins %v, places %v, markers %v", pins, items, markers)
	}
	for i, want := range [][2]string{{"① 南京博物院", "① 南京博物院"}, {"新街口", "新街口"}, {"32.06020, 118.84850", ""}} {
		if pins[i][1] != strconv.Itoa(i+1) || items[i][1] != want[0] || markers[i].Name != want[1] {
			t.Errorf("place %d: pin %s, listed %q, marked %q", i+1, pins[i][1], items[i][1], markers[i].Name)
		}
	}

	// A printed page, and one without frames, show the drawing alone.
	for _, opts := range []HTMLOptions{{Map: frame, Print: true}, {Map: func(*Node) string { return "" }}, {}} {
		asked = 0
		if out := PageHTML("T", page, opts); strings.Contains(out, "<iframe") || strings.Contains(out, `class="mapframe"`) || !strings.Contains(out, `<figure id="b-m1" data-block="m1" class="map"><svg class="mapsvg"`) {
			t.Errorf("print %v: a frame, or no drawing: %s", opts.Print, out)
		}
		if opts.Print && asked != 0 {
			t.Error("a printed page asks for a map's frame")
		}
	}
}

func TestPrintedMapIsDrawnByItsScript(t *testing.T) {
	m := Map{Places: []MapPlace{{Name: "新街口", Lat: 32.0461, Lng: 118.7922}}}
	page := &Content{Children: []*Node{{ID: "m1", Type: "map", Data: m.Data()}, {ID: "m2", Type: "map", Data: map[string]any{"title": "Nowhere"}}}}
	script := func(name string) string { return map[string]string{"map": "drawMaps()"}[name] }
	data := func(n *Node) string {
		if n.ID == "m2" {
			return ""
		}
		return `{"places":"<b>"}`
	}
	out := PageHTML("T", page, HTMLOptions{Print: true, MapData: data, ScriptSource: script, Map: func(*Node) string { return "/m" }})
	if !strings.Contains(out, `<figure id="b-m1" data-block="m1" class="map"><div class="mapshot" data-map="{&#34;places&#34;:&#34;&lt;b&gt;&#34;}"><svg class="mapsvg"`) ||
		!strings.Contains(out, `<figure id="b-m2" data-block="m2" class="map"><figcaption>`) || !strings.Contains(out, "<script type=\"module\">drawMaps()</script>") ||
		strings.Contains(out, "<iframe") {
		t.Errorf("printed maps: %s", out)
	}
	// Without the script, or what it draws from, the drawing stays alone.
	for _, opts := range []HTMLOptions{{Print: true, MapData: data}, {Print: true, ScriptSource: script}, {MapData: data, ScriptSource: script}} {
		if out := PageHTML("T", page, opts); strings.Contains(out, `class="mapshot"`) || strings.Contains(out, "drawMaps") {
			t.Errorf("print %v: %s", opts.Print, out)
		}
	}
}

func TestMapFenceMakesAMap(t *testing.T) {
	m := Map{Places: []MapPlace{{Name: "Sha Tin", Lat: 22.3825, Lng: 114.1873}}}
	raw, _ := json.Marshal(m)
	nodes := MarkdownBlocks("```map\n" + string(raw) + "\n```\n\n```map\n{\"route\": \"r1\"}\n```")
	if len(nodes) != 2 || nodes[0].Type != "map" || nodes[1].Type != "code" {
		t.Fatalf("nodes %+v", nodes)
	}
}

func TestDecodePathKeepsToAPathsPrecision(t *testing.T) {
	path := EncodePath([]LatLng{{22.3825, 114.1873}, {22.55206, 114.15382}}, 7)
	for _, precision := range []int{-1, 11, 400} {
		if got, err := DecodePath(path, precision); err == nil {
			t.Errorf("precision %d read as %v", precision, got)
		}
	}
}

func TestRouteGoesByAWayOfGoing(t *testing.T) {
	r := busRoute()
	r.Mode = "bus"
	if _, err := MapData(map[string]any{"route": r}); err == nil {
		t.Error("a route going by bus, a leg's way of going")
	}
}

func TestMapSVGDrawsAPlaceInItsMiddle(t *testing.T) {
	svg := MapSVG(Map{Places: []MapPlace{{Name: "Sha Tin", Lat: 22.3825, Lng: 114.1873}}})
	if !strings.Contains(svg, `<circle class="pin" cx="320.0" cy="150.0"`) {
		t.Errorf("a single place drawn off the middle: %s", svg)
	}
}

func TestMapSVGDrawsOnlyALinesColor(t *testing.T) {
	r := busRoute()
	r.Legs[1].Color = `red" onload="alert(1)`
	if svg := MapSVG(Map{Route: r}); strings.Contains(svg, "onload") || strings.Contains(svg, "style=") {
		t.Errorf("a leg's color that is none drawn: %s", svg)
	}
}

func TestRouteCredit(t *testing.T) {
	r := busRoute()
	en, ja := locales.In("en"), locales.In("ja")
	if got := MapCredit(en, Map{Route: r}); got != "© OpenStreetMap contributors · Routes: Transitous" {
		t.Errorf("credit %q", got)
	}
	if got := RouteCredit(ja, Map{Route: r}); got != "経路：Transitous" {
		t.Errorf("credit in Japanese %q", got)
	}
	r.Source = ""
	if got, by := MapCredit(en, Map{Route: r}), RouteCredit(en, Map{Route: r}); got != "© OpenStreetMap contributors" || by != "" {
		t.Errorf("a route from no router known: %q, %q", got, by)
	}
}

func TestEditFencesLeavesOtherCodeAsItIs(t *testing.T) {
	md := "````markdown\n```map\n{\"route\": \"r1\"}\n```\n````\n\n$$\n```map\nx\n```\n$$\n\n```map\n{\"route\": \"r1\"}\n```\n\nAfter."
	var edited []string
	out := EditFences(md, "map", func(body string) string {
		edited = append(edited, body)
		return "{}"
	})
	if len(edited) != 1 || edited[0] != `{"route": "r1"}` {
		t.Errorf("edited %q", edited)
	}
	if want := strings.Replace(md, "```map\n{\"route\": \"r1\"}\n```\n\nAfter.", "```map\n{}\n```\n\nAfter.", 1); out != want {
		t.Errorf("made\n%s\nwant\n%s", out, want)
	}
	// A fence of another language left open runs to the end: what follows is its code.
	if out := EditFences("```go\nx\n```map\n{}\n```", "map", func(string) string { return "edited" }); strings.Contains(out, "edited") {
		t.Errorf("edited the code of an open fence: %q", out)
	}
	// A fence's closing line opens none, whatever language is edited.
	edited = nil
	EditFences("```\na\n```\nb\n```\nc\n```", "", func(body string) string {
		edited = append(edited, body)
		return body
	})
	if strings.Join(edited, ",") != "a,c" {
		t.Errorf("plain fences' bodies %q", edited)
	}
}

func TestMapPlaceNeedsItsLatAndLng(t *testing.T) {
	for _, place := range []map[string]any{
		{"name": "Sha Tin"},
		{"name": "Sha Tin", "lat": 22.3825},
		{"name": "Sha Tin", "lat": 22.3825, "lng": nil},
	} {
		_, err := MapData(map[string]any{"places": []any{place}})
		if err == nil || !strings.Contains(err.Error(), "Place 1 needs lat and lng") || !strings.Contains(err.Error(), "find_place") {
			t.Errorf("%v: %v", place, err)
		}
	}
	if _, err := MapData(map[string]any{"places": []any{map[string]any{"name": "Null Island", "lat": 0.0, "lng": 0.0}}}); err != nil {
		t.Errorf("a place at 0, 0: %v", err)
	}
	// A map kept with such a place leaves it out.
	n := &Node{Type: "map", Data: map[string]any{"places": []any{
		map[string]any{"name": "Sha Tin"}, map[string]any{"name": "Tai Wai", "lat": 22.3727, "lng": 114.1786},
	}}}
	if got := MapOf(n).Places; len(got) != 1 || got[0].Name != "Tai Wai" {
		t.Errorf("places %+v", got)
	}
}

func TestMapOfLeavesOutWhatItCannotShow(t *testing.T) {
	r := busRoute()
	n := &Node{Type: "map", Data: Map{Title: "x", Route: r}.Data()}
	if m := MapOf(n); !m.Equal(Map{Title: "x", Route: busRoute()}) {
		t.Errorf("read as %+v", m)
	}
	// A leg whose path does not read draws nothing; the rest of the route
	// stays.
	n.Data["route"].(map[string]any)["legs"].([]any)[0].(map[string]any)["path"] = "\x01\x02"
	m := MapOf(n)
	if m.Route == nil || len(m.Route.Legs) != 2 {
		t.Fatalf("route %+v", m.Route)
	}
	if s := m.Shape(); !s.OK || s.Paths[0] != nil || len(s.Paths[1]) != 3 || len(s.Markers) != 2 || s.Markers[0].Lat != 22.3784 {
		t.Errorf("shape %+v", s)
	}
	for _, bad := range []func(r map[string]any){
		func(r map[string]any) { r["mode"] = "teleport" },
		func(r map[string]any) { r["legs"] = []any{} },
		func(r map[string]any) { r["legs"].([]any)[1].(map[string]any)["mode"] = "rocket" },
		func(r map[string]any) { r["duration_s"] = -1.0 },
	} {
		n := &Node{Type: "map", Data: Map{Route: busRoute()}.Data()}
		bad(n.Data["route"].(map[string]any))
		if m := MapOf(n); m.Route != nil {
			t.Errorf("a route that is none read as %+v", m.Route)
		}
	}
	// A block whose data holds its map as other than JSON's reads alike.
	if m := MapOf(&Node{Type: "map", Data: map[string]any{"route": busRoute()}}); !m.Equal(Map{Route: busRoute()}) {
		t.Errorf("read through JSON as %+v", m)
	}
}

func TestMapEqual(t *testing.T) {
	a, b := Map{Title: "x", Route: busRoute()}, Map{Title: "x", Route: busRoute()}
	if !a.Equal(b) || a.Equal(Map{Title: "x"}) {
		t.Error("maps alike differ, or a map without its route is alike")
	}
	b.Route.Legs[1].Line = "B9"
	if a.Equal(b) {
		t.Error("a leg's line changed and the maps are alike")
	}
}

// longRoute is a map of a route of 20 legs, 20,000 points in all.
func longRoute() *Node {
	var legs []any
	for l := range 20 {
		var pts []LatLng
		for i := range 1000 {
			pts = append(pts, LatLng{Lat: 22.3 + float64(l*1000+i)*1e-5, Lng: 114.1 + float64(i%7)*1e-5})
		}
		legs = append(legs, map[string]any{"mode": "bus", "duration_s": 60.0, "path": EncodePath(pts, PathPrecision)})
	}
	data, err := MapData(map[string]any{"title": "x", "route": map[string]any{"mode": "transit", "duration_s": 600.0, "legs": legs}})
	if err != nil {
		panic(err)
	}
	return &Node{ID: "m", Type: "map", Data: data}
}

// A map is read as often as a frame draws it: its paths are read only as
// they are drawn.
func BenchmarkMapOfLongRoute(b *testing.B) {
	n := longRoute()
	for b.Loop() {
		_ = MapOf(n)
	}
}

func BenchmarkMapShapeOfLongRoute(b *testing.B) {
	m := MapOf(longRoute())
	for b.Loop() {
		_ = m.Shape()
	}
}
