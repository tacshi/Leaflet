package rendered

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"leaflet/internal/doc"
)

func TestMapPage(t *testing.T) {
	if !MapLibreLoads() {
		t.Fatal("MapLibre's module made no maplibregl")
	}
	path := doc.EncodePath([]doc.LatLng{{Lat: 22.3784, Lng: 114.1918}, {Lat: 22.55206, Lng: 114.15382}}, doc.PathPrecision)
	m := doc.Map{Places: []doc.MapPlace{{Name: "</script><b>x</b>", Lat: 22.3784, Lng: 114.1918}},
		Route: &doc.MapRoute{Mode: "transit", Source: doc.RouteFromTransitous, DurationS: 2400, Legs: []doc.MapLeg{{Mode: "bus", Line: "B8", Color: "#0053b9", DurationS: 2400, Path: path}}}}
	page := MapPage(m, "block:m", true, false, "#23272e", "#8a97ff")
	sum := sha256.Sum256([]byte(mapScript))
	for _, want := range []string{
		"'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'",
		"worker-src blob:", "connect-src " + TileHost, `<script type="module">` + mapScript + `</script>`,
		`"style":"https://tiles.openfreemap.org/styles/dark"`, `"color":"#4d87ce"`, `"credit":"Routes: Transitous"`, ".maplibregl-ctrl-group{background:#23272e}",
		`<svg class="mapsvg"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %.80q", want)
		}
	}
	if strings.Contains(page, "</script><b>") || strings.Count(page, "</script>") != 3 {
		t.Error("a place's name closes a script")
	}
	if strings.Contains(page, "sourceMappingURL") {
		t.Error("the page names a source map it has none of")
	}
	if MapPage(doc.Map{}, "block:m", false, false, "#fff", "#00f") != "" {
		t.Error("a page for a map with nothing on it")
	}
	// The tiles credit OpenStreetMap themselves: a route from no router
	// known adds nothing to them, and a color that is none draws nothing.
	m.Route.Source, m.Route.Legs[0].Color = "", `#0053b9"><b>x</b>`
	page = MapPage(m, "block:m", false, false, "#fff", "#00f")
	if !strings.Contains(page, `"credit":""`) || strings.Contains(page, "OpenStreetMap contributors") {
		t.Error("the page credits OpenStreetMap twice")
	}
	if strings.Contains(page, "<b>x</b>") || !strings.Contains(page, `"color":"#00f"`) {
		t.Error("a leg's color that is none drawn")
	}
}

func TestLighter(t *testing.T) {
	for in, want := range map[string]string{"#0053b9": "#4d87ce", "#ffffff": "#ffffff", "#000000": "#4d4d4d", "red": "red"} {
		if got := lighter(in, 0.3); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
