package editor

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
)

func (e *Editor) tableBlock(c *ui.Context, n *doc.Node, pal *theme.Palette) {
	rows, cols := n.Int("rowsLen", 0), n.Int("colsLen", 0)
	if rows == 0 || cols == 0 {
		return
	}
	if e.cellMove != 0 && strings.HasPrefix(e.cellEdit, n.ID+":") {
		e.moveCell(n)
		rows = n.Int("rowsLen", 0)
	}
	colW := max((e.width-float32(e.depth(n.ID))*indentStep-2)/float32(cols), 80)
	wrap := ui.Column(c.Key("table:" + n.ID)).Gap(6)
	editing := wrap.FocusWithin()
	wrap.Children(func() {
		ui.ScrollHorizontal(c).FillWidth().Children(func() {
			grid := ui.Column(c).Radius(8).Border(1, pal.Border).Clip()
			grid.Children(func() {
				for r := 0; r < rows; r++ {
					row := ui.Row(c.Key(r)).AlignItems(ui.Stretch)
					if r > 0 {
						row.BorderWidth(1, 0, 0, 0).BorderColor(pal.Border)
					}
					if r == 0 {
						row.Background(pal.Ground)
					}
					row.Children(func() {
						for col := 0; col < cols; col++ {
							cell := doc.TableCell(n, r, col)
							e.tableCell(c, n, cell, r, col, colW, pal)
						}
					})
				}
			})
		})
		// The row and column buttons show while a cell is being edited, so
		// they take no room otherwise.
		if !e.ReadOnly && editing {
			ui.Row(c).Gap(6).Children(func() {
				if small(c, "add-row", "plus", locales.EditorTableRow(), pal) {
					e.Edit(func(*doc.Content) { addTableRow(n) })
				}
				if small(c, "add-col", "plus", locales.EditorTableColumn(), pal) {
					e.Edit(func(*doc.Content) { addTableColumn(n) })
				}
				if rows > 1 && small(c, "del-row", "minus", locales.EditorTableRow(), pal) {
					e.Edit(func(*doc.Content) { removeTableRow(n) })
				}
				if cols > 1 && small(c, "del-col", "minus", locales.EditorTableColumn(), pal) {
					e.Edit(func(*doc.Content) { removeTableColumn(n) })
				}
			})
		}
	})
}

func small(c *ui.Context, key, icon, label string, pal *theme.Palette) bool {
	b := ui.ButtonBase(c.Key(key)).Height(24).Padding(0, 8).Gap(4).Radius(6).Border(1, pal.Divider).
		Background(pal.Surface).TextColor(pal.TextMuted).AlignItems(ui.Center)
	if b.Hovered() {
		b.TextColor(pal.Text)
	}
	b.Children(func() {
		ui.Icon(c, theme.Icon(icon)).FontSize(12)
		ui.Text(c, label).FontSize(12)
	})
	return b.Clicked()
}

// Moves between a table's cells, from Tab, Shift-Tab and Enter.
const (
	cellNext = 1 + iota
	cellPrev
	cellDown
)

func cellKey(table string, r, col int) string { return fmt.Sprintf("%s:%d:%d", table, r, col) }

// moveCell makes the move asked for in a cell of table n: Tab past the
// last cell adds a row, as other editors do, and Enter on the last row
// leaves the table for the block after it.
func (e *Editor) moveCell(n *doc.Node) {
	move := e.cellMove
	e.cellMove = 0
	var r, col int
	if _, err := fmt.Sscanf(strings.TrimPrefix(e.cellEdit, n.ID+":"), "%d:%d", &r, &col); err != nil {
		return
	}
	rows, cols := n.Int("rowsLen", 0), n.Int("colsLen", 0)
	switch move {
	case cellNext:
		if col++; col == cols {
			col, r = 0, r+1
		}
		if r == rows {
			e.Edit(func(*doc.Content) { addTableRow(n) })
		}
	case cellPrev:
		if col--; col < 0 {
			col, r = cols-1, r-1
		}
		if r < 0 {
			r, col = 0, 0
		}
	case cellDown:
		if r++; r == rows {
			e.cellEdit = ""
			us := e.units()
			if i := e.index(n.ID); i >= 0 && i+1 < len(us) {
				e.PlaceCaret(us[i+1].ID, 0)
				e.Focus()
			}
			return
		}
	}
	e.cellEdit, e.cellFocused = cellKey(n.ID, r, col), false
}

func (e *Editor) tableCell(c *ui.Context, table, cell *doc.Node, r, col int, w float32, pal *theme.Palette) {
	key := fmt.Sprintf("cell:%d:%d", r, col)
	box := ui.Box(c.Key(key)).Width(w).Padding(8, 10)
	if col > 0 {
		box.BorderWidth(0, 0, 0, 1).BorderColor(pal.Border)
	}
	var para *doc.Node
	if cell != nil && len(cell.Children) > 0 {
		para = cell.Children[0]
	}
	font := ui.Font{Family: theme.PageFamily(""), Size: e.Size * 0.88}
	if para != nil {
		font.Family = theme.PageFamily(para.Delta().Plain())
	}
	if r == 0 {
		font.Weight = 600
	}
	id := cellKey(table.ID, r, col)
	box.Children(func() {
		if e.ReadOnly || e.cellEdit != id {
			// Its text formatted, wrapping in the column; a click edits it.
			view := ui.Box(c.Key("view")).FillWidth().MinHeight(font.Size * 1.3).Cursor(ui.CursorText)
			view.Children(func() {
				if para != nil && para.Delta().Len() > 0 {
					ui.RichText(c, e.spans(para, pal, font)...).Font(font.Family).FontSize(font.Size).FontWeight(font.Weight).TextColor(pal.Text)
				}
			})
			if !e.ReadOnly && view.Clicked() {
				e.cellEdit, e.cellFocused = id, false
			}
			return
		}
		// The cell's Markdown, wrapping, while it is edited.
		stored := ""
		if para != nil {
			stored = para.Delta().ToMarkdown()
		}
		if e.cellFor != id || e.cellSaved != stored {
			e.cellFor, e.cellText, e.cellSaved = id, stored, stored
		}
		before := e.cellText
		in := ui.TextAreaBase(c.Key("in"), &e.cellText).Font(font.Family).FontSize(font.Size).FontWeight(font.Weight).
			TextColor(pal.Text).FillWidth().AutoFocus().
			OnTextKey(func(k ui.TextKey) bool {
				switch {
				case k.Key == ui.KeyTab && k.Mods == 0:
					e.cellMove = cellNext
				case k.Key == ui.KeyTab && k.Mods == ui.Shift:
					e.cellMove = cellPrev
				case k.Key == ui.KeyEnter && k.Mods == 0:
					e.cellMove = cellDown // a cell holds one paragraph
				default:
					return false
				}
				return true
			})
		if in.Focused() {
			e.cellFocused = true
		} else if e.cellFocused && e.cellMove == 0 {
			// The focus went elsewhere: show the cell formatted now,
			// not at whatever next redraws the window.
			e.cellEdit, e.cellFocused = "", false
			e.services.Invalidate()
		}
		if text := e.cellText; text != before {
			e.pushUndo(true)
			if cell == nil {
				cell = &doc.Node{ID: doc.NewBlockID(), Type: "table/cell", Data: map[string]any{"colPosition": col, "rowPosition": r}}
				table.Children = append(table.Children, cell)
				doc.SortTableCells(table)
			}
			if len(cell.Children) == 0 {
				cell.Children = []*doc.Node{newText("paragraph", nil)}
			}
			cell.Children[0].SetDelta(doc.FromMarkdown(text))
			e.cellSaved = cell.Children[0].Delta().ToMarkdown()
			e.invalidate(table.ID)
			e.changed()
		}
		if from, to := in.TextSelection(); from != to && in.Focused() {
			e.cellTools(c, in, table.ID, e.cellText, from, to, pal)
		}
	})
}

// cellTools offers Comment and Ask agent on text selected in a table
// cell, as the selection toolbar does in the page's text.
func (e *Editor) cellTools(c *ui.Context, in ui.Element, table, text string, from, to int, pal *theme.Palette) {
	runes := []rune(text)
	from, to = max(min(from, to, len(runes)), 0), min(max(from, to), len(runes))
	quote := string(runes[from:to])
	ui.Overlay(c, func() {
		bar := ui.Row(c.Key("cell-tools")).AttachTo(in, ui.AnchorTopLeft, ui.AnchorBottomLeft).Top(-8).Padding(4).Gap(2).
			Radius(9).Background(pal.Inverse).Shadow(0, 8, 24, 0, pal.Shadow).AlignItems(ui.Center)
		bar.Children(func() {
			// Comment shows its icon alone, as in the selection toolbar.
			text := func(key, icon, label string, iconOnly bool, fn func()) {
				b := ui.ButtonBase(c.Key(key)).Height(28).Padding(0, 9).Gap(6).Radius(6).KeepFocus().TextColor(pal.OnInverse).AlignItems(ui.Center)
				if b.Hovered() {
					b.Background(pal.OnInverse.Alpha(0.1))
				}
				b.Children(func() {
					ui.Icon(c, theme.Icon(icon)).FontSize(14)
					if !iconOnly {
						ui.Text(c, label).FontSize(13).FontWeight(500)
					}
				})
				if iconOnly {
					b.Label(label).Tooltip(label)
				}
				if b.Clicked() {
					fn()
				}
			}
			text("comment", "message-square", locales.CommonComment(), true, func() { e.Host.Comment(table, quote) })
			text("ask", "sparkles", locales.EditorAskAgent(), false, func() { e.Host.AskAgent(table, quote) })
		})
	})
}

func addTableRow(n *doc.Node) {
	rows, cols := n.Int("rowsLen", 0), n.Int("colsLen", 0)
	for col := 0; col < cols; col++ {
		n.Children = append(n.Children, &doc.Node{ID: doc.NewBlockID(), Type: "table/cell",
			Data: map[string]any{"colPosition": col, "rowPosition": rows}, Children: []*doc.Node{newText("paragraph", nil)}})
	}
	n.Set("rowsLen", rows+1)
	doc.SortTableCells(n)
}

func addTableColumn(n *doc.Node) {
	rows, cols := n.Int("rowsLen", 0), n.Int("colsLen", 0)
	for r := 0; r < rows; r++ {
		n.Children = append(n.Children, &doc.Node{ID: doc.NewBlockID(), Type: "table/cell",
			Data: map[string]any{"colPosition": cols, "rowPosition": r}, Children: []*doc.Node{newText("paragraph", nil)}})
	}
	n.Set("colsLen", cols+1)
	doc.SortTableCells(n)
}

func removeTableRow(n *doc.Node) {
	rows := n.Int("rowsLen", 0)
	var kept []*doc.Node
	for _, cell := range n.Children {
		if cell.Int("rowPosition", 0) != rows-1 {
			kept = append(kept, cell)
		}
	}
	n.Children = kept
	n.Set("rowsLen", rows-1)
}

func removeTableColumn(n *doc.Node) {
	cols := n.Int("colsLen", 0)
	var kept []*doc.Node
	for _, cell := range n.Children {
		if cell.Int("colPosition", 0) != cols-1 {
			kept = append(kept, cell)
		}
	}
	n.Children = kept
	n.Set("colsLen", cols-1)
}

// ChartSeries is one series of a chart block.
type ChartSeries struct {
	Name   string
	Values []float64
}

// ChartOf reads a chart block's data.
func ChartOf(n *doc.Node) (kind, title, caption string, labels []string, series []ChartSeries) {
	kind, title, caption = n.Str("chart_type"), n.Str("title"), n.Str("caption")
	if kind == "" {
		kind = "bar"
	}
	if ls, ok := n.Data["labels"].([]any); ok {
		for _, l := range ls {
			labels = append(labels, fmt.Sprint(l))
		}
	}
	if ss, ok := n.Data["series"].([]any); ok {
		for _, s := range ss {
			m, _ := s.(map[string]any)
			cs := ChartSeries{}
			cs.Name, _ = m["name"].(string)
			if vs, ok := m["values"].([]any); ok {
				for _, v := range vs {
					f, _ := doc.AsFloat(v)
					cs.Values = append(cs.Values, f)
				}
			}
			series = append(series, cs)
		}
	}
	return
}

// FormatValue writes a chart's value as its bars show it.
func FormatValue(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e12 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func (e *Editor) chartBlock(c *ui.Context, n *doc.Node, pal *theme.Palette) {
	kind, title, caption, labels, series := ChartOf(n)
	card := ui.Column(c.Key("chart:"+n.ID)).Padding(16, 18).Gap(10).Radius(10).Border(1, pal.Divider).Background(pal.Surface)
	hovered := card.Hovered()
	card.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontWeight(650).FontSize(15).TextColor(pal.Text).Grow(1)
			if !e.ReadOnly {
				b := ui.ButtonBase(c.Key("edit")).Height(24).Padding(0, 8).Gap(4).Radius(6).TextColor(pal.TextMuted).
					Opacity(map[bool]float32{true: 1, false: 0}[hovered]).AlignItems(ui.Center)
				if b.Hovered() {
					b.Background(pal.Hover)
				}
				b.Children(func() {
					ui.Icon(c, theme.Icon("pencil")).FontSize(12)
					ui.Text(c, locales.EditorEditData()).FontSize(12)
				})
				if b.Clicked() {
					e.Host.EditBlock(n.ID)
				}
			}
		})
		if len(labels) > 0 && len(series) > 0 {
			switch kind {
			case "line":
				lineChart(c, labels, series, pal)
			case "pie":
				pieChart(c, labels, series, pal)
			default:
				barChart(c, labels, series, pal)
			}
		}
		if len(series) > 1 || kind == "pie" {
			legend(c, kind, labels, series, pal)
		}
		if caption != "" {
			ui.Text(c, caption).FontSize(12).TextColor(pal.TextMuted)
		}
	})
}

// ChartColors are the colors of a chart's series after the first, which
// is the accent.
var ChartColors = []string{"#2F9E8F", "#D9822B", "#B04A9A", "#5B8DEF", "#8C6D46"}

// seriesColors are the colors of a chart's series: the accent first.
func seriesColors(pal *theme.Palette, n int) []ui.Color {
	base := []ui.Color{pal.Accent}
	for _, h := range ChartColors {
		base = append(base, ui.Hex(h))
	}
	out := make([]ui.Color, n)
	for i := range out {
		out[i] = base[i%len(base)]
	}
	return out
}

func legend(c *ui.Context, kind string, labels []string, series []ChartSeries, pal *theme.Palette) {
	names := []string{}
	if kind == "pie" {
		names = labels
	} else {
		for _, s := range series {
			names = append(names, s.Name)
		}
	}
	colors := seriesColors(pal, len(names))
	ui.Row(c).Wrap().GapX(14).GapY(4).Children(func() {
		for i, name := range names {
			ui.Row(c.Key(i)).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(9, 9).Radius(2).Background(colors[i])
				ui.Text(c, name).FontSize(12).TextColor(pal.TextMuted)
			})
		}
	})
}

// Charts are MyGo's: its scales, plot and axes, marks and paths, in the
// palette's colors.

// values are all the values of a chart's series.
func values(series []ChartSeries) []float64 {
	var out []float64
	for _, s := range series {
		out = append(out, s.Values...)
	}
	return out
}

// axisLabel is a tick's label, under or beside a plot.
func axisLabel(c *ui.Context, pal *theme.Palette) func(chart.Side, chart.Tick) {
	return func(_ chart.Side, t chart.Tick) {
		ui.Text(c, t.Label).FontSize(12).TextColor(pal.TextMuted).NoWrap()
	}
}

// barChart shows bars from zero with their values above them and their
// labels below: one series' tallest in the accent and the others muted,
// several series side by side in their own colors.
func barChart(c *ui.Context, labels []string, series []ChartSeries, pal *theme.Palette) {
	lo, hi := chart.Extent(append(values(series), 0)...)
	y := chart.NewLinear(lo, hi).Nice(4)
	x := chart.NewBand(labels...).Padding(0.38, 0.1)
	ns := len(series)
	colors := seriesColors(pal, ns)
	tallest := -1
	if ns == 1 {
		for i, v := range series[0].Values {
			if tallest < 0 || v > series[0].Values[tallest] {
				tallest = i
			}
		}
	}
	chart.Plot(c, chart.Axes{Bottom: x.Ticks(), Label: axisLabel(c, pal)}, func(area ui.Element) {
		chart.RuleY(c, y.At(0), 1).Background(pal.Divider)
		for i := range labels {
			band := x.Band(i)
			w := (band.Hi - band.Lo) / float32(ns)
			for si, s := range series {
				if i >= len(s.Values) {
					continue
				}
				v := s.Values[i]
				color := colors[si]
				if ns == 1 {
					color = pal.ChartMuted
					if i == tallest {
						color = pal.Accent
					}
				}
				span := chart.Span{Lo: band.Lo + w*float32(si) + w*0.06, Hi: band.Lo + w*float32(si+1) - w*0.06}
				chart.Rect(c, span, y.Span(0, v)).Background(color).Radius(4)
				chart.Anchor(c, span.Mid(), y.At(max(v, 0)), ui.Center, ui.End, func() {
					ui.Text(c, FormatValue(v)).FontSize(12).TextColor(pal.TextSecondary).NoWrap().Margin(0, 0, 3, 0)
				})
			}
		}
	}).FillWidth().Height(220).Gap(6).Padding(18, 0, 0) // room for the tallest bar's value
}

// lineChart shows lines with a dot at each value, over gridlines whose
// values are at the left.
func lineChart(c *ui.Context, labels []string, series []ChartSeries, pal *theme.Palette) {
	lo, hi := chart.Extent(values(series)...)
	y := chart.NewLinear(lo, hi).Nice(3)
	x := chart.NewBand(labels...)
	ticks := y.Ticks(3)
	colors := seriesColors(pal, len(series))
	chart.Plot(c, chart.Axes{Left: ticks, Bottom: x.Ticks(), Label: axisLabel(c, pal)}, func(area ui.Element) {
		area.Draw(func(p *ui.Painter, r ui.Rect) {
			for _, t := range ticks {
				p.Line(r.X, chart.Y(r, t.At), r.X+r.W, chart.Y(r, t.At), 1, pal.Divider)
			}
			for si, s := range series {
				var xs, ys []float32
				for i, v := range s.Values {
					if i < len(labels) {
						xs, ys = append(xs, x.At(i)), append(ys, y.At(v))
					}
				}
				p.StrokePath(chart.Line(r, xs, ys, chart.CurveLinear), 2.2, colors[si])
			}
		})
		for si, s := range series {
			for i, v := range s.Values {
				if i < len(labels) {
					chart.Point(c, x.At(i), y.At(v), 7, 7).Radius(3.5).Background(colors[si])
				}
			}
		}
	}).FillWidth().Height(220).Gap(8)
}

// pieChart shows the first series' shares as slices, with the share of
// each one large enough on it.
func pieChart(c *ui.Context, labels []string, series []ChartSeries, pal *theme.Palette) {
	vals := series[0].Values
	total := 0.0
	for _, v := range vals {
		total += max(v, 0)
	}
	if total == 0 {
		return
	}
	slices := chart.Pie(vals, 0.015)
	colors := seriesColors(pal, len(vals))
	ui.Box(c).Height(200).FillWidth().Role(ui.RoleImage).Label(locales.EditorPieChart()).Draw(func(p *ui.Painter, r ui.Rect) {
		radius := min(r.H, r.W) / 2 * 0.9
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		for i, s := range slices {
			p.FillPath(chart.Sector(cx, cy, 0, radius, s), colors[i])
			if s.End-s.Start > 0.35 {
				lx, ly := chart.Polar(cx, cy, radius*0.62, s.Mid())
				share := ui.Span{Text: locales.EditorPieShare(max(vals[i], 0) / total), Size: 12, Color: ui.Hex("#FFFFFF"), Font: "Figtree", Features: "tnum"}
				w, h := p.MeasureText(0, share)
				p.RichText(lx-w/2, ly-h/2, 0, share)
			}
		}
	})
}
