package doc

import "testing"

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
	ok := map[string]any{"type": "bulleted_list", "text": "x", "children": []any{map[string]any{"type": "bulleted_list", "text": "y"}}}
	if _, err := SpecToNode(ok, "", "Agent"); err != nil {
		t.Error(err)
	}
}
