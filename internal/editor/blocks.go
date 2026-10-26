package editor

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
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
				if small(c, "add-row", "plus", "Row", pal) {
					e.Edit(func(*doc.Content) { addTableRow(n) })
				}
				if small(c, "add-col", "plus", "Column", pal) {
					e.Edit(func(*doc.Content) { addTableColumn(n) })
				}
				if rows > 1 && small(c, "del-row", "minus", "Row", pal) {
					e.Edit(func(*doc.Content) { removeTableRow(n) })
				}
				if cols > 1 && small(c, "del-col", "minus", "Column", pal) {
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
	font := ui.Font{Family: e.Family, Size: e.Size * 0.88}
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
		text := ""
		if para != nil {
			text = para.Delta().ToMarkdown()
		}
		before := text
		in := ui.TextAreaBase(c.Key("in"), &text).Font(font.Family).FontSize(font.Size).FontWeight(font.Weight).
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
		if text != before {
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
			e.invalidate(table.ID)
			e.changed()
		}
		if from, to := in.TextSelection(); from != to && in.Focused() {
			e.cellTools(c, in, table.ID, text, from, to, pal)
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
			text := func(key, icon, label string, fn func()) {
				b := ui.ButtonBase(c.Key(key)).Height(28).Padding(0, 9).Gap(6).Radius(6).KeepFocus().TextColor(pal.OnInverse).AlignItems(ui.Center)
				if b.Hovered() {
					b.Background(pal.OnInverse.Alpha(0.1))
				}
				b.Children(func() {
					ui.Icon(c, theme.Icon(icon)).FontSize(14)
					ui.Text(c, label).FontSize(13).FontWeight(500)
				})
				if b.Clicked() {
					fn()
				}
			}
			text("comment", "message-square", "Comment", func() { e.Host.Comment(table, quote) })
			text("ask", "sparkles", "Ask agent", func() { e.Host.AskAgent(table, quote) })
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

func fmtValue(v float64) string {
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
					ui.Text(c, "Edit data").FontSize(12)
				})
				if b.Clicked() {
					e.Host.EditBlock(n.ID)
				}
			}
		})
		height := float32(220)
		if kind == "pie" {
			height = 200
		}
		ui.Box(c).Height(height).FillWidth().Draw(func(p *ui.Painter, r ui.Rect) {
			switch kind {
			case "line":
				drawLine(p, r, labels, series, pal, e.Family)
			case "pie":
				drawPie(p, r, labels, series, pal, e.Family)
			default:
				drawBars(p, r, labels, series, pal, e.Family)
			}
		})
		if len(series) > 1 || kind == "pie" {
			legend(c, kind, labels, series, pal)
		}
		if caption != "" {
			ui.Text(c, caption).FontSize(12).TextColor(pal.TextMuted)
		}
	})
}

// seriesColors are the colors of a chart's series: the accent first.
func seriesColors(pal *theme.Palette, n int) []ui.Color {
	base := []ui.Color{pal.Accent, ui.Hex("#2F9E8F"), ui.Hex("#D9822B"), ui.Hex("#B04A9A"), ui.Hex("#5B8DEF"), ui.Hex("#8C6D46")}
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

func valueRange(series []ChartSeries) (float64, float64) {
	lo, hi := 0.0, 0.0
	for _, s := range series {
		for _, v := range s.Values {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	if hi == lo {
		hi = lo + 1
	}
	return lo, hi
}

func label(p *ui.Painter, x, y, maxW float32, text string, size float32, color ui.Color, family string, center bool) {
	s := ui.Span{Text: text, Size: size, Color: color, Font: "Figtree", Features: "tnum"}
	w, _ := p.MeasureText(0, s)
	if w > maxW && maxW > 0 {
		for len([]rune(text)) > 1 && w > maxW {
			rs := []rune(text)
			text = string(rs[:len(rs)-1])
			s.Text = text + "…"
			w, _ = p.MeasureText(0, s)
		}
	}
	if center {
		x -= w / 2
	}
	p.RichText(x, y, 0, s)
}

// drawBars draws the design's bars: the value above each, its label below,
// the largest in the accent color and the others muted. Several series
// stand side by side in their own colors.
func drawBars(p *ui.Painter, r ui.Rect, labels []string, series []ChartSeries, pal *theme.Palette, family string) {
	if len(labels) == 0 || len(series) == 0 {
		return
	}
	lo, hi := valueRange(series)
	top, bottom := r.Y+20, r.Y+r.H-22
	plotH := bottom - top
	zero := bottom - float32((0-lo)/(hi-lo))*plotH
	group := r.W / float32(len(labels))
	ns := len(series)
	barW := min(group*0.62/float32(ns), 64)
	colors := seriesColors(pal, ns)
	maxIdx, maxV := -1, math.Inf(-1)
	if ns == 1 {
		for i, v := range series[0].Values {
			if v > maxV {
				maxIdx, maxV = i, v
			}
		}
	}
	p.Fill(ui.Rect{X: r.X, Y: zero, W: r.W, H: 1}, pal.Divider, 0)
	for i, l := range labels {
		cx := r.X + group*(float32(i)+0.5)
		start := cx - barW*float32(ns)/2
		for si, s := range series {
			if i >= len(s.Values) {
				continue
			}
			v := s.Values[i]
			y := bottom - float32((v-lo)/(hi-lo))*plotH
			color := colors[si]
			if ns == 1 {
				color = pal.ChartMuted
				if i == maxIdx {
					color = pal.Accent
				}
			}
			x := start + barW*float32(si) + 2
			bar := ui.Rect{X: x, Y: min(y, zero), W: barW - 4, H: max(abs32(zero-y), 1)}
			p.Fill(bar, color, 4)
			label(p, x+(barW-4)/2, min(y, zero)-17, barW+8, fmtValue(v), 12, pal.TextSecondary, family, true)
		}
		label(p, cx, bottom+5, group-4, l, 12, pal.TextMuted, family, true)
	}
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// niceTicks splits lo..hi into about n steps of 1, 2 or 5 times a power
// of ten, widened to whole steps: the values of a chart's gridlines.
func niceTicks(lo, hi float64, n int) []float64 {
	raw := (hi - lo) / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 5, 10} {
		if step = m * mag; step >= raw {
			break
		}
	}
	var ticks []float64
	for v := math.Floor(lo/step) * step; v < hi+step*0.999; v += step {
		ticks = append(ticks, math.Round(v/step)*step)
	}
	return ticks
}

func textWidth(p *ui.Painter, text string, size float32) float32 {
	w, _ := p.MeasureText(0, ui.Span{Text: text, Size: size, Font: "Figtree", Features: "tnum"})
	return w
}

// drawLine draws the lines over gridlines with their values at the left,
// inset so the first and last labels stay inside.
func drawLine(p *ui.Painter, r ui.Rect, labels []string, series []ChartSeries, pal *theme.Palette, family string) {
	if len(labels) == 0 || len(series) == 0 {
		return
	}
	lo, hi := valueRange(series)
	ticks := niceTicks(lo, hi, 3)
	lo, hi = ticks[0], ticks[len(ticks)-1]
	gutter := float32(0)
	for _, t := range ticks {
		gutter = max(gutter, textWidth(p, fmtValue(t), 11))
	}
	gutter += 10
	left := r.X + gutter + max(8, textWidth(p, labels[0], 12)/2)
	right := r.X + r.W - max(8, textWidth(p, labels[len(labels)-1], 12)/2)
	top, bottom := r.Y+14, r.Y+r.H-22
	for _, t := range ticks {
		y := bottom - float32((t-lo)/(hi-lo))*(bottom-top)
		p.Fill(ui.Rect{X: r.X + gutter, Y: y, W: r.W - gutter, H: 1}, pal.Divider, 0)
		text := fmtValue(t)
		label(p, r.X+gutter-10-textWidth(p, text, 11), y-7, 0, text, 11, pal.TextMuted, family, false)
	}
	step := float32(0)
	if len(labels) > 1 {
		step = (right - left) / float32(len(labels)-1)
	}
	colors := seriesColors(pal, len(series))
	for si, s := range series {
		var path ui.Path
		var pts [][2]float32
		for i, v := range s.Values {
			if i >= len(labels) {
				break
			}
			x := left + step*float32(i)
			y := bottom - float32((v-lo)/(hi-lo))*(bottom-top)
			pts = append(pts, [2]float32{x, y})
			if i == 0 {
				path.MoveTo(x, y)
			} else {
				path.LineTo(x, y)
			}
		}
		p.StrokePath(&path, 2.2, colors[si])
		for _, pt := range pts {
			var dot ui.Path
			dot.Circle(pt[0], pt[1], 3.5)
			p.FillPath(&dot, colors[si])
		}
	}
	for i, l := range labels {
		label(p, left+step*float32(i), bottom+5, max(step-4, 40), l, 12, pal.TextMuted, family, true)
	}
}

func drawPie(p *ui.Painter, r ui.Rect, labels []string, series []ChartSeries, pal *theme.Palette, family string) {
	if len(series) == 0 || len(series[0].Values) == 0 {
		return
	}
	values := series[0].Values
	total := 0.0
	for _, v := range values {
		total += math.Max(v, 0)
	}
	if total == 0 {
		return
	}
	radius := min(r.H, r.W) / 2 * 0.9
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	colors := seriesColors(pal, len(values))
	angle := -math.Pi / 2
	for i, v := range values {
		sweep := math.Max(v, 0) / total * 2 * math.Pi
		var path ui.Path
		path.MoveTo(cx, cy)
		steps := max(int(sweep/(math.Pi/48)), 1)
		for s := 0; s <= steps; s++ {
			a := angle + sweep*float64(s)/float64(steps)
			path.LineTo(cx+radius*float32(math.Cos(a)), cy+radius*float32(math.Sin(a)))
		}
		path.Close()
		p.FillPath(&path, colors[i])
		p.StrokePath(&path, 1.5, pal.Surface)
		if sweep > 0.35 {
			mid := angle + sweep/2
			pct := fmt.Sprintf("%.0f%%", v/total*100)
			label(p, cx+radius*0.62*float32(math.Cos(mid)), cy+radius*0.62*float32(math.Sin(mid))-8, 0, pct, 12, ui.Hex("#FFFFFF"), family, true)
		}
		angle += sweep
	}
}
