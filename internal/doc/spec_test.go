package doc

import (
	"encoding/json"
	"math"
	"testing"
)

// A chart's value of NaN or infinity, as an agent can write "NaN" or the
// chart editor read "inf", is no value: JSON has none, and the page
// holding it could not be saved.
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

// Nested items as deep or as many as an agent likes are refused, not
// recursed into without end.
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
	// A table's cells count, so a table can't be as big as an agent likes.
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
