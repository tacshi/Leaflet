package doc

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
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
	if got := MapCredit(Map{Route: r}); got != "© OpenStreetMap contributors · Routes: Transitous" {
		t.Errorf("credit %q", got)
	}
	r.Source = ""
	if got, by := MapCredit(Map{Route: r}), RouteCredit(Map{Route: r}); got != "© OpenStreetMap contributors" || by != "" {
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
