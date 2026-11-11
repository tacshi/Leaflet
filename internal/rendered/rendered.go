package rendered

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"

	"leaflet/internal/doc"
)

//go:embed third_party/pele/pele.js
var peleJS string

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

func ScriptString(s string) string {
	b, _ := json.Marshal(s)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

const renderedStyle = `html,body{margin:0;background:transparent;color:#16181d}
:root[data-theme="dark"] body{color:#e9ebef}
body{padding:6px 0;overflow:hidden;text-align:center}
.err{font:12.5px -apple-system,system-ui,sans-serif;color:#a61b1b;white-space:pre-wrap;text-align:left;padding:2px 4px}
:root[data-theme="dark"] .err{color:#ffa3a3}`

func scriptsCSP(scripts ...string) string { return ScriptsCSP(scripts...) + "; img-src data:" }

func ScriptsCSP(scripts ...string) string {
	policy := "default-src 'none'; script-src"
	for _, s := range scripts {
		sum := sha256.Sum256([]byte(s))
		policy += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return policy + "; style-src 'unsafe-inline'"
}

func dataScript(id string, v any) string {
	b, _ := json.Marshal(v)
	return `<script type="application/json" id="` + id + `">` + strings.ReplaceAll(string(b), "</", `<\/`) + `</script>`
}

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

var diagramScript = PeleModule + "\n" + ZoomScript + `
// In a block of its own, clear of the names Pele's module takes.
{
  const view = document.getElementById("v"), box = document.getElementById("d");
  const {source, full} = JSON.parse(document.getElementById("diagram").textContent);
  const draw = (src) => { box.innerHTML = peleRender(src, {fontFamily: "-apple-system, system-ui, sans-serif"}).svg; };
  try {
    draw(source);
    // A flowchart drawn across a narrow block, as a phone's, shrinks past
    // reading to fit: there it is drawn down instead.
    const across = /^(\s*(?:flowchart|graph)\s+)(LR|RL)\b/m, svg = box.querySelector("svg");
    const natural = svg ? (svg.viewBox.baseVal && svg.viewBox.baseVal.width) || svg.getBBox().width : 0;
    if (!full && across.test(source) && view.clientWidth > 0 && natural * 0.6 > view.clientWidth)
      draw(source.replace(across, "$1TD"));
  }
  catch (e) { box.className = "err"; box.textContent = e.message; }
  const zoom = leafletZoom(view, box, window, full, (z) => parent.postMessage({leafletZoomed: z}, "*"));
  if (full) addEventListener("keydown", e => {
    if (e.key === "Escape") parent.postMessage({leafletExit: true}, "*");
  });
  addEventListener("message", e => {
    if (e.source === parent && e.data && e.data.leafletZoom) zoom(e.data.leafletZoom);
  });
}`

var PrintDiagramJS = PeleModule + `
{
  document.querySelectorAll(".diagram[data-source]").forEach((el) => {
    try {
      el.innerHTML = peleRender(el.dataset.source, {fontFamily: '"Leaflet CJK", -apple-system, system-ui, sans-serif'}).svg;
      el.classList.add("pele");
    } catch (e) {}
  });
}`

var DiagramCSP = scriptsCSP(diagramScript)

const VideoOrigin = "https://app.capyvibe.leaflet/"

const SafariAgent = "Version/26.0 Safari/605.1.15"

const VideoCSP = "default-src 'none'; style-src 'unsafe-inline'; frame-src " + doc.VideoPlayers + "; media-src https: http:"

func VideoPage(v doc.Video) string {
	player := `<iframe src="` + html.EscapeString(v.PlayerURL(true)) + `" allow="autoplay; fullscreen; picture-in-picture; encrypted-media" allowfullscreen referrerpolicy="strict-origin-when-cross-origin"></iframe>`
	if v.Provider == "file" {
		player = `<video src="` + html.EscapeString(v.PlayerURL(true)) + `" controls autoplay playsinline></video>`
	}
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<meta http-equiv="Content-Security-Policy" content="` + VideoCSP + `">` +
		`<style>html,body{margin:0;height:100%;background:#000;overflow:hidden}iframe,video{border:0;width:100%;height:100%;display:block}</style></head><body>` +
		player + `</body></html>`
}

const FilePlayerCSP = "default-src 'none'; media-src 'self'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func FilePlayerPage(video bool, at int) string {
	src := "media"
	if at > 0 {
		src += "#t=" + strconv.Itoa(at)
	}
	el, look := "audio", "html{color-scheme:light dark}body{background:transparent}"
	if video {
		el, look = "video", "html,body{background:#000}"
	}
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<style>html,body{margin:0;height:100%;overflow:hidden}` + look + `audio,video{border:0;width:100%;height:100%;display:block}</style></head><body>` +
		`<` + el + ` src="` + src + `" controls autoplay playsinline></` + el + `></body></html>`
}

const FilePlayerStatus = `(function () {
  var el = document.querySelector("audio, video");
  if (!el || el.error) return {failed: true};
  return {at: el.currentTime, length: isFinite(el.duration) ? el.duration : 0, paused: el.paused, rate: el.playbackRate};
})()`

func FileMetadata(title string) string {
	name, err := json.Marshal(title)
	if err != nil {
		name = []byte(`""`)
	}
	return `(function () { document.title = ` + string(name) +
		`; if (!navigator.mediaSession || typeof MediaMetadata === "undefined") return false; ` +
		`navigator.mediaSession.metadata = new MediaMetadata({title: ` + string(name) +
		`, artist: "Leaflet", artwork: [{src: "cover"}]}); return true; })()`
}

func FilePlaying(on bool) string {
	act := "pause"
	if on {
		act = "play"
	}
	return `(function () { var el = document.querySelector("audio, video"); if (el) el.` + act + `(); })()`
}

func FileSeekTo(sec float64) string {
	return `(function () { var el = document.querySelector("audio, video"); if (el) el.currentTime = ` +
		strconv.FormatFloat(max(sec, 0), 'f', 3, 64) + `; })()`
}

func FileSpeed(speed float64) string {
	s := strconv.FormatFloat(speed, 'f', 2, 64)
	return `(function () { var el = document.querySelector("audio, video"); if (el) { el.defaultPlaybackRate = ` +
		s + `; el.playbackRate = ` + s + `; } })()`
}

const VideoSeekJS = `function leafletSeek(el, provider, t) {
  if (el.tagName === "VIDEO" || el.tagName === "AUDIO") {
    el.currentTime = t;
    el.play();
    return true;
  }
  var w = el.contentWindow;
  if (!w) return false;
  if (provider === "youtube") {
    [["seekTo", [t, true]], ["playVideo", []]].forEach(function (c) {
      w.postMessage(JSON.stringify({event: "command", func: c[0], args: c[1]}), "https://www.youtube-nocookie.com");
    });
    return true;
  }
  if (provider === "vimeo") {
    [{method: "setCurrentTime", value: t}, {method: "play"}].forEach(function (m) {
      w.postMessage(JSON.stringify(m), "https://player.vimeo.com");
    });
    return true;
  }
  return false;
}
function leafletPause(el, provider) {
  if (el.tagName === "VIDEO" || el.tagName === "AUDIO") {
    el.pause();
    return true;
  }
  var w = el.contentWindow;
  if (!w) return false;
  if (provider === "youtube") {
    w.postMessage(JSON.stringify({event: "command", func: "pauseVideo", args: []}), "https://www.youtube-nocookie.com");
    return true;
  }
  if (provider === "vimeo") {
    w.postMessage(JSON.stringify({method: "pause"}), "https://player.vimeo.com");
    return true;
  }
  return false;
}
`

func SeeksInPlace(v doc.Video) bool {
	return v.Provider == "youtube" || v.Provider == "vimeo" || v.Provider == "file"
}

func VideoSeek(v doc.Video, sec int) string {
	if !SeeksInPlace(v) {
		return ""
	}
	return "(function () {\n" + VideoSeekJS + "return leafletSeek(document.querySelector(\"iframe, video, audio\"), " +
		strconv.Quote(v.Provider) + ", " + strconv.Itoa(max(sec, 0)) + ");\n})()"
}

func VideoPause(v doc.Video) string {
	if !SeeksInPlace(v) {
		return ""
	}
	return "(function () {\n" + VideoSeekJS + "return leafletPause(document.querySelector(\"iframe, video, audio\"), " +
		strconv.Quote(v.Provider) + ");\n})()"
}

const EmbedCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; webrtc 'block'"

const NoRTC = `(function () {
  function guard(w) {
    ["RTCPeerConnection", "webkitRTCPeerConnection", "mozRTCPeerConnection", "RTCDataChannel",
     "RTCSessionDescription", "RTCIceCandidate", "RTCRtpSender", "RTCRtpReceiver", "RTCRtpTransceiver",
     "RTCDtlsTransport", "RTCIceTransport", "RTCSctpTransport", "RTCCertificate"].forEach(function (k) {
      try { Object.defineProperty(w, k, {value: undefined, writable: false, configurable: false}); } catch (e) {}
    });
    ["HTMLIFrameElement", "HTMLFrameElement", "HTMLObjectElement"].forEach(function (t) {
      var proto;
      try { proto = w[t] && w[t].prototype; } catch (e) { return; }
      if (!proto) return;
      ["contentWindow", "contentDocument"].forEach(function (p) {
        var d = Object.getOwnPropertyDescriptor(proto, p);
        if (!d || !d.get || !d.configurable) return;
        var get = d.get;
        Object.defineProperty(proto, p, {configurable: false, enumerable: d.enumerable, get: function () {
          var v = get.call(this);
          try { var f = p === "contentWindow" ? v : v && v.defaultView; if (f) guard(f); } catch (e) {}
          return v;
        }});
      });
    });
  }
  guard(window);
})();`

const EmbedNoRTC = "<script>" + NoRTC + "</script>"

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

func EmbedPage(id, src, bg string, dark, wheel bool) string {
	scheme := "light"
	if dark {
		scheme = "dark"
	}
	script := `<script>`
	if wheel {
		script += `addEventListener("wheel", function () {}, {passive: true});`
	}
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<meta http-equiv="Content-Security-Policy" content="` + EmbedCSP + `; frame-src about:">` +
		`<meta name="color-scheme" content="` + scheme + `">` +
		`<style>html,body{margin:0;height:100%;background:` + bg + `}iframe{border:0;width:100%;height:100%;display:block;background:` + bg + `}</style></head><body>` +
		`<iframe id="f" sandbox="allow-scripts" srcdoc="` + html.EscapeString(EmbedFrame(src, scheme, wheel)) + `"></iframe>` +
		script +
		`var id = ` + ScriptString(id) + `;` +
		`addEventListener("message", function (e) {` +
		`if (e.source !== document.getElementById("f").contentWindow || !e.data || !window.mygo) return;` +
		`if (typeof e.data.leafletWheel === "number") window.mygo.call("Embeds.Scroll", id, e.data.leafletWheel);` +
		`var z = e.data.leafletSize;` +
		`if (z && typeof z[0] === "number" && typeof z[1] === "number") window.mygo.call("Embeds.Size", id, z[0], z[1]);` +
		`if (typeof e.data.leafletZoomed === "boolean") window.mygo.call("Embeds.Zoomed", id, e.data.leafletZoomed);` +
		`if (e.data.leafletExit === true) window.mygo.call("Embeds.Exit", id);` +
		`});</script></body></html>`
}

func EmbedFrame(src, scheme string, wheel bool) string {
	head := `<meta http-equiv="Content-Security-Policy" content="` + EmbedCSP + `">` + EmbedNoRTC +
		`<meta name="color-scheme" content="` + scheme + `">` +
		`<script>document.documentElement.dataset.theme = "` + scheme + `";</script>`
	if wheel {
		head += EmbedWheel
	}
	return LeadWith(head+EmbedFit, src)
}

func LeadWith(head, src string) string {
	rest := strings.TrimLeft(src, " \t\r\n\f")
	if len(rest) >= 9 && strings.EqualFold(rest[:9], "<!doctype") {
		if i := strings.IndexByte(rest, '>'); i >= 0 {
			return rest[:i+1] + head + rest[i+1:]
		}
	}
	return head + src
}
