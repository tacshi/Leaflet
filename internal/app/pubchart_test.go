package app

import (
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/locales"
)

// chartBlock is a chart block of kind, with a series of values for each
// of series, over labels.
func chartBlock(t *testing.T, kind string, labels []any, series ...[]any) *doc.Node {
	t.Helper()
	var ss []any
	for i, vals := range series {
		ss = append(ss, map[string]any{"name": "s" + string(rune('a'+i)), "values": vals})
	}
	data, err := doc.ChartData(map[string]any{"chart_type": kind, "title": "T", "labels": labels, "series": ss})
	if err != nil {
		t.Fatal(err)
	}
	return &doc.Node{ID: "c", Type: "chart", Data: data}
}

// A chart of one value draws whole: a pie of it is one slice, all of it,
// and a line or bar of it sits within the plot.
func TestChartOfOneValue(t *testing.T) {
	pie := chartHTML(chartBlock(t, "pie", []any{"All"}, []any{4.0}))
	if strings.Count(pie, "<path") != 1 || !strings.Contains(pie, locales.EditorPieShare(1)) {
		t.Errorf("pie:\n%s", pie)
	}
	for _, kind := range []string{"line", "bar"} {
		got := chartHTML(chartBlock(t, kind, []any{"Only"}, []any{7.0}))
		if got == "" || strings.Contains(got, "NaN") || strings.Contains(got, "Inf") || !strings.Contains(got, ">Only<") {
			t.Errorf("%s:\n%s", kind, got)
		}
	}
}

// A pie with nothing to share draws nothing, which the page shows as the
// chart's table instead.
func TestPieOfNothing(t *testing.T) {
	if got := chartHTML(chartBlock(t, "pie", []any{"A", "B"}, []any{0.0, -2.0})); got != "" {
		t.Errorf("pie:\n%s", got)
	}
}

// More series than colors take the colors again from the first, each in
// the legend.
func TestChartColorsComeRound(t *testing.T) {
	n := len(editor.ChartColors) + 3
	var series [][]any
	for i := range n {
		series = append(series, []any{float64(i + 1)})
	}
	got := chartHTML(chartBlock(t, "bar", []any{"A"}, series...))
	if strings.Count(got, "<i style=") != n || strings.Count(got, `<i style="background:var(--accent)">`) != 2 ||
		strings.Count(got, `<i style="background:`+editor.ChartColors[0]+`">`) != 2 {
		t.Errorf("legend:\n%s", got)
	}
}

// A pie with a slice more than there are colors gives the last slice,
// beside the first, the second's color rather than the first's, in the
// legend too, as the editor does.
func TestPieLastSliceNotLikeTheFirst(t *testing.T) {
	n := len(editor.ChartColors) + 2
	var labels, vals []any
	for i := range n {
		labels, vals = append(labels, string(rune('A'+i))), append(vals, 1.0)
	}
	got := chartHTML(chartBlock(t, "pie", labels, vals))
	_, last, _ := strings.Cut(got[strings.LastIndex(got, "<path "):], `fill="`)
	last, _, _ = strings.Cut(last, `"`)
	if last != editor.ChartColors[0] || strings.Count(got, `fill="var(--accent)"`) != 1 {
		t.Errorf("last slice %q:\n%s", last, got)
	}
	if !strings.HasSuffix(got, `<span><i style="background:`+editor.ChartColors[0]+`"></i>`+string(rune('A'+n-1))+`</span></div>`) {
		t.Errorf("legend:\n%s", got)
	}
}
