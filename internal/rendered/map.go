package rendered

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	"leaflet/internal/doc"
	"leaflet/locales"
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
func maplibreModule() string {
	code, exports := moduleExports(withoutSourceMap(maplibreJS))
	if exports == nil {
		return "const maplibregl = null;"
	}
	fields := make([]string, len(exports))
	for i, e := range exports {
		fields[i] = e.name + ":" + e.local
	}
	return code + "\nconst maplibregl = {" + strings.Join(fields, ",") + "};\n"
}

// MapLibreLoads reports that the bundled module made maplibregl with its
// Map and its worker's address in it.
func MapLibreLoads() bool {
	js := strings.TrimSuffix(mapParts().script, mapLegs+mapDraw+mapDraws)
	return strings.Contains(js, "Map:") && strings.Contains(js, "setWorkerUrl:")
}

// mapPageParts are what every map's page holds alike: the script that
// draws it, MapLibre's module then mapLegs, mapDraw and mapDraws; its
// head, from the policy that lets the script and its worker run to
// MapLibre's styles; and its tail, MapLibre's worker as data of the page,
// then the script.
type mapPageParts struct{ script, head, tail string }

// mapParts makes a map's page's parts at the first page, once: they are
// megabytes, which each page that shows a map would build again.
var mapParts = sync.OnceValue(func() mapPageParts {
	script := maplibreModule() + mapLegs + mapDraw + mapDraws
	return mapPageParts{
		script: script,
		head:   `<meta http-equiv="Content-Security-Policy" content="` + mapCSP(script) + `"><style>` + maplibreCSS,
		tail:   dataScript("map-worker", withoutSourceMap(maplibreWorkerJS)) + `<script type="module">` + script + `</script></body></html>`,
	}
})

// mapLegs draws a map's route's legs, colored as their lines over a
// casing, walks dotted.
const mapLegs = `
function leafletLegs(map, data) {
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
}
`

// mapDraws draws the map of the app's page, its worker in the page.
const mapDraws = `
{
  const data = JSON.parse(document.getElementById("map-data").textContent);
  const worker = JSON.parse(document.getElementById("map-worker").textContent);
  leafletMap(data, URL.createObjectURL(new Blob([worker], {type: "text/javascript"})));
  if (data.full) addEventListener("keydown", (e) => {
    if (e.key === "Escape" && window.mygo) window.mygo.call("Maps.Exit", data.key);
  });
}
`

// mapDraw draws the map its page's data says, its worker at the address
// given: its route's legs, colored as their lines, walks dotted, and its
// places pinned, numbered and named, over the style's tiles, which fade
// in once they load, and is marked idle once all of them are drawn; the
// plain drawing of the map under it shows meanwhile, and without a
// network. A map of a web page lets a scroll go on to the page, and zooms
// with Ctrl or ⌘, or two fingers.
const mapDraw = `
function leafletMap(data, worker) {
  maplibregl.setWorkerUrl(worker);
  const box = document.getElementById("map");
  const options = {
    container: box, style: data.style, bounds: data.bounds,
    fitBoundsOptions: {padding: data.full ? {top: 64, bottom: 96, left: 64, right: 96} : {top: 36, bottom: 52, left: 36, right: 64}, maxZoom: 16},
    attributionControl: {compact: true, customAttribution: data.credit},
    dragRotate: false, pitchWithRotate: false, touchPitch: false,
  };
  if (data.web) Object.assign(options, {cooperativeGestures: true, locale: data.locale});
  const map = new maplibregl.Map(options);
  map.addControl(new maplibregl.NavigationControl({showCompass: false}), "top-right");
  map.on("load", () => {
    leafletLegs(map, data);
    // Every name goes on the map before any number, so that no place's name
    // covers another's pin.
    const names = [];
    data.places.forEach((p) => {
      if (!p.name) return;
      const at = document.createElement("div");
      at.className = "place";
      const name = document.createElement("span");
      name.className = "name";
      name.textContent = p.name;
      at.append(name);
      names.push({p, at, name});
      new maplibregl.Marker({element: at}).setLngLat([p.lng, p.lat]).addTo(map);
    });
    data.places.forEach((p, i) => {
      const pin = document.createElement("div");
      pin.className = "pin";
      pin.textContent = String(i + 1);
      new maplibregl.Marker({element: pin}).setLngLat([p.lng, p.lat]).addTo(map);
    });
    // A name that would run off the map's right edge goes left of its pin.
    const place = () => {
      const right = box.clientWidth - 4;
      names.forEach(({p, at, name}) => {
        at.classList.toggle("west", map.project([p.lng, p.lat]).x + 16 + name.offsetWidth > right);
      });
    };
    place();
    map.on("moveend", place);
    map.on("resize", place);
    // The credits start folded behind their button, as the map's caption names its data's sources.
    const credits = box.querySelector(".maplibregl-ctrl-attrib.maplibregl-compact-show");
    if (credits) {
      credits.setAttribute("open", "");
      credits.classList.remove("maplibregl-compact-show");
    }
    box.classList.add("ready");
  });
  map.once("idle", () => box.classList.add("idle"));
  return map;
}
`

// PrintMapWait is how long a printed page draws its maps on their tiles,
// at most, before it is printed with their plain drawings.
const PrintMapWait = 15 * time.Second

// PrintMapJS draws each map of a printed page on its tiles, as a picture
// in place of its plain drawing (.mapshot, its data in data-map, as
// PrintMapData has it), its pins and their names drawn over it, once all
// of its tiles are there; in a page that can't be seen too, as a hidden
// window's. window.leafletMapsDrawn settles once all are drawn or failed,
// or after PrintMapWait: the app waits for it before printing, and a map
// not drawn by then shows its plain drawing.
var PrintMapJS = sync.OnceValue(func() string { return maplibreModule() + mapLegs + printMapDraws })

// PrintMapCSP lets a printed page with maps run its scripts and MapLibre's
// worker, and reach the tile server alone. The worker is a blob of the
// page, or, in a page of no origin of its own, as one loaded from a file,
// MapLibre's data: URL of the blob.
var PrintMapCSP = sync.OnceValue(func() string {
	return ScriptsCSP(PrintDiagramJS, PrintMapJS()) + "; worker-src blob: data:; child-src blob: data:; img-src data: blob: " + TileHost +
		"; connect-src blob: data: " + TileHost + "; font-src data:"
})

// PrintMapWorker is MapLibre's worker as data of a printed page with maps,
// for PrintMapJS.
var PrintMapWorker = sync.OnceValue(func() string { return dataScript("map-worker", withoutSourceMap(maplibreWorkerJS)) })

// PrintMapData is what PrintMapJS draws m from, light: its route's legs
// without a line's color of their own left for the page's accent. "" for
// a map with nothing on it.
func PrintMapData(msgs locales.Messages, m doc.Map) string {
	shape := m.Shape()
	if !shape.OK {
		return ""
	}
	b, _ := json.Marshal(mapData(msgs, m, shape, false, ""))
	return string(b)
}

// printMapDraws draws a printed page's maps, each in a box of its size out
// of the page's view, into a picture in place of its drawing.
var printMapDraws = `
{
  // A page that can't be seen, as a hidden window's, gets no animation
  // frames, which MapLibre waits for as it loads a source and draws: timers
  // stand in for them there.
  if (document.visibilityState !== "visible") {
    window.requestAnimationFrame = (f) => setTimeout(() => f(performance.now()), 16);
    window.cancelAnimationFrame = (id) => clearTimeout(id);
  }
  const worker = JSON.parse(document.getElementById("map-worker").textContent);
  maplibregl.setWorkerUrl(URL.createObjectURL(new Blob([worker], {type: "text/javascript"})));
  const accent = getComputedStyle(document.documentElement).getPropertyValue("--link").trim() || "#3446c9";
  const drawn = [...document.querySelectorAll(".mapshot[data-map]")].map((el) => leafletShot(el, accent));
  window.leafletMapsDrawn = Promise.race([Promise.allSettled(drawn), new Promise((r) => setTimeout(r, ` + printMapWaitMS + `))]);
}

function leafletShot(el, accent) {
  const data = JSON.parse(el.dataset.map), w = el.clientWidth, h = el.clientHeight;
  if (!w || !h) return Promise.resolve();
  // Sharp on paper, and at most as wide as a printed page's pictures.
  const ratio = Math.min(2, ` + strconv.Itoa(printMapPixels) + ` / w);
  data.legs.forEach((l) => { l.color = l.color || accent; });
  const box = document.createElement("div");
  box.className = "mapshot-draw";
  box.style.width = w + "px";
  box.style.height = h + "px";
  document.body.append(box);
  return new Promise((resolve, reject) => {
    let map = null;
    const done = (err) => {
      if (!box.isConnected) return;
      clearTimeout(late);
      box.remove();
      if (map) map.remove();
      err ? reject(err) : resolve();
    };
    const late = setTimeout(() => done(new Error("map: its tiles are late")), ` + printMapWaitMS + `);
    try {
      map = new maplibregl.Map({
        container: box, style: data.style, bounds: data.bounds,
        fitBoundsOptions: {padding: {top: 36, bottom: 36, left: 36, right: 64}, maxZoom: 16},
        interactive: false, attributionControl: false, fadeDuration: 0, pixelRatio: ratio, trackResize: false,
        canvasContextAttributes: {preserveDrawingBuffer: true},
      });
    } catch (e) {
      done(e);
      return;
    }
    let legs = false;
    map.on("load", () => {
      leafletLegs(map, data);
      legs = true;
    });
    // A map whose style doesn't load, as offline, draws nothing: it keeps
    // its plain drawing at once.
    map.on("error", (e) => {
      if (!e.sourceId && !map.isStyleLoaded()) done(e.error || new Error("map: no style"));
    });
    const tick = () => {
      if (!box.isConnected) return;
      try {
        if (legs && map.loaded() && map.areTilesLoaded()) {
          // The picture takes the drawing's place once loaded, which a page
          // printed at once would show blank before. Its load, unlike its
          // decode(), comes in a page that can't be seen too.
          const img = leafletPicture(map, data, accent, ratio);
          const shown = () => {
            if (!box.isConnected) return;
            el.replaceChildren(img);
            done();
          };
          img.onerror = () => done(new Error("map: no picture"));
          if (img.complete && img.naturalWidth) shown();
          else img.onload = shown;
          return;
        }
      } catch (e) {
        done(e);
        return;
      }
      setTimeout(tick, 100);
    };
    tick();
  });
}

// leafletPicture is the map as drawn, its pins and their names over it, as
// a picture.
function leafletPicture(map, data, accent, ratio) {
  map.redraw();
  const gl = map.getCanvas(), c = document.createElement("canvas");
  c.width = gl.width;
  c.height = gl.height;
  const g = c.getContext("2d");
  g.drawImage(gl, 0, 0);
  g.scale(ratio, ratio);
  const font = "-apple-system, system-ui, 'Segoe UI', sans-serif";
  g.textBaseline = "middle";
  g.shadowOffsetY = 1;
  // Every name is drawn before any pin, so that no place's name covers
  // another's pin.
  data.places.forEach((p) => {
    if (!p.name) return;
    const at = map.project([p.lng, p.lat]);
    g.font = "600 12px " + font;
    g.textAlign = "left";
    const chars = [...p.name];
    let name = p.name;
    while (chars.length > 1 && g.measureText(name).width > 168) {
      chars.pop();
      name = chars.join("") + "…";
    }
    // A name that would run off the picture's right edge goes left of its pin.
    const wide = g.measureText(name).width + 12;
    const x = at.x + 16 + wide > c.width / ratio - 4 ? at.x - 16 - wide : at.x + 16;
    g.shadowColor = "rgba(0,0,0,.2)";
    g.shadowBlur = 3;
    g.fillStyle = "#ffffff";
    g.beginPath();
    g.roundRect ? g.roundRect(x, at.y - 10, wide, 20, 6) : g.rect(x, at.y - 10, wide, 20);
    g.fill();
    g.shadowColor = "transparent";
    g.fillStyle = "#16181d";
    g.fillText(name, x + 6, at.y + 0.5);
  });
  data.places.forEach((p, i) => {
    const at = map.project([p.lng, p.lat]);
    g.shadowColor = "rgba(0,0,0,.3)";
    g.shadowBlur = 4;
    g.fillStyle = "#ffffff";
    g.beginPath();
    g.arc(at.x, at.y, 13, 0, 2 * Math.PI);
    g.fill();
    g.shadowColor = "transparent";
    g.fillStyle = accent;
    g.beginPath();
    g.arc(at.x, at.y, 11, 0, 2 * Math.PI);
    g.fill();
    g.fillStyle = "#ffffff";
    g.font = "700 11px " + font;
    g.textAlign = "center";
    g.fillText(String(i + 1), at.x, at.y + 0.5);
  });
  // The tiles' credits, which the map's "i" holds on a screen, in its
  // corner, read as text of an inert document.
  const credit = [...new Set(Object.keys(map.getStyle().sources).map((id) => map.getSource(id)?.attribution).filter(Boolean))]
    .map((a) => new DOMParser().parseFromString(a, "text/html").body.textContent.replace(/\s+/g, " ").trim()).join(" · ");
  if (credit) {
    g.font = "10px " + font;
    g.textAlign = "right";
    const wide = g.measureText(credit).width + 10, right = c.width / ratio, bottom = c.height / ratio;
    g.fillStyle = "rgba(255,255,255,.8)";
    g.fillRect(right - wide, bottom - 16, wide, 16);
    g.fillStyle = "#3d424c";
    g.fillText(credit, right - 5, bottom - 8);
  }
  const img = document.createElement("img");
  img.alt = "";
  // WebKit paints a large picture it hasn't decoded yet as nothing, as a
  // page printed at once would show it, unless it decodes as it paints.
  img.decoding = "sync";
  img.src = c.toDataURL("image/jpeg", 0.88);
  return img;
}
`

// printMapPixels is how wide a printed map's picture is at most, as wide
// as a printed page's pictures from the web.
const printMapPixels = 1400

// printMapWaitMS is PrintMapWait in milliseconds, as printMapDraws waits.
var printMapWaitMS = strconv.FormatInt(PrintMapWait.Milliseconds(), 10)

// mapCSP lets a map's page run its script and its worker, made of a blob,
// and reach the tile server alone.
func mapCSP(script string) string {
	return ScriptsCSP(script) + "; worker-src blob:; child-src blob:; img-src data: blob: " + TileHost +
		"; connect-src " + TileHost + "; font-src data:"
}

// MapPage is the page that shows a map block on the Mac and Windows: m on
// its tiles, light or dark, its route in its lines' colors, as a block of
// a page, where a scroll goes to the page and a pinch or its buttons zoom
// it, or full, filling the window, where a scroll zooms it too and Escape
// asks the app, by its key, to leave it. Its credits are in msgs'
// language.
func MapPage(msgs locales.Messages, m doc.Map, key string, dark, full bool, bg, accent string) string {
	shape := m.Shape()
	if !shape.OK {
		return ""
	}
	data := mapData(msgs, m, shape, dark, accent)
	data["full"], data["key"] = full, key
	theme := "light"
	if dark {
		theme = "dark"
	}
	parts := mapParts()
	return `<!doctype html><html data-theme="` + theme + `"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		parts.head + mapStyle(bg, accent, dark) + `</style></head><body><div id="ground">` + shape.SVG() + `</div><div id="map"></div>` +
		dataScript("map-data", data) + parts.tail
}

// mapData is what a map's page's script draws, light or dark, the legs
// of its route without a line's color of their own in accent.
func mapData(msgs locales.Messages, m doc.Map, shape doc.MapShape, dark bool, accent string) map[string]any {
	sw, ne := shape.SW, shape.NE
	colors := legColors(shape, dark, accent)
	legs := []map[string]any{}
	for i, path := range shape.Paths {
		coords := make([][2]float64, len(path))
		for k, p := range path {
			coords[k] = [2]float64{p.Lng, p.Lat}
		}
		legs = append(legs, map[string]any{"color": colors[i], "walk": shape.Legs[i].Mode == doc.RouteWalk, "coords": coords})
	}
	data := mapLook(dark, accent)
	// The credits name the route's router after the tiles' own, which name
	// OpenStreetMap already.
	data["bounds"], data["places"], data["legs"], data["credit"] = [2][2]float64{{sw.Lng, sw.Lat}, {ne.Lng, ne.Lat}}, shape.Markers, legs, doc.RouteCredit(msgs, m)
	return data
}

// mapLook is what a map draws in, light or dark: its style, the casing of
// its route's lines, its walks' color, and its pins'.
func mapLook(dark bool, accent string) map[string]any {
	casing, walk := "#ffffff", "#5b616e"
	if dark {
		casing, walk = "#16181d", "#b4bac4"
	}
	return map[string]any{"style": MapStyles[dark], "casing": casing, "walk": walk, "accent": accent}
}

// legColors are the colors of a route's legs: each its line's own, made
// lighter on a dark map, or accent for one without.
func legColors(shape doc.MapShape, dark bool, accent string) []string {
	colors := make([]string, len(shape.Paths))
	for i := range shape.Paths {
		colors[i] = doc.LineColor(shape.Legs[i])
		if colors[i] == "" {
			colors[i] = accent
		} else if dark {
			colors[i] = lighter(colors[i], 0.3)
		}
	}
	return colors
}

// MapFiles are the files a web page's maps load from the page's own server:
// MapLibre's module with the script that draws a map, MapLibre's worker,
// and their styles; their contents as WebMapFiles has them, or their
// addresses as WebMapPage takes them.
type MapFiles struct{ Script, Worker, Style string }

// WebMapFiles are the files of the maps of a web page, made once: they are
// megabytes, which the page's server keeps for its visitors' browsers to
// keep too, rather than each map's page holding them again.
var WebMapFiles = sync.OnceValue(func() MapFiles {
	return MapFiles{
		Script: maplibreModule() + mapLegs + mapDraw + webMapDraws,
		Worker: withoutSourceMap(maplibreWorkerJS),
		Style:  maplibreCSS + webMapStyle,
	}
})

// webMapDraws draws the map of a web page's frame, light or dark as its
// visitor's system is, as the page around it, again as that changes; its
// worker comes from the page's server.
const webMapDraws = `
{
  const data = JSON.parse(document.getElementById("map-data").textContent);
  const dark = matchMedia("(prefers-color-scheme: dark)");
  if (dark.matches) {
    data.legs.forEach((l, i) => { l.color = data.dark.colors[i]; });
    Object.assign(data, data.dark);
  }
  dark.addEventListener("change", () => location.reload());
  document.documentElement.style.setProperty("--pin", data.accent);
  fetch(data.worker).then((r) => r.ok ? r.text() : Promise.reject(new Error("worker: " + r.status)))
    .then((js) => leafletMap(data, URL.createObjectURL(new Blob([js], {type: "text/javascript"}))))
    .catch((e) => console.error("map:", e));
}
`

// WebMapCSP lets the page of a web page's map, framed by the page alone,
// run its script and styles from the page's server, its worker made of a
// blob of the one from there, and reach the tile server alone besides.
const WebMapCSP = "default-src 'none'; script-src 'self'; style-src 'self'; worker-src blob:; child-src blob:; img-src data: blob: " + TileHost +
	"; connect-src 'self' " + TileHost + "; font-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

// WebMapPage is the page a web page shows a map block in, in a frame over
// the map's plain drawing, as MapPage shows it in the app: m on its tiles,
// light or dark as its visitor's system is, its pins in lightAccent or
// darkAccent, where a scroll goes on to the page and Ctrl or ⌘ with a
// scroll, a pinch, two fingers or its buttons zoom and move it. Its
// script, worker and styles are at the addresses in files, as WebMapFiles
// has them; its credits and help are in msgs' language. "" for a map with
// nothing on it.
func WebMapPage(msgs locales.Messages, m doc.Map, files MapFiles, lightAccent, darkAccent string) string {
	shape := m.Shape()
	if !shape.OK {
		return ""
	}
	data := mapData(msgs, m, shape, false, lightAccent)
	dark := mapLook(true, darkAccent)
	dark["colors"] = legColors(shape, true, darkAccent)
	data["dark"], data["web"], data["worker"] = dark, true, files.Worker
	data["locale"] = map[string]string{
		"CooperativeGesturesHandler.WindowsHelpText": msgs.MapZoomHelp("Ctrl"),
		"CooperativeGesturesHandler.MacHelpText":     msgs.MapZoomHelp("⌘"),
		"CooperativeGesturesHandler.MobileHelpText":  msgs.MapMoveHelp(),
		"NavigationControl.ZoomIn":                   msgs.RenderedZoomIn(),
		"NavigationControl.ZoomOut":                  msgs.RenderedZoomOut(),
	}
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<meta name="color-scheme" content="light dark"><meta name="referrer" content="no-referrer">` +
		`<link rel="stylesheet" href="` + html.EscapeString(files.Style) + `"></head><body><div id="map"></div>` +
		dataScript("map-data", data) + `<script type="module" src="` + html.EscapeString(files.Script) + `"></script></body></html>`
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

// mapPinsCSS draws a map's pins, in --pin, with --ink names on --paper,
// and the map fading in over what shows under it as it is ready.
const mapPinsCSS = `
#map{position:absolute;inset:0;opacity:0;transition:opacity .25s}
#map.ready{opacity:1}
.pin{width:22px;height:22px;border-radius:50%;background:var(--pin);color:#fff;font-weight:700;font-size:11px;pointer-events:none;
display:flex;align-items:center;justify-content:center;box-shadow:0 0 0 2px var(--paper),0 1px 4px rgba(0,0,0,.3)}
.place{width:0;height:0;pointer-events:none}
.place .name{position:absolute;left:16px;top:0;transform:translateY(-50%);max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;
font-weight:600;color:var(--ink);background:var(--paper);padding:2px 6px;border-radius:6px;box-shadow:0 1px 3px rgba(0,0,0,.2)}
.place.west .name{left:auto;right:16px}
.maplibregl-ctrl-attrib{font-size:10px}`

// mapInk and mapPaper are a map's names' color and what they are on, light
// or dark.
var mapInk, mapPaper = map[bool]string{false: "#16181d", true: "#e9ebef"}, map[bool]string{false: "#ffffff", true: "#23272e"}

func mapStyle(bg, accent string, dark bool) string {
	return doc.MapSVGCSS + `
html,body{margin:0;height:100%;background:` + bg + `;overflow:hidden;font:12px -apple-system,system-ui,"Segoe UI",sans-serif}
#ground{position:absolute;inset:0;display:flex;align-items:center;justify-content:center}
#ground svg{width:100%;height:100%}
#ground .ground{fill:transparent}
:root{--code:` + bg + `;--link:` + accent + `;--muted:#8a909b;--bg:` + mapPaper[dark] + `;--pin:` + accent + `;--ink:` + mapInk[dark] + `;--paper:` + mapPaper[dark] + `}` +
		mapPinsCSS + map[bool]string{true: mapDarkControls}[dark]
}

// webMapStyle is the look of a web page's map: clear, over the plain
// drawing under its frame, light or dark as its visitor's system is.
var webMapStyle = `
:root{color-scheme:light dark;--pin:#3446c9;--ink:` + mapInk[false] + `;--paper:` + mapPaper[false] + `}
html,body{margin:0;height:100%;background:transparent;overflow:hidden;font:12px -apple-system,system-ui,"Segoe UI",sans-serif}` +
	mapPinsCSS + `
@media (prefers-color-scheme:dark){:root{--ink:` + mapInk[true] + `;--paper:` + mapPaper[true] + `}` + mapDarkControls + `}
`
