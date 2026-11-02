package doc

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Markdown reads and writes here as the server's agent function reads and
// writes it (supabase/functions/_shared/testdata/markdown.json): blocks
// from Markdown, as write_markdown writes them, inline Markdown both ways,
// and code languages.
func TestMarkdownAsTheServerHasIt(t *testing.T) {
	var f struct {
		Blocks []struct {
			Markdown string `json:"markdown"`
			Nodes    any    `json:"nodes"`
		} `json:"blocks"`
		Inline []struct {
			Markdown string `json:"markdown"`
			Delta    any    `json:"delta"`
		} `json:"inline"`
		Deltas []struct {
			Delta    json.RawMessage `json:"delta"`
			Markdown string          `json:"markdown"`
		} `json:"deltas"`
		Languages map[string]string `json:"languages"`
	}
	readFixture(t, "markdown.json", &f)
	if len(f.Blocks) == 0 || len(f.Inline) == 0 || len(f.Deltas) == 0 || len(f.Languages) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for _, tc := range f.Blocks {
		if got := withoutIDs(asJSON(t, MarkdownBlocks(tc.Markdown))); !reflect.DeepEqual(got, tc.Nodes) {
			t.Errorf("%q read as\n%v\nwant\n%v", tc.Markdown, got, tc.Nodes)
		}
	}
	for _, tc := range f.Inline {
		if got := asJSON(t, FromMarkdown(tc.Markdown).JSON()); !reflect.DeepEqual(got, tc.Delta) {
			t.Errorf("%q read as %v, want %v", tc.Markdown, got, tc.Delta)
		}
	}
	for _, tc := range f.Deltas {
		var n Node
		if err := json.Unmarshal([]byte(`{"id":"x","type":"paragraph","data":{"delta":`+string(tc.Delta)+`}}`), &n); err != nil {
			t.Fatal(err)
		}
		if got := n.Delta().ToMarkdown(); got != tc.Markdown {
			t.Errorf("%s wrote %q, want %q", tc.Delta, got, tc.Markdown)
		}
	}
	for name, want := range f.Languages {
		if got := CodeLanguageID(name); got != want {
			t.Errorf("language %q is %q, want %q", name, got, want)
		}
	}
}
