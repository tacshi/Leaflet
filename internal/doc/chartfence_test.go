package doc

import (
	"reflect"
	"strings"
	"testing"
)

func TestChartFence(t *testing.T) {
	fence := func(json string) string { return "Before.\n\n```chart\n" + json + "\n```\n\nAfter.\n" }
	nodes := MarkdownBlocks(fence(`{"chart_type": "line", "title": "Visits", "labels": ["Mon", "Tue"], "series": [{"name": "Web", "values": [3, 5.5]}], "caption": "This week"}`))
	if len(nodes) != 3 || nodes[1].Type != "chart" {
		t.Fatalf("a chart fence makes %v", nodes)
	}
	c := ChartOf(nodes[1])
	if nodes[1].Str("chart_type") != "line" || c.Title != "Visits" || nodes[1].Str("caption") != "This week" ||
		!reflect.DeepEqual(c.Labels, []string{"Mon", "Tue"}) || len(c.Series) != 1 || !reflect.DeepEqual(c.Series[0].Values, []float64{3, 5.5}) {
		t.Errorf("chart %+v, data %v", c, nodes[1].Data)
	}
	if n := MarkdownBlocks(fence(`{"labels": ["a"], "series": [{"name": "s", "values": [1]}]}`)); n[1].Type != "chart" || n[1].Str("chart_type") != "bar" {
		t.Errorf("a chart with no type makes %v %v, want a bar chart", n[1].Type, n[1].Data)
	}
	for _, bad := range []string{
		`not json`,
		`{"chart_type": "radar", "labels": ["a"], "series": [{"name": "s", "values": [1]}]}`,
		`{"labels": ["a", "b"], "series": [{"name": "s", "values": [1]}]}`,
		`{"labels": ["a"], "series": []}`,
		`{"chart_type": "pie", "labels": ["a"], "series": [{"name": "s", "values": [1]}, {"name": "t", "values": [2]}]}`,
	} {
		n := MarkdownBlocks(fence(bad))
		if len(n) != 3 || n[1].Type != "code" || n[1].Str("language") != "chart" || n[1].Delta().Plain() != bad {
			t.Errorf("%s: %v, want it kept as code", bad, n)
		}
	}
	if n := MarkdownBlocks("````\n```chart\n{\"labels\": [\"a\"], \"series\": [{\"name\": \"s\", \"values\": [1]}]}\n```\n````\n"); len(n) != 1 || n[0].Type != "code" {
		t.Errorf("a chart fence inside another fence's code makes %v", n)
	}
}

func TestFenceError(t *testing.T) {
	ok := "```chart\n{\"labels\": [\"a\"], \"series\": [{\"name\": \"s\", \"values\": [1]}]}\n```\n\n```json\n{\"labels\": 1}\n```\n\n```go\nfunc chart() {}\n```\n"
	if err := FenceError(MarkdownBlocks(ok)); err != nil {
		t.Errorf("a chart, and code in other languages: %v", err)
	}
	for md, want := range map[string]string{
		"```chart\n{\"labels\": [\"a\", \"b\"], \"series\": [{\"name\": \"s\", \"values\": [1]}]}\n```\n":                                                       `Series "s" has 1 values but there are 2 labels.`,
		"```chart\n{\"chart_type\": \"pie\", \"labels\": [\"a\"], \"series\": [{\"name\": \"s\", \"values\": [1]}, {\"name\": \"t\", \"values\": [2]}]}\n```\n": "exactly one series",
		"- item\n\n  ```card\n  {\"facts\": 3}\n  ```\n":                                                                                                        "```card fence",
		"```map\nnot json\n```\n": "not a JSON object",
	} {
		err := FenceError(MarkdownBlocks(md))
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Nothing was written") {
			t.Errorf("%q: %v, want %q", md, err, want)
		}
	}
}
