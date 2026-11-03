// Package rendered makes the pages that draw math and diagram blocks:
// TeX drawn by KaTeX as MathML, and Mermaid drawn by Pele as SVG. The
// desktop shows them in sandboxed frames, the iOS app in web views; either
// way their policy keeps them off the network. The scripts are in
// third_party.
package rendered

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"regexp"
	"strings"

	"leaflet/internal/doc"
)

// KatexJS is KaTeX, which draws TeX.
//
//go:embed third_party/katex/katex.min.js
var KatexJS string

//go:embed third_party/pele/pele.js
var peleJS string

// PeleModule is Pele's module with its render function as peleRender, for
// the script after it to call: the module's own export names it by a
// minified name.
var PeleModule = func() string {
	m := regexp.MustCompile(`export\{([^}]*)\};?\s*$`).FindStringSubmatchIndex(peleJS)
	if m == nil {
		return "var peleRender = null;"
	}
	for _, part := range strings.Split(peleJS[m[2]:m[3]], ",") {
		if local, name, ok := strings.Cut(strings.TrimSpace(part), " as "); ok && name == "render" {
			return peleJS[:m[0]] + "\nconst peleRender = " + local + ";\n"
		}
	}
	return "var peleRender = null;"
}()

// ScriptString is s as a JavaScript string inside a script element.
func ScriptString(s string) string {
	b, _ := json.Marshal(s)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

// renderedStyle is the look both pages share: the page's text colors in
// either theme, and an error as a note in the danger color.
const renderedStyle = `html,body{margin:0;background:transparent;color:#16181d}
:root[data-theme="dark"] body{color:#e9ebef}
body{padding:6px 0;overflow:hidden;text-align:center}
.err{font:12.5px -apple-system,system-ui,sans-serif;color:#a61b1b;white-space:pre-wrap;text-align:left;padding:2px 4px}
:root[data-theme="dark"] .err{color:#ffa3a3}`

// scriptsCSP is the policy of a page that runs only scripts, by their
// hashes: nothing a formula or a diagram's source makes of the page, as an
// element with an event handler in the SVG, runs, and nothing comes from
// the network but pictures in data URLs.
func scriptsCSP(scripts ...string) string {
	policy := "default-src 'none'; script-src"
	for _, s := range scripts {
		sum := sha256.Sum256([]byte(s))
		policy += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return policy + "; style-src 'unsafe-inline'; img-src data:"
}

// dataScript is v as JSON in a script element that runs nothing, for a
// page's own script to read by its id.
func dataScript(id string, v any) string {
	b, _ := json.Marshal(v)
	return `<script type="application/json" id="` + id + `">` + strings.ReplaceAll(string(b), "</", `<\/`) + `</script>`
}

// mathScript draws the formula in the page's data.
const mathScript = `var m = document.getElementById("m");
try { katex.render(JSON.parse(document.getElementById("formula").textContent), m, {displayMode: true, output: "mathml", throwOnError: true}); }
catch (e) { m.className = "err"; m.textContent = e.message; }`

// MathCSP is the math page's policy: KaTeX and the script that calls it.
var MathCSP = scriptsCSP(KatexJS, mathScript)

// MathPage draws a TeX formula in display style.
func MathPage(formula string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<meta http-equiv="Content-Security-Policy" content="` + MathCSP + `"><style>` + renderedStyle + `
math{font-size:1.35em}</style></head><body><div id="m"></div>` + dataScript("formula", formula) + `
<script>` + KatexJS + `</script>
<script>` + mathScript + `</script></body></html>`
}

// ZoomScript defines leafletZoom(view, box, on, full, zoomed), which lets
// the diagram drawn in box zoom and move over view: at a point with a
// pinch, or Ctrl and the wheel, over on, from 1 to 8 times its fitted
// size; zoomed in, a drag moves it and a double click fits it again. Where
// a browser tells a pinch only as two touches, those zoom it too. It tells
// zoomed whether it is zoomed in, and gives a function that zooms "in",
// "out" or to "fit".
//
// full draws it to fill view, up to twice its size: there the wheel moves a
// zoomed diagram rather than the page.
const ZoomScript = `function leafletZoom(view, box, on, full, zoomed) {
  const svg = box.querySelector("svg");
  if (full && svg) {
    svg.style.maxWidth = 2 * (parseFloat(svg.getAttribute("width")) || 800) + "px";
    svg.style.maxHeight = 2 * (parseFloat(svg.getAttribute("height")) || 600) + "px";
  }

  // The diagram is k times its size, moved by x and y, kept over the view.
  let k = 1, x = 0, y = 0, told = false;
  function show() {
    const w = view.clientWidth, h = view.clientHeight;
    x = Math.min(0, Math.max(w - w * k, x));
    y = Math.min(0, Math.max(h - h * k, y));
    box.style.transform = k === 1 ? "" : "translate(" + x + "px," + y + "px) scale(" + k + ")";
    view.classList.toggle("zoomed", k > 1);
    if (told !== k > 1) {
      told = k > 1;
      zoomed(told);
    }
  }
  // zoomAt zooms by f, keeping the point at px, py of the view in place.
  function zoomAt(px, py, f) {
    const n = Math.min(Math.max(k * f, 1), 8);
    x = px - (px - x) * n / k;
    y = py - (py - y) * n / k;
    k = n;
    show();
  }
  function at(e) {
    const r = view.getBoundingClientRect();
    return typeof e.clientX === "number" ? [e.clientX - r.left, e.clientY - r.top] : [r.width / 2, r.height / 2];
  }
  // WebKit tells a pinch as gestures; others as the wheel with Ctrl.
  const gestures = "ongesturestart" in window;
  let pinch = 0;
  on.addEventListener("gesturestart", e => { e.preventDefault(); pinch = 1; });
  on.addEventListener("gesturechange", e => {
    e.preventDefault();
    if (!pinch) return;
    zoomAt(...at(e), e.scale / pinch);
    pinch = e.scale;
  });
  on.addEventListener("gestureend", e => { e.preventDefault(); pinch = 0; });
  on.addEventListener("wheel", e => {
    if (!e.ctrlKey) return;
    e.preventDefault();
    if (!pinch) zoomAt(...at(e), Math.exp(-e.deltaY * (e.deltaMode ? 0.1 : 0.01)));
  }, {passive: false});
  // Filling the page, the wheel is the diagram's alone. With Ctrl, or as a
  // pinch, it goes on to zoom it.
  if (full) view.addEventListener("wheel", e => {
    if (e.ctrlKey) return;
    e.stopPropagation();
    e.preventDefault();
    x -= e.deltaX;
    y -= e.deltaY;
    show();
  }, {passive: false});
  // One finger or the mouse moves it; two fingers apart, span, pinch it.
  const touches = new Map();
  let drag = null, span = 0;
  const apart = () => {
    const [a, b] = [...touches.values()];
    return [Math.hypot(a[0] - b[0], a[1] - b[1]), (a[0] + b[0]) / 2, (a[1] + b[1]) / 2];
  };
  view.addEventListener("pointerdown", e => {
    if (e.button !== 0) return;
    if (e.pointerType === "touch") {
      touches.set(e.pointerId, [e.clientX, e.clientY]);
      view.setPointerCapture(e.pointerId);
      if (touches.size > 1) {
        drag = null;
        view.classList.remove("dragging");
        if (touches.size === 2 && !gestures) span = apart()[0];
        return;
      }
    }
    if (k === 1) return;
    e.preventDefault();
    drag = {id: e.pointerId, x: e.clientX - x, y: e.clientY - y};
    view.setPointerCapture(e.pointerId);
    view.classList.add("dragging");
  });
  view.addEventListener("pointermove", e => {
    if (touches.has(e.pointerId)) touches.set(e.pointerId, [e.clientX, e.clientY]);
    if (span) {
      const [d, mx, my] = apart();
      zoomAt(...at({clientX: mx, clientY: my}), d / span);
      span = d;
      return;
    }
    if (!drag || e.pointerId !== drag.id) return;
    x = e.clientX - drag.x;
    y = e.clientY - drag.y;
    show();
  });
  const drop = e => {
    touches.delete(e.pointerId);
    if (touches.size < 2) span = 0;
    if (drag && e.pointerId === drag.id) {
      drag = null;
      view.classList.remove("dragging");
    }
  };
  view.addEventListener("pointerup", drop);
  view.addEventListener("pointercancel", drop);
  view.addEventListener("dblclick", () => { k = 1; show(); });
  new ResizeObserver(show).observe(view);
  return how => {
    if (how === "fit") { k = 1; show(); }
    else if (how === "in" || how === "out") zoomAt(view.clientWidth / 2, view.clientHeight / 2, how === "in" ? 1.5 : 1 / 1.5);
  };
}`

// DiagramPage draws Mermaid source, in the page's colors. It zooms at the
// pointer with a pinch, or Ctrl and the wheel, and from 1 to 8 times;
// zoomed in, a drag moves it and a double click fits it again. Its bar
// sends "in", "out" and "fit", and hears whether it is zoomed in.
//
// full draws it to fill the frame, up to twice its size, for the page's
// full screen view: there the wheel moves a zoomed diagram rather than
// the page, and Escape asks to leave.
//
// bg is the color behind it, which its edge labels and notes are set on
// so lines do not run through their text.
func DiagramPage(source, bg string, full bool) string {
	fill := ""
	if full {
		fill = `
html,body{height:100%}
body{padding:0}
#v{height:100%}
#d{height:100%;box-sizing:border-box;padding:24px;display:flex;align-items:center;justify-content:center}
#d svg{width:100%;height:100%}`
	}
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<meta http-equiv="Content-Security-Policy" content="` + DiagramCSP + `"><style>` + renderedStyle + `
.pele{--pele-bg:` + bg + `;--pele-fg:#16181d;--pele-muted:#565c67;--pele-line:#3d424c;--pele-surface:#f5f6f8;--pele-surface-alt:#eceef2;--pele-border:#c9ced8;--pele-accent:#3446c9;--pele-fg-on-light:#16181d;--pele-fg-on-dark:#e9ebef;--pele-font:-apple-system,system-ui,sans-serif}
:root[data-theme="dark"] .pele{--pele-fg:#e9ebef;--pele-muted:#9ea4af;--pele-line:#c0c5ce;--pele-surface:#23272e;--pele-surface-alt:#2b3038;--pele-border:#3a404a;--pele-accent:#8a97ff}
svg{max-width:100%;height:auto}
#v{overflow:hidden}
#d{transform-origin:0 0}
#v.zoomed{cursor:grab;-webkit-user-select:none;user-select:none}
#v.dragging{cursor:grabbing}` + fill + `</style></head><body><div id="v"><div id="d"></div></div>` +
		dataScript("diagram", map[string]any{"source": source, "full": full}) + `
<script type="module">` + diagramScript + `</script></body></html>`
}

// diagramScript draws the diagram in the page's data with Pele, and lets
// it zoom.
var diagramScript = PeleModule + "\n" + ZoomScript + `
// In a block of its own, clear of the names Pele's module takes.
{
  const view = document.getElementById("v"), box = document.getElementById("d");
  const {source, full} = JSON.parse(document.getElementById("diagram").textContent);
  try { box.innerHTML = peleRender(source, {fontFamily: "-apple-system, system-ui, sans-serif"}).svg; }
  catch (e) { box.className = "err"; box.textContent = e.message; }
  const zoom = leafletZoom(view, box, window, full, (z) => parent.postMessage({leafletZoomed: z}, "*"));
  if (full) addEventListener("keydown", e => {
    if (e.key === "Escape") parent.postMessage({leafletExit: true}, "*");
  });
  addEventListener("message", e => {
    if (e.source === parent && e.data && e.data.leafletZoom) zoom(e.data.leafletZoom);
  });
}`

// DiagramCSP is the diagram page's policy: its one script, with Pele.
var DiagramCSP = scriptsCSP(diagramScript)

// Video blocks play their site's player, or the file, in a page of their
// own, once play is pressed.

// VideoOrigin is the origin of the page a video's player is framed in.
// YouTube's player wants the page that frames it named in the Referer, as
// an https origin of the app's bundle ID for an app; from about:blank it
// gets none, and refuses to play.
const VideoOrigin = "https://app.capyvibe.leaflet/"

// VideoCSP lets a video's page frame only the two players, or play only
// web video files, and run nothing of its own.
const VideoCSP = "default-src 'none'; style-src 'unsafe-inline'; frame-src https://www.youtube-nocookie.com https://player.vimeo.com; media-src https: http:"

// VideoPage frames a video's player, or plays the file, to fill the web
// view.
func VideoPage(v doc.Video) string {
	player := `<iframe src="` + html.EscapeString(v.PlayerURL()) + `" allow="autoplay; fullscreen; picture-in-picture; encrypted-media" allowfullscreen referrerpolicy="strict-origin-when-cross-origin"></iframe>`
	if v.Provider == "file" {
		player = `<video src="` + html.EscapeString(v.PlayerURL()) + `" controls autoplay playsinline></video>`
	}
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<meta http-equiv="Content-Security-Policy" content="` + VideoCSP + `">` +
		`<style>html,body{margin:0;height:100%;background:#000;overflow:hidden}iframe,video{border:0;width:100%;height:100%;display:block}</style></head><body>` +
		player + `</body></html>`
}

// Interactive embeds run their HTML in a sandboxed frame of a page of
// their own, off the network.

// EmbedCSP is the policy of an embed's frame: inline scripts and styles,
// images, fonts and media from data and blob URLs, and nothing from the
// network. default-src does not cover WebRTC; the webrtc directive does
// where the web view knows it, and EmbedNoRTC where it does not.
const EmbedCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; webrtc 'block'"

// EmbedNoRTC runs first in an embed's frame: it takes away WebRTC, which
// would reach the network around the policy, before the embed's own
// script runs. It is best effort: a frame the embed makes itself has them
// back, which the webrtc directive, where known, still blocks.
const EmbedNoRTC = `<script>(function () {
  ["RTCPeerConnection", "webkitRTCPeerConnection", "mozRTCPeerConnection", "RTCDataChannel",
   "RTCSessionDescription", "RTCIceCandidate", "RTCRtpSender", "RTCRtpReceiver", "RTCRtpTransceiver",
   "RTCDtlsTransport", "RTCIceTransport", "RTCSctpTransport", "RTCCertificate"].forEach(function (k) {
    try { Object.defineProperty(window, k, {value: undefined, writable: false, configurable: false}); } catch (e) {}
  });
})();</script>`

// EmbedWheel runs in an embed's frame: wheel movement the embed cannot
// use itself, as most embeds do not scroll, goes to the page around it.
const EmbedWheel = `<script>(function () {
  function scrolls(el, dy) {
    for (; el && el.nodeType === 1; el = el.parentElement) {
      var s = getComputedStyle(el), o = s.overflowY;
      var root = el === document.scrollingElement;
      if ((root || o === "auto" || o === "scroll") && el.scrollHeight > el.clientHeight + 1) {
        if (dy < 0 && el.scrollTop > 0) return true;
        if (dy > 0 && el.scrollTop + el.clientHeight < el.scrollHeight - 1) return true;
      }
    }
    return false;
  }
  addEventListener("wheel", function (e) {
    if (e.ctrlKey) return;
    var k = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1;
    var dy = e.deltaY * k;
    if (dy && !scrolls(e.target, dy)) parent.postMessage({leafletWheel: dy}, "*");
  }, {passive: true});
})();</script>`

// EmbedFit runs in an embed's frame: it tells the page how tall the
// embed's content is at the frame's width, so a block too short for it,
// as a page narrows and its layout stacks, grows to show it all.
const EmbedFit = `<script>(function () {
  var last = "";
  function fit() {
    var b = document.body;
    if (!b) return;
    var s = getComputedStyle(b);
    var h = Math.ceil(b.scrollHeight + (parseFloat(s.marginTop) || 0) + (parseFloat(s.marginBottom) || 0));
    var key = h + ":" + innerWidth;
    if (key === last) return;
    last = key;
    parent.postMessage({leafletSize: [h, innerWidth]}, "*");
  }
  addEventListener("load", function () {
    fit();
    new ResizeObserver(fit).observe(document.body);
  });
  addEventListener("resize", fit);
})();</script>`

// EmbedPage wraps an embed's HTML in a sandboxed frame: frames never call
// Go, and the policy keeps it off the network. A srcdoc frame inherits the
// wrapper's policy, so the wrapper has the frame's. The wrapper's own
// script only passes the frame's leftover wheel movement and its content's
// size, whether it is zoomed in and its asking to leave full screen to the
// page, for the embed id. Without wheel, for a web view that gives the
// page all of the wheel, neither passes it.
//
// Its background is the page's. In the dark theme the frame's root gets
// data-theme="dark" and a dark color scheme, so an embed that styles
// nothing follows the page, and one that does can match it with
// :root[data-theme="dark"].
func EmbedPage(id, src, bg string, dark, wheel bool) string {
	scheme := "light"
	if dark {
		scheme = "dark"
	}
	script := `<script>`
	if wheel {
		// Without a wheel listener of its own, WebKit scrolls the wrapper
		// without telling a frame that could scroll, so it never forwards.
		script += `addEventListener("wheel", function () {}, {passive: true});`
	}
	// The viewport has a phone's web view lay the page out at its own
	// width, not 980 points wide and shrunk to it.
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<meta http-equiv="Content-Security-Policy" content="` + EmbedCSP + `; frame-src about:">` +
		`<meta name="color-scheme" content="` + scheme + `">` +
		`<style>html,body{margin:0;height:100%;background:` + bg + `}iframe{border:0;width:100%;height:100%;display:block;background:` + bg + `}</style></head><body>` +
		`<iframe id="f" sandbox="allow-scripts" srcdoc="` + html.EscapeString(EmbedFrame(src, scheme, wheel)) + `"></iframe>` +
		script +
		`var id = ` + ScriptString(id) + `;` +
		`addEventListener("message", function (e) {` +
		`if (e.source !== document.getElementById("f").contentWindow || !e.data || !window.mygo) return;` +
		`if (typeof e.data.leafletWheel === "number") window.mygo.call("Embeds.Scroll", e.data.leafletWheel);` +
		`var z = e.data.leafletSize;` +
		`if (z && typeof z[0] === "number" && typeof z[1] === "number") window.mygo.call("Embeds.Size", id, z[0], z[1]);` +
		`if (typeof e.data.leafletZoomed === "boolean") window.mygo.call("Embeds.Zoomed", id, e.data.leafletZoomed);` +
		`if (e.data.leafletExit === true) window.mygo.call("Embeds.Exit", id);` +
		`});</script></body></html>`
}

// EmbedFrame is the document of an embed's frame: its policy, theme and
// scripts first, then the embed's HTML. They go at the very start rather
// than after the HTML's "<head>", which may be in a comment or a string;
// the browser still puts them in the head, and the HTML's own <html> and
// <head> after them only add to those. A doctype the HTML starts with
// moves ahead of them, so it keeps the HTML's rendering mode. wheel adds
// EmbedWheel.
func EmbedFrame(src, scheme string, wheel bool) string {
	head := `<meta http-equiv="Content-Security-Policy" content="` + EmbedCSP + `">` + EmbedNoRTC +
		`<meta name="color-scheme" content="` + scheme + `">` +
		`<script>document.documentElement.dataset.theme = "` + scheme + `";</script>`
	if wheel {
		head += EmbedWheel
	}
	return LeadWith(head+EmbedFit, src)
}

// LeadWith puts head at the very start of the HTML src, after a doctype
// it starts with.
func LeadWith(head, src string) string {
	rest := strings.TrimLeft(src, " \t\r\n\f")
	if len(rest) >= 9 && strings.EqualFold(rest[:9], "<!doctype") {
		if i := strings.IndexByte(rest, '>'); i >= 0 {
			return rest[:i+1] + head + rest[i+1:]
		}
	}
	return head + src
}
