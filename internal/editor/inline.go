package editor

import (
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

// InlineSpans are the spans that show md, inline Markdown, as text size
// DIPs large, of weight and ink, with its marks as a page shows them: as
// a fact card's values and note show it, outside the editor. A link is
// colored and underlined; a formula in the line shows its TeX as code.
func InlineSpans(md string, size float32, weight int, ink ui.Color, pal *theme.Palette) []ui.Span {
	d := doc.FromMarkdown(md)
	code := theme.CodeFamily(d.Plain())
	out := make([]ui.Span, 0, len(d))
	for _, r := range d {
		a := r.Attrs
		s := ui.Span{Text: r.Text, Size: size, Weight: weight, Color: ink, Italic: a.Italic, Underline: a.Under, Strikethrough: a.Strike}
		if a.Bold {
			s.Weight = max(weight, 700)
		}
		if a.Code || a.Math {
			s.Font, s.Size, s.Background, s.Radius = code, size*0.9, pal.Ground, 3
		}
		if a.Href != "" {
			s.Color, s.Underline = pal.AccentText, true
		}
		if a.Color != "" {
			s.Color = textColor(a.Color, pal)
		}
		if a.Background != "" {
			s.Background = hexColor(a.Background)
		}
		out = append(out, s)
	}
	return out
}
