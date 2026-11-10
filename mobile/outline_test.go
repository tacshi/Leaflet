package mobile

import (
	"encoding/json"
	"testing"

	"leaflet/internal/doc"
)

func TestOutline(t *testing.T) {
	h := func(id, text string, level int) *doc.Node {
		n := &doc.Node{ID: id, Type: "heading"}
		n.SetDelta(doc.FromMarkdown(text))
		n.Set("level", level)
		return n
	}
	in, _ := json.Marshal([]*doc.Node{h("a", "Trip **plan**", 1), h("b", "Kyoto", 2), h("c", "", 3), h("d", "Costs", 1)})
	raw, err := Outline(string(in))
	if err != nil {
		t.Fatal(err)
	}
	var rows []outlineRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	want := []outlineRow{
		{ID: "a", Text: "Trip plan", Level: 1, HasChildren: true},
		{ID: "b", Text: "Kyoto", Level: 2, Depth: 1, Parent: "a", HasChildren: true},
		{ID: "c", Level: 3, Depth: 2, Parent: "b"},
		{ID: "d", Text: "Costs", Level: 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, rows[i], want[i])
		}
	}
	if _, err := Outline("not json"); err == nil {
		t.Error("an outline of what isn't blocks")
	}
}
