package mobile

import (
	"encoding/json"
	"testing"
)

func TestMarkdownBlocks(t *testing.T) {
	raw, err := MarkdownBlocks("## Plan\n\n- one\n- two\n\n```python\n# not a heading\nprint(1)\n```\n\n```go\nfunc f() {")
	if err != nil {
		t.Fatal(err)
	}
	var blocks []struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, b := range blocks {
		types = append(types, b.Type)
	}
	want := []string{"heading", "bulleted_list", "bulleted_list", "code", "code"}
	if len(types) != len(want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("types = %v, want %v", types, want)
		}
	}
	if lang := blocks[3].Data["language"]; lang != "python" {
		t.Errorf("language = %v", lang)
	}
	if empty, err := MarkdownBlocks(""); err != nil || empty != "[]" {
		t.Errorf("empty = %q, %v", empty, err)
	}
}

func TestInlineMathPicture(t *testing.T) {
	raw := InlineMath(`x^2+\frac{1}{2}`, 17, 3, 0, 0, 0, 255)
	var p mathPicture
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p.Width <= 0 || p.Ascent <= 0 || p.Descent <= 0 || len(p.PNG) < 100 {
		t.Fatalf("a formula's picture: %v, %+v", err, p.Width)
	}
	if InlineMath(`\frac{1`, 17, 3, 0, 0, 0, 255) != "" {
		t.Error("half a formula has a picture")
	}
}
