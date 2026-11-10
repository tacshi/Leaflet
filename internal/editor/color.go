package editor

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
)

func hexColor(s string) ui.Color {
	if !doc.ValidColor(s) {
		return ui.Color{}
	}
	return ui.Hex(s)
}

func colorHex(c ui.Color) string {
	switch c.A {
	case 0:
		return ""
	case 255:
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

func (e *Editor) SetColor(hex string) {
	e.Format(func(a doc.Attrs) bool { return a.Color == hex }, func(a *doc.Attrs, _ bool) { a.Color = hex })
}

func (e *Editor) SetHighlight(hex string) {
	e.Format(func(a doc.Attrs) bool { return a.Background == hex }, func(a *doc.Attrs, _ bool) { a.Background = hex })
}

func (e *Editor) selectionAttrs() doc.Attrs {
	a, _ := e.ordered()
	n := e.node(a.ID)
	if n == nil || !n.IsText() {
		return doc.Attrs{}
	}
	return n.Delta().AttrsAt(a.Off + 1)
}

type SelectionView struct {
	Start, End       Pos
	Text             string
	Color, Highlight string
	Mixed            bool
}

func (e *Editor) SelectionView() SelectionView {
	var v SelectionView
	e.indexed(func() {
		v.Start, v.End = e.ordered()
		attrs := e.selectionAttrs()
		if e.Collapsed() {
			attrs = doc.Attrs{}
			if e.hasTyping {
				attrs = e.typing
			} else if n := e.node(e.focus.ID); n != nil && n.IsText() {
				attrs = n.Delta().AttrsAt(e.focus.Off)
			}
		} else {
			_, v.Text = e.SelectedText()
			v.Mixed = v.Text != "" && !e.Has(func(a doc.Attrs) bool { return a.Color == attrs.Color && a.Background == attrs.Background })
		}
		v.Color, v.Highlight = attrs.Color, attrs.Background
	})
	return v
}

func (e *Editor) colored() bool {
	return !e.Collapsed() && !e.Has(func(a doc.Attrs) bool { return a.Color == "" && a.Background == "" })
}

var colors = []struct {
	key, hex, dark string
	name           func() string
}{
	{"red", "#ef4444", "#f87171", locales.EditorColorRed}, {"orange", "#f97316", "#fb923c", locales.EditorColorOrange},
	{"yellow", "#eab308", "#facc15", locales.EditorColorYellow}, {"green", "#22c55e", "#4ade80", locales.EditorColorGreen},
	{"teal", "#14b8a6", "#2dd4bf", locales.EditorColorTeal}, {"blue", "#3b82f6", "#60a5fa", locales.EditorColorBlue},
	{"indigo", "#6366f1", "#818cf8", locales.EditorColorIndigo}, {"purple", "#a855f7", "#c084fc", locales.EditorColorPurple},
	{"pink", "#ec4899", "#f472b6", locales.EditorColorPink}, {"brown", "#92400e", "#b98a5e", locales.EditorColorBrown},
	{"gray", "#71717a", "#a1a1aa", locales.EditorColorGray},
}

func textColor(hex string, pal *theme.Palette) ui.Color {
	c := hexColor(hex)
	if !pal.Dark || c.A == 0 {
		return c
	}
	for _, col := range colors {
		if light := ui.Hex(col.hex); light.R == c.R && light.G == c.G && light.B == c.B {
			return ui.Hex(col.dark).Alpha(float32(c.A) / 255)
		}
	}
	return c
}

const highlightAlpha = "40"

func (e *Editor) colorButton(c *ui.Context, pal *theme.Palette) {
	attrs := e.selectionAttrs()
	b := ui.Button(c.Key("color"), "").Label(locales.EditorColor()).Tooltip(locales.EditorColor()).KeepFocus()
	b.Children(func() {
		dot := ui.Box(c).Size(12, 12).Radius(999).Background(pal.Text)
		if attrs.Color != "" {
			dot.Background(textColor(attrs.Color, pal))
		}
		if attrs.Background != "" {
			dot.Border(2, hexColor(attrs.Background).Alpha(1))
		}
	})
	if b.Clicked() {
		e.colorOpen = !e.colorOpen
	}
	ui.Popover(c, b, &e.colorOpen, func() {
		ui.Column(c).Gap(6).Children(func() {
			e.colorDots(c, pal, "text", locales.EditorColorText(), locales.EditorNoTextColor(), attrs.Color, "", textColor, e.SetColor)
			e.colorDots(c, pal, "highlight", locales.EditorColorHighlight(), locales.EditorNoHighlight(), attrs.Background, highlightAlpha,
				func(hex string, _ *theme.Palette) ui.Color { return hexColor(hex) }, e.SetHighlight)
		})
	})
}

func (e *Editor) colorDots(c *ui.Context, pal *theme.Palette, key, label, none, value, alpha string,
	show func(string, *theme.Palette) ui.Color, set func(string)) {
	ui.Text(c, label).FontSize(12).FontWeight(600).TextColor(pal.TextMuted)
	ui.Row(c.Key("dots:" + key)).Gap(6).Children(func() {
		dot := func(key, name, hex string) {
			d := ui.ButtonBase(c.Key(key)).Size(18, 18).Radius(999).KeepFocus().Label(name).Tooltip(name)
			if hex == "" {
				d.Border(1.5, pal.Border)
			} else {
				d.Background(show(hex, pal))
			}
			if value == hex || hexColor(value) == hexColor(hex) && hex != "" {
				d.Border(2, pal.Text)
			} else if d.Hovered() && hex != "" {
				d.Border(2, pal.Border)
			}
			if d.Clicked() {
				set(hex)
			}
		}
		dot("none", none, "")
		for _, col := range colors {
			hex := col.hex + "ff"
			if alpha != "" {
				hex = col.hex + alpha
			}
			dot(col.key, col.name(), hex)
		}
	})
}
