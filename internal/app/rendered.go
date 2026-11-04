package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/rendered"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// renderedBlock shows a math or diagram block. While the pointer is over
// it, a border and a bar that names it and, while editing, opens its source
// show; they keep their room otherwise, so nothing moves.
func (a *App) renderedBlock(c *ui.Context, n *doc.Node, width float32) {
	pal := theme.Of(c)
	_, source := doc.BlockSource(n)
	name, empty, edit := locales.RenderedMath(), locales.RenderedWriteFormula(), locales.RenderedEditMath()
	// The page, with KaTeX or Pele in it, is made only as the view loads.
	page := func() string { return rendered.MathPage(source) }
	if n.Type == "diagram" {
		name, empty, edit = locales.RenderedDiagram(), locales.RenderedWriteDiagram(), locales.RenderedEditDiagram()
		page = func() string { return rendered.DiagramPage(source, hexOf(pal.Surface), false) }
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
				a.zoomTools(c, n.ID, shown)
				if barButton(c, "full", "maximize-2", locales.RenderedFullScreen(), shown).Clicked() {
					a.page.fullDiagram = n.ID
				}
			}
			if editable && strings.TrimSpace(source) != "" {
				b := ui.ButtonBase(c.Key("edit")).Height(24).Padding(0, 8).Gap(4).Radius(6).TextColor(pal.TextMuted).
					Opacity(shown).IgnoreInput(shown == 0).AlignItems(ui.Center).Label(edit)
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
		// The frame takes its content's height. Math gives the page the
		// wheel; a diagram keeps Ctrl and the wheel to zoom.
		in := embedInput{src: source, kind: n.Type, toPage: n.Type == "math_equation"}
		a.sandbox(c, n.ID, in, page, width-2-2*pad, func(content float32) float32 {
			return min(max(content, 32), 1600)
		}, height)
	})
}

// barButton is an icon button of a rendered block's bar, shown as much as
// shown says.
func barButton(c *ui.Context, key, icon, label string, shown float32) ui.Element {
	pal := theme.Of(c)
	b := ui.ButtonBase(c.Key(key)).Size(24, 24).Center().Radius(6).TextColor(pal.TextMuted).
		Opacity(shown).IgnoreInput(shown == 0).Label(label).Tooltip(label)
	if b.Hovered() {
		b.Background(pal.Hover).TextColor(pal.Text)
	}
	b.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(13) })
	return b
}

// zoomTools are the buttons that zoom the diagram of embed key, and fit it
// again while it is zoomed in.
func (a *App) zoomTools(c *ui.Context, key string, shown float32) {
	if a.embedZoomed[key] && barButton(c, "zoom-fit", "rotate-ccw", locales.RenderedZoomFit(), shown).Clicked() {
		a.zoomEmbed(key, "fit")
	}
	if barButton(c, "zoom-out", "zoom-out", locales.RenderedZoomOut(), shown).Clicked() {
		a.zoomEmbed(key, "out")
	}
	if barButton(c, "zoom-in", "zoom-in", locales.RenderedZoomIn(), shown).Clicked() {
		a.zoomEmbed(key, "in")
	}
}

// fullDiagram fills the page's body with diagram n, to zoom and move about
// it; Escape or its button brings back the page where it was.
func (a *App) fullDiagram(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	key := "full:" + n.ID
	_, source := doc.BlockSource(n)
	ui.Column(c.Key(key)).Fill().Padding(0, pagePadX, 16).Children(func() {
		ui.Row(c).Height(40).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.RenderedDiagram()).FontSize(12).TextColor(pal.TextMuted).Grow(1)
			a.zoomTools(c, key, 1)
			if barButton(c, "exit-full", "minimize-2", locales.RenderedExitFullScreen(), 1).Clicked() {
				a.leaveFullDiagram()
			}
		})
		a.sandbox(c, key, embedInput{src: source, kind: "full"}, func() string {
			return rendered.DiagramPage(source, hexOf(pal.Surface), true)
		}, 0, nil, 0)
	})
	c.OnShortcut(0, ui.KeyEscape, a.leaveFullDiagram)
}

// leaveFullDiagram brings back the page from a diagram's full screen view,
// and lets its frame go.
func (a *App) leaveFullDiagram() {
	ps := a.page
	if ps == nil || ps.fullDiagram == "" {
		return
	}
	key := "full:" + ps.fullDiagram
	ps.fullDiagram = ""
	a.letGoEmbed(key)
	delete(a.embedIn, key)
	delete(a.embedSizes, key)
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
