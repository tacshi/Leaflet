package app

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// Lines after their times, as a recording's transcript and a video's notes
// show them: a click on a line plays its block from the line's time.

// timeColumn is the width of the times beside lines, size big: as wide as
// the longest of times, its digits all as wide, as the times' figures are.
func timeColumn(c *ui.Context, times []string, size float32) float32 {
	widest := ""
	for _, t := range times {
		if len(t) > len(widest) {
			widest = t
		}
	}
	widest = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '0'
		}
		return r
	}, widest)
	w, _ := c.MeasureText(0, ui.Span{Text: widest, Size: size, Features: "tnum"})
	return float32(math.Ceil(float64(w)))
}

// timeGap is the room between a line's time and its text.
const timeGap = 10

// timedRow draws a line after its time, label, in a column col wide and
// size big: a click on either plays from that time, unless body took the
// click for a time of its own in the line's text, as it reports. It
// returns the row, for its menu.
func timedRow(c *ui.Context, key any, label string, col, size float32, body func() (took bool), play func()) ui.Element {
	pal := theme.Of(c)
	// The time sits on the line's baseline, though its type is smaller.
	row := ui.Row(c.Key(key)).Gap(timeGap).AlignItems(ui.Baseline)
	row.Children(func() {
		// At the column's start, as the iOS app and a shared page have it,
		// in line with the notes' lines that have no time.
		ts := ui.ButtonBase(c.Key("ts")).Width(col).Justify(ui.Start)
		ts.Children(func() {
			ui.Text(c, label).FontSize(size).TextColor(pal.Accent).FontFeatures("tnum")
		})
		if ts.Clicked() {
			play()
		}
		line := ui.Column(c.Key("line")).Grow(1).Padding(0, 6).Margin(0, -6).Radius(6)
		if line.Hovered() {
			line.Background(pal.Hover)
		}
		took := false
		line.Children(func() { took = body() })
		if line.Clicked() && !took {
			play()
		}
	})
	return row
}

// timedText draws a line's text, at size and in color, its times in it
// links that play from each, as play gives them; it reports whether one
// was clicked.
func timedText(c *ui.Context, parts []doc.NotePart, size float32, color ui.Color, play func(at int)) (took bool) {
	pal := theme.Of(c)
	text := ""
	for _, p := range parts {
		text += p.Text
	}
	ui.RichText(c).FontSize(size).Font(theme.PageFamily(text)).TextColor(color).Children(func() {
		for i, p := range parts {
			if p.At < 0 {
				ui.Text(c.Key(i), p.Text)
				continue
			}
			t := ui.Text(c.Key(i), p.Text).TextColor(pal.Accent).FontFeatures("tnum").Cursor(ui.CursorPointer)
			if t.Hovered() {
				t.Underline()
			}
			if t.Clicked() {
				took = true
				play(p.At)
			}
		}
	})
	return took
}

// foldButton shows a long transcript's or notes' other lines, or folds
// them again: under the lines' text, past their times, col wide. It
// reports whether it was pressed.
func foldButton(c *ui.Context, total int, open bool, col float32) bool {
	pal := theme.Of(c)
	label, turn := locales.RecShowAll(total), float32(0)
	if open {
		label, turn = locales.RecShowLess(), 180
	}
	fold := ui.ButtonBase(c.Key("fold")).Margin(0, 0, 0, col+timeGap-6).Padding(2, 6).Radius(6).AlignSelf(ui.Start)
	if fold.Hovered() {
		fold.Background(pal.Hover)
	}
	fold.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).FontSize(12.5).FontWeight(500).TextColor(pal.Accent)
			ui.Icon(c, theme.Icon("chevron-down")).FontSize(13).TextColor(pal.Accent).Rotate(turn)
		})
	})
	return fold.Clicked()
}
