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

// A block only one side changed since base takes that side's version,
// whatever the times say: an edit in the same millisecond as base (an
// agent writing right after a save) or from a device whose clock is behind
// is not lost.
func TestMergeOneSidedEditWinsWhateverTheTimes(t *testing.T) {
	base := page(map[string]int64{"a": 5, "b": 5}, para("a", "one"), para("b", "two"))
	for _, remoteAt := range []int64{5, 2} {
		local := page(map[string]int64{"a": 9, "b": 5}, para("a", "ONE"), para("b", "two"))
		remote := page(map[string]int64{"a": 5, "b": remoteAt}, para("a", "one"), para("b", "TWO"))
		got, conflicts := Merge(base, local, remote)
		if !reflect.DeepEqual(texts(got), []string{"ONE", "TWO"}) || len(conflicts) != 0 {
			t.Errorf("remote at %d: texts %v conflicts %v", remoteAt, texts(got), conflicts)
		}
		// The same with the sides the other way round.
		got, conflicts = Merge(base, remote, local)
		if !reflect.DeepEqual(texts(got), []string{"ONE", "TWO"}) || len(conflicts) != 0 {
			t.Errorf("local at %d: texts %v conflicts %v", remoteAt, texts(got), conflicts)
		}
	}
	// A move one side made, the other side's time newer.
	local := page(map[string]int64{"a": 9, "b": 5}, para("a", "ONE"), para("b", "two"))
	remote := page(map[string]int64{"a": 5, "b": 3}, para("b", "two"), para("a", "one"))
	got, _ := Merge(base, local, remote)
	if !reflect.DeepEqual(texts(got), []string{"two", "ONE"}) {
		t.Errorf("move: texts %v", texts(got))
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

// A block one side deleted stays deleted if the other side left it as it
// was in base. If the other side edited it, it comes back, whichever came
// later, as a conflict whose deleted side is nil.
func TestMergeDeletesAndEdits(t *testing.T) {
	base := page(map[string]int64{"a": 1, "b": 1}, para("a", "one"), para("b", "two"))
	local := page(map[string]int64{"b": 8}, para("b", "two!"))
	local.Deleted["a"] = 4
	untouched := page(map[string]int64{"a": 1, "b": 1}, para("a", "one"), para("b", "two"))
	got, conflicts := Merge(base, local, untouched)
	if !reflect.DeepEqual(texts(got), []string{"two!"}) || len(conflicts) != 0 {
		t.Errorf("delete should win: %v %+v", texts(got), conflicts)
	}
	if got.Deleted["a"] == 0 {
		t.Error("no tombstone for a")
	}
	for _, editedAt := range []int64{3, 9} {
		remote := page(map[string]int64{"a": editedAt, "b": 1}, para("a", "one?"), para("b", "two"))
		for _, sides := range [][2]*Content{{local, remote}, {remote, local}} {
			got, conflicts := Merge(base, sides[0], sides[1])
			if !reflect.DeepEqual(texts(got), []string{"one?", "two!"}) {
				t.Errorf("edit at %d should come back: %v", editedAt, texts(got))
			}
			if len(conflicts) != 1 || conflicts[0].BlockID != "a" {
				t.Fatalf("edit at %d: conflicts %+v", editedAt, conflicts)
			}
			c := conflicts[0]
			deleted, kept := c.Local, c.Remote
			if sides[0] == remote {
				deleted, kept = c.Remote, c.Local
			}
			if deleted != nil || kept == nil || kept.Delta().Plain() != "one?" || c.Kept() == "" {
				t.Errorf("edit at %d: conflict %+v", editedAt, c)
			}
			if (c.Local == nil) != (c.Kept() == "remote") {
				t.Errorf("edit at %d: kept %s", editedAt, c.Kept())
			}
		}
	}
}

// A list one side deleted comes back whole when the other side edited an
// item in it, and stays deleted when the other side only added a block
// beside it.
func TestMergeDeletedParentWithEditedChild(t *testing.T) {
	item := func(id, text string, kids ...*Node) *Node {
		n := para(id, text)
		n.Type, n.Children = "bulleted_list", kids
		return n
	}
	base := page(map[string]int64{"l": 1, "x": 1, "y": 1, "p": 1}, item("l", "list", item("x", "x"), item("y", "y")), para("p", "end"))
	local := page(map[string]int64{"p": 1}, para("p", "end"))
	local.Deleted["l"], local.Deleted["x"], local.Deleted["y"] = 5, 5, 5
	remote := page(map[string]int64{"l": 1, "x": 2, "y": 1, "p": 1}, item("l", "list", item("x", "x!"), item("y", "y")), para("p", "end"))
	got, conflicts := Merge(base, local, remote)
	if got.Find("l") == nil || got.Find("y") == nil || got.Find("x").Delta().Plain() != "x!" {
		t.Errorf("the list did not come back whole: %v", texts(got))
	}
	if len(conflicts) != 1 || conflicts[0].BlockID != "l" || conflicts[0].Local != nil {
		t.Errorf("conflicts %+v", conflicts)
	}
	inserted := page(map[string]int64{"l": 1, "x": 1, "y": 1, "n": 6, "p": 1}, item("l", "list", item("x", "x"), item("y", "y")), para("n", "new"), para("p", "end"))
	got, conflicts = Merge(base, local, inserted)
	if !reflect.DeepEqual(texts(got), []string{"new", "end"}) || len(conflicts) != 0 {
		t.Errorf("texts %v conflicts %+v", texts(got), conflicts)
	}
	// A block added above it does not move it; moving it below "end" does.
	above := page(map[string]int64{"n": 6, "l": 1, "x": 1, "y": 1, "p": 1}, para("n", "new"), item("l", "list", item("x", "x"), item("y", "y")), para("p", "end"))
	got, conflicts = Merge(base, local, above)
	if !reflect.DeepEqual(texts(got), []string{"new", "end"}) || len(conflicts) != 0 {
		t.Errorf("added above: texts %v conflicts %+v", texts(got), conflicts)
	}
	moved := page(map[string]int64{"p": 1, "l": 6, "x": 1, "y": 1}, para("p", "end"), item("l", "list", item("x", "x"), item("y", "y")))
	got, conflicts = Merge(base, local, moved)
	if !reflect.DeepEqual(texts(got), []string{"end", "list", "x", "y"}) || len(conflicts) != 1 {
		t.Errorf("moved: texts %v conflicts %+v", texts(got), conflicts)
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

func TestRulesAreDividers(t *testing.T) {
	for _, s := range []string{"---", "***", "___", "- - -", " ----- "} {
		if !IsRule(s) {
			t.Errorf("%q is a rule", s)
		}
	}
	for _, s := range []string{"--", "-*-", "**bold**", "--- x", ""} {
		if IsRule(s) {
			t.Errorf("%q is not a rule", s)
		}
	}
	n, err := SpecToNode(map[string]any{"type": "paragraph", "text": "---"}, "", "Agent")
	if err != nil || n.Type != "divider" {
		t.Errorf("paragraph of --- became %v, %v", n, err)
	}
}

func TestMarkdownNestsByMarkerWidth(t *testing.T) {
	item := func(typ, text string, kids ...*Node) *Node {
		n := &Node{ID: NewBlockID(), Type: typ, Children: kids}
		n.SetDelta(FromMarkdown(text))
		return n
	}
	c := FromBlocks([]*Node{
		item("numbered_list", "Clone the repo", item("bulleted_list", "Use SSH")),
		item("numbered_list", "Run the tests"),
		item("bulleted_list", "Notes", item("bulleted_list", "One")),
	})
	got := PageMarkdown("", c, nil)
	want := "1. Clone the repo\n   - Use SSH\n2. Run the tests\n- Notes\n  - One\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestColorsRoundTrip(t *testing.T) {
	d := Delta{{Text: "red", Attrs: Attrs{Color: "#d1242fff"}}, {Text: " marked", Attrs: Attrs{Background: "#fff8c5ff"}}}
	back := DeltaFromJSON(d.JSON())
	if len(back) != 2 || back[0].Attrs.Color != "#d1242fff" || back[1].Attrs.Background != "#fff8c5ff" {
		t.Errorf("back %+v", back)
	}
	bad := DeltaFromJSON([]any{map[string]any{"insert": "x", "attributes": map[string]any{"color": "red; x"}}})
	if bad[0].Attrs.Color != "" {
		t.Errorf("kept a color that is not one: %q", bad[0].Attrs.Color)
	}
	// Markdown has no colors, as in MyGo's rich text.
	if md := d.ToMarkdown(); md != "red marked" {
		t.Errorf("markdown %q", md)
	}
}

// One side wraps blocks in a new toggle while the other edits elsewhere:
// the blocks move into the toggle, once each, in order.
func TestMergeMoveIntoANewBlock(t *testing.T) {
	base := page(map[string]int64{"a": 1, "x": 1, "y": 1}, para("a", "one"), para("x", "two"), para("y", "three"))
	box := para("box", "box")
	box.Type = "toggle"
	box.Children = []*Node{para("x", "two"), para("y", "three")}
	wrapped := page(map[string]int64{"a": 1, "x": 2, "y": 2, "box": 2}, para("a", "one"), box)
	edited := page(map[string]int64{"a": 3, "x": 1, "y": 1}, para("a", "ONE"), para("x", "two"), para("y", "three"))
	for _, got := range []*Content{first(Merge(base, edited, wrapped)), first(Merge(base, wrapped, edited))} {
		if !reflect.DeepEqual(texts(got), []string{"ONE", "box", "two", "three"}) {
			t.Errorf("texts %v", texts(got))
		}
		if b := got.Find("box"); b == nil || len(b.Children) != 2 {
			t.Errorf("box %+v", b)
		}
	}
}

func first(c *Content, _ []Conflict) *Content { return c }

// A block's time never goes back, so an edit on a device whose clock is
// behind the one it merged from still wins.
func TestStampingNeverGoesBack(t *testing.T) {
	before := page(map[string]int64{"a": 100, "b": 100}, para("a", "one"), para("b", "two"))
	after := page(map[string]int64{}, para("a", "one!"))
	s := after.Stamped(before, 40)
	if s.Blocks["a"] != 101 || s.Deleted["b"] != 101 {
		t.Errorf("blocks %v deleted %v", s.Blocks, s.Deleted)
	}
	back := page(map[string]int64{}, para("a", "one!"), para("b", "back")).Stamped(s, 40)
	if back.Blocks["b"] != 102 {
		t.Errorf("a block back over its tombstone got %d", back.Blocks["b"])
	}
}

// Two devices merging the same two versions, each as its local one, keep
// the same block when the times are equal.
func TestMergeTiesAgree(t *testing.T) {
	base := page(map[string]int64{"a": 1}, para("a", "one"))
	mine := page(map[string]int64{"a": 7}, para("a", "mine"))
	theirs := page(map[string]int64{"a": 7}, para("a", "theirs"))
	here, hc := Merge(base, mine, theirs)
	there, tc := Merge(base, theirs, mine)
	if !reflect.DeepEqual(texts(here), texts(there)) {
		t.Fatalf("here %v there %v", texts(here), texts(there))
	}
	if len(hc) != 1 || len(tc) != 1 || hc[0].Kept() == tc[0].Kept() {
		t.Errorf("kept here %v there %v", hc, tc)
	}
	// A tie on a move too.
	base = page(map[string]int64{"a": 1, "b": 1, "c": 1}, para("a", "a"), para("b", "b"), para("c", "c"))
	mine = page(map[string]int64{"a": 1, "b": 1, "c": 5}, para("c", "c"), para("a", "a"), para("b", "b"))
	theirs = page(map[string]int64{"a": 1, "b": 1, "c": 5}, para("a", "a"), para("c", "c"), para("b", "b"))
	here, _ = Merge(base, mine, theirs)
	there, _ = Merge(base, theirs, mine)
	if !reflect.DeepEqual(texts(here), texts(there)) {
		t.Errorf("move: here %v there %v", texts(here), texts(there))
	}
}
