package doc

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
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

// A mention is one unit: text typed inside it goes after it, and taking
// out or formatting part of it takes all of it.
func TestMentionIsOneUnit(t *testing.T) {
	alice := Attrs{Mention: &Mention{Kind: "user", ID: "a", Name: "Alice"}}
	d := Delta{{Text: "hi "}, {Text: "@Alice", Attrs: alice}}
	if got := d.Insert(6, "x", Attrs{}); got.Plain() != "hi @Alicex" || len(got) != 3 || got[1].Text != "@Alice" {
		t.Errorf("typed inside: %+v", got)
	}
	if got := d.Delete(7, 8); got.Plain() != "hi " {
		t.Errorf("deleted part: %+v", got)
	}
	if got := d.Format(5, 7, func(a *Attrs) { a.Bold = true }); len(got) != 2 || !got[1].Attrs.Bold || got[1].Text != "@Alice" {
		t.Errorf("formatted part: %+v", got)
	}
	if got := d.Insert(6, "x", Attrs{}).ToMarkdown(); got != "hi [@Alice](mention:user:a)x" {
		t.Errorf("markdown %q", got)
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
	box.Type = "toggle_list"
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

// One side moves a block into a toggle while the other inserts a block
// after it at the top level: the new block stays at the top level, where
// it was put, whichever side is primary.
func TestMergeInsertAfterABlockMovedElsewhere(t *testing.T) {
	toggle := func(kids ...*Node) *Node {
		n := para("t", "t")
		n.Type = "toggle_list"
		n.Children = kids
		return n
	}
	base := page(map[string]int64{"a": 1, "b": 1, "t": 1}, para("a", "a"), para("b", "b"), toggle())
	for _, moverAt := range []int64{5, 9} {
		mover := page(map[string]int64{"a": moverAt, "b": 1, "t": 1}, para("b", "b"), toggle(para("a", "a")))
		inserter := page(map[string]int64{"a": 1, "b": 1, "c": 7, "t": 1}, para("a", "a"), para("c", "c"), para("b", "b"), toggle())
		for _, got := range []*Content{first(Merge(base, mover, inserter)), first(Merge(base, inserter, mover))} {
			if loc := got.Locate("c"); loc == nil || loc.Parent != nil {
				t.Errorf("mover at %d: c is not at the top level: %s", moverAt, got.Encode())
			}
			if loc := got.Locate("a"); loc == nil || loc.Parent == nil || loc.Parent.ID != "t" {
				t.Errorf("mover at %d: a is not in t: %s", moverAt, got.Encode())
			}
			if !reflect.DeepEqual(texts(got), []string{"c", "b", "t", "a"}) {
				t.Errorf("mover at %d: texts %v", moverAt, texts(got))
			}
		}
	}
}

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

func TestUndoDeleteOfSeveralBlocks(t *testing.T) {
	list := func() *Node {
		n := &Node{ID: "p", Type: "bulleted_list", Children: []*Node{para("a", "a"), para("b", "b"), para("c", "c")}}
		n.SetDelta(Delta{{Text: "p"}})
		return n
	}
	for _, tc := range []struct {
		name   string
		blocks func() []*Node
		ids    []string
	}{
		{"first two", func() []*Node { return []*Node{para("a", "a"), para("b", "b"), para("c", "c")} }, []string{"a", "b"}},
		{"reversed ids", func() []*Node { return []*Node{para("x", "x"), para("a", "a"), para("b", "b"), para("c", "c")} }, []string{"b", "a"}},
		{"all of them", func() []*Node { return []*Node{para("a", "a"), para("b", "b")} }, []string{"b", "a"}},
		{"first children", func() []*Node { return []*Node{list()} }, []string{"a", "b"}},
		{"only children", func() []*Node { return []*Node{list()} }, []string{"c", "a", "b"}},
		{"either side of a survivor", func() []*Node { return []*Node{para("a", "a"), para("b", "b"), para("c", "c")} }, []string{"c", "a"}},
		{"parent and its child", func() []*Node { return []*Node{list(), para("z", "z")} }, []string{"b", "p"}},
	} {
		c := FromBlocks(tc.blocks())
		original := c.Copy().Encode()
		o := c.DeleteOp(tc.ids)
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
		for _, inv := range o.Invert() {
			if err := c.Apply(inv); err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
		}
		if got := c.Encode(); got != original {
			t.Errorf("%s: undo gave\n%s\nwant\n%s", tc.name, got, original)
		}
	}
}

func TestFailedMoveKeepsTheBlock(t *testing.T) {
	list := &Node{ID: "p", Type: "bulleted_list", Children: []*Node{para("k", "k")}}
	list.SetDelta(Delta{{Text: "p"}})
	for _, p := range []Position{{After: "gone"}, {Before: "gone"}, {Parent: "gone"}, {After: "k"}, {Parent: "p"}} {
		c := FromBlocks([]*Node{para("a", "a"), list})
		original := c.Copy().Encode()
		if err := c.Move("p", p); err == nil {
			t.Errorf("move to %+v: no error", p)
		}
		if got := c.Encode(); got != original {
			t.Errorf("move to %+v changed the page:\n%s", p, got)
		}
	}
}

// A block moves into a list, but not into a block of another kind, nor
// into or out of a table.
func TestMoveOnlyIntoWhatHoldsBlocks(t *testing.T) {
	table, _ := TableNode([][]string{{"x", "y"}}, "t")
	cell := table.Children[0]
	for _, tc := range []struct {
		id string
		p  Position
	}{
		{"b", Position{Parent: "a"}},
		{"b", Position{Parent: "t"}},
		{"b", Position{After: cell.ID}},
		{"b", Position{Before: cell.Children[0].ID}},
		{cell.ID, Position{After: "a"}},
		{cell.Children[0].ID, Position{At: "end"}},
	} {
		c := FromBlocks([]*Node{para("a", "a"), para("b", "b"), table.Clone()})
		original := c.Copy().Encode()
		if err := c.Move(tc.id, tc.p); err == nil {
			t.Errorf("move of %s to %+v: no error", tc.id, tc.p)
		}
		if got := c.Encode(); got != original {
			t.Errorf("move of %s to %+v changed the page:\n%s", tc.id, tc.p, got)
		}
	}
	// Cells still move within their table, as a merge moves them.
	c := FromBlocks([]*Node{table.Clone()})
	if err := c.Move(cell.ID, Position{After: table.Children[1].ID}); err != nil {
		t.Error(err)
	}
}

// Undo run puts a deleted block back even when the block it went after
// has gone since, and once only when it is back already.
func TestUndoRunInsertsWhatIsMissing(t *testing.T) {
	c := FromBlocks([]*Node{para("a", "a"), para("x", "x"), para("b", "b")})
	o := c.DeleteOp([]string{"x"})
	if err := c.Apply(o); err != nil {
		t.Fatal(err)
	}
	c.Remove("a")
	for _, inv := range InvertRun([]Op{o}) {
		if err := c.Apply(inv); err != nil {
			t.Error(err)
		}
	}
	if got := texts(c); !reflect.DeepEqual(got, []string{"b", "x"}) {
		t.Errorf("anchor gone: %v", got)
	}

	c = FromBlocks([]*Node{para("a", "a"), para("x", "x"), para("b", "b")})
	o = c.DeleteOp([]string{"x"})
	_ = c.Apply(o)
	_ = c.Insert(Position{After: "a"}, para("x", "x"))
	for _, inv := range InvertRun([]Op{o}) {
		_ = c.Apply(inv)
	}
	if got := texts(c); !reflect.DeepEqual(got, []string{"a", "x", "b"}) {
		t.Errorf("back already: %v", got)
	}

	// A list whose item came back on its own comes back without it.
	list := &Node{ID: "l", Type: "bulleted_list", Children: []*Node{para("k", "k")}}
	list.SetDelta(Delta{{Text: "l"}})
	c = FromBlocks([]*Node{para("a", "a"), list})
	o = c.DeleteOp([]string{"l"})
	_ = c.Apply(o)
	_ = c.Insert(Position{At: "end"}, para("k", "k"))
	for _, inv := range InvertRun([]Op{o}) {
		_ = c.Apply(inv)
	}
	if got := texts(c); !reflect.DeepEqual(got, []string{"a", "l", "k"}) || len(c.Find("l").Children) != 0 {
		t.Errorf("child back already: %v", got)
	}

	// An item whose list became a paragraph since comes back after it.
	list = &Node{ID: "l", Type: "bulleted_list", Children: []*Node{para("k", "k")}}
	list.SetDelta(Delta{{Text: "l"}})
	c = FromBlocks([]*Node{list, para("z", "z")})
	o = c.DeleteOp([]string{"k"})
	_ = c.Apply(o)
	c.Find("l").Type = "paragraph"
	for _, inv := range InvertRun([]Op{o}) {
		_ = c.Apply(inv)
	}
	if got := texts(c); !reflect.DeepEqual(got, []string{"l", "k", "z"}) || len(c.Find("l").Children) != 0 {
		t.Errorf("list gone: %v", got)
	}
}

// One side makes a list item, moves a child into it and deletes the
// child's old parent, while the other edits elsewhere: the child lands in
// the new item whichever side is primary and whichever is local.
func TestMergeMoveOutOfADeletedBlock(t *testing.T) {
	item := func(id, text string, kids ...*Node) *Node {
		n := &Node{ID: id, Type: "bulleted_list", Children: kids}
		n.SetDelta(Delta{{Text: text}})
		return n
	}
	base := page(map[string]int64{"p": 10, "c": 10, "z": 10}, item("p", "p", item("c", "c", para("g", "g"))), para("z", "z"))
	base.Blocks["g"] = 10
	for _, moverAt := range []int64{100, 300} {
		mover := page(map[string]int64{"n": moverAt, "c": moverAt, "g": 10, "z": 10}, item("n", "n", item("c", "c", para("g", "g"))), para("z", "z"))
		mover.Deleted["p"] = moverAt
		editor := page(map[string]int64{"p": 10, "c": 10, "g": 10, "z": 200}, item("p", "p", item("c", "c", para("g", "g"))), para("z", "Z"))
		for _, got := range []*Content{first(Merge(base, mover, editor)), first(Merge(base, editor, mover))} {
			if !reflect.DeepEqual(texts(got), []string{"n", "c", "g", "Z"}) {
				t.Errorf("mover at %d: texts %v", moverAt, texts(got))
			}
			if loc := got.Locate("c"); loc == nil || loc.Parent == nil || loc.Parent.ID != "n" {
				t.Errorf("mover at %d: c is not in n: %s", moverAt, got.Encode())
			}
			if got.Find("p") != nil {
				t.Errorf("mover at %d: p came back", moverAt)
			}
		}
	}
}

// Text that looks like markup is escaped, so it reads back as written.
// Spaces at the ends of a formatted run go outside its markers, and so
// read back unformatted.
func TestInlineMarkdownEscapes(t *testing.T) {
	for _, tc := range []struct{ d, want Delta }{
		{d: Delta{{Text: "2*3*4"}}},
		{d: Delta{{Text: "use `x` and ``y``"}}},
		{d: Delta{{Text: "a\\b \\* c\\"}}},
		{d: Delta{{Text: "~~not struck~~ [not a link](x)"}}},
		{d: Delta{{Text: "_x_ and snake_case_name and café_bar"}}},
		{d: Delta{{Text: "trailing_"}, {Text: "_leading", Attrs: Attrs{Bold: true}}}},
		{d: Delta{{Text: "x*", Attrs: Attrs{Italic: true}}}},
		{d: Delta{{Text: "**", Attrs: Attrs{Bold: true}}}},
		{d: Delta{{Text: "a`b", Attrs: Attrs{Code: true}}}},
		{d: Delta{{Text: "`x`", Attrs: Attrs{Code: true}}}},
		{d: Delta{{Text: " pad ", Attrs: Attrs{Code: true}}}},
		{d: Delta{{Text: "x", Attrs: Attrs{Href: "https://e.com/a_(b)"}}}},
		{d: Delta{{Text: "y", Attrs: Attrs{Href: "https://e.com/a)b"}}}},
		{d: Delta{{Text: "z", Attrs: Attrs{Href: `https://e.com/a b\c<d>`}}}},
		{d: Delta{{Text: "[x] ", Attrs: Attrs{Href: "https://e.com"}}, {Text: "after"}}},
		{d: Delta{{Text: "# not a heading"}}},
		{d: Delta{{Text: "a ", Attrs: Attrs{Italic: true}}, {Text: "b"}},
			want: Delta{{Text: "a", Attrs: Attrs{Italic: true}}, {Text: " b"}}},
		{d: Delta{{Text: " both ", Attrs: Attrs{Bold: true}}, {Text: "c"}},
			want: Delta{{Text: " "}, {Text: "both", Attrs: Attrs{Bold: true}}, {Text: " c"}}},
	} {
		want := tc.want
		if want == nil {
			want = tc.d.Compact()
		}
		md := tc.d.ToMarkdown()
		if got := FromMarkdown(md); !reflect.DeepEqual(got, want) {
			t.Errorf("%+v wrote %q, which reads back as %+v", tc.d, md, got)
		}
	}
}

// Links as agents write them: parentheses in the url, or in brackets.
func TestInlineMarkdownLinks(t *testing.T) {
	for md, href := range map[string]string{
		"[w](https://en.wikipedia.org/wiki/Go_(game))": "https://en.wikipedia.org/wiki/Go_(game)",
		"[w](<https://e.com/a b>)":                     "https://e.com/a b",
		`[w](https://e.com/a\)b)`:                      "https://e.com/a)b",
	} {
		if d := FromMarkdown(md); len(d) != 1 || d[0].Text != "w" || d[0].Attrs.Href != href {
			t.Errorf("%q read as %+v", md, d)
		}
	}
}

// Runs side by side in any formats Markdown can say read back as they
// were: bold and italic together, one run's markers touching the next's,
// code beside code, markup characters in the text.
func TestInlineMarkdownRoundTripsRuns(t *testing.T) {
	for _, d := range []Delta{
		{{Text: "x", Attrs: Attrs{Bold: true, Italic: true}}},
		{{Text: "ab", Attrs: Attrs{Bold: true, Italic: true}}, {Text: " c"}},
		{{Text: "foo", Attrs: Attrs{Italic: true}}, {Text: "bar", Attrs: Attrs{Bold: true}}},
		{{Text: "a"}, {Text: "b", Attrs: Attrs{Bold: true}}, {Text: "c", Attrs: Attrs{Bold: true, Italic: true}}},
		{{Text: "a", Attrs: Attrs{Code: true}}, {Text: "b", Attrs: Attrs{Code: true, Bold: true}}},
		{{Text: "a*b", Attrs: Attrs{Code: true, Italic: true}}, {Text: "c", Attrs: Attrs{Italic: true}}},
	} {
		md := d.ToMarkdown()
		if got := FromMarkdown(md); !reflect.DeepEqual(got, d.Compact()) {
			t.Errorf("%+v wrote %q, which reads back as %+v", d, md, got)
		}
	}
	words := []string{"a", "bc", "x_y", "*", "**", "_", "`", "[", "]", "(", ")", "\\", "~", "中文", "@"}
	hrefs := []string{"https://a.b", "https://c.d/x_(y)"}
	rng := rand.New(rand.NewPCG(1, 2)) // #nosec G404 -- seeded, for runs a test can repeat.
	for range 5000 {
		var d Delta
		for range 1 + rng.IntN(6) {
			a := Attrs{Bold: rng.IntN(2) == 0, Italic: rng.IntN(2) == 0, Strike: rng.IntN(4) == 0, Code: rng.IntN(4) == 0}
			if rng.IntN(4) == 0 {
				a.Href = hrefs[rng.IntN(len(hrefs))]
			}
			var text strings.Builder
			for i := range 1 + rng.IntN(3) {
				// Spaces only between words: at the ends of a format they
				// go outside its markers.
				if i > 0 {
					text.WriteString(" ")
				}
				text.WriteString(words[rng.IntN(len(words))])
			}
			d = append(d, Run{Text: text.String(), Attrs: a})
		}
		md := d.ToMarkdown()
		if got := FromMarkdown(md); !reflect.DeepEqual(got, d.Compact()) {
			t.Fatalf("%+v wrote %q, which reads back as %+v", d, md, got)
		}
	}
	if got := FromMarkdown("***both***"); len(got) != 1 || !got[0].Attrs.Bold || !got[0].Attrs.Italic {
		t.Errorf("***both*** reads as %+v", got)
	}
}

// A format that runs on into a link stays open around it, rather than
// closing after a space, where its marker would read as text.
func TestInlineMarkdownFormatIntoLink(t *testing.T) {
	for _, tc := range []struct {
		d    Delta
		want string
	}{
		{Delta{{Text: "Read the ", Attrs: Attrs{Italic: true}}, {Text: "docs", Attrs: Attrs{Italic: true, Href: "https://x.dev"}}},
			"*Read the [docs](https://x.dev)*"},
		{Delta{{Text: "docs", Attrs: Attrs{Italic: true, Href: "https://x.dev"}}, {Text: " here", Attrs: Attrs{Italic: true}}},
			"*[docs](https://x.dev) here*"},
		{Delta{{Text: "a ", Attrs: Attrs{Italic: true}}, {Text: "b", Attrs: Attrs{Italic: true, Bold: true}}},
			"*a __b__*"},
		// A format closed at a link's end and opened again past it keeps
		// its markers off the spaces there, which lose it.
		{Delta{{Text: "read ", Attrs: Attrs{Href: "https://x.dev"}}, {Text: "this ", Attrs: Attrs{Href: "https://x.dev", Italic: true}},
			{Text: "and that", Attrs: Attrs{Italic: true}}},
			"[read *this* ](https://x.dev)*and that*"},
		{Delta{{Text: "this", Attrs: Attrs{Href: "https://x.dev", Italic: true}}, {Text: " and that", Attrs: Attrs{Italic: true}}},
			"*[this](https://x.dev) and that*"},
		{Delta{{Text: "go ", Attrs: Attrs{Href: "https://x.dev"}}, {Text: "there", Attrs: Attrs{Href: "https://x.dev", Italic: true}},
			{Text: " now", Attrs: Attrs{Italic: true}}},
			"[go *there*](https://x.dev) *now*"},
	} {
		md := tc.d.ToMarkdown()
		if md != tc.want {
			t.Errorf("%+v wrote %q, want %q", tc.d, md, tc.want)
		}
		if got := FromMarkdown(md); !sameShown(got, tc.d.Compact()) {
			t.Errorf("%q reads back as %+v", md, got)
		}
	}
	// Runs with spaces at their ends, which go outside the markers of a
	// format ending there, links' text included.
	words := []string{"a", "bc", "*", "_", "[", "中文"}
	hrefs := []string{"https://a.b", "https://c.d"}
	rng := rand.New(rand.NewPCG(3, 4)) // #nosec G404 -- seeded, for runs a test can repeat.
	for range 5000 {
		var d Delta
		for range 1 + rng.IntN(5) {
			a := Attrs{Bold: rng.IntN(2) == 0, Italic: rng.IntN(2) == 0, Strike: rng.IntN(4) == 0}
			if rng.IntN(3) == 0 {
				a.Href = hrefs[rng.IntN(len(hrefs))]
			}
			text := words[rng.IntN(len(words))]
			if rng.IntN(2) == 0 {
				text += " "
			}
			if rng.IntN(3) == 0 {
				text = " " + text
			}
			d = append(d, Run{Text: text, Attrs: a})
		}
		md := d.ToMarkdown()
		if want, got := trimSpans(d.Compact()), FromMarkdown(md); !sameShown(got, want) {
			t.Fatalf("%+v wrote %q, which reads back as %+v", want, md, got)
		}
	}
}

// sameShown reports whether two texts read the same: the same characters
// in the same formats, but for bold, italic and strike on spaces, which
// Markdown can't always say.
func sameShown(a, b Delta) bool {
	type char struct {
		r rune
		a Attrs
	}
	chars := func(d Delta) []char {
		var out []char
		for _, r := range d {
			for _, c := range r.Text {
				a := r.Attrs
				if c == ' ' && !a.Code {
					a.Bold, a.Italic, a.Strike = false, false, false
				}
				out = append(out, char{c, a})
			}
		}
		return out
	}
	return reflect.DeepEqual(chars(a), chars(b))
}

func TestEscapeBlockStart(t *testing.T) {
	for in, want := range map[string]string{
		"# x":       `\# x`,
		"### x":     `\### x`,
		"#tag":      "#tag",
		"- x":       `\- x`,
		"-5 below":  "-5 below",
		"+ x":       `\+ x`,
		"> x":       `\> x`,
		"| a |":     `\| a |`,
		"1. x":      `1\. x`,
		"12) x":     `12\) x`,
		"3.14 is π": "3.14 is π",
		"$$":        `\$$`,
		"---":       `\---`,
		"  # x":     `  \# x`,
		"plain":     "plain",
	} {
		if got := EscapeBlockStart(in); got != want {
			t.Errorf("%q escaped as %q, want %q", in, got, want)
		}
	}
}

// A table replaced with a list keeps none of its cells, whoever made the
// op; a list's own items stay.
func TestReplaceTableWithListDropsCells(t *testing.T) {
	table, _ := TableNode([][]string{{"a", "b"}}, "t")
	c := FromBlocks([]*Node{table})
	list := &Node{ID: "t", Type: "bulleted_list", Children: table.Clone().Children}
	list.SetDelta(Delta{{Text: "now a list"}})
	list.Children = append(list.Children, para("k", "item"))
	if err := c.Apply(Op{Op: "replace", ID: "t", Node: list, PrevNode: table}); err != nil {
		t.Fatal(err)
	}
	n := c.Find("t")
	if n.Type != "bulleted_list" || len(n.Children) != 1 || n.Children[0].ID != "k" {
		t.Errorf("replaced with %s holding %d blocks", n.Type, len(n.Children))
	}
	if len(list.Children) != 3 {
		t.Error("the op's node changed")
	}
}

// A replace that kept the block's children, applied to the page as it is
// now, keeps the children it has now: one edited or added since the agent
// read the page stays. A replace with children of its own sets them.
func TestReplaceKeepingChildrenKeepsTheirEdits(t *testing.T) {
	read := &Node{ID: "t", Type: "toggle_list", Children: []*Node{para("a", "A")}}
	read.SetDelta(Delta{{Text: "T"}})
	c := FromBlocks([]*Node{read.Clone()})
	agent := read.Clone()
	agent.SetDelta(Delta{{Text: "T2"}})
	op := Op{Op: "replace", ID: "t", Node: agent, PrevNode: read}
	// Meanwhile, in the editor.
	tg := c.Find("t")
	tg.Children[0].SetDelta(Delta{{Text: "A2"}})
	tg.Children = append(tg.Children, para("b", "B"))
	if err := c.Apply(op); err != nil {
		t.Fatal(err)
	}
	if got := texts(c); !reflect.DeepEqual(got, []string{"T2", "A2", "B"}) {
		t.Errorf("kept children: %v", got)
	}
	fresh := read.Clone()
	fresh.Children = []*Node{para("n", "New")}
	if err := c.Apply(Op{Op: "replace", ID: "t", Node: fresh, PrevNode: c.Find("t").Clone()}); err != nil {
		t.Fatal(err)
	}
	if got := texts(c); !reflect.DeepEqual(got, []string{"T", "New"}) {
		t.Errorf("new children: %v", got)
	}
}

// Rebase takes back what edits changed, on the page as it is since: a
// block they changed comes back as it was, over a change from outside
// too; a block they made goes, one they deleted comes back, and the
// blocks from outside stay.
func TestRebase(t *testing.T) {
	old := page(map[string]int64{"a": 1, "b": 1, "d": 1}, para("a", "a"), para("b", "b"), para("d", "gone"))
	after := page(map[string]int64{"a": 1, "b": 1, "n": 1}, para("a", "a1"), para("b", "b"), para("n", "made"))
	current := page(map[string]int64{"a": 5, "b": 5, "n": 1, "q": 5},
		para("a", "a1 from outside"), para("b", "b from outside"), para("n", "made"), para("q", "from outside"))
	got := texts(Rebase(old, after, current))
	if want := []string{"a", "b from outside", "gone", "from outside"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rebased %v, want %v", got, want)
	}
}

// JSON that is not a page's content, or is of a newer version, is refused
// rather than read as an empty page.
func TestDecodeRefusesWhatIsNotContent(t *testing.T) {
	for _, s := range []string{"null", "{}", `{"meta":{"v":1}}`, `{"document":null}`, `{"document":{"children":[]},"meta":{"v":2}}`} {
		if _, err := Decode(s); !errors.Is(err, ErrNotContent) {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"", `{"document":{"children":[]}}`, Empty().Encode()} {
		if _, err := Decode(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

// A table cell one side deleted, by writing the table anew or taking out
// its row, stays deleted when the other side edited it: brought back, it
// would share its row and column with a cell of the table kept. The table
// as the editing side had it is a conflict, whichever side is newer.
func TestMergeTableCellDeletedAndEdited(t *testing.T) {
	table, _ := TableNode([][]string{{"a", "b"}, {"c", "d"}}, "T")
	base := FromBlocks([]*Node{table})
	for id := range base.Blocks {
		base.Blocks[id] = 1000
	}
	cellText := func(c *Content, row, col int) string {
		cell := TableCell(c.Find("T"), row, col)
		if cell == nil || len(cell.Children) == 0 {
			return ""
		}
		return cell.Children[0].Delta().Plain()
	}
	edit := func(at int64) *Content {
		c := base.Copy()
		TableCell(c.Find("T"), 1, 1).Children[0].SetDelta(Delta{{Text: "edited"}})
		return c.Stamped(base, at)
	}
	rewritten := base.Copy()
	fresh, _ := TableNode([][]string{{"x", "y"}, {"z", "w"}}, "T")
	_ = rewritten.Replace("T", fresh)
	rewritten = rewritten.Stamped(base, 2000)
	rowGone := base.Copy()
	rt := rowGone.Find("T")
	rt.Children = slices.DeleteFunc(rt.Children, func(k *Node) bool { return k.Int("rowPosition", 0) == 1 })
	rt.Set("rowsLen", 1)
	rowGone = rowGone.Stamped(base, 2000)
	for _, tc := range []struct {
		name    string
		deleter *Content
		want    [][]string
	}{
		{"rewritten", rewritten, [][]string{{"x", "y"}, {"z", "w"}}},
		{"row taken out", rowGone, [][]string{{"a", "b"}}},
	} {
		for _, editedAt := range []int64{1500, 2500} {
			editor := edit(editedAt)
			for _, sides := range [][2]*Content{{tc.deleter, editor}, {editor, tc.deleter}} {
				got, conflicts := Merge(base, sides[0], sides[1])
				tt := got.Find("T")
				if rows := TableRows(tt); len(tt.Children) != len(tc.want)*2 || !reflect.DeepEqual(rows, tc.want) {
					t.Errorf("%s, edited at %d: %d cells, rows %v", tc.name, editedAt, len(tt.Children), rows)
				}
				if len(conflicts) != 1 || conflicts[0].BlockID != "T" {
					t.Fatalf("%s, edited at %d: conflicts %+v", tc.name, editedAt, conflicts)
				}
				c := conflicts[0]
				lost, kept := c.Remote, "local"
				if sides[0] == editor {
					lost, kept = c.Local, "remote"
				}
				if c.Kept() != kept || cellText(&Content{Children: []*Node{lost}}, 1, 1) != "edited" {
					t.Errorf("%s, edited at %d: kept %s, conflict %+v", tc.name, editedAt, c.Kept(), c)
				}
			}
		}
	}
}

// A list item one side nested a block under while the other turned it
// into a paragraph stays a paragraph, and the nested block comes out to
// right after it, where it shows.
func TestMergeChildrenOfABlockThatHoldsNone(t *testing.T) {
	item := para("A", "item")
	item.Type = "bulleted_list"
	base := page(map[string]int64{"A": 1, "B": 1}, item, para("B", "after"))
	nested := base.Copy()
	nested.Find("A").Children = []*Node{para("K", "nested")}
	nested = nested.Stamped(base, 5)
	retyped := base.Copy()
	retyped.Find("A").Type = "paragraph"
	retyped = retyped.Stamped(base, 6)
	for _, sides := range [][2]*Content{{nested, retyped}, {retyped, nested}} {
		got, _ := Merge(base, sides[0], sides[1])
		var shape []string
		for _, n := range got.Children {
			shape = append(shape, n.Type+":"+n.Delta().Plain())
			if len(n.Children) > 0 {
				t.Errorf("%s still holds %d blocks", n.ID, len(n.Children))
			}
		}
		if want := []string{"paragraph:item", "paragraph:nested", "paragraph:after"}; !reflect.DeepEqual(shape, want) {
			t.Errorf("merged %v, want %v", shape, want)
		}
	}
}

// A page written as Markdown reads back as the same blocks: each line of a
// quote or an item is in it and escaped where it would start another
// block, a heading stays one line, blocks nested under an item stay
// indented and apart from the item's text, and titles and links of cards
// are escaped.
func TestPageMarkdownKeepsBlocksApart(t *testing.T) {
	n := func(typ, text string, kids ...*Node) *Node {
		x := &Node{ID: NewBlockID(), Type: typ, Children: kids}
		x.SetDelta(Delta{{Text: text}})
		return x
	}
	heading := n("heading", "Plan\nfor *now*")
	heading.Set("level", 2)
	inner := n("heading", "inner")
	inner.Set("level", 3)
	c := FromBlocks([]*Node{
		heading,
		n("quote", "said\n- not a list"),
		n("bulleted_list", "item\n1. not a list", n("bulleted_list", "sub"), n("paragraph", "after sub"), n("quote", "q")),
		n("numbered_list", "ten", inner),
		n("paragraph", "end"),
		{ID: "c", Type: "link_preview", Data: map[string]any{"url": "https://e.com/x_(1) y", "title": "A [b] *c*"}},
		{ID: "v", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "title": "_Never_"}},
		{ID: "i", Type: "image", Data: map[string]any{"url": "https://e.com/a b.png", "alt": "*a*"}},
		{ID: "s", Type: "subpage", Data: map[string]any{"page_id": "P1"}},
	})
	got := PageMarkdown("", c, func(string) string { return "Sub [page]" })
	want := "## Plan for \\*now\\*\n\n" +
		"> said\n> \\- not a list\n\n" +
		"- item\n  1\\. not a list\n  - sub\n\n  after sub\n\n  > q\n\n" +
		"1. ten\n\n   ### inner\n\n" +
		"end\n\n" +
		"[A \\[b\\] \\*c\\*](<https://e.com/x_(1) y>)\n\n" +
		"[\\_Never\\_](https://youtu.be/dQw4w9WgXcQ)\n\n" +
		"![\\*a\\*](<https://e.com/a b.png>)\n\n" +
		"[Sub \\[page\\]](leaflet://page/P1)\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
