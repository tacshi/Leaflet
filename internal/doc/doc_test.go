package doc

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func para(id, text string) *Node {
	n := &Node{ID: id, Type: "paragraph"}
	n.SetDelta(Delta{{Text: text}})
	return n
}

func page(times map[string]int64, blocks ...*Node) *Content {
	return &Content{Children: blocks, Blocks: times, Deleted: map[string]int64{}}
}

func texts(c *Content) []string {
	var out []string
	c.Walk(func(n, _ *Node) { out = append(out, n.Delta().Plain()) })
	return out
}

func TestInlineMarkdownRoundTrip(t *testing.T) {
	md := "Ship **v1** with *offline* `drift` and [docs](https://x.dev)"
	d := FromMarkdown(md)
	if got := d.ToMarkdown(); got != md {
		t.Errorf("round trip %q", got)
	}
	if d[1].Text != "v1" || !d[1].Attrs.Bold {
		t.Errorf("second run %+v", d[1])
	}
}

func TestInlineMarkdownSnakeCase(t *testing.T) {
	d := FromMarkdown("the page_content table")
	if len(d) != 1 || d[0].Text != "the page_content table" || !d[0].Attrs.Zero() {
		t.Errorf("got %+v", d)
	}
}

func TestInlineMarkdownBoldInLink(t *testing.T) {
	d := FromMarkdown("[**go**](https://a.b)")
	if len(d) != 1 || d[0].Attrs.Href != "https://a.b" || !d[0].Attrs.Bold {
		t.Errorf("got %+v", d)
	}
}

func TestDeltaJSONShape(t *testing.T) {
	n := para("a", "")
	n.SetDelta(Delta{{Text: "Ship ", Attrs: Attrs{Bold: true}}, {Text: "v1"}})
	raw, _ := json.Marshal(n)
	want := `{"id":"a","type":"paragraph","data":{"delta":[{"attributes":{"bold":true},"insert":"Ship "},{"insert":"v1"}]}}`
	if string(raw) != want {
		t.Errorf("got %s", raw)
	}
}

func TestTableInCellOrder(t *testing.T) {
	n, err := SpecToNode(map[string]any{
		"type": "table",
		"rows": []any{[]any{"A", "B"}, []any{"1", "2"}},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if n.Int("colsLen", 0) != 2 || n.Int("rowsLen", 0) != 2 {
		t.Errorf("data %v", n.Data)
	}
	if got := TableRows(n); !reflect.DeepEqual(got, [][]string{{"A", "B"}, {"1", "2"}}) {
		t.Errorf("rows %v", got)
	}
	// Column-major: the second cell is row 1 of column 0.
	if c := n.Children[1]; c.Int("colPosition", -1) != 0 || c.Int("rowPosition", -1) != 1 {
		t.Errorf("second cell %v", c.Data)
	}
}

func TestChartSeriesMustMatchLabels(t *testing.T) {
	_, err := SpecToNode(map[string]any{
		"type": "chart",
		"chart": map[string]any{
			"labels": []any{"a", "b"},
			"series": []any{map[string]any{"name": "s", "values": []any{1.0}}},
		},
	}, "", "")
	var te *ToolError
	if !errors.As(err, &te) {
		t.Errorf("err %v", err)
	}
}

func TestChildrenOnlyOnLists(t *testing.T) {
	_, err := SpecToNode(map[string]any{
		"type": "paragraph", "text": "x",
		"children": []any{map[string]any{"type": "paragraph", "text": "y"}},
	}, "", "")
	var te *ToolError
	if !errors.As(err, &te) {
		t.Errorf("err %v", err)
	}
}

func TestStamping(t *testing.T) {
	before := page(map[string]int64{"a": 1, "b": 1}, para("a", "one"), para("b", "two"))
	after := page(map[string]int64{}, para("a", "one!"))
	s := after.Stamped(before, 50)
	if !reflect.DeepEqual(s.Blocks, map[string]int64{"a": 50}) {
		t.Errorf("blocks %v", s.Blocks)
	}
	if !reflect.DeepEqual(s.Deleted, map[string]int64{"b": 50}) {
		t.Errorf("deleted %v", s.Deleted)
	}
	same := page(map[string]int64{}, para("a", "one"), para("b", "two")).Stamped(before, 60)
	if same.Blocks["a"] != 1 || same.Blocks["b"] != 1 {
		t.Errorf("unchanged blocks restamped: %v", same.Blocks)
	}
}

func TestOpsInvert(t *testing.T) {
	c := page(map[string]int64{}, para("a", "one"), para("b", "two"), para("c", "three"))
	original := c.Copy().Encode()
	var ops []Op
	apply := func(o Op) {
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
		ops = append(ops, o)
	}
	apply(InsertOp(Position{After: "a"}, para("n", "new")))
	apply(c.ReplaceOp("b", para("b", "TWO")))
	apply(c.MoveOp("c", Position{At: "start"}))
	apply(c.DeleteOp([]string{"a", "n"}))
	if got := texts(c); !reflect.DeepEqual(got, []string{"three", "TWO"}) {
		t.Fatalf("after ops %v", got)
	}
	// Through JSON, as ops are stored and streamed.
	raw, _ := json.Marshal(ops)
	var decoded []Op
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, o := range InvertRun(decoded) {
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.Encode(); got != original {
		t.Errorf("undo gave\n%s\nwant\n%s", got, original)
	}
}

func TestMergeDifferentBlocks(t *testing.T) {
	base := page(map[string]int64{"a": 1, "b": 1}, para("a", "one"), para("b", "two"))
	local := page(map[string]int64{"a": 5, "b": 1}, para("a", "ONE"), para("b", "two"))
	remote := page(map[string]int64{"a": 1, "b": 6}, para("a", "one"), para("b", "TWO"))
	got, conflicts := Merge(base, local, remote)
	if !reflect.DeepEqual(texts(got), []string{"ONE", "TWO"}) || len(conflicts) != 0 {
		t.Errorf("texts %v conflicts %v", texts(got), conflicts)
	}
}

func TestMergeSameBlockNewerWins(t *testing.T) {
	base := page(map[string]int64{"a": 1}, para("a", "one"))
	local := page(map[string]int64{"a": 5}, para("a", "mine"))
	remote := page(map[string]int64{"a": 9}, para("a", "theirs"))
	got, conflicts := Merge(base, local, remote)
	if !reflect.DeepEqual(texts(got), []string{"theirs"}) {
		t.Errorf("texts %v", texts(got))
	}
	if len(conflicts) != 1 || conflicts[0].Kept() != "remote" || conflicts[0].BlockID != "a" {
		t.Errorf("conflicts %+v", conflicts)
	}
}

func TestMergeInsertsInPlace(t *testing.T) {
	base := page(map[string]int64{"a": 1, "c": 1}, para("a", "one"), para("c", "three"))
	local := page(map[string]int64{"a": 1, "b": 5, "c": 1}, para("a", "one"), para("b", "two"), para("c", "three"))
	remote := page(map[string]int64{"a": 1, "c": 1, "d": 7}, para("a", "one"), para("c", "three"), para("d", "four"))
	got, _ := Merge(base, local, remote)
	if !reflect.DeepEqual(texts(got), []string{"one", "two", "three", "four"}) {
		t.Errorf("texts %v", texts(got))
	}
}

func TestMergeDeletesAndEdits(t *testing.T) {
	base := page(map[string]int64{"a": 1, "b": 1}, para("a", "one"), para("b", "two"))
	local := page(map[string]int64{"b": 8}, para("b", "two!"))
	local.Deleted["a"] = 4
	remote := page(map[string]int64{"a": 3, "b": 1}, para("a", "one?"), para("b", "two"))
	got, _ := Merge(base, local, remote)
	if !reflect.DeepEqual(texts(got), []string{"two!"}) {
		t.Errorf("delete should win: %v", texts(got))
	}
	remote2 := page(map[string]int64{"a": 9, "b": 1}, para("a", "one?"), para("b", "two"))
	got2, _ := Merge(base, local, remote2)
	if !reflect.DeepEqual(texts(got2), []string{"one?", "two!"}) {
		t.Errorf("edit should win: %v", texts(got2))
	}
}

func TestContentJSONRoundTrip(t *testing.T) {
	src := `{"document":{"type":"page","id":"root","children":[{"id":"a1b2c3","type":"heading","data":{"level":2,"delta":[{"insert":"Goals"}]}}]},"meta":{"v":1,"blocks":{"a1b2c3":1761400000000},"deleted":{"zz9999":1761400009000}}}`
	c, err := Decode(src)
	if err != nil {
		t.Fatal(err)
	}
	if c.Children[0].Int("level", 0) != 2 || c.Children[0].Delta().Plain() != "Goals" {
		t.Errorf("decoded %+v", c.Children[0])
	}
	if c.Blocks["a1b2c3"] != 1761400000000 || c.Deleted["zz9999"] != 1761400009000 {
		t.Errorf("meta %v %v", c.Blocks, c.Deleted)
	}
	again := MustDecode(c.Encode())
	if !reflect.DeepEqual(Signatures(c), Signatures(again)) {
		t.Error("signatures changed through a round trip")
	}
}

func TestDeltaEdits(t *testing.T) {
	d := Delta{{Text: "hello world"}}
	d = d.Format(0, 5, func(a *Attrs) { a.Bold = true })
	if len(d) != 2 || !d[0].Attrs.Bold || d[0].Text != "hello" {
		t.Fatalf("format %+v", d)
	}
	d = d.Insert(5, "!", d.AttrsAt(5))
	if d[0].Text != "hello!" {
		t.Errorf("typing continues bold: %+v", d)
	}
	d = d.Delete(0, 6)
	if d.Plain() != " world" || len(d) != 1 {
		t.Errorf("delete %+v", d)
	}
}
