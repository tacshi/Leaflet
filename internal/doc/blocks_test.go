package doc

import (
	"strings"
	"testing"
)

func TestCalloutTones(t *testing.T) {
	if CalloutTone("TIP") != "tip" || CalloutTone("loud") != "info" {
		t.Error("tones")
	}
	for line, want := range map[string]string{"> [!NOTE]": "info", ">[!tip]": "tip", "> [!WARNING] ": "warning", "> [!CAUTION]": "danger", "> [!IMPORTANT]": "info"} {
		if got, ok := AlertTone(line); !ok || got != want {
			t.Errorf("%q: %q %v", line, got, ok)
		}
	}
	if _, ok := AlertTone("> just a quote"); ok {
		t.Error("a quote is not an alert")
	}
}

func TestNewBlockSpecs(t *testing.T) {
	spec := func(m map[string]any) *Node {
		t.Helper()
		n, err := SpecToNode(m, "", "Agent")
		if err != nil {
			t.Fatalf("%v: %v", m, err)
		}
		return n
	}
	callout := spec(map[string]any{"type": "callout", "text": "Back up **first**", "tone": "warning"})
	toggle := spec(map[string]any{"type": "toggle_list", "text": "Details", "collapsed": true,
		"children": []any{map[string]any{"type": "paragraph", "text": "Inside"}, map[string]any{"type": "code", "text": "x := 1"}}})
	link := spec(map[string]any{"type": "link_preview", "link": map[string]any{"url": "https://go.dev"}})
	math := spec(map[string]any{"type": "math_equation", "formula": " e^{i\\pi}+1=0 "})
	diagram := spec(map[string]any{"type": "diagram", "source": "graph TD\n  A-->B\n"})
	if callout.Str("tone") != "warning" || !toggle.Bool("collapsed") || len(toggle.Children) != 2 ||
		link.Str("url") != "https://go.dev" || math.Str("formula") != "e^{i\\pi}+1=0" || diagram.Str("source") != "graph TD\n  A-->B" {
		t.Errorf("nodes %+v %+v %+v %+v %+v", callout.Data, toggle.Data, link.Data, math.Data, diagram.Data)
	}
	for _, bad := range []map[string]any{
		{"type": "link_preview", "link": map[string]any{"url": "go.dev"}},
		{"type": "math_equation"},
		{"type": "diagram", "source": " "},
		{"type": "callout", "children": []any{map[string]any{"type": "paragraph"}}},
	} {
		if _, err := SpecToNode(bad, "", "Agent"); err == nil {
			t.Errorf("%v made a block", bad)
		}
	}
	c := FromBlocks([]*Node{callout, toggle, link, math, diagram})
	md := PageMarkdown("", c, nil)
	for _, want := range []string{
		"> [!WARNING]\n> Back up **first**",
		"<details>\n<summary>Details</summary>\n\nInside\n\n```\nx := 1\n```\n\n</details>",
		"[https://go.dev](https://go.dev)",
		"$$\ne^{i\\pi}+1=0\n$$",
		"```mermaid\ngraph TD\n  A-->B\n```",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	views := BlockViews(c)
	if views[0]["tone"] != "warning" || views[1]["collapsed"] != true || views[5]["formula"] != "e^{i\\pi}+1=0" || views[6]["source"] == nil {
		t.Errorf("views %v", views)
	}
}
