package app

import (
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"leaflet/internal/theme"
)

// linesTables makes o draw Markdown's tables with simple lines, as agents'
// answers and comments show them: a bold header and a hairline under it
// and between rows, with no borders, each column as wide as its text asks
// among the others and aligned as the table's delimiter row says. Each
// cell is a view of its own Markdown, cells, which shares the selection of
// the view around it.
func linesTables(o, cells *markdown.Options) {
	cells.ShareSelection = true
	o.Block = func(c *ui.Context, d *parser.Document, i int) bool {
		if d.Block(i).Kind != parser.Table {
			return false
		}
		linesTable(c, d, i, cells)
		return true
	}
}

// answerCell is a cell of a Markdown table: its Markdown and how wide its
// text reads, in ems.
type answerCell struct {
	md    string
	width float32
}

func linesTable(c *ui.Context, d *parser.Document, i int, cells *markdown.Options) {
	pal := theme.Of(c)
	b := d.Block(i)
	cols := len(b.Align)
	if cols == 0 {
		return
	}
	src := d.Source()
	var rows [][]answerCell
	var header []bool
	weights := make([]float32, cols)
	for r := i + 1; r < b.Next; r = d.Block(r).Next {
		row := d.Block(r)
		out := make([]answerCell, cols)
		k := 0
		for cell := r + 1; cell < row.Next && k < cols; cell = d.Block(cell).Next {
			cb := d.Block(cell)
			if cb.Start >= 0 && cb.End > cb.Start && cb.End <= len(src) {
				out[k].md = strings.TrimSpace(src[cb.Start:cb.End])
			}
			out[k].width = textWidth(d, cell, out[k].md)
			weights[k] = max(weights[k], min(out[k].width, 24))
			k++
		}
		rows = append(rows, out)
		header = append(header, row.Header)
	}
	ui.Column(c).FillWidth().Role(ui.RoleTable).Children(func() {
		for r, row := range rows {
			el := ui.Row(c.Key(r)).FillWidth().Gap(16).AlignItems(ui.Stretch).Role(ui.RoleRow)
			if r > 0 {
				el.BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider)
			}
			el.Children(func() {
				for k, cell := range row {
					role := ui.RoleCell
					if header[r] {
						role = ui.RoleColumnHeader
					}
					box := ui.Column(c.Key(k)).Grow(max(weights[k], 2)).Basis(0).MinWidth(36).Padding(7, 0).Role(role)
					box.Children(func() {
						v := markdown.View(c.Key("md"), cell.md, cells).FillWidth().TextAlign(columnAlign(b.Align[k]))
						if header[r] {
							v.FontWeight(600)
						}
					})
				}
			})
		}
	})
}

// columnAlign is how a Markdown table's column of this alignment aligns
// its text.
func columnAlign(a parser.Align) ui.Align {
	switch a {
	case parser.AlignCenter:
		return ui.Center
	case parser.AlignRight:
		return ui.End
	}
	return ui.Start
}

// textWidth is how wide cell's text reads, in ems: a character wide in
// East Asian text counts twice, as it takes about an em, and others once,
// about half of one. The text is that of the cell's inlines, or its
// Markdown where it has none.
func textWidth(d *parser.Document, cell int, md string) float32 {
	var text strings.Builder
	for _, in := range d.Inlines(cell) {
		switch in.Kind {
		case parser.Text, parser.Code, parser.Math, parser.Mention, parser.Reference:
			text.WriteString(in.Text)
		}
	}
	s := text.String()
	if s == "" {
		s = md
	}
	var w float32
	for _, r := range s {
		w += 0.5
		if wide(r) {
			w += 0.5
		}
	}
	return w
}

// wide reports a character East Asian text draws an em wide.
func wide(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) || r >= 0x3000 && r <= 0x303f || r >= 0xff00 && r <= 0xffef
}
