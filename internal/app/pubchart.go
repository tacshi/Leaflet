package app

import (
	"fmt"
	"html"
	"math"
	"strings"

	"github.com/egoist/mygo/ui/chart"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/locales"
)

// chartHTML draws a chart block for a web page as the editor does, with
// MyGo's scales: its marks sit in percentages of the plot, as MyGo's do,
// so they follow the page's width, and their labels stay text.
func chartHTML(n *doc.Node) string {
	kind, _, _, labels, series := editor.ChartOf(n)
	if len(labels) == 0 || len(series) == 0 {
		return ""
	}
	var b strings.Builder
	switch kind {
	case "line":
		lineHTML(&b, labels, series)
	case "pie":
		if !pieHTML(&b, series) {
			return ""
		}
	default:
		barHTML(&b, labels, series)
	}
	if len(series) > 1 || kind == "pie" {
		var names []string
		if kind == "pie" {
			names = labels
		} else {
			for _, s := range series {
				names = append(names, s.Name)
			}
		}
		colors := chartColors(len(names))
		b.WriteString(`<div class="legend">`)
		for i, name := range names {
			fmt.Fprintf(&b, `<span><i style="background:%s"></i>%s</span>`, colors[i], html.EscapeString(name))
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

// chartColors are the colors of a chart's series, as the editor's: the
// accent first.
func chartColors(n int) []string {
	base := append([]string{"var(--accent)"}, editor.ChartColors...)
	out := make([]string, n)
	for i := range out {
		out[i] = base[i%len(base)]
	}
	return out
}

// pct is a fraction of the plot as a CSS percentage.
func pct(f float32) string { return fmt.Sprintf("%.3f%%", f*100) }

// chartValues are all the values of a chart's series.
func chartValues(series []editor.ChartSeries) []float64 {
	var out []float64
	for _, s := range series {
		out = append(out, s.Values...)
	}
	return out
}

// xLabels writes the labels under a plot, one under each band.
func xLabels(b *strings.Builder, x chart.Band) {
	for _, t := range x.Ticks() {
		fmt.Fprintf(b, `<span class="x" style="left:%s">%s</span>`, pct(t.At), html.EscapeString(t.Label))
	}
}

// barHTML writes bars from zero with their values above them: one
// series' tallest in the accent and the others muted, several series
// side by side in their own colors.
func barHTML(b *strings.Builder, labels []string, series []editor.ChartSeries) {
	lo, hi := chart.Extent(append(chartValues(series), 0)...)
	y := chart.NewLinear(lo, hi).Nice(4)
	x := chart.NewBand(labels...).Padding(0.38, 0.1)
	ns := len(series)
	colors := chartColors(ns)
	tallest := -1
	if ns == 1 {
		for i, v := range series[0].Values {
			if tallest < 0 || v > series[0].Values[tallest] {
				tallest = i
			}
		}
	}
	b.WriteString(`<div class="plot"><div class="area">`)
	fmt.Fprintf(b, `<span class="g" style="bottom:%s"></span>`, pct(y.At(0)))
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
				color = "var(--chart-muted)"
				if i == tallest {
					color = "var(--accent)"
				}
			}
			span := chart.Span{Lo: band.Lo + w*float32(si) + w*0.06, Hi: band.Lo + w*float32(si+1) - w*0.06}
			ys := y.Span(0, v)
			bottom, top := min(ys.Lo, ys.Hi), max(ys.Lo, ys.Hi)
			fmt.Fprintf(b, `<span class="b" style="left:%s;width:%s;bottom:%s;height:%s;background:%s"></span>`,
				pct(span.Lo), pct(span.Hi-span.Lo), pct(bottom), pct(top-bottom), color)
			fmt.Fprintf(b, `<span class="v" style="left:%s;bottom:%s">%s</span>`, pct(span.Mid()), pct(y.At(max(v, 0))), editor.FormatValue(v))
		}
	}
	xLabels(b, x)
	b.WriteString(`</div></div>`)
}

// lineHTML writes lines with a dot at each value, over gridlines whose
// values are at the left.
func lineHTML(b *strings.Builder, labels []string, series []editor.ChartSeries) {
	lo, hi := chart.Extent(chartValues(series)...)
	y := chart.NewLinear(lo, hi).Nice(3)
	x := chart.NewBand(labels...)
	ticks := y.Ticks(3)
	colors := chartColors(len(series))
	// Room at the left for the longest of the values there.
	wide := 0
	for _, t := range ticks {
		wide = max(wide, len([]rune(t.Label)))
	}
	fmt.Fprintf(b, `<div class="plot line" style="padding-left:calc(%.1fem + 8px)"><div class="area">`, float64(wide)*0.62)
	for _, t := range ticks {
		fmt.Fprintf(b, `<span class="g" style="bottom:%s"></span><span class="y" style="bottom:%s">%s</span>`, pct(t.At), pct(t.At), html.EscapeString(t.Label))
	}
	b.WriteString(`<svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">`)
	for si, s := range series {
		var pts []string
		for i, v := range s.Values {
			if i < len(labels) {
				pts = append(pts, fmt.Sprintf("%.3f,%.3f", x.At(i)*100, (1-y.At(v))*100))
			}
		}
		fmt.Fprintf(b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2.2" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"/>`,
			strings.Join(pts, " "), colors[si])
	}
	b.WriteString(`</svg>`)
	for si, s := range series {
		for i, v := range s.Values {
			if i < len(labels) {
				fmt.Fprintf(b, `<span class="d" style="left:%s;bottom:%s;background:%s"></span>`, pct(x.At(i)), pct(y.At(v)), colors[si])
			}
		}
	}
	xLabels(b, x)
	b.WriteString(`</div></div>`)
}

// pieHTML writes the first series' shares as slices, with the share of
// each one large enough on it. It reports whether there was anything to
// share.
func pieHTML(b *strings.Builder, series []editor.ChartSeries) bool {
	vals := series[0].Values
	total := 0.0
	for _, v := range vals {
		total += max(v, 0)
	}
	if total == 0 {
		return false
	}
	const cx, cy, radius = 100, 100, 90
	colors := chartColors(len(vals))
	fmt.Fprintf(b, `<svg class="pie" viewBox="0 0 200 200" role="img" aria-label="%s">`, html.EscapeString(locales.EditorPieChart()))
	for i, s := range chart.Pie(vals, 0.015) {
		// The gap between slices, as MyGo's Sector leaves it on a pie.
		d := math.Asin(min(1, s.Pad/2))
		a0, a1 := s.Start+d, s.End-d
		if a1 <= a0 {
			continue
		}
		x0, y0 := chart.Polar(cx, cy, radius, a0)
		x1, y1 := chart.Polar(cx, cy, radius, a1)
		large := 0
		if a1-a0 > math.Pi {
			large = 1
		}
		if a1-a0 >= 2*math.Pi-1e-6 {
			fmt.Fprintf(b, `<circle cx="%d" cy="%d" r="%d" fill="%s"/>`, cx, cy, radius, colors[i])
		} else {
			fmt.Fprintf(b, `<path d="M%d %d L%.2f %.2f A%d %d 0 %d 1 %.2f %.2f Z" fill="%s"/>`, cx, cy, x0, y0, radius, radius, large, x1, y1, colors[i])
		}
		if s.End-s.Start > 0.35 {
			lx, ly := chart.Polar(cx, cy, radius*0.62, s.Mid())
			fmt.Fprintf(b, `<text x="%.2f" y="%.2f" fill="#fff" font-size="12" text-anchor="middle" dominant-baseline="central" style="font-variant-numeric:tabular-nums">%s</text>`,
				lx, ly, html.EscapeString(locales.EditorPieShare(max(vals[i], 0)/total)))
		}
	}
	b.WriteString(`</svg>`)
	return true
}
