package doc

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
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

func TestIsEmpty(t *testing.T) {
	nested := para("a", "")
	nested.Children = []*Node{para("b", "")}
	for _, c := range []struct {
		page  *Content
		empty bool
	}{
		{Empty(), true},
		{&Content{}, true},
		{FromBlocks([]*Node{para("a", "")}), true},
		{FromBlocks([]*Node{para("a", "Hi")}), false},
		{FromBlocks([]*Node{nested}), false},
		{FromBlocks([]*Node{para("a", ""), para("b", "")}), false},
		{FromBlocks([]*Node{{ID: "d", Type: "divider"}}), false},
	} {
		if got := c.page.IsEmpty(); got != c.empty {
			t.Errorf("%s: empty %v, want %v", c.page.Encode(), got, c.empty)
		}
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

func TestMergeOneSidedEditWinsWhateverTheTimes(t *testing.T) {
	base := page(map[string]int64{"a": 5, "b": 5}, para("a", "one"), para("b", "two"))
	for _, remoteAt := range []int64{5, 2} {
		local := page(map[string]int64{"a": 9, "b": 5}, para("a", "ONE"), para("b", "two"))
		remote := page(map[string]int64{"a": 5, "b": remoteAt}, para("a", "one"), para("b", "TWO"))
		got, conflicts := Merge(base, local, remote)
		if !reflect.DeepEqual(texts(got), []string{"ONE", "TWO"}) || len(conflicts) != 0 {
			t.Errorf("remote at %d: texts %v conflicts %v", remoteAt, texts(got), conflicts)
		}
		got, conflicts = Merge(base, remote, local)
		if !reflect.DeepEqual(texts(got), []string{"ONE", "TWO"}) || len(conflicts) != 0 {
			t.Errorf("local at %d: texts %v conflicts %v", remoteAt, texts(got), conflicts)
		}
	}
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

func TestMergeKeepsWhatCameBackOverADelete(t *testing.T) {
	base := page(map[string]int64{"a": 1, "b": 1}, para("a", "one"), para("b", "two"))
	edited := base.Copy()
	edited.Find("a").SetDelta(Delta{{Text: "one?"}})
	edited = edited.Stamped(base, 100)
	deleted := base.Copy()
	deleted.Remove("a")
	deleted = deleted.Stamped(base, 200)

	merged, conflicts := Merge(base, edited, deleted)
	if len(conflicts) != 1 || !reflect.DeepEqual(texts(merged), []string{"one?", "two"}) {
		t.Fatalf("merged %v, conflicts %+v", texts(merged), conflicts)
	}
	if merged.Blocks["a"] <= deleted.Deleted["a"] || merged.Deleted["a"] != 0 {
		t.Errorf("came back at %d over a delete at %d", merged.Blocks["a"], deleted.Deleted["a"])
	}
	typed := deleted.Copy()
	typed.Find("b").SetDelta(Delta{{Text: "two!"}})
	typed = typed.Stamped(deleted, 300)
	got, conflicts := Merge(deleted, typed, merged)
	if !reflect.DeepEqual(texts(got), []string{"one?", "two!"}) || len(conflicts) != 0 {
		t.Errorf("the side that deleted it has %v, conflicts %+v", texts(got), conflicts)
	}

	list := func(kids ...*Node) *Node {
		n := textNode("bulleted_list", Delta{{Text: "list"}})
		n.ID, n.Children = "l", kids
		return n
	}
	base = page(map[string]int64{"l": 1, "b": 1}, list(), para("b", "two"))
	added := base.Copy()
	added.Find("l").Children = []*Node{para("k", "new")}
	added = added.Stamped(base, 100)
	deleted = base.Copy()
	deleted.Remove("l")
	deleted = deleted.Stamped(base, 200)
	merged, _ = Merge(base, added, deleted)
	typed = deleted.Copy()
	typed.Find("b").SetDelta(Delta{{Text: "two!"}})
	typed = typed.Stamped(deleted, 300)
	got, _ = Merge(deleted, typed, merged)
	if !reflect.DeepEqual(texts(got), []string{"list", "new", "two!"}) {
		t.Errorf("the side that deleted it has %v", texts(got))
	}
}

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

func TestAttrsAtKeepALinkOnlyInsideIt(t *testing.T) {
	d := Delta{{Text: "a "}, {Text: "li", Attrs: Attrs{Href: "https://x.dev"}},
		{Text: "nk", Attrs: Attrs{Href: "https://x.dev", Bold: true}}, {Text: "ed", Attrs: Attrs{Href: "https://y.dev"}}, {Text: " b"}}
	for at, want := range map[int]string{0: "", 2: "", 3: "https://x.dev", 4: "https://x.dev", 5: "https://x.dev", 6: "", 7: "https://y.dev", 8: "", 10: ""} {
		if got := d.AttrsAt(at).Href; got != want {
			t.Errorf("at %d the link is %q, want %q", at, got, want)
		}
	}
	if !d.AttrsAt(6).Bold {
		t.Error("the format before the caret is lost at a link's edge")
	}
	whole := Delta{{Text: "link", Attrs: Attrs{Href: "https://x.dev"}}}
	if got := whole.AttrsAt(4).Href; got != "" {
		t.Errorf("after a link that ends the text the link is %q", got)
	}
}

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
	got := PageMarkdown("", c, MarkdownOptions{})
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
	if md := d.ReadableMarkdown(); md != "red marked" {
		t.Errorf("markdown %q", md)
	}
}

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
	c := FromBlocks([]*Node{table.Clone()})
	if err := c.Move(cell.ID, Position{After: table.Children[1].ID}); err != nil {
		t.Error(err)
	}
}

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

func TestInlineLinksReadInLinearTime(t *testing.T) {
	if testing.Short() {
		t.Skip("times reads")
	}
	for _, tc := range []struct{ lead, unit string }{
		{"", "["}, {"", "[a]("}, {"", "[a\n"}, {"[", "``x`` "}, {"[", "`x`` ```y`` "},
	} {
		read := func(n int) func() {
			md := tc.lead + strings.Repeat(tc.unit, n)
			return func() {
				FromMarkdown(md)
				markdownLines(md)
			}
		}
		small, large := bestInTurns(read(4000), read(64000))
		if large > 64*small {
			t.Errorf("%q: 4000 read in %v, 64000 in %v", tc.lead+tc.unit, small, large)
		}
	}
	if d := FromMarkdown("[[a](b) [c](d)"); len(d) != 4 || d[1].Attrs.Href != "b" || d[3].Attrs.Href != "d" {
		t.Errorf("links among [s read as %+v", d)
	}
}

func TestInlineFormatsReadInLinearTime(t *testing.T) {
	if testing.Short() {
		t.Skip("times reads")
	}
	for _, unit := range []string{"*a ", "_a ", "*", "*_[`a", "2 * 3 * "} {
		read := func(n int) func() {
			md := strings.Repeat(unit, n)
			return func() { FromMarkdown(md) }
		}
		small, large := bestInTurns(read(1000), read(16000))
		if large > 64*small {
			t.Errorf("%q: 1000 read in %v, 16000 in %v", unit, small, large)
		}
	}
	nested := func(n int) string { return strings.Repeat("[", n) + "x" + strings.Repeat("](u)", n) }
	read := func(n int) func() {
		md := nested(n)
		return func() { FromMarkdown(md) }
	}
	small, large := bestInTurns(read(1000), read(16000))
	if large > 64*small {
		t.Errorf("nested links: 1000 read in %v, 16000 in %v", small, large)
	}
	if got := FromMarkdown(nested(10)); len(got) != 1 || got[0].Text != "x" || got[0].Attrs.Href != "u" {
		t.Errorf("links nested 10 deep read as %+v", got)
	}
	if got := FromMarkdown(nested(40)).Plain(); !strings.Contains(got, "x](u)") {
		t.Errorf("links nested 40 deep read as %q", got)
	}
	name := "x"
	for range 40 {
		name = "[" + name + "](mention:user:u1)"
	}
	if got := FromMarkdown(name).Plain(); !strings.Contains(got, "](mention:user:u1)") {
		t.Errorf("mentions nested 40 deep read as %q", got)
	}
}

func TestTagsInTextReadInLinearTime(t *testing.T) {
	if testing.Short() {
		t.Skip("times reads")
	}
	read := func(n int) func() {
		md := strings.Repeat("<", n) + "<br>"
		return func() {
			cellMarkdown(md)
			cellBreaks(md)
			summaryMarkdown(md)
			(&lineParser{cur: md}).summaryEnd(md)
		}
	}
	small, large := bestInTurns(read(2000), read(32000))
	if large > 64*small {
		t.Errorf("2000 read in %v, 32000 in %v", small, large)
	}
}

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
	rng := rand.New(rand.NewPCG(1, 2)) // #nosec G404
	for range 5000 {
		var d Delta
		for range 1 + rng.IntN(6) {
			a := Attrs{Bold: rng.IntN(2) == 0, Italic: rng.IntN(2) == 0, Strike: rng.IntN(4) == 0, Code: rng.IntN(4) == 0}
			if rng.IntN(4) == 0 {
				a.Href = hrefs[rng.IntN(len(hrefs))]
			}
			var text strings.Builder
			for i := range 1 + rng.IntN(3) {
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
	words := []string{"a", "bc", "*", "_", "[", "中文"}
	hrefs := []string{"https://a.b", "https://c.d"}
	rng := rand.New(rand.NewPCG(3, 4)) // #nosec G404
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

func TestTrimSpansAsCharByChar(t *testing.T) {
	texts := []string{"a", " ", "  ", "\n", " b ", "中文", "\t", "x\n", "é ", "👍", ""}
	rng := rand.New(rand.NewPCG(5, 6)) // #nosec G404
	for range 20000 {
		var d Delta
		for range 1 + rng.IntN(6) {
			a := Attrs{Bold: rng.IntN(3) == 0, Italic: rng.IntN(3) == 0, Strike: rng.IntN(4) == 0,
				Code: rng.IntN(4) == 0, Under: rng.IntN(5) == 0}
			switch rng.IntN(6) {
			case 0:
				a.Href = "https://a.b"
			case 1:
				a.Mention = &Mention{Kind: "user", ID: "u"}
			case 2:
				a.Color = "#ff0000"
			}
			text := ""
			for range 1 + rng.IntN(3) {
				text += texts[rng.IntN(len(texts))]
			}
			d = append(d, Run{Text: text, Attrs: a})
		}
		d = d.Compact()
		if got, want := trimSpans(d), trimSpansByChar(d); !reflect.DeepEqual(got, want) {
			t.Fatalf("%+v trims to %+v, want %+v", d, got, want)
		}
	}
	plain := Delta{{Text: strings.Repeat("words and spaces ", 200)}, {Text: "a link", Attrs: Attrs{Href: "https://a.b"}}}
	if n := testing.AllocsPerRun(10, func() { _ = trimSpans(plain) }); n > 1 {
		t.Errorf("a text with no mark to trim allocates %v times", n)
	}
}

func trimSpansByChar(d Delta) Delta {
	type char struct {
		r   rune
		a   Attrs
		run int
	}
	var cs []char
	for i, r := range d {
		for _, c := range r.Text {
			cs = append(cs, char{c, r.Attrs, i})
		}
	}
	marks := []func(*Attrs) *bool{
		func(a *Attrs) *bool { return &a.Bold },
		func(a *Attrs) *bool { return &a.Italic },
		func(a *Attrs) *bool { return &a.Strike },
	}
	space := func(i int) bool {
		return !writtenByName(cs[i].a.Mention) && (!cs[i].a.Code || cs[i].r == '\n') && unicode.IsSpace(cs[i].r)
	}
	for _, mark := range marks {
		for i := 0; i < len(cs); {
			if !*mark(&cs[i].a) {
				i++
				continue
			}
			j := i
			for j < len(cs) && *mark(&cs[j].a) {
				j++
			}
			lo, hi := i, j
			for lo < hi && space(lo) {
				*mark(&cs[lo].a) = false
				lo++
			}
			for hi > lo && space(hi-1) {
				*mark(&cs[hi-1].a) = false
				hi--
			}
			i = j
		}
	}
	var out Delta
	for i := 0; i < len(cs); {
		j := i + 1
		for j < len(cs) && cs[j].run == cs[i].run && cs[j].a.Equal(cs[i].a) {
			j++
		}
		text := make([]rune, 0, j-i)
		for _, c := range cs[i:j] {
			text = append(text, c.r)
		}
		out = append(out, Run{Text: string(text), Attrs: cs[i].a})
		i = j
	}
	return out.Compact()
}

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
		"# x":          `\# x`,
		"### x":        `\### x`,
		"#tag":         "#tag",
		"- x":          `\- x`,
		"-5 below":     "-5 below",
		"+ x":          `\+ x`,
		"> x":          `\> x`,
		"| a |":        `\| a |`,
		"1. x":         `1\. x`,
		"12) x":        `12\) x`,
		"3.14 is π":    "3.14 is π",
		"$$":           `\$$`,
		"---":          `\---`,
		"  # x":        `  \# x`,
		" $$":          " \\$$",
		"　| a":         "　\\| a",
		"\v</details>": "\v\\</details>",
		"===":          `\===`,
		"--  ":         `\--  `,
		"= =":          "= =",
		"<!-- x -->":   `\<!-- x -->`,
		"<div>":        `\<div>`,
		"<?x":          `\<?x`,
		"<3":           "<3",
		"plain":        "plain",
	} {
		if got := EscapeBlockStart(in); got != want {
			t.Errorf("%q escaped as %q, want %q", in, got, want)
		}
	}
}

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

func TestReplaceKeepingChildrenKeepsTheirEdits(t *testing.T) {
	read := &Node{ID: "t", Type: "toggle_list", Children: []*Node{para("a", "A")}}
	read.SetDelta(Delta{{Text: "T"}})
	c := FromBlocks([]*Node{read.Clone()})
	agent := read.Clone()
	agent.SetDelta(Delta{{Text: "T2"}})
	op := Op{Op: "replace", ID: "t", Node: agent, PrevNode: read}
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

func TestMergeTableCellDeletedAndEdited(t *testing.T) {
	table, _ := TableNode([][]string{{"a", "b"}, {"c", "d"}}, "T")
	base := FromBlocks([]*Node{table})
	for id := range base.Blocks {
		base.Blocks[id] = 1000
	}
	cellText := func(c *Content, row, col int) string {
		cell := tableCell(c.Find("T"), row, col)
		if cell == nil || len(cell.Children) == 0 {
			return ""
		}
		return cell.Children[0].Delta().Plain()
	}
	edit := func(at int64) *Content {
		c := base.Copy()
		tableCell(c.Find("T"), 1, 1).Children[0].SetDelta(Delta{{Text: "edited"}})
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
	got := PageMarkdown("", c, MarkdownOptions{PageTitle: func(string) string { return "Sub [page]" }})
	want := "## Plan for \\*now\\*\n\n" +
		"> said\\\n> \\- not a list\n\n" +
		"- item\\\n  1\\. not a list\n  - sub\n\n  after sub\n\n  > q\n\n" +
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

func TestPlainCopy(t *testing.T) {
	heading := &Node{ID: "h", Type: "heading", Data: map[string]any{"level": 2}}
	heading.SetDelta(FromMarkdown("Trip **plan** with [map](https://x.dev)"))
	item := &Node{ID: "i", Type: "bulleted_list"}
	item.SetDelta(FromMarkdown("Day *1*"))
	todo := &Node{ID: "t", Type: "todo_list", Data: map[string]any{"checked": true}}
	todo.SetDelta(Delta{{Text: "Kiyomizu-dera"}})
	item.Children = []*Node{todo}
	table, err := TableNode([][]string{{"Item", "Cost"}, {"**Hotel**", "800"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	nodes := []*Node{
		heading, item, para("e", ""), {ID: "d", Type: "divider"}, table,
		CodeNode("a()\n\nb()", "go"),
		{ID: "l", Type: "link_preview", Data: map[string]any{"url": "https://x.dev", "title": "X"}},
		{ID: "s", Type: "subpage", Data: map[string]any{"page_id": "P1"}},
	}
	got := PlainCopy(nodes, func(id string) string { return "Kyoto (" + id + ")" })
	want := "Trip plan with map\nDay 1\n\tKiyomizu-dera\n\nItem\tCost\nHotel\t800\na()\n\nb()\nhttps://x.dev\nKyoto (P1)"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestMergeTableLinesAddedInside(t *testing.T) {
	table, _ := TableNode([][]string{{"h", "i"}, {"a", "b"}, {"c", "d"}}, "T")
	base := FromBlocks([]*Node{table})
	for id := range base.Blocks {
		base.Blocks[id] = 1000
	}
	insert := func(c *Content, key, across, sizeKey string, at int, text string) {
		tt := c.Find("T")
		size := tt.Int(sizeKey, 0)
		lines := tt.Int(map[string]string{"rowsLen": "colsLen", "colsLen": "rowsLen"}[sizeKey], 0)
		for _, cell := range tt.Children {
			if p := cell.Int(key, 0); p >= at {
				cell.Set(key, p+1)
			}
		}
		for k := range lines {
			cell := TableCellNode(0, 0, Delta{{Text: fmt.Sprint(text, k)}})
			cell.Set(key, at)
			cell.Set(across, k)
			tt.Children = append(tt.Children, cell)
		}
		tt.Set(sizeKey, size+1)
		SortTableCells(tt)
	}
	row := func(c *Content, at int, text string) { insert(c, "rowPosition", "colPosition", "rowsLen", at, text) }
	col := func(c *Content, at int, text string) { insert(c, "colPosition", "rowPosition", "colsLen", at, text) }
	side := func(at int64, edits ...func(*Content)) *Content {
		c := base.Copy()
		for _, edit := range edits {
			edit(c)
		}
		return c.Stamped(base, at)
	}
	for _, tc := range []struct {
		name         string
		mine, theirs *Content
		want         [][]string
	}{
		{"rows in different places", side(2000, func(c *Content) { row(c, 1, "m") }), side(1500, func(c *Content) { row(c, 2, "t") }),
			[][]string{{"h", "i"}, {"m0", "m1"}, {"a", "b"}, {"t0", "t1"}, {"c", "d"}}},
		{"the other side first", side(1500, func(c *Content) { row(c, 1, "m") }), side(2000, func(c *Content) { row(c, 2, "t") }),
			[][]string{{"h", "i"}, {"m0", "m1"}, {"a", "b"}, {"t0", "t1"}, {"c", "d"}}},
		{"rows in one place", side(2000, func(c *Content) { row(c, 1, "m") }), side(1500, func(c *Content) { row(c, 1, "t") }),
			[][]string{{"h", "i"}, {"m0", "m1"}, {"t0", "t1"}, {"a", "b"}, {"c", "d"}}},
		{"two rows and one", side(2000, func(c *Content) { row(c, 2, "m") }), side(1500, func(c *Content) { row(c, 1, "t"); row(c, 1, "u") }),
			[][]string{{"h", "i"}, {"u0", "u1"}, {"t0", "t1"}, {"a", "b"}, {"m0", "m1"}, {"c", "d"}}},
		{"inside and at the end", side(2000, func(c *Content) { row(c, 3, "m") }), side(1500, func(c *Content) { row(c, 1, "t") }),
			[][]string{{"h", "i"}, {"t0", "t1"}, {"a", "b"}, {"c", "d"}, {"m0", "m1"}}},
		{"columns", side(2000, func(c *Content) { col(c, 1, "m") }), side(1500, func(c *Content) { col(c, 1, "t") }),
			[][]string{{"h", "m0", "t0", "i"}, {"a", "m1", "t1", "b"}, {"c", "m2", "t2", "d"}}},
		{"a row and a column", side(2000, func(c *Content) { row(c, 1, "m") }), side(1500, func(c *Content) { col(c, 1, "t") }),
			[][]string{{"h", "t0", "i"}, {"m0", "", "m1"}, {"a", "t1", "b"}, {"c", "t2", "d"}}},
	} {
		var first *Content
		for _, sides := range [][2]*Content{{tc.mine, tc.theirs}, {tc.theirs, tc.mine}} {
			got, conflicts := Merge(base, sides[0], sides[1])
			if rows := TableRows(got.Find("T")); !reflect.DeepEqual(rows, tc.want) {
				t.Errorf("%s: rows %q, want %q", tc.name, rows, tc.want)
			}
			if len(conflicts) != 0 {
				t.Errorf("%s: conflicts %+v", tc.name, conflicts)
			}
			if first == nil {
				first = got
			} else if got.Encode() != first.Encode() {
				t.Errorf("%s: the merge differs as the sides swap", tc.name)
			}
		}
		for _, side := range []*Content{tc.mine, tc.theirs} {
			again, conflicts := Merge(base, side, first)
			if rows := TableRows(again.Find("T")); !reflect.DeepEqual(rows, tc.want) || len(conflicts) != 0 {
				t.Errorf("%s: merged again, rows %q, conflicts %+v", tc.name, rows, conflicts)
			}
		}
	}
	less := side(2000, func(c *Content) {
		tt := c.Find("T")
		tt.Children = slices.DeleteFunc(tt.Children, func(k *Node) bool { return k.Int("rowPosition", 0) == 1 })
		for _, cell := range tt.Children {
			if cell.Int("rowPosition", 0) == 2 {
				cell.Set("rowPosition", 1)
			}
		}
		tt.Set("rowsLen", 2)
	})
	more := side(1500, func(c *Content) { col(c, 1, "t") })
	for _, sides := range [][2]*Content{{less, more}, {more, less}} {
		got, _ := Merge(base, sides[0], sides[1])
		want := [][]string{{"h", "t0", "i"}, {"", "t1", ""}, {"c", "t2", "d"}}
		if rows := TableRows(got.Find("T")); !reflect.DeepEqual(rows, want) {
			t.Errorf("a row out and a column in: rows %q, want %q", rows, want)
		}
	}
	anew := func(text string, at int64) *Content {
		c := base.Copy()
		fresh, _ := TableNode([][]string{{text, text + "!"}}, "T")
		c.Children[0].Children = fresh.Children
		c.Children[0].Set("rowsLen", 1)
		return c.Stamped(base, at)
	}
	got, _ := Merge(base, anew("mine", 2000), anew("theirs", 1500))
	if rows, want := TableRows(got.Find("T")), [][]string{{"mine", "mine!"}, {"theirs", "theirs!"}}; !reflect.DeepEqual(rows, want) {
		t.Errorf("written anew on both sides, rows %q", rows)
	}
}

func TestMergeTableRowsAddedOnBothSides(t *testing.T) {
	table, _ := TableNode([][]string{{"a", "b"}, {"c", "d"}}, "T")
	base := FromBlocks([]*Node{table})
	for id := range base.Blocks {
		base.Blocks[id] = 1000
	}
	cell := func(row, col int, text string) *Node {
		return &Node{ID: NewBlockID(), Type: "table/cell", Data: map[string]any{"rowPosition": row, "colPosition": col},
			Children: []*Node{{ID: NewBlockID(), Type: "paragraph", Data: map[string]any{"delta": Delta{{Text: text}}.JSON()}}}}
	}
	addRow := func(text string, at int64) *Content {
		c := base.Copy()
		tt := c.Find("T")
		tt.Children = append(tt.Children, cell(2, 0, text), cell(2, 1, text+"!"))
		tt.Set("rowsLen", 3)
		return c.Stamped(base, at)
	}
	addCol := func(text string, at int64) *Content {
		c := base.Copy()
		tt := c.Find("T")
		tt.Children = append(tt.Children, cell(0, 2, text), cell(1, 2, text+"!"))
		tt.Set("colsLen", 3)
		return c.Stamped(base, at)
	}
	for _, tc := range []struct {
		name         string
		mine, theirs *Content
		want         [][]string
	}{
		{"rows", addRow("mine", 2000), addRow("theirs", 1500),
			[][]string{{"a", "b"}, {"c", "d"}, {"mine", "mine!"}, {"theirs", "theirs!"}}},
		{"columns", addCol("mine", 2000), addCol("theirs", 1500),
			[][]string{{"a", "b", "mine", "theirs"}, {"c", "d", "mine!", "theirs!"}}},
		{"a row and a column", addRow("mine", 2000), addCol("theirs", 1500),
			[][]string{{"a", "b", "theirs"}, {"c", "d", "theirs!"}, {"mine", "mine!", ""}}},
	} {
		var first *Content
		for _, sides := range [][2]*Content{{tc.mine, tc.theirs}, {tc.theirs, tc.mine}} {
			got, conflicts := Merge(base, sides[0], sides[1])
			if rows := TableRows(got.Find("T")); !reflect.DeepEqual(rows, tc.want) {
				t.Errorf("%s: rows %q, want %q", tc.name, rows, tc.want)
			}
			if len(conflicts) != 0 {
				t.Errorf("%s: conflicts %+v", tc.name, conflicts)
			}
			if first == nil {
				first = got
			} else if got.Encode() != first.Encode() {
				t.Errorf("%s: the merge differs as the sides swap", tc.name)
			}
		}
		for _, side := range []*Content{tc.mine, tc.theirs} {
			again, conflicts := Merge(base, side, first)
			if rows := TableRows(again.Find("T")); !reflect.DeepEqual(rows, tc.want) || len(conflicts) != 0 {
				t.Errorf("%s: merged again, rows %q, conflicts %+v", tc.name, rows, conflicts)
			}
		}
	}
}

func TestMergeKeepsMovesApart(t *testing.T) {
	order := func(c *Content) string {
		var ids []string
		c.Walk(func(n, _ *Node) { ids = append(ids, n.ID) })
		return strings.Join(ids, ",")
	}
	list := func(id string, kids ...*Node) *Node {
		n := &Node{ID: id, Type: "bulleted_list", Children: kids}
		n.SetDelta(Delta{{Text: id}})
		return n
	}
	at := func(t0 int64, c *Content, base *Content) *Content { return c.Stamped(base, t0) }
	base := page(map[string]int64{"a": 1, "b": 1, "c": 1}, para("a", "a"), para("b", "b"), para("c", "c"))
	four := page(map[string]int64{"a": 1, "b": 1, "c": 1, "d": 1}, para("a", "a"), para("b", "b"), para("c", "c"), para("d", "d"))
	nested := page(map[string]int64{"l": 1, "x": 1, "y": 1, "p": 1}, list("l", para("x", "x"), para("y", "y")), para("p", "p"))
	for _, tc := range []struct {
		name          string
		base          *Content
		local, remote *Content
		want          []string
	}{
		{"a deleted block stays deleted as its neighbour moves",
			base, at(5, page(nil, para("a", "a"), para("c", "c")), base), at(4, page(nil, para("b", "b"), para("c", "c"), para("a", "a")), base),
			[]string{"c,a"}},
		{"two blocks moved to the end on each side",
			four, at(5, page(nil, para("b", "b"), para("c", "c"), para("d", "d"), para("a", "a")), four),
			at(4, page(nil, para("a", "a"), para("c", "c"), para("d", "d"), para("b", "b")), four),
			[]string{"c,d,a,b", "c,d,b,a"}},
		{"a move after a block the other side made",
			base, at(5, page(nil, para("a", "a"), para("b", "b"), para("c", "c!")), base),
			at(4, page(nil, para("b", "b"), para("c", "c"), para("n", "n"), para("a", "a")), base),
			[]string{"b,c,n,a"}},
		{"a move in a list after a block the other side made",
			nested, at(5, page(nil, list("l", para("x", "x"), para("y", "y")), para("p", "p!")), nested),
			at(4, page(nil, list("l", para("y", "y"), para("n", "n"), para("x", "x")), para("p", "p")), nested),
			[]string{"l,y,n,x,p"}},
	} {
		var first string
		for _, sides := range [][2]*Content{{tc.local, tc.remote}, {tc.remote, tc.local}} {
			got, conflicts := Merge(tc.base, sides[0], sides[1])
			if !slices.Contains(tc.want, order(got)) {
				t.Errorf("%s: %s, want one of %v", tc.name, order(got), tc.want)
			}
			if len(conflicts) != 0 {
				t.Errorf("%s: conflicts %+v", tc.name, conflicts)
			}
			if first == "" {
				first = order(got)
			} else if order(got) != first {
				t.Errorf("%s: %s as the sides swap, %s before", tc.name, order(got), first)
			}
		}
	}
}

func TestOldTombstonesGo(t *testing.T) {
	now := int64(400 * 24 * 60 * 60 * 1000)
	prev := &Content{Children: []*Node{{ID: "a", Type: "paragraph"}}, Blocks: map[string]int64{"a": 1},
		Deleted: map[string]int64{"old": now - TombstoneLife - 1, "new": now - 1000}}
	got := prev.Copy().Stamped(prev, now)
	if _, ok := got.Deleted["old"]; ok {
		t.Error("an old tombstone stayed")
	}
	if _, ok := got.Deleted["new"]; !ok {
		t.Error("a new tombstone went")
	}
}

func TestRepairedIDsAreTheSameEachRead(t *testing.T) {
	stored := `{"document":{"type":"page","id":"root","children":[` +
		`{"id":"a","type":"paragraph","data":{"delta":[{"insert":"one"}]}},` +
		`{"id":"a","type":"paragraph","data":{"delta":[{"insert":"two"}]}},` +
		`{"id":"a-2","type":"paragraph","data":{"delta":[{"insert":"three"}]}},` +
		`{"id":"","type":"paragraph","data":{"delta":[{"insert":"four"}]},"children":[` +
		`{"id":"root","type":"paragraph","data":{"delta":[{"insert":"five"}]}}]}]},"meta":{"v":1}}`
	read := func() *Content {
		c, err := Decode(stored)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	var ids []string
	read().Walk(func(n, _ *Node) { ids = append(ids, n.ID) })
	if want := []string{"a", "a-3", "a-2", "block-1", "root-1"}; !slices.Equal(ids, want) {
		t.Errorf("IDs %q, want %q", ids, want)
	}
	base, local, remote := read(), read(), read()
	local.Find("a-3").SetDelta(Delta{{Text: "two, edited"}})
	got, _ := Merge(base, local, remote)
	if want := []string{"one", "two, edited", "three", "four", "five"}; !slices.Equal(texts(got), want) {
		t.Errorf("merged %q, want %q", texts(got), want)
	}
}

func TestNewBlocksOnTwoDevicesKeepApart(t *testing.T) {
	base := FromBlocks([]*Node{para("a", "first")})
	edit := func(text string) *Content {
		c := base.Copy()
		c.Children = append(c.Children, para("", text), para("a", text+", pasted"))
		got, err := DecodeEdit(c.Encode())
		if err != nil {
			t.Fatal(err)
		}
		return got.Stamped(base, NowMs())
	}
	got, _ := Merge(base, edit("from A"), edit("from B"))
	if want := []string{"first", "from A", "from A, pasted", "from B", "from B, pasted"}; !slices.Equal(slices.Sorted(slices.Values(texts(got))), want) {
		t.Errorf("merged %q, want %q", texts(got), want)
	}
	made := func() string { return FromBlocks([]*Node{{Type: "paragraph"}}).Children[0].ID }
	copied := func() string { return CopyPages(map[string]*Content{"p": base}, nil)["p"].Children[0].ID }
	if a, b := made(), made(); a == b {
		t.Errorf("two pages made of a block without an ID both have %s", a)
	}
	if a, b := copied(), copied(); a == b || a == "a" {
		t.Errorf("two copies of a page have %s and %s", a, b)
	}
}

func TestMergeIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("times merges")
	}
	merge := func(n int) func() {
		blocks := make([]*Node, n)
		for i := range blocks {
			blocks[i] = para(fmt.Sprintf("b%d", i), fmt.Sprintf("line %d with some words", i))
		}
		base := FromBlocks(blocks)
		local, remote := base.Copy(), base.Copy()
		local.Find("b1").SetDelta(Delta{{Text: "local"}})
		remote.Find(fmt.Sprintf("b%d", n-2)).SetDelta(Delta{{Text: "remote"}})
		local, remote = local.Stamped(base, NowMs()+1), remote.Stamped(base, NowMs()+2)
		return func() { Merge(base, local, remote) }
	}
	small, large := bestInTurns(merge(1000), merge(8000))
	if large > 24*small {
		t.Errorf("a merge of 1000 blocks takes %v, of 8000 %v", small, large)
	}
}

func bestInTurns(small, large func()) (time.Duration, time.Duration) {
	timed := func(fn func()) time.Duration {
		runtime.GC()
		start := time.Now()
		for runs := 1; ; runs++ {
			fn()
			if took := time.Since(start); took >= 32*time.Millisecond {
				return took / time.Duration(runs)
			}
		}
	}
	bestSmall, bestLarge := time.Duration(math.MaxInt64), time.Duration(math.MaxInt64)
	for range 5 {
		bestSmall = min(bestSmall, timed(small))
		bestLarge = min(bestLarge, timed(large))
	}
	return bestSmall, bestLarge
}

func TestOpsDropNulls(t *testing.T) {
	var insert Op
	raw := `{"op":"insert","position":{"at":"end"},"nodes":[null,{"id":"a","type":"bulleted_list","children":[null,{"id":"b","type":"paragraph"}]}]}`
	if err := json.Unmarshal([]byte(raw), &insert); err != nil {
		t.Fatal(err)
	}
	c := page(map[string]int64{"p": 1}, para("p", "one"))
	if err := c.Apply(insert); err != nil {
		t.Fatal(err)
	}
	if a := c.Find("a"); a == nil || len(a.Children) != 1 || c.Find("b") == nil {
		t.Errorf("inserted %+v", c.Children)
	}
	var del Op
	raw = `{"op":"delete","ids":["a"],"previous":[null,{"node":null,"position":{"at":"end"}},{"node":{"id":"a","type":"paragraph","children":[null]},"position":{"at":"end"}}]}`
	if err := json.Unmarshal([]byte(raw), &del); err != nil {
		t.Fatal(err)
	}
	if len(del.Removed) != 1 || del.Removed[0].Node.ID != "a" || len(del.Removed[0].Node.Children) != 0 {
		t.Errorf("removed %+v", del.Removed)
	}
	var replace Op
	raw = `{"op":"replace","id":"p","node":{"id":"p","type":"bulleted_list","children":[null]},"previous":{"id":"p","type":"paragraph","children":[null]}}`
	if err := json.Unmarshal([]byte(raw), &replace); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(replace); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(replace.Invert()[0]); err != nil {
		t.Fatal(err)
	}
}

func TestPageOpsRoundTripAndInvert(t *testing.T) {
	ops := []Op{
		{Op: "move_page", PageID: "p1", Title: "Plan", Place: &PagePlace{ParentID: "p2"}, PrevPlace: &PagePlace{FolderID: "f1", SortKey: "a1"}},
		{Op: "rename_page", PageID: "p1", Title: "Plan", PrevTitle: "Draft"},
		{Op: "set_page_tags", PageID: "p1", Title: "Plan", Tags: []string{"work"}, PrevTags: []string{}},
		{Op: "create_folder", Folder: &NewFolder{ID: "f2", SpaceID: "s1", Name: "Clippings", SortKey: "a2"}},
		{Op: "delete_folder", FolderID: "f2"},
	}
	for _, o := range ops {
		raw, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		var back Op
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, o) {
			t.Errorf("%s came back as %+v", raw, back)
		}
		if !o.IsPageOp() {
			t.Errorf("%s is no page op", o.Op)
		}
	}
	if raw, _ := json.Marshal(ops[0]); !strings.Contains(string(raw), `"previous":{"folder_id":"f1","sort_key":"a1"}`) {
		t.Errorf("move_page %s", raw)
	}
	inv := InvertRun(ops[:4])
	want := []Op{
		{Op: "delete_folder", FolderID: "f2"},
		{Op: "set_page_tags", PageID: "p1", Title: "Plan", Tags: []string{}, PrevTags: []string{"work"}},
		{Op: "rename_page", PageID: "p1", Title: "Draft", PrevTitle: "Plan"},
		{Op: "move_page", PageID: "p1", Title: "Plan", Place: &PagePlace{FolderID: "f1", SortKey: "a1"}, PrevPlace: &PagePlace{ParentID: "p2"}},
	}
	if !reflect.DeepEqual(inv, want) {
		t.Errorf("inverse %+v", inv)
	}
	if (Op{Op: "insert"}).IsPageOp() {
		t.Error("an insert is a page op")
	}
	c := Empty()
	before := c.Encode()
	if err := c.Apply(ops[0]); err != nil || c.Encode() != before {
		t.Errorf("apply: %v, %s", err, c.Encode())
	}
}

func TestColorSpans(t *testing.T) {
	red, mark := "#ef4444ff", "#eab30840"
	d := Delta{
		{Text: "plain "},
		{Text: "red ", Attrs: Attrs{Color: red}},
		{Text: "bold", Attrs: Attrs{Color: red, Bold: true}},
		{Text: " both", Attrs: Attrs{Color: red, Background: mark}},
		{Text: " code", Attrs: Attrs{Background: mark, Code: true}},
		{Text: "link", Attrs: Attrs{Color: red, Href: "https://example.com"}},
	}
	md := d.ToMarkdown()
	if want := `plain <span style="color:#ef4444ff">red **bold**</span><span style="color:#ef4444ff;background:#eab30840"> both</span><span style="background:#eab30840">` + "` code`" + `</span>[<span style="color:#ef4444ff">link</span>](https://example.com)`; md != want {
		t.Errorf("markdown\n%s\nwant\n%s", md, want)
	}
	if back := FromMarkdown(md); !sameRuns(back, d.Compact()) {
		t.Errorf("read back %+v", back)
	}
	if got := d.ReadableMarkdown(); strings.Contains(got, "span") {
		t.Errorf("readable %q", got)
	}

	for src, want := range map[string]Delta{
		`<span style="color: #ef4444; background-color: #fde68a;">x</span>`: {{Text: "x", Attrs: Attrs{Color: "#ef4444", Background: "#fde68a"}}},
		`**<span style="color:#ef4444">x</span>**`:                          {{Text: "x", Attrs: Attrs{Color: "#ef4444", Bold: true}}},
		`<span style="color:#ef4444">a *b* c</span>`:                        {{Text: "a ", Attrs: Attrs{Color: "#ef4444"}}, {Text: "b", Attrs: Attrs{Color: "#ef4444", Italic: true}}, {Text: " c", Attrs: Attrs{Color: "#ef4444"}}},
		`<span style="color:red">x</span>`:                                  {{Text: `<span style="color:red">x</span>`}},
		`<span style="color:#ef4444;font-size:40px">x</span>`:               {{Text: `<span style="color:#ef4444;font-size:40px">x</span>`}},
		`<span style="color:#ef4444">x`:                                     {{Text: `<span style="color:#ef4444">x`}},
		`<span style="color:#ef4444"></span>`:                               {{Text: `<span style="color:#ef4444"></span>`}},
		"`<span style=\"color:#ef4444\">x</span>`":                          {{Text: `<span style="color:#ef4444">x</span>`, Attrs: Attrs{Code: true}}},
		`a < b </div>`: {{Text: `a < b </div>`}},
	} {
		if got := FromMarkdown(src); !sameRuns(got, want) {
			t.Errorf("%q read as %+v", src, got)
		}
	}

	for _, text := range []string{`<span style="color:#ef4444">x</span>`, `</span>`, `a <span style="`} {
		for _, a := range []Attrs{{}, {Color: red}} {
			d := Delta{{Text: text, Attrs: a}}
			if md := d.ToMarkdown(); !sameRuns(FromMarkdown(md), d) {
				t.Errorf("%q read back from %q as %+v", text, md, FromMarkdown(md))
			}
		}
	}
}

func sameRuns(a, b Delta) bool {
	return slices.EqualFunc(a, b, func(x, y Run) bool { return x.Text == y.Text && x.Attrs.Equal(y.Attrs) })
}

func TestColorSpansAreLinear(t *testing.T) {
	src := strings.Repeat(`<span style="color:#ef4444">a `, 20000)
	start := time.Now()
	if got := FromMarkdown(src).Plain(); got != src {
		t.Fatalf("read %d characters of %d", len(got), len(src))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestColorSpansInLines(t *testing.T) {
	nodes := MarkdownBlocks("<span style=\"color:#ef4444\">first</span> line\n- <span style=\"color:#ef4444\">item</span>")
	if len(nodes) != 2 || nodes[0].Type != "paragraph" || nodes[1].Type != "bulleted_list" {
		t.Fatalf("blocks %+v", nodes)
	}
	for _, n := range nodes {
		if d := n.Delta(); d[0].Attrs.Color != "#ef4444" {
			t.Errorf("%s: %+v", n.Type, d)
		}
	}
}

func bullet(id, text string, kids ...*Node) *Node {
	n := &Node{ID: id, Type: "bulleted_list", Children: kids}
	n.SetDelta(Delta{{Text: text}})
	return n
}

func TestReplaceKeepsBlocksNestedSince(t *testing.T) {
	heading := func(id, text string) *Node {
		n := para(id, text)
		n.Type = "heading"
		n.Set("level", 2)
		return n
	}
	apply := func(c *Content, ops ...Op) {
		t.Helper()
		for _, o := range ops {
			if err := c.Apply(o); err != nil {
				t.Fatal(err)
			}
		}
	}

	c := FromBlocks([]*Node{para("P", "Shopping"), bullet("K", "milk")})
	run := c.ReplaceOp("P", bullet("P", "Shopping"))
	apply(c, run)
	if err := c.Move("K", Position{Parent: "P"}); err != nil {
		t.Fatal(err)
	}
	apply(c, InvertRun([]Op{run})...)
	if got, want := tree(c), []string{`P paragraph "Shopping"`, `K bulleted_list "milk"`}; !slices.Equal(got, want) {
		t.Errorf("undone under a nested block: %q, want %q", got, want)
	}

	c = FromBlocks([]*Node{bullet("L", "list"), para("z", "z")})
	toHeading := c.ReplaceOp("L", heading("L", "list"))
	_ = c.Insert(Position{Parent: "L"}, bullet("K", "milk", para("G", "whole")))
	apply(c, toHeading)
	if got, want := tree(c), []string{`L heading "list"`, `K bulleted_list "milk"`, `  G paragraph "whole"`, `z paragraph "z"`}; !slices.Equal(got, want) {
		t.Errorf("a list made a heading: %q, want %q", got, want)
	}

	c = FromBlocks([]*Node{bullet("L", "list", bullet("a", "a"), para("b", "b"))})
	rewrite := c.ReplaceOp("L", bullet("L", "new", bullet("a", "a2"), para("n", "n")))
	_ = c.Insert(Position{Parent: "L"}, para("K", "milk"))
	_ = c.Insert(Position{Parent: "a"}, para("G", "under a"))
	_ = c.Insert(Position{Parent: "b"}, para("H", "under b"))
	apply(c, rewrite)
	want := []string{`L bulleted_list "new"`, `  a bulleted_list "a2"`, `    G paragraph "under a"`, `  n paragraph "n"`, `  K paragraph "milk"`, `H paragraph "under b"`}
	if got := tree(c); !slices.Equal(got, want) {
		t.Errorf("children rewritten: %q, want %q", got, want)
	}

	c = FromBlocks([]*Node{bullet("L", "list", para("a", "a"))})
	original := c.Copy().Encode()
	toHeading = c.ReplaceOp("L", heading("L", "list"))
	apply(c, toHeading)
	if c.Find("a") != nil {
		t.Errorf("the child the op took out is back: %q", tree(c))
	}
	apply(c, toHeading.Invert()...)
	if got := c.Encode(); got != original {
		t.Errorf("undo gave\n%s\nwant\n%s", got, original)
	}
}

func TestDeleteKeepsBlocksNestedSince(t *testing.T) {
	c := FromBlocks([]*Node{para("a", "a"), para("z", "z")})
	run := InsertOp(Position{After: "a"}, bullet("X", "made", para("x1", "made too")))
	if err := c.Apply(run); err != nil {
		t.Fatal(err)
	}
	_ = c.Insert(Position{Parent: "X"}, bullet("K", "mine", para("G", "mine too")))
	_ = c.Insert(Position{Parent: "x1"}, para("H", "under theirs"))
	_ = c.Move("x1", Position{Parent: "K"})
	for _, o := range InvertRun([]Op{run}) {
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{`a paragraph "a"`, `K bulleted_list "mine"`, `  G paragraph "mine too"`, `  H paragraph "under theirs"`, `z paragraph "z"`}
	if got := tree(c); !slices.Equal(got, want) {
		t.Errorf("an insert undone: %q, want %q", got, want)
	}

	c = FromBlocks([]*Node{para("a", "a"), bullet("X", "list", bullet("K", "item", para("G", "deep")))})
	if err := c.Apply(c.DeleteOp([]string{"X"})); err != nil {
		t.Fatal(err)
	}
	if got, want := tree(c), []string{`a paragraph "a"`}; !slices.Equal(got, want) {
		t.Errorf("a block deleted with what it holds: %q, want %q", got, want)
	}
	if err := c.Apply(Op{Op: "delete", IDs: []string{"a"}}); err != nil || len(c.Children) != 0 {
		t.Errorf("a delete that names no blocks read: %q, %v", tree(c), err)
	}
}

func TestDeepPagesEncode(t *testing.T) {
	deep := func(n int) *Node {
		root := bullet("b0", "0")
		at := root
		for i := 1; i < n; i++ {
			k := bullet(fmt.Sprintf("b%d", i), strconv.Itoa(i))
			at.Children = []*Node{k, para(fmt.Sprintf("p%d", i), "beside")}
			at = k
		}
		return root
	}
	depth := func(c *Content) int {
		var of func([]*Node) int
		of = func(nodes []*Node) int {
			most := 0
			for _, n := range nodes {
				if n.Type != "table" {
					most = max(most, of(n.Children))
				}
			}
			return most + 1
		}
		return of(c.Children) - 1
	}
	const n = 6000
	c := FromBlocks([]*Node{deep(n)})
	table, _ := TableNode([][]string{{"a", "b"}}, "t")
	c.Find(fmt.Sprintf("b%d", n-1)).Children = []*Node{table}
	back, err := Decode(c.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if got := depth(back); got != maxDepth {
		t.Errorf("written %d deep, want %d", got, maxDepth)
	}
	var want, got []string
	c.Walk(func(n, _ *Node) { want = append(want, n.ID) })
	back.Walk(func(n, _ *Node) { got = append(got, n.ID) })
	if !slices.Equal(got, want) {
		t.Errorf("%d blocks written of %d, or out of order", len(got), len(want))
	}
	if cells := back.Find("t"); cells == nil || len(cells.Children) != 2 || len(cells.Children[0].Children) != 1 {
		t.Errorf("the table came apart")
	}
	if depth(c) <= maxDepth {
		t.Error("the page itself changed")
	}
	shallow := FromBlocks([]*Node{deep(maxDepth)})
	if back, _ := Decode(shallow.Encode()); depth(back) != maxDepth || !slices.Equal(tree(back), tree(shallow)) {
		t.Errorf("a page %d deep changed", maxDepth)
	}

	var nested any = "x"
	for range 12000 {
		nested = []any{nested}
	}
	c = FromBlocks([]*Node{para("a", "kept")})
	c.Children[0].Data["odd"] = nested
	c.Children[0].Data["worse"] = math.NaN()
	if back, err := Decode(c.Encode()); err != nil || back.Find("a").Delta().Plain() != "kept" {
		t.Errorf("a block with data too deep to write: %v", err)
	}
}

func TestBackticksReadInLinearTime(t *testing.T) {
	if testing.Short() {
		t.Skip("times reads")
	}
	lengths := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			b.WriteString(strings.Repeat("`", i) + "x")
		}
		return b.String()
	}
	for name, md := range map[string]func(n int) string{
		"one run":      func(n int) string { return "x" + strings.Repeat("`", n*n/2) },
		"runs":         lengths,
		"in a row":     func(n int) string { return "| " + lengths(n) + " |\n| a |" },
		"in a break":   func(n int) string { return "| <br>" + lengths(n) + " |\n| a |" },
		"in a summary": func(n int) string { return "<details><summary>" + lengths(n) + "</summary>\n\nx\n\n</details>" },
	} {
		read := func(n int) func() {
			text := md(n)
			return func() { ParseLines(text) }
		}
		small, large := bestInTurns(read(50), read(400))
		if large > 64*4*small {
			t.Errorf("%s: 50 read in %v, 400 in %v", name, small, large)
		}
	}
	for md, want := range map[string]Delta{
		"``a`":        {{Text: "``a`"}},
		"`a``b`":      {{Text: "a``b", Attrs: Attrs{Code: true}}},
		"```a``b`c``": {{Text: "```a"}, {Text: "b`c", Attrs: Attrs{Code: true}}},
		"\\``a`":      {{Text: "`"}, {Text: "a", Attrs: Attrs{Code: true}}},
		"*a ``` b*":   {{Text: "a ``` b", Attrs: Attrs{Italic: true}}},
		"[a ``](u) `": {{Text: "a ``", Attrs: Attrs{Href: "u"}}, {Text: " `"}},
	} {
		if got := FromMarkdown(md); !reflect.DeepEqual(got, want) {
			t.Errorf("%q read as %+v, want %+v", md, got, want)
		}
	}
}

func TestMentionsWrittenOnce(t *testing.T) {
	ann := &Mention{Kind: "user", ID: "u1", Name: "Ann "}
	for _, a := range []Attrs{{Bold: true}, {Italic: true}, {Strike: true}, {Bold: true, Italic: true}} {
		a.Mention = ann
		d := Delta{{Text: "ask "}, {Text: "@Ann ", Attrs: a}, {Text: "today"}}
		md := d.ToMarkdown()
		if n := strings.Count(md, "mention:user:u1"); n != 1 {
			t.Errorf("%q names the mention %d times", md, n)
		}
		if got := FromMarkdown(md); !reflect.DeepEqual(got, d) {
			t.Errorf("%q read back as %+v, want %+v", md, got, d)
		}
	}
	bold := PageMention("p1", "Plan ")
	bold.Bold = true
	for _, d := range []Delta{
		{{Text: "Plan", Attrs: PageMention("p1", "Plan")}, {Text: "Plan", Attrs: PageMention("p1", "Plan")}},
		{{Text: "see "}, {Text: "Plan ", Attrs: bold}, {Text: "Plan ", Attrs: bold}, {Text: "Plan", Attrs: PageMention("p1", "Plan")}},
		{{Text: "a", Attrs: Attrs{Href: PageLink("p1") + "#b1"}}, {Text: "Plan", Attrs: PageMention("p1", "Plan")}},
	} {
		md := d.ToMarkdown()
		if got := FromMarkdown(md); !reflect.DeepEqual(got, d) {
			t.Errorf("%q read back as %+v, want %+v", md, got, d)
		}
	}
}

func TestReplaceLeavesABlockMovedOutWhereItWent(t *testing.T) {
	c := FromBlocks([]*Node{bullet("L", "list", bullet("a", "a")), para("z", "z")})
	rewrite := c.ReplaceOp("L", bullet("L", "new", bullet("a", "a2")))
	if err := c.Move("a", Position{After: "z"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(rewrite); err != nil {
		t.Fatal(err)
	}
	if got, want := tree(c), []string{`L bulleted_list "new"`, `z paragraph "z"`, `a bulleted_list "a"`}; !slices.Equal(got, want) {
		t.Errorf("replaced after a block moved out: %q, want %q", got, want)
	}
}

func TestInlineMathMarkdown(t *testing.T) {
	math := func(tex string) Run { return Run{Text: tex, Attrs: Attrs{Math: true}} }
	for md, want := range map[string]Delta{
		"the area is $\\pi r^2$.": {{Text: "the area is "}, math(`\pi r^2`), {Text: "."}},
		"$a$ and $b$":             {math("a"), {Text: " and "}, math("b")},
		"it costs $5 and $10":     {{Text: "it costs $5 and $10"}},
		"from $5 to $6-ish":       {{Text: "from $5 to $6-ish"}},
		"a lone $ sign":           {{Text: "a lone $ sign"}},
		"spaced $ x $ out":        {{Text: "spaced $ x $ out"}},
		"escaped \\$x\\$":         {{Text: "escaped $x$"}},
		"fenced $`a $ b`$ here":   {{Text: "fenced "}, math("a $ b"), {Text: " here"}},
		"doubled $$x^2$$ here":    {{Text: "doubled "}, math("x^2"), {Text: " here"}},
		"**bold $x$**":            {{Text: "bold ", Attrs: Attrs{Bold: true}}, {Text: "x", Attrs: Attrs{Bold: true, Math: true}}},
		"`code $x$`":              {{Text: "code $x$", Attrs: Attrs{Code: true}}},
	} {
		got := FromMarkdown(md)
		if len(got) != len(want) {
			t.Errorf("%q read as %+v", md, got)
			continue
		}
		for i := range got {
			if got[i].Text != want[i].Text || !got[i].Attrs.Equal(want[i].Attrs) {
				t.Errorf("%q read as %+v", md, got)
				break
			}
		}
	}
	for _, d := range []Delta{
		{{Text: "x = "}, math(`\frac{1}{2}`), {Text: "5 apples"}},
		{{Text: "$a"}, math("b"), {Text: "c$ and $d$"}},
		{math(" spaced "), {Text: " then $x$ in text"}},
		{math("has $ sign"), math("`tick")},
		{{Text: "price $5 and $10 and a$b$c"}},
	} {
		md := d.ToMarkdown()
		back := FromMarkdown(md)
		if back.Plain() != d.Compact().Plain() || len(back) != len(d.Compact()) {
			t.Errorf("%+v wrote %q, read back as %+v", d, md, back)
			continue
		}
		for i, r := range d.Compact() {
			if back[i].Text != r.Text || back[i].Attrs.Math != r.Attrs.Math {
				t.Errorf("%+v wrote %q, read back as %+v", d, md, back)
				break
			}
		}
	}
	if md := (Delta{{Text: "it costs $5 and $10"}}).ToMarkdown(); md != "it costs $5 and $10" {
		t.Errorf("prices wrote as %q", md)
	}
	if h := (Delta{math("a<b")}).HTML(); !strings.HasPrefix(h, `<span class="tex" title="a&lt;b"><svg `) || !strings.HasSuffix(h, "</svg></span>") {
		t.Errorf("a formula's HTML: %s", h)
	}
	if h := (Delta{math(`\frac{1`)}).HTML(); h != `<code>\frac{1</code>` {
		t.Errorf("half a formula's HTML: %s", h)
	}
}
