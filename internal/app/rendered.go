package app

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
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

// diagramPage draws Mermaid source, in the page's colors.
func diagramPage(source string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>` + renderedStyle + `
.pele{--pele-bg:transparent;--pele-fg:#16181d;--pele-muted:#565c67;--pele-line:#3d424c;--pele-surface:#f5f6f8;--pele-surface-alt:#eceef2;--pele-border:#c9ced8;--pele-accent:#3446c9;--pele-font:-apple-system,system-ui,sans-serif}
:root[data-theme="dark"] .pele{--pele-fg:#e9ebef;--pele-muted:#9ea4af;--pele-line:#c0c5ce;--pele-surface:#23272e;--pele-surface-alt:#2b3038;--pele-border:#3a404a;--pele-accent:#8a97ff}
svg{max-width:100%;height:auto}</style></head><body><div id="d"></div>
<script type="module">` + peleModule + `
// In a block of its own, clear of the names Pele's module takes.
{
  const box = document.getElementById("d");
  try { box.innerHTML = peleRender(` + scriptString(source) + `, {fontFamily: "-apple-system, system-ui, sans-serif"}).svg; }
  catch (e) { box.className = "err"; box.textContent = e.message; }
}</script></body></html>`
}

// renderedBlock shows a math or diagram block, with a bar over it that
// names it and, while editing, opens its source.
func (a *App) renderedBlock(c *ui.Context, n *doc.Node, width float32) {
	pal := theme.Of(c)
	_, source := doc.BlockSource(n)
	name, empty, page := "Math", "Write a formula", mathPage(source)
	if n.Type == "diagram" {
		name, empty, page = "Diagram", "Write a diagram", diagramPage(source)
	}
	editable := a.page != nil && !a.page.editor.ReadOnly
	const pad = 12
	box := ui.Column(c.Key("rendered:"+n.ID)).Padding(4, pad, pad).Gap(4).Radius(8).Border(1, pal.Divider).Background(pal.Surface)
	hovered := box.Hovered()
	box.Children(func() {
		ui.Row(c).Height(26).AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).FontSize(12).TextColor(pal.TextMuted).Grow(1)
			if editable && strings.TrimSpace(source) != "" {
				b := ui.ButtonBase(c.Key("edit")).Height(24).Padding(0, 8).Gap(4).Radius(6).TextColor(pal.TextMuted).
					Opacity(map[bool]float32{true: 1, false: 0}[hovered]).AlignItems(ui.Center).Label("Edit " + strings.ToLower(name))
				if b.Hovered() {
					b.Background(pal.Hover).TextColor(pal.Text)
				}
				b.Children(func() {
					ui.Icon(c, theme.Icon("pencil")).FontSize(12)
					ui.Text(c, "Edit").FontSize(12)
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
	title, subtitle := "Math", "A formula in TeX, as \\frac{a}{b} or \\sum_{i=1}^n i^2, drawn in display style."
	if d.sourceKind == "diagram" {
		title, subtitle = "Diagram", "Mermaid: a flowchart, sequence, class, state, Gantt or other diagram."
	}
	dialog(c, &d.sourceOpen, 600, func() {
		dialogHeader(c, title, subtitle, &d.sourceOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			ui.TextArea(c.Key("source"), &d.sourceText).Height(220).Font("monospace").FontSize(12.5).AutoFocus()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", "Cancel").Clicked() {
					d.sourceOpen = false
				}
				if primary(c, "save", "", "Save").Clicked() && a.page != nil {
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
