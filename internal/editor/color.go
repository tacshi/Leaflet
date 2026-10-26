package editor

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

// colorHex is a color as runs keep it, "" for a clear one, which takes
// the color away.
func colorHex(c ui.Color) string {
	if c.A == 0 {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

// hexColor reads a color runs keep, clear for none.
func hexColor(s string) ui.Color {
	if !doc.ValidColor(s) {
		return ui.Color{}
	}
	return ui.Hex(s)
}

// SetColor colors the selected text, or what is typed next; "" takes the
// color away.
func (e *Editor) SetColor(hex string) {
	e.Format(func(a doc.Attrs) bool { return a.Color == hex }, func(a *doc.Attrs, _ bool) { a.Color = hex })
}

// SetHighlight highlights the selected text, or what is typed next; ""
// takes the highlight away.
func (e *Editor) SetHighlight(hex string) {
	e.Format(func(a doc.Attrs) bool { return a.Background == hex }, func(a *doc.Attrs, _ bool) { a.Background = hex })
}

// selectionAttrs are the formats of the first selected character.
func (e *Editor) selectionAttrs() doc.Attrs {
	a, _ := e.ordered()
	n := e.node(a.ID)
	if n == nil || !n.IsText() {
		return doc.Attrs{}
	}
	return n.Delta().AttrsAt(a.Off + 1)
}

// colored reports whether any selected text has a color or a highlight.
func (e *Editor) colored() bool {
	return !e.Collapsed() && !e.Has(func(a doc.Attrs) bool { return a.Color == "" && a.Background == "" })
}

// colors are the colors a run takes, MyGo's color picker's swatches, by
// name; a highlight is one at a quarter of its strength, which text
// reads through.
var colors = []struct {
	name, hex string
}{
	{"Red", "#ef4444"}, {"Orange", "#f97316"}, {"Yellow", "#eab308"}, {"Green", "#22c55e"},
	{"Teal", "#14b8a6"}, {"Blue", "#3b82f6"}, {"Indigo", "#6366f1"}, {"Purple", "#a855f7"},
	{"Pink", "#ec4899"}, {"Brown", "#92400e"}, {"Gray", "#71717a"},
}

// highlightAlpha is the strength of a highlight, in hex.
const highlightAlpha = "40"

// colorButton is the toolbar's color button, a dot of the selected text's
// color, the theme's text color while it has none, which opens a popover
// of dots: the text's colors and the highlights, each row after one that
// takes it away.
func (e *Editor) colorButton(c *ui.Context, pal *theme.Palette) {
	attrs := e.selectionAttrs()
	b := ui.Button(c, "").Label("Color").Tooltip("Color").KeepFocus()
	b.Children(func() {
		// The text's color: the theme's while it has none of its own.
		dot := ui.Box(c).Size(12, 12).Radius(999).Background(pal.Text)
		if attrs.Color != "" {
			dot.Background(hexColor(attrs.Color))
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
			e.colorDots(c, pal, "Text", attrs.Color, "", e.SetColor)
			e.colorDots(c, pal, "Highlight", attrs.Background, highlightAlpha, e.SetHighlight)
		})
	})
}

// colorDots is a row of dots choosing a color, alpha its strength, after
// one that takes the color away.
func (e *Editor) colorDots(c *ui.Context, pal *theme.Palette, label, value, alpha string, set func(string)) {
	ui.Text(c, label).FontSize(12).FontWeight(600).TextColor(pal.TextMuted)
	ui.Row(c.Key("dots:" + label)).Gap(6).Children(func() {
		dot := func(key, name, hex string) {
			d := ui.ButtonBase(c.Key(key)).Size(18, 18).Radius(999).KeepFocus().Label(name).Tooltip(name)
			if hex == "" {
				d.Border(1.5, pal.Border)
			} else {
				d.Background(hexColor(hex))
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
		dot("none", "No "+strings.ToLower(label)+" color", "")
		for _, col := range colors {
			hex := col.hex + "ff"
			if alpha != "" {
				hex = col.hex + alpha
			}
			dot(col.name, col.name, hex)
		}
	})
}
