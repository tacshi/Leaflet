package app

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// Math and diagram blocks: TeX drawn by KaTeX as MathML, and Mermaid
// drawn by Pele as SVG, each in a sandboxed frame as an embed is, since
// the frame has no network. The scripts are in third_party.

//go:embed third_party/katex/katex.min.js
var katexJS string

//go:embed third_party/pele/pele.js
var peleJS string

// peleModule is Pele's module with its render function as peleRender, for
// the script after it to call: the module's own export names it by a
// minified name.
var peleModule = func() string {
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

// scriptString is s as a JavaScript string inside a script element.
func scriptString(s string) string {
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

// mathPage draws a TeX formula in display style.
func mathPage(formula string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>` + renderedStyle + `
math{font-size:1.35em}</style></head><body><div id="m"></div>
<script>` + katexJS + `</script>
<script>var m = document.getElementById("m");
try { katex.render(` + scriptString(formula) + `, m, {displayMode: true, output: "mathml", throwOnError: true}); }
catch (e) { m.className = "err"; m.textContent = e.message; }</script></body></html>`
}

// diagramPage draws Mermaid source, in the page's colors. It zooms at the
// pointer with a pinch, or Ctrl and the wheel, and from 1 to 8 times;
// zoomed in, a drag moves it and a double click fits it again. Its bar
// sends "in", "out" and "fit", and hears whether it is zoomed in.
func diagramPage(source string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>` + renderedStyle + `
.pele{--pele-bg:transparent;--pele-fg:#16181d;--pele-muted:#565c67;--pele-line:#3d424c;--pele-surface:#f5f6f8;--pele-surface-alt:#eceef2;--pele-border:#c9ced8;--pele-accent:#3446c9;--pele-fg-on-light:#16181d;--pele-fg-on-dark:#e9ebef;--pele-font:-apple-system,system-ui,sans-serif}
:root[data-theme="dark"] .pele{--pele-fg:#e9ebef;--pele-muted:#9ea4af;--pele-line:#c0c5ce;--pele-surface:#23272e;--pele-surface-alt:#2b3038;--pele-border:#3a404a;--pele-accent:#8a97ff}
svg{max-width:100%;height:auto}
#v{overflow:hidden}
#d{transform-origin:0 0}
#v.zoomed{cursor:grab;-webkit-user-select:none;user-select:none}
#v.dragging{cursor:grabbing}</style></head><body><div id="v"><div id="d"></div></div>
<script type="module">` + peleModule + `
// In a block of its own, clear of the names Pele's module takes.
{
  const view = document.getElementById("v"), box = document.getElementById("d");
  try { box.innerHTML = peleRender(` + scriptString(source) + `, {fontFamily: "-apple-system, system-ui, sans-serif"}).svg; }
  catch (e) { box.className = "err"; box.textContent = e.message; }

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
      parent.postMessage({leafletZoomed: told}, "*");
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
  let pinch = 0;
  addEventListener("gesturestart", e => { e.preventDefault(); pinch = 1; });
  addEventListener("gesturechange", e => {
    e.preventDefault();
    if (!pinch) return;
    zoomAt(...at(e), e.scale / pinch);
    pinch = e.scale;
  });
  addEventListener("gestureend", e => { e.preventDefault(); pinch = 0; });
  addEventListener("wheel", e => {
    if (!e.ctrlKey) return;
    e.preventDefault();
    if (!pinch) zoomAt(...at(e), Math.exp(-e.deltaY * (e.deltaMode ? 0.1 : 0.01)));
  }, {passive: false});
  let drag = null;
  view.addEventListener("pointerdown", e => {
    if (k === 1 || e.button !== 0) return;
    e.preventDefault();
    drag = {id: e.pointerId, x: e.clientX - x, y: e.clientY - y};
    view.setPointerCapture(e.pointerId);
    view.classList.add("dragging");
  });
  view.addEventListener("pointermove", e => {
    if (!drag || e.pointerId !== drag.id) return;
    x = e.clientX - drag.x;
    y = e.clientY - drag.y;
    show();
  });
  const drop = e => {
    if (drag && e.pointerId === drag.id) {
      drag = null;
      view.classList.remove("dragging");
    }
  };
  view.addEventListener("pointerup", drop);
  view.addEventListener("pointercancel", drop);
  view.addEventListener("dblclick", () => { k = 1; show(); });
  addEventListener("resize", show);
  addEventListener("message", e => {
    if (e.source !== parent || !e.data) return;
    const how = e.data.leafletZoom;
    if (how === "fit") { k = 1; show(); }
    else if (how === "in" || how === "out") zoomAt(view.clientWidth / 2, view.clientHeight / 2, how === "in" ? 1.5 : 1 / 1.5);
  });
}</script></body></html>`
}

// renderedBlock shows a math or diagram block. While the pointer is over
// it, a border and a bar that names it and, while editing, opens its source
// show; they keep their room otherwise, so nothing moves.
func (a *App) renderedBlock(c *ui.Context, n *doc.Node, width float32) {
	pal := theme.Of(c)
	_, source := doc.BlockSource(n)
	name, empty, edit := locales.RenderedMath(), locales.RenderedWriteFormula(), locales.RenderedEditMath()
	var page string
	if n.Type == "diagram" {
		name, empty, edit = locales.RenderedDiagram(), locales.RenderedWriteDiagram(), locales.RenderedEditDiagram()
		page = diagramPage(source)
	} else {
		page = mathPage(source)
	}
	editable := a.page != nil && !a.page.editor.ReadOnly
	const pad = 12
	box := ui.Column(c.Key("rendered:"+n.ID)).Padding(4, pad, pad).Gap(4).Radius(8).Border(1, ui.Transparent).Background(pal.Surface)
	hovered := box.Hovered()
	if hovered {
		box.BorderColor(pal.Divider)
	}
	shown := map[bool]float32{true: 1, false: 0}[hovered]
	box.Children(func() {
		ui.Row(c).Height(26).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).FontSize(12).TextColor(pal.TextMuted).Opacity(shown).Grow(1)
			if n.Type == "diagram" && strings.TrimSpace(source) != "" {
				zoom := func(key, icon, label, how string) {
					b := ui.ButtonBase(c.Key(key)).Size(24, 24).Center().Radius(6).TextColor(pal.TextMuted).
						Opacity(shown).Label(label).Tooltip(label)
					if b.Hovered() {
						b.Background(pal.Hover).TextColor(pal.Text)
					}
					b.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(13) })
					if b.Clicked() {
						a.zoomEmbed(n.ID, how)
					}
				}
				if a.embedZoomed[n.ID] {
					zoom("zoom-fit", "rotate-ccw", locales.RenderedZoomFit(), "fit")
				}
				zoom("zoom-out", "zoom-out", locales.RenderedZoomOut(), "out")
				zoom("zoom-in", "zoom-in", locales.RenderedZoomIn(), "in")
			}
			if editable && strings.TrimSpace(source) != "" {
				b := ui.ButtonBase(c.Key("edit")).Height(24).Padding(0, 8).Gap(4).Radius(6).TextColor(pal.TextMuted).
					Opacity(shown).AlignItems(ui.Center).Label(edit)
				if b.Hovered() {
					b.Background(pal.Hover).TextColor(pal.Text)
				}
				b.Children(func() {
					ui.Icon(c, theme.Icon("pencil")).FontSize(12)
					ui.Text(c, locales.CommonEdit()).FontSize(12)
				})
				if b.Clicked() {
					a.dialogs.openSource(n)
				}
			}
		})
		if strings.TrimSpace(source) == "" {
			// Nothing to draw yet: a button to write it.
			b := ui.ButtonBase(c.Key("write")).Height(56).FillWidth().Center().Gap(6).Radius(6).Background(pal.Ground).
				TextColor(pal.TextMuted).Label(empty)
			if b.Hovered() {
				b.TextColor(pal.Text)
			}
			b.Children(func() {
				ui.Icon(c, theme.Icon(map[bool]string{true: "workflow", false: "sigma"}[n.Type == "diagram"])).FontSize(15)
				ui.Text(c, empty).FontSize(13)
			})
			if b.Clicked() && editable {
				a.dialogs.openSource(n)
			}
			return
		}
		height := float32(64)
		if n.Type == "diagram" {
			height = 220
		}
		// The frame takes its content's height.
		a.sandbox(c, n.ID, page, width-2-2*pad, func(content float32) float32 {
			return min(max(content, 32), 1600)
		}, height, 4)
	})
}

// Math and diagram source --------------------------------------------------

func (d *dialogs) openSource(n *doc.Node) {
	d.sourceOpen, d.sourceID, d.sourceKind = true, n.ID, n.Type
	_, d.sourceText = doc.BlockSource(n)
}

func (a *App) sourceDialog(c *ui.Context) {
	d := &a.dialogs
	title, subtitle := locales.RenderedMath(), locales.RenderedMathHelp()
	if d.sourceKind == "diagram" {
		title, subtitle = locales.RenderedDiagram(), locales.RenderedDiagramHelp()
	}
	dialog(c, &d.sourceOpen, 600, func() {
		dialogHeader(c, title, subtitle, &d.sourceOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			ui.TextArea(c.Key("source"), &d.sourceText).Height(220).Font("monospace").FontSize(12.5).AutoFocus()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.sourceOpen = false
				}
				if primary(c, "save", "", locales.CommonSave()).Clicked() && a.page != nil {
					id, text := d.sourceID, d.sourceText
					a.page.editor.Edit(func(content *doc.Content) {
						if n := content.Find(id); n != nil {
							key, _ := doc.BlockSource(n)
							n.Set(key, strings.Trim(text, "\n"))
						}
					})
					d.sourceOpen = false
				}
			})
		})
	})
}
