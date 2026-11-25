package rendered

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/locales"
)

func TestMapPage(t *testing.T) {
	if !MapLibreLoads() {
		t.Fatal("MapLibre's module made no maplibregl")
	}
	path := doc.EncodePath([]doc.LatLng{{Lat: 22.3784, Lng: 114.1918}, {Lat: 22.55206, Lng: 114.15382}}, doc.PathPrecision)
	m := doc.Map{Places: []doc.MapPlace{{Name: "</script><b>x</b>", Lat: 22.3784, Lng: 114.1918}},
		Route: &doc.MapRoute{Mode: "transit", Source: doc.RouteFromTransitous, DurationS: 2400, Legs: []doc.MapLeg{{Mode: "bus", Line: "B8", Color: "#0053b9", DurationS: 2400, Path: path}}}}
	en := locales.In("en")
	page := MapPage(en, m, "block:m", true, false, "#23272e", "#8a97ff")
	if ja := MapPage(locales.In("ja"), m, "block:m", true, false, "#23272e", "#8a97ff"); !strings.Contains(ja, `"credit":"経路：Transitous"`) {
		t.Error("the page's credits are not in its language")
	}
	script := mapParts().script
	sum := sha256.Sum256([]byte(script))
	for _, want := range []string{
		"'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'",
		"worker-src blob:", "connect-src " + TileHost, `<script type="module">` + script + `</script>`,
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
	if MapPage(en, doc.Map{}, "block:m", false, false, "#fff", "#00f") != "" {
		t.Error("a page for a map with nothing on it")
	}
	// The tiles credit OpenStreetMap themselves: a route from no router
	// known adds nothing to them, and a color that is none draws nothing.
	m.Route.Source, m.Route.Legs[0].Color = "", `#0053b9"><b>x</b>`
	page = MapPage(en, m, "block:m", false, false, "#fff", "#00f")
	if !strings.Contains(page, `"credit":""`) || strings.Contains(page, "OpenStreetMap contributors") {
		t.Error("the page credits OpenStreetMap twice")
	}
	if strings.Contains(page, "<b>x</b>") || !strings.Contains(page, `"color":"#00f"`) {
		t.Error("a leg's color that is none drawn")
	}
}

func TestWebMapPage(t *testing.T) {
	path := doc.EncodePath([]doc.LatLng{{Lat: 32.0423, Lng: 118.8199}, {Lat: 32.0461, Lng: 118.7922}}, doc.PathPrecision)
	m := doc.Map{Places: []doc.MapPlace{{Name: "</script><b>x</b>", Lat: 32.0423, Lng: 118.8199}, {Name: "新街口", Lat: 32.0461, Lng: 118.7922}},
		Route: &doc.MapRoute{Mode: "transit", Source: doc.RouteFromTransitous, DurationS: 900, Legs: []doc.MapLeg{
			{Mode: "subway", Line: "2", Color: "#0053b9", DurationS: 600, Path: path}, {Mode: "walk", DurationS: 300, Path: path}}}}
	files := MapFiles{Script: "/tok/map.1.js", Worker: "/tok/map-worker.2.js", Style: `/tok/map.3.css?"><b>`}
	page := WebMapPage(locales.In("zh-Hans"), m, files, "#3446c9", "#4f5fd9")
	for _, want := range []string{
		`<link rel="stylesheet" href="/tok/map.3.css?&#34;&gt;&lt;b&gt;">`, `<script type="module" src="/tok/map.1.js"></script></body></html>`,
		`"worker":"/tok/map-worker.2.js"`, `"web":true`, `"style":"https://tiles.openfreemap.org/styles/liberty"`,
		`"dark":{"accent":"#4f5fd9","casing":"#16181d","colors":["#4d87ce","#4f5fd9"],"style":"https://tiles.openfreemap.org/styles/dark","walk":"#b4bac4"}`,
		`"color":"#0053b9"`, `"color":"#3446c9"`, `"credit":"路线：Transitous"`, `"CooperativeGesturesHandler.MacHelpText":"按住 ⌘ 键滚动即可缩放地图"`,
		`"CooperativeGesturesHandler.MobileHelpText":"用双指移动地图"`, `<meta name="color-scheme" content="light dark"><meta name="referrer" content="no-referrer">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %.120q", want)
		}
	}
	// It holds no script nor style of its own, which its policy lets in from
	// the page's server alone, and a place's name closes none of its data.
	if strings.Contains(page, "</script><b>") || strings.Count(page, "<script") != 2 || strings.Count(page, "</script>") != 2 ||
		strings.Contains(page, "<style") || strings.Contains(page, "<svg") || strings.Contains(page, "maplibregl") {
		t.Errorf("the page holds more than its data: %.400s", page)
	}
	for _, want := range []string{"default-src 'none'", "script-src 'self';", "style-src 'self';", "worker-src blob:", "connect-src 'self' " + TileHost + ";",
		"img-src data: blob: " + TileHost + ";", "frame-ancestors 'self'"} {
		if !strings.Contains(WebMapCSP, want) {
			t.Errorf("the policy lacks %q", want)
		}
	}
	if strings.Contains(WebMapCSP, "unsafe") || strings.Contains(WebMapCSP, "sha256") {
		t.Errorf("the policy lets in scripts or styles of the page's: %s", WebMapCSP)
	}
	f := WebMapFiles()
	if !strings.Contains(f.Script, "setWorkerUrl:") || !strings.Contains(f.Script, "cooperativeGestures: true") || !strings.HasSuffix(f.Script, webMapDraws) ||
		!strings.Contains(f.Worker, "self.worker=") || !strings.Contains(f.Style, ".maplibregl-map{") || !strings.Contains(f.Style, "@media (prefers-color-scheme:dark){:root{--ink:#e9ebef") {
		t.Error("the map's files lack MapLibre or what draws the map")
	}
	for _, s := range []string{f.Script, f.Worker, f.Style} {
		if strings.Contains(s, "sourceMappingURL") {
			t.Error("a map's file names a source map it has none of")
		}
	}
	if WebMapPage(locales.In("en"), doc.Map{}, files, "#fff", "#000") != "" {
		t.Error("a page for a map with nothing on it")
	}
}

func TestPrintMap(t *testing.T) {
	path := doc.EncodePath([]doc.LatLng{{Lat: 32.0423, Lng: 118.8199}, {Lat: 32.0461, Lng: 118.7922}}, doc.PathPrecision)
	m := doc.Map{Places: []doc.MapPlace{{Name: "新街口", Lat: 32.0461, Lng: 118.7922}}, Route: &doc.MapRoute{Mode: "transit", DurationS: 900, Legs: []doc.MapLeg{
		{Mode: "subway", Line: "2", Color: "#c4161c", DurationS: 600, Path: path}, {Mode: "walk", DurationS: 300, Path: path}}}}
	data := PrintMapData(locales.In("en"), m)
	for _, want := range []string{`"style":"https://tiles.openfreemap.org/styles/liberty"`, `"color":"#c4161c"`, `"color":""`, `"name":"新街口"`, `"walk":"#5b616e"`} {
		if !strings.Contains(data, want) {
			t.Errorf("the map's data lacks %q: %s", want, data)
		}
	}
	if PrintMapData(locales.In("en"), doc.Map{Title: "Nowhere"}) != "" {
		t.Error("data for a map with nothing on it")
	}
	js := PrintMapJS()
	sum := sha256.Sum256([]byte(js))
	if !strings.Contains(PrintMapCSP(), "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") || !strings.Contains(PrintMapCSP(), "connect-src blob: data: "+TileHost+";") {
		t.Errorf("the policy doesn't let the script run, or reach the tiles: %s", PrintMapCSP())
	}
	for _, want := range []string{"setWorkerUrl:", "function leafletLegs(", "window.requestAnimationFrame = ", "window.leafletMapsDrawn = Promise.race(", ", 15000)",
		"preserveDrawingBuffer: true", `toDataURL("image/jpeg"`, `img.decoding = "sync"`, "Math.min(2, 1400 / w)", "new DOMParser().parseFromString(a"} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	if w := PrintMapWorker(); !strings.HasPrefix(w, `<script type="application/json" id="map-worker">"`) || strings.Count(w, "</script>") != 1 || strings.Contains(w, "sourceMappingURL") {
		t.Errorf("the worker's data: %.100s", w)
	}
}

func TestLighter(t *testing.T) {
	for in, want := range map[string]string{"#0053b9": "#4d87ce", "#ffffff": "#ffffff", "#000000": "#4d4d4d", "red": "red"} {
		if got := lighter(in, 0.3); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
