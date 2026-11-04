package editor

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

// longPage is an editor of n paragraphs, built once in a window, with the
// caret in the first.
func longPage(t testing.TB, n int) (*Editor, *ui.Tester, []*doc.Node) {
	t.Helper()
	e := New(doc.Empty(), &fakeHost{})
	e.SetWidth(500)
	var nodes []*doc.Node
	for i := range n {
		nodes = append(nodes, newText("paragraph", doc.Delta{{Text: fmt.Sprintf("line %d", i)}}))
	}
	e.Content.Children = nodes
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { e.View(c) })
	}, 600, 400)
	tt.Frame()
	e.PlaceCaret(nodes[0].ID, 0)
	return e, tt, nodes
}

// layoutsOf are the blocks' layouts, as last built.
func layoutsOf(e *Editor, nodes []*doc.Node) []*blockLayout {
	var out []*blockLayout
	for _, n := range nodes {
		out = append(out, e.layouts[n.ID])
	}
	return out
}

// The steps of undo share the blocks an edit leaves: each keeps anew only
// what changed since the step before, never a block of the page itself,
// and going back keeps the blocks that were as they are.
func TestStepsShareWhatEditsLeave(t *testing.T) {
	e, _, nodes := longPage(t, 50)
	e.PlaceCaret(nodes[10].ID, 0)
	for _, s := range []string{"a", "b", "c"} {
		e.InsertText(s)
		e.BreakUndo()
	}
	if len(e.undo) != 3 {
		t.Fatalf("%d steps, want 3", len(e.undo))
	}
	first, last := e.undo[0].content.Children, e.undo[2].content.Children
	for i := range nodes {
		if (first[i] == last[i]) != (i != 10) {
			t.Errorf("block %d shared: %v", i, first[i] == last[i])
		}
		for _, s := range e.undo {
			if s.content.Children[i] == e.Content.Children[i] {
				t.Fatalf("a step keeps block %d of the page itself", i)
			}
		}
	}
	if size := e.undo[2].size; size <= 0 || size > 4096 {
		t.Errorf("a step of one block keeps %d bytes", size)
	}
	for range 3 {
		e.Undo()
	}
	if got := e.Content.Children[10].Delta().Plain(); got != "line 10" {
		t.Errorf("after undo %q", got)
	}
	for i, n := range e.Content.Children {
		if (n == nodes[i]) != (i != 10) {
			t.Errorf("block %d is the page's own after undo: %v", i, n == nodes[i])
		}
	}
	for range 3 {
		e.Redo()
	}
	if got := e.Content.Children[10].Delta().Plain(); got != "abcline 10" {
		t.Errorf("after redo %q", got)
	}
}

// What a step keeps stays as it was, whatever changes the page after: its
// blocks are its own, which no edit reaches.
func TestStepsKeepThePageAsItWas(t *testing.T) {
	e, _, nodes := longPage(t, 5)
	nodes[1].Children = []*doc.Node{newText("paragraph", doc.Delta{{Text: "under"}})}
	e.Edit(func(c *doc.Content) { c.Children[0].Set("level", 2) })
	kept := e.undo[len(e.undo)-1].content.Encode()
	// Changed in place, as the app's dialogs and the table's cells do.
	e.Edit(func(c *doc.Content) {
		c.Children[0].Set("level", 3)
		c.Children[1].Children[0].SetDelta(doc.Delta{{Text: "changed"}})
		c.Children[2].Data["extra"] = map[string]any{"k": []any{1.0}}
	})
	nodes[3].Data["extra"] = []any{"in place"}
	if got := e.undo[len(e.undo)-2].content.Encode(); got != kept {
		t.Errorf("a step changed with the page:\n%s\nwant\n%s", got, kept)
	}
	e.Undo()
	if n := e.Content.Children[1].Children[0]; n.Delta().Plain() != "under" || e.Content.Children[0].Int("level", 0) != 2 {
		t.Errorf("after undo %q, level %d", n.Delta().Plain(), e.Content.Children[0].Int("level", 0))
	}
}

// The steps keep about maxHistory of their own: the oldest go first, and
// the newest stays, whatever it holds.
func TestStepsKeptToTheirSize(t *testing.T) {
	was := maxHistory
	t.Cleanup(func() { maxHistory = was })
	maxHistory = 64 << 10
	e, _, nodes := longPage(t, 3)
	big := strings.Repeat("x", 16<<10)
	for i := range 20 {
		e.Edit(func(c *doc.Content) { c.Children[1].SetDelta(doc.Delta{{Text: fmt.Sprint(i, big)}}) })
	}
	if size := stepsSize(e.undo); size > maxHistory+20<<10 || len(e.undo) >= 20 || len(e.undo) < 2 {
		t.Errorf("%d steps keep %d bytes", len(e.undo), size)
	}
	e.Undo()
	if got := e.Content.Children[1].Delta().Plain(); !strings.HasPrefix(got, "18x") {
		t.Errorf("after undo %.8q", got)
	}
	maxHistory = 1
	e.Edit(func(c *doc.Content) { c.Children[1].SetDelta(doc.Delta{{Text: "last"}}) })
	if len(e.undo) != 1 {
		t.Fatalf("%d steps over the budget, want the newest alone", len(e.undo))
	}
	e.Undo()
	if got := e.Content.Children[1].Delta().Plain(); !strings.HasPrefix(got, "18x") || nodes[0] != e.Content.Children[0] {
		t.Errorf("after the newest undone %.8q", got)
	}
}

// Undo, sync, an agent's ops and menu actions lay out again only the
// blocks they change: the others keep their layouts, though undo and sync
// bring the page anew.
func TestLayoutsOutliveChanges(t *testing.T) {
	e, tt, nodes := longPage(t, 30)
	before := layoutsOf(e, nodes)
	check := func(what string, changed int) {
		t.Helper()
		tt.Frame()
		for i, l := range layoutsOf(e, e.Content.Children) {
			if (l != before[i]) != (i == changed) {
				t.Errorf("%s: block %d laid out again: %v", what, i, l != before[i])
			}
		}
		before = layoutsOf(e, e.Content.Children)
	}
	e.Edit(func(c *doc.Content) { c.Children[3].SetDelta(doc.Delta{{Text: "edited"}}) })
	check("edit", 3)
	e.Undo()
	check("undo", 3)
	e.Redo()
	check("redo", 3)
	c := e.Content.Copy()
	c.Children[5].SetDelta(doc.Delta{{Text: "synced"}})
	e.Replace(c)
	if e.Content.Children[4] != nodes[4] {
		t.Error("sync left a block it did not change another")
	}
	check("sync", 5)
	if err := e.ApplyOp(doc.Op{Op: "replace", ID: nodes[7].ID, Node: newText("paragraph", doc.Delta{{Text: "agent"}})}, true); err != nil {
		t.Fatal(err)
	}
	check("op", 7)
	if err := e.ApplyOps([]doc.Op{{Op: "replace", ID: nodes[9].ID, Node: newText("paragraph", doc.Delta{{Text: "run"}})}}); err != nil {
		t.Fatal(err)
	}
	check("ops", 9)
	e.Undo()
	check("undo after sync", 9)
	if got := e.Content.Children[5].Delta().Plain(); got != "synced" {
		t.Errorf("undo took back what sync brought: %q", got)
	}
}

// A block given its own text again, in another slice, as sync and undo
// make, keeps its layout and look: its text is read, not shaped again.
func TestSameTextKeepsItsLayout(t *testing.T) {
	e, tt, nodes := longPage(t, 3)
	n := nodes[1]
	n.SetDelta(doc.Delta{{Text: "some "}, {Text: "bold", Attrs: doc.Attrs{Bold: true}}})
	tt.Frame()
	l, lk := e.layouts[n.ID], e.looks[n.ID]
	n.SetDelta(doc.Delta{{Text: "some "}, {Text: "bold", Attrs: doc.Attrs{Bold: true}}})
	if e.layoutOf(n) != nil {
		t.Error("a layout of the text before stands for the text now, unread")
	}
	tt.Frame()
	if e.layouts[n.ID] != l || e.looks[n.ID] != lk {
		t.Error("the same text laid out again")
	}
	n.SetDelta(doc.Delta{{Text: "some "}, {Text: "bold", Attrs: doc.Attrs{Italic: true}}})
	tt.Frame()
	if e.layouts[n.ID] == l {
		t.Error("another format kept the layout")
	}
	n.Set("level", 2)
	l = e.layouts[n.ID]
	tt.Frame()
	if e.layouts[n.ID] == l {
		t.Error("other data kept the layout")
	}
}

// Each frame tells what folds hide from the page as it is, though nothing
// says it changed: from what it worked out before while the blocks are
// as they were.
func TestFoldsFollowThePage(t *testing.T) {
	e, tt, nodes := longPage(t, 6)
	tt.Frame()
	hid := e.shaped.hid
	tt.Frame()
	if reflect.ValueOf(e.shaped.hid).Pointer() != reflect.ValueOf(hid).Pointer() {
		t.Error("a frame worked the folds out again with nothing changed")
	}
	// Changed under the editor, which is not told.
	nodes[1].Type = "heading"
	nodes[1].Data["collapsed"] = true
	tt.Frame()
	if !e.hid[nodes[2].ID] || !e.hid[nodes[5].ID] || e.hid[nodes[1].ID] {
		t.Errorf("folded: %v", e.hid)
	}
	nodes[4].Type, nodes[4].Data["level"] = "heading", 1
	tt.Frame()
	if !e.hid[nodes[3].ID] || e.hid[nodes[4].ID] || e.hid[nodes[5].ID] {
		t.Errorf("a heading ending the section: %v", e.hid)
	}
	nodes[0].Children = []*doc.Node{nodes[5]}
	e.Content.Children = slices.Clone(nodes[:5])
	tt.Frame()
	if e.depths[nodes[5].ID] != 1 || e.hid[nodes[5].ID] {
		t.Errorf("moved under the first: depth %d, hidden %v", e.depths[nodes[5].ID], e.hid[nodes[5].ID])
	}
	if !e.HasBlock(nodes[5].ID) {
		t.Error("the block moved is gone")
	}
	e.Edit(func(c *doc.Content) { c.Children[0].Children = nil })
	if e.HasBlock(nodes[5].ID) {
		t.Error("the block deleted is there till the page builds")
	}
}

// Where a block shows is answered from what the page last built, while
// nothing changed since, and from the page as it is after a change.
func TestBlockTopFollowsFolds(t *testing.T) {
	e, tt, nodes := longPage(t, 4)
	e.Edit(func(c *doc.Content) { c.Children[0].Type, c.Children[0].Data["level"] = "heading", 1 })
	tt.Frame()
	if _, ok := e.BlockTop(nodes[2].ID); !ok {
		t.Fatal("a block shown has no place")
	}
	e.ToggleFold(nodes[0].ID)
	if _, ok := e.BlockTop(nodes[2].ID); ok {
		t.Error("a block folded away since the page built shows")
	}
	tt.Frame()
	if _, ok := e.BlockTop(nodes[2].ID); ok {
		t.Error("a block folded away shows")
	}
	if _, ok := e.BlockTop(nodes[0].ID); !ok {
		t.Error("the folded heading has no place")
	}
}

// One step of undo on a long page keeps what it changed, not the page,
// and costs a walk of it, not a copy.
func BenchmarkUndoStepOnLongPage(b *testing.B) {
	e, _, nodes := longPage(b, 6000)
	e.PlaceCaret(nodes[100].ID, 3)
	for b.Loop() {
		e.InsertText("w")
		e.BreakUndo()
	}
}

// Where the outline's headings show, asked of each on a long page, is a
// look-up each.
func BenchmarkBlockTopOfEveryHeading(b *testing.B) {
	e, tt, nodes := longPage(b, 6000)
	var heads []string
	for i := 0; i < len(nodes); i += 10 {
		nodes[i].Type, nodes[i].Data["level"] = "heading", 2
		heads = append(heads, nodes[i].ID)
	}
	tt.Frame()
	for i, id := range heads {
		e.rel[id] = ui.Rect{Y: float32(i * 40), W: 500, H: 30}
	}
	for b.Loop() {
		for _, id := range heads {
			e.BlockTop(id)
		}
	}
}
