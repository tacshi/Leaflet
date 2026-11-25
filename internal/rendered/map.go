package rendered

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"

	"leaflet/internal/doc"
)

// MapLibre GL JS draws map blocks on the Mac and Windows: its module and its
// worker's, bundled (third_party/README.md), so a map loads nothing but
// its tiles, from OpenFreeMap.

//go:embed third_party/maplibre/maplibre-gl.mjs
var maplibreJS string

//go:embed third_party/maplibre/maplibre-gl-worker.mjs
var maplibreWorkerJS string

//go:embed third_party/maplibre/maplibre-gl.css
var maplibreCSS string

// TileHost is the server a map's style, tiles, glyphs and sprites come from.
const TileHost = "https://tiles.openfreemap.org"

// MapOrigin is the address a map's page loads at: a secure context of
// its own, which its worker needs, from which tiles come across origins.
const MapOrigin = "https://map.app.capyvibe.leaflet/"

// MapStyles are the map's looks, light and dark.
var MapStyles = map[bool]string{false: TileHost + "/styles/liberty", true: TileHost + "/styles/dark"}

// withoutSourceMap is a bundle without its comment naming a source map,
// which a page built of it has none of.
func withoutSourceMap(js string) string {
	if i := strings.LastIndex(js, "\n//# sourceMappingURL="); i >= 0 {
		return js[:i] + "\n"
	}
	return js
}

// maplibreModule is MapLibre's module as a script of the page: its exports
// the fields of maplibregl.
var maplibreModule = func() string {
	js := withoutSourceMap(maplibreJS)
	m := regexp.MustCompile(`export\{([^}]*)\};?\s*$`).FindStringSubmatchIndex(js)
	if m == nil {
		return "const maplibregl = null;"
	}
	var fields []string
	for _, part := range strings.Split(js[m[2]:m[3]], ",") {
		local, name, ok := strings.Cut(strings.TrimSpace(part), " as ")
		if !ok {
			local, name = strings.TrimSpace(part), strings.TrimSpace(part)
		}
		fields = append(fields, name+":"+local)
	}
	return js[:m[0]] + "\nconst maplibregl = {" + strings.Join(fields, ",") + "};\n"
}()

// MapLibreLoads reports that the bundled module made maplibregl with its
// Map and its worker's address in it.
func MapLibreLoads() bool {
	return strings.Contains(maplibreModule, "Map:") && strings.Contains(maplibreModule, "setWorkerUrl:")
}

// mapScript draws the map its page's data says: its route's legs, colored
// as their lines, walks dotted, and its places pinned, numbered and named,
// over the style's tiles, which fade in once they load; the plain
// drawing of the map under it shows meanwhile, and without a network.
var mapScript = maplibreModule + `
{
  const data = JSON.parse(document.getElementById("map-data").textContent);
  const worker = JSON.parse(document.getElementById("map-worker").textContent);
  maplibregl.setWorkerUrl(URL.createObjectURL(new Blob([worker], {type: "text/javascript"})));
  const box = document.getElementById("map");
  const map = new maplibregl.Map({
    container: box, style: data.style, bounds: data.bounds,
    fitBoundsOptions: {padding: data.full ? {top: 64, bottom: 96, left: 64, right: 96} : {top: 36, bottom: 52, left: 36, right: 64}, maxZoom: 16},
    attributionControl: {compact: true, customAttribution: data.credit},
    dragRotate: false, pitchWithRotate: false, touchPitch: false,
  });
  map.addControl(new maplibregl.NavigationControl({showCompass: false}), "top-right");
  map.on("load", () => {
    map.addSource("legs", {type: "geojson", data: {type: "FeatureCollection", features: data.legs.map((l) => ({
      type: "Feature", properties: {color: l.color, walk: l.walk}, geometry: {type: "LineString", coordinates: l.coords},
    }))}});
    const round = {"line-cap": "round", "line-join": "round"};
    map.addLayer({id: "legs-casing", type: "line", source: "legs", filter: ["!", ["get", "walk"]], layout: round,
      paint: {"line-color": data.casing, "line-width": 7}});
    map.addLayer({id: "legs", type: "line", source: "legs", filter: ["!", ["get", "walk"]], layout: round,
      paint: {"line-color": ["get", "color"], "line-width": 4}});
    map.addLayer({id: "walks", type: "line", source: "legs", filter: ["get", "walk"], layout: round,
      paint: {"line-color": data.walk, "line-width": 3.5, "line-dasharray": [0.1, 2]}});
    data.places.forEach((p, i) => {
      const pin = document.createElement("div");
      pin.className = "pin";
      const n = document.createElement("span");
      n.className = "n";
      n.textContent = String(i + 1);
      pin.append(n);
      if (p.name) {
        const name = document.createElement("span");
        name.className = "name";
        name.textContent = p.name;
        pin.append(name);
      }
      new maplibregl.Marker({element: pin, anchor: "left", offset: [-11, 0]}).setLngLat([p.lng, p.lat]).addTo(map);
    });
    // The credits start folded behind their button, as the map's caption names its data's sources.
    const credits = box.querySelector(".maplibregl-ctrl-attrib.maplibregl-compact-show");
    if (credits) {
      credits.setAttribute("open", "");
      credits.classList.remove("maplibregl-compact-show");
    }
    box.classList.add("ready");
  });
  if (data.full) addEventListener("keydown", (e) => {
    if (e.key === "Escape" && window.mygo) window.mygo.call("Maps.Exit", data.key);
  });
}
`

// MapCSP lets a map's page run its script and its worker, made of a blob,
// and reach the tile server alone.
var MapCSP = ScriptsCSP(mapScript) + "; worker-src blob:; child-src blob:; img-src data: blob: " + TileHost +
	"; connect-src " + TileHost + "; font-src data:"

// MapPage is the page that shows a map block on the Mac and Windows: m on
// its tiles, light or dark, its route in its lines' colors, as a block of
// a page, where a scroll goes to the page and a pinch or its buttons zoom
// it, or full, filling the window, where a scroll zooms it too and Escape
// asks the app, by its key, to leave it.
func MapPage(m doc.Map, key string, dark, full bool, bg, accent string) string {
	sw, ne, ok := m.Bounds()
	if !ok {
		return ""
	}
	legs := []map[string]any{}
	for i, path := range m.Paths() {
		leg := m.Route.Legs[i]
		coords := make([][2]float64, len(path))
		for k, p := range path {
			coords[k] = [2]float64{p.Lng, p.Lat}
		}
		color := doc.LineColor(leg)
		if color == "" {
			color = accent
		} else if dark {
			color = lighter(color, 0.3)
		}
		legs = append(legs, map[string]any{"color": color, "walk": leg.Mode == doc.RouteWalk, "coords": coords})
	}
	casing, walk := "#ffffff", "#5b616e"
	if dark {
		casing, walk = "#16181d", "#b4bac4"
	}
	// The credits name the route's router after the tiles' own, which name
	// OpenStreetMap already.
	data := map[string]any{
		"style": MapStyles[dark], "bounds": [2][2]float64{{sw.Lng, sw.Lat}, {ne.Lng, ne.Lat}}, "full": full,
		"places": m.Markers(), "legs": legs, "credit": doc.RouteCredit(m),
		"casing": casing, "walk": walk, "key": key,
	}
	theme := "light"
	if dark {
		theme = "dark"
	}
	return `<!doctype html><html data-theme="` + theme + `"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<meta http-equiv="Content-Security-Policy" content="` + MapCSP + `"><style>` + maplibreCSS + mapStyle(bg, accent, dark) +
		`</style></head><body><div id="ground">` + doc.MapSVG(m) + `</div><div id="map"></div>` +
		dataScript("map-data", data) + dataScript("map-worker", withoutSourceMap(maplibreWorkerJS)) +
		`<script type="module">` + mapScript + `</script></body></html>`
}

// lighter is the hex color mixed with white by part, so that a line's own
// color, as dark as many are, shows on a dark map.
func lighter(hex string, part float64) string {
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return hex
	}
	mix := func(c int) int { return c + int(float64(255-c)*part+0.5) }
	return fmt.Sprintf("#%02x%02x%02x", mix(r), mix(g), mix(b))
}

// mapDarkControls are MapLibre's buttons and credits in a dark map's
// colors, which it draws light.
const mapDarkControls = `
.maplibregl-ctrl-group{background:#23272e}
.maplibregl-ctrl-group button+button{border-top-color:#3a404a}
.maplibregl-ctrl button .maplibregl-ctrl-icon,.maplibregl-ctrl-attrib-button{filter:invert(1)}
.maplibregl-ctrl.maplibregl-ctrl-attrib,.maplibregl-ctrl-attrib.maplibregl-compact{background:rgba(35,39,46,.85);color:#c0c5ce}
.maplibregl-ctrl-attrib a{color:#c0c5ce}`

func mapStyle(bg, accent string, dark bool) string {
	ink, paper := "#16181d", "#ffffff"
	if dark {
		ink, paper = "#e9ebef", "#23272e"
	}
	return doc.MapSVGCSS + `
html,body{margin:0;height:100%;background:` + bg + `;overflow:hidden;font:12px -apple-system,system-ui,"Segoe UI",sans-serif}
#ground,#map{position:absolute;inset:0}
#ground{display:flex;align-items:center;justify-content:center}
#ground svg{width:100%;height:100%}
#ground .ground{fill:transparent}
:root{--code:` + bg + `;--link:` + accent + `;--muted:#8a909b;--bg:` + paper + `}
#map{opacity:0;transition:opacity .25s}
#map.ready{opacity:1}
.pin{display:flex;align-items:center;gap:5px;pointer-events:none}
.pin .n{flex:none;width:22px;height:22px;border-radius:50%;background:` + accent + `;color:#fff;font-weight:700;font-size:11px;
display:flex;align-items:center;justify-content:center;box-shadow:0 0 0 2px ` + paper + `,0 1px 4px rgba(0,0,0,.3)}
.pin .name{max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:600;color:` + ink + `;
background:` + paper + `;padding:2px 6px;border-radius:6px;box-shadow:0 1px 3px rgba(0,0,0,.2)}
.maplibregl-ctrl-attrib{font-size:10px}` + map[bool]string{true: mapDarkControls}[dark]
}
