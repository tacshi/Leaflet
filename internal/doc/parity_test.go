package doc

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The server's agent function writes these fixtures, as its own side makes
// them (supabase/functions/_shared/tools_test.ts): this side must make
// the same of them.
const fixtures = "../../supabase/functions/_shared/testdata/"

func readFixture(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(fixtures + name) // #nosec G304 -- the repository's own fixtures.
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// asJSON is v as JSON reads it back, so values compare whatever their Go
// types.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A cloud run's ops apply here as on the server, and undo back to the
// page as it was: blocks it deleted side by side come back in their order.
func TestCloudRunOpsApplyAndUndo(t *testing.T) {
	var f struct {
		Before *Content `json:"before"`
		Ops    []Op     `json:"ops"`
		After  *Content `json:"after"`
	}
	readFixture(t, "run_ops.json", &f)
	c := f.Before.Copy()
	for _, o := range f.Ops {
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := asJSON(t, c.Children), asJSON(t, f.After.Children); !reflect.DeepEqual(got, want) {
		t.Errorf("applied\n%v\nwant\n%v", got, want)
	}
	for _, o := range InvertRun(f.Ops) {
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := asJSON(t, c.Children), asJSON(t, f.Before.Children); !reflect.DeepEqual(got, want) {
		t.Errorf("undone\n%v\nwant\n%v", got, want)
	}
}

// Agents read a page's blocks here as they do on the server.
func TestBlockViewsAsTheServerHasThem(t *testing.T) {
	var f struct {
		Content *Content `json:"content"`
		Views   any      `json:"views"`
	}
	readFixture(t, "views.json", &f)
	got := asJSON(t, BlockViews(f.Content))
	for i, want := range f.Views.([]any) {
		if i >= len(got.([]any)) || !reflect.DeepEqual(got.([]any)[i], want) {
			t.Errorf("block %d: %v\nwant %v", i, got.([]any)[i:min(i+1, len(got.([]any)))], want)
		}
	}
	if len(got.([]any)) != len(f.Views.([]any)) {
		t.Errorf("%d views, want %d", len(got.([]any)), len(f.Views.([]any)))
	}
}

// withoutIDs is a block as JSON has it, without the IDs each side draws
// at random.
func withoutIDs(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k != "id" {
				out[k] = withoutIDs(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = withoutIDs(e)
		}
		return out
	}
	return v
}

// Specs make here the blocks they make on the server, and are refused
// where they are refused there.
func TestSpecsAsTheServerTakesThem(t *testing.T) {
	var cases []struct {
		Spec  map[string]any `json:"spec"`
		Node  any            `json:"node"`
		Error bool           `json:"error"`
	}
	readFixture(t, "specs.json", &cases)
	for _, tc := range cases {
		n, err := SpecToNode(tc.Spec, "", "")
		switch {
		case tc.Error:
			if err == nil {
				t.Errorf("%v made %v; the server refuses it", tc.Spec, asJSON(t, n))
			}
		case err != nil:
			t.Errorf("%v: %v", tc.Spec, err)
		default:
			if got := withoutIDs(asJSON(t, n)); !reflect.DeepEqual(got, tc.Node) {
				t.Errorf("%v made\n%v\nwant\n%v", tc.Spec, got, tc.Node)
			}
		}
	}
}

// A call's blocks count together toward the most a call makes, as on the
// server, and a table's cells among them, however many a table has.
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

// Blocks a call writes as Markdown nest only as deep as specs do, as on
// the server.
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
