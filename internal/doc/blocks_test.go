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

// A callout's icon gives it the next tone, the first after the last; a
// tone not known reads as the first.
func TestNextTone(t *testing.T) {
	for tone, want := range map[string]string{"info": "tip", "tip": "warning", "warning": "danger", "danger": "info", " Warning ": "danger", "loud": "tip", "": "tip"} {
		if got := NextTone(tone); got != want {
			t.Errorf("NextTone(%q) = %q, want %q", tone, got, want)
		}
	}
}

// Math keeps its formula, and a diagram its Mermaid, under keys of their
// own, which their editors write.
func TestSourceKey(t *testing.T) {
	for _, n := range []*Node{MathNode("x^2"), DiagramNode("graph TD")} {
		key, source := BlockSource(n)
		if key != SourceKey(n.Type) || source == "" || n.Str(key) != source {
			t.Errorf("%s: %q %q", n.Type, key, source)
		}
	}
	if SourceKey("math_equation") != "formula" || SourceKey("diagram") != "source" {
		t.Error("keys")
	}
}

// The children of a block that can't hold them come out after it, in
// order, as a merge lifts them, in a copy; a list item and a table keep
// theirs, and a page with none stranded is itself.
func TestLifted(t *testing.T) {
	kid := func(id string) *Node { return &Node{ID: id, Type: "paragraph"} }
	c := &Content{Children: []*Node{
		{ID: "p", Type: "paragraph", Children: []*Node{kid("a"), {ID: "q", Type: "quote", Children: []*Node{kid("b")}}}},
		{ID: "l", Type: "bulleted_list", Children: []*Node{kid("c")}},
		{ID: "t", Type: "table", Children: []*Node{{ID: "cell", Type: "table/cell", Children: []*Node{kid("d")}}}},
	}}
	lifted := c.Lifted()
	var ids []string
	for _, n := range lifted.Children {
		ids = append(ids, n.ID)
	}
	if strings.Join(ids, " ") != "p a q b l t" {
		t.Errorf("top level %v", ids)
	}
	if len(lifted.Find("l").Children) != 1 || len(lifted.Find("t").Children) != 1 {
		t.Error("a list item and a table keep their children")
	}
	if len(c.Children) != 3 || len(c.Children[0].Children) != 2 {
		t.Error("the page lifted is a copy")
	}
	if again := lifted.Lifted(); again != lifted {
		t.Error("a page with none stranded is itself")
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
	md := PageMarkdown("", c, MarkdownOptions{})
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
