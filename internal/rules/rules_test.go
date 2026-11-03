package rules

import (
	"reflect"
	"testing"
)

// A new line after a list goes on as the list, a to-do unchecked; after
// a heading, a quote, a callout or code it is a paragraph.
func TestNewLine(t *testing.T) {
	for typ, want := range map[string]string{
		"bulleted_list": "bulleted_list", "numbered_list": "numbered_list", "toggle_list": "toggle_list",
		"todo_list": "todo_list", "paragraph": "paragraph", "heading": "paragraph", "quote": "paragraph",
		"callout": "paragraph", "code": "paragraph",
	} {
		if got, _ := NewLine(typ); got != want {
			t.Errorf("NewLine(%s) = %s, want %s", typ, got, want)
		}
	}
	if _, data := NewLine("todo_list"); !reflect.DeepEqual(data, map[string]any{"checked": false}) {
		t.Errorf("a to-do's new line has %v", data)
	}
}

// Enter splits a block, ends an empty one, or puts a line before it at
// its start, as the block and where it is say.
func TestEnter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		b       Block
		atStart bool
		want    Line
	}{
		{"a block without text", Block{Type: "image"}, false, Line{Action: LineAfter, Type: "paragraph"}},
		{"code", Block{Type: "code"}, false, Line{Action: CodeLine}},
		{"an empty nested item", Block{Type: "bulleted_list", Empty: true, Nested: true}, true, Line{Action: Outdent}},
		{"an empty item", Block{Type: "todo_list", Empty: true}, true, Line{Action: Plain}},
		{"an empty quote", Block{Type: "quote", Empty: true, Nested: true}, true, Line{Action: Plain}},
		{"an empty paragraph last in a toggle", Block{Type: "paragraph", Empty: true, Nested: true, Last: true}, true, Line{Action: Outdent}},
		// An empty paragraph with more after it in a toggle stays in it,
		// a line after it: going out, it would take them with it.
		{"an empty paragraph in a toggle", Block{Type: "paragraph", Empty: true, Nested: true}, true, Line{Action: Split, Type: "paragraph"}},
		// An empty heading keeps its kind: Enter writes on after it.
		{"an empty heading", Block{Type: "heading", Empty: true}, true, Line{Action: Split, Type: "paragraph"}},
		{"the start of a to-do", Block{Type: "todo_list"}, true, Line{Action: LineBefore, Type: "todo_list", Data: map[string]any{"checked": false}}},
		{"the start of a heading", Block{Type: "heading"}, true, Line{Action: LineBefore, Type: "paragraph"}},
		{"an open toggle", Block{Type: "toggle_list"}, false, Line{Action: SplitInside, Type: "paragraph"}},
		{"a closed toggle", Block{Type: "toggle_list", Collapsed: true, Holds: true}, false, Line{Action: Split, Type: "toggle_list"}},
		{"an item holding items", Block{Type: "numbered_list", Holds: true}, false, Line{Action: SplitInside, Type: "numbered_list"}},
		{"a folded heading", Block{Type: "heading", Collapsed: true}, false, Line{Action: Split, Type: "paragraph", Unfold: true}},
		{"a quote", Block{Type: "quote"}, false, Line{Action: Split, Type: "paragraph"}},
	} {
		if got := Enter(tc.b, tc.atStart); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Backspace at a block's start ends a list item, a quote or a heading,
// takes a paragraph last in a toggle out of it, and else joins the block
// to the one before: one without text is chosen first.
func TestBackspace(t *testing.T) {
	text, image := Block{Type: "paragraph"}, Block{Type: "image"}
	for _, tc := range []struct {
		name    string
		b, prev Block
		hasPrev bool
		want    Action
	}{
		{"a heading", Block{Type: "heading"}, text, true, Plain},
		{"a nested item", Block{Type: "bulleted_list", Nested: true}, text, true, Outdent},
		{"a nested quote", Block{Type: "quote", Nested: true}, text, true, Plain},
		{"a paragraph last in a toggle", Block{Type: "paragraph", Nested: true, Last: true}, text, true, Outdent},
		// A paragraph with more after it in a toggle joins the block
		// before, as text does: going out, it would take them with it.
		{"a paragraph in a toggle", Block{Type: "paragraph", Nested: true}, text, true, Key},
		{"the page's first paragraph", text, Block{}, false, Nothing},
		{"an empty paragraph after an image", Block{Type: "paragraph", Empty: true}, image, true, Remove},
		{"a paragraph after an image", text, image, true, SelectBefore},
		{"a paragraph after text", text, text, true, Key},
	} {
		if got := Backspace(tc.b, tc.prev, tc.hasPrev); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Delete at a block's end chooses a block without text, after it or
// itself, before joining.
func TestDelete(t *testing.T) {
	text, image := Block{Type: "paragraph"}, Block{Type: "image"}
	if Delete(text, image) != SelectAfter || Delete(image, text) != SelectThis || Delete(text, text) != Key {
		t.Error("delete")
	}
}

// Tab nests list items in list items, and any text in a toggle: not a
// paragraph in a list item, nor a block without text.
func TestNests(t *testing.T) {
	for _, tc := range []struct {
		typ, under string
		want       bool
	}{
		{"bulleted_list", "bulleted_list", true},
		{"todo_list", "numbered_list", true},
		{"paragraph", "toggle_list", true},
		{"heading", "toggle_list", true},
		{"paragraph", "bulleted_list", false},
		{"image", "toggle_list", false},
		{"bulleted_list", "paragraph", false},
	} {
		if got := Nests(tc.typ, tc.under); got != tc.want {
			t.Errorf("Nests(%s, %s) = %v", tc.typ, tc.under, got)
		}
	}
}

// A closed toggle hides what is in it; a folded heading its section, up
// to a heading of its level or higher, or a block out of what it is in.
func TestHidden(t *testing.T) {
	blocks := []Fold{
		{Type: "toggle_list", Depth: 0, Collapsed: true},
		{Type: "paragraph", Depth: 1},
		{Type: "heading", Depth: 2, Level: 1},
		{Type: "heading", Depth: 0, Level: 2, Collapsed: true},
		{Type: "paragraph", Depth: 0},
		{Type: "heading", Depth: 0, Level: 3},
		{Type: "bulleted_list", Depth: 0},
		{Type: "heading", Depth: 1, Level: 1},
		{Type: "heading", Depth: 0, Level: 2},
		{Type: "paragraph", Depth: 0},
	}
	want := []bool{false, true, true, false, true, true, true, true, false, false}
	if got := Hidden(blocks); !reflect.DeepEqual(got, want) {
		t.Errorf("Hidden = %v, want %v", got, want)
	}
	// A folded heading in a list item ends with it.
	nested := []Fold{
		{Type: "bulleted_list", Depth: 0},
		{Type: "heading", Depth: 1, Collapsed: true},
		{Type: "paragraph", Depth: 1},
		{Type: "paragraph", Depth: 0},
	}
	if got := Hidden(nested); !reflect.DeepEqual(got, []bool{false, false, true, false}) {
		t.Errorf("nested: %v", got)
	}
}

// Text mostly in Chinese, Japanese or Korean is set higher: their scripts
// count, halfwidth katakana and iteration marks too, a few English terms
// among them leaving it so.
func TestMostlyCJK(t *testing.T) {
	for text, want := range map[string]bool{
		"":                    false,
		"Plain English prose": false,
		"An English paragraph that quotes 模型 once":            false,
		"模型可以通过两种方式访问到这些 MCP 服务":                              true,
		"更完整的介绍可以参考官方文档 https://modelcontextprotocol.io/docs": true,
		"ひらがなとカタカナ":                                           true,
		"한국어 문장":                                              true,
		"ﾊﾝｶｸｶﾀｶﾅ":                                            true,
		"人々":                                                  true,
	} {
		if got := MostlyCJK(text); got != want {
			t.Errorf("MostlyCJK(%q) = %v, want %v", text, got, want)
		}
	}
}
