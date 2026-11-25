package doc

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"leaflet/locales"
)

func TestChartValuesAreFinite(t *testing.T) {
	for _, v := range []any{"NaN", "inf", "-Inf", math.NaN(), math.Inf(1)} {
		n, err := SpecToNode(map[string]any{"type": "chart", "chart": map[string]any{
			"labels": []any{"a", "b"}, "series": []any{map[string]any{"name": "s", "values": []any{v, 2.0}}},
		}}, "", "Agent")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := json.Marshal(FromBlocks([]*Node{n})); err != nil {
			t.Errorf("%v: %v", v, err)
		}
	}
}

func TestSpecNestingIsBounded(t *testing.T) {
	deep := map[string]any{"type": "bulleted_list", "text": "x"}
	for range 100 {
		deep = map[string]any{"type": "bulleted_list", "text": "x", "children": []any{deep}}
	}
	if _, err := SpecToNode(deep, "", "Agent"); err == nil {
		t.Error("nested 100 deep")
	}
	var many []any
	for range 3000 {
		many = append(many, map[string]any{"type": "bulleted_list", "text": "x"})
	}
	if _, err := SpecToNode(map[string]any{"type": "bulleted_list", "text": "x", "children": many}, "", "Agent"); err == nil {
		t.Error("3000 items")
	}
	var rows []any
	for range 100 {
		row := make([]any, 100)
		for i := range row {
			row[i] = "x"
		}
		rows = append(rows, row)
	}
	if _, err := SpecToNode(map[string]any{"type": "table", "rows": rows}, "", "Agent"); err == nil {
		t.Error("100 by 100 table")
	}
	if _, err := SpecToNode(map[string]any{"type": "table", "rows": rows[:10]}, "", "Agent"); err != nil {
		t.Error(err)
	}
	ok := map[string]any{"type": "bulleted_list", "text": "x", "children": []any{map[string]any{"type": "bulleted_list", "text": "y"}}}
	if _, err := SpecToNode(ok, "", "Agent"); err != nil {
		t.Error(err)
	}
}

func TestSetTableRows(t *testing.T) {
	n, _ := TableNode([][]string{{"A", "B"}, {"**1**", "2"}}, "")
	bold := tableCell(n, 1, 0)
	if err := SetTableRows(n, [][]string{{"A", "B", "C"}, {"**1**", "two", ""}, {"", "", ""}}); err != nil {
		t.Fatal(err)
	}
	if rows, cols := TableSize(n); rows != 3 || cols != 3 {
		t.Fatalf("table is %dx%d, want 3x3", rows, cols)
	}
	if tableCell(n, 1, 0) != bold || tableCell(n, 1, 1).Children[0].Delta().Plain() != "two" {
		t.Errorf("rows are %v", TableRows(n))
	}
	if err := SetTableRows(n, [][]string{{"A"}}); err != nil || len(n.Children) != 1 {
		t.Errorf("shrunk to %d cells, %v", len(n.Children), err)
	}
	if err := SetTableRows(n, nil); err == nil {
		t.Error("a table of no rows was kept")
	}
}

func TestEmbedSizeInBytes(t *testing.T) {
	embed := func(html string) error {
		_, err := SpecToNode(map[string]any{"type": "embed", "embed": map[string]any{"html": html}}, "", "")
		return err
	}
	if err := embed(strings.Repeat("x", maxEmbedHTML)); err != nil {
		t.Errorf("HTML of %d bytes refused: %v", maxEmbedHTML, err)
	}
	if err := embed(strings.Repeat("字", maxEmbedHTML/3+1)); err == nil {
		t.Errorf("HTML of %d characters, %d bytes, taken", maxEmbedHTML/3+1, 3*(maxEmbedHTML/3+1))
	}
}

func TestACallsBlocksCountTogether(t *testing.T) {
	list := func(n int) map[string]any {
		var kids []any
		for range n {
			kids = append(kids, map[string]any{"type": "bulleted_list", "text": "x"})
		}
		return map[string]any{"type": "bulleted_list", "text": "x", "children": kids}
	}
	if _, err := SpecsToNodes([]any{list(maxSpecNodes / 2), list(maxSpecNodes / 2)}, ""); err == nil {
		t.Error("a call made more blocks than a call makes")
	}
	var rows []any
	for range 21 {
		rows = append(rows, []any{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t"})
	}
	var many []any
	for range 300 {
		many = append(many, map[string]any{"text": "x"})
	}
	if _, err := SpecsToNodes(append(many, map[string]any{"type": "table", "rows": rows}), ""); err != nil {
		t.Error(err)
	}
}

func TestCallNodesNestAsDeepAsSpecs(t *testing.T) {
	nested := func(depth int) []*Node {
		n := &Node{ID: "x", Type: "bulleted_list"}
		top := n
		for range depth - 1 {
			k := &Node{ID: "x", Type: "bulleted_list"}
			n.Children = []*Node{k}
			n = k
		}
		return []*Node{top}
	}
	if err := CallNodes(nested(maxSpecDepth)); err != nil {
		t.Error(err)
	}
	if err := CallNodes(nested(maxSpecDepth + 1)); err == nil {
		t.Errorf("blocks %d deep taken", maxSpecDepth+1)
	}
}

func TestChartsAreBounded(t *testing.T) {
	chart := func(labels, series, label, name, title, caption int) map[string]any {
		ls := make([]any, labels)
		for i := range ls {
			ls[i] = strings.Repeat("l", label)
		}
		ss := make([]any, series)
		for i := range ss {
			vs := make([]any, labels)
			for k := range vs {
				vs[k] = 1.0
			}
			ss[i] = map[string]any{"name": strings.Repeat("s", name), "values": vs}
		}
		return map[string]any{"title": strings.Repeat("t", title), "caption": strings.Repeat("c", caption), "labels": ls, "series": ss}
	}
	if _, err := ChartData(chart(MaxChartLabels, MaxChartSeries, MaxChartLabel, MaxChartName, MaxChartTitle, MaxChartCaption)); err != nil {
		t.Errorf("a chart as large as one may be: %v", err)
	}
	for name, c := range map[string]map[string]any{
		"labels":  chart(MaxChartLabels+1, 1, 1, 1, 0, 0),
		"series":  chart(1, MaxChartSeries+1, 1, 1, 0, 0),
		"label":   chart(1, 1, MaxChartLabel+1, 1, 0, 0),
		"name":    chart(1, 1, 1, MaxChartName+1, 0, 0),
		"title":   chart(1, 1, 1, 1, MaxChartTitle+1, 0),
		"caption": chart(1, 1, 1, 1, 0, MaxChartCaption+1),
	} {
		raw, _ := json.Marshal(c)
		if _, err := SpecToNode(map[string]any{"type": "chart", "chart": c}, "", ""); err == nil {
			t.Errorf("too long a %s: no error", name)
		}
		if nodes := MarkdownBlocks("```chart\n" + string(raw) + "\n```"); nodes[0].Type != "code" || FenceError(nodes) == nil {
			t.Errorf("too long a %s: the fence made a %s", name, nodes[0].Type)
		}
	}
}

func TestChartSeriesWithoutANameIsNamedWhereItShows(t *testing.T) {
	n, err := SpecToNode(map[string]any{"type": "chart", "chart": map[string]any{"labels": []any{"a"},
		"series": []any{map[string]any{"values": []any{1.0}}, map[string]any{"name": "Web", "values": []any{2.0}}}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ch := ChartOf(n)
	if ch.Series[0].Name != "" {
		t.Errorf("kept the name %q", ch.Series[0].Name)
	}
	if got := SeriesName(locales.In("ja"), 0, ch.Series[0]); got != "系列 1" {
		t.Errorf("named %q in Japanese", got)
	}
	if got := SeriesName(locales.In("ja"), 1, ch.Series[1]); got != "Web" {
		t.Errorf("a series' own name read as %q", got)
	}
	md := PageMarkdown("", FromBlocks([]*Node{n}), MarkdownOptions{Lang: "zh-Hans"})
	page := PageHTML("", FromBlocks([]*Node{n}), HTMLOptions{Lang: "zh-Hans"})
	if !strings.Contains(md, "| 系列 1 | 1 |") || !strings.Contains(page, "<th>系列 1</th>") {
		t.Errorf("a series without a name not named in the reader's language:\n%s\n%s", md, page)
	}
	_, err = ChartData(map[string]any{"labels": []any{"a", "b"}, "series": []any{map[string]any{"values": []any{1.0}}}})
	if err == nil || err.Error() != "Series 1 has 1 values but there are 2 labels." {
		t.Errorf("a series without a name named in an error as %v", err)
	}
}
