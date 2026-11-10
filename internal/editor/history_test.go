package editor

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

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

func layoutsOf(e *Editor, nodes []*doc.Node) []*blockLayout {
	var out []*blockLayout
	for _, n := range nodes {
		out = append(out, e.layouts[n.ID])
	}
	return out
}

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

func TestStepsKeepThePageAsItWas(t *testing.T) {
	e, _, nodes := longPage(t, 5)
	nodes[1].Children = []*doc.Node{newText("paragraph", doc.Delta{{Text: "under"}})}
	e.Edit(func(c *doc.Content) { c.Children[0].Set("level", 2) })
	kept := e.undo[len(e.undo)-1].content.Encode()
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

func TestFoldsFollowThePage(t *testing.T) {
	e, tt, nodes := longPage(t, 6)
	tt.Frame()
	hid := e.shaped.hid
	tt.Frame()
	if reflect.ValueOf(e.shaped.hid).Pointer() != reflect.ValueOf(hid).Pointer() {
		t.Error("a frame worked the folds out again with nothing changed")
	}
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

func shapeOf(c *doc.Content) string {
	var b strings.Builder
	c.Walk(func(n, parent *doc.Node) {
		fmt.Fprintf(&b, "%s<%v %s %v|", n.ID, parent != nil, n.Type, n.Data)
	})
	return b.String()
}

func wholePage(t *testing.T, e *Editor, after string) {
	t.Helper()
	if len(e.Content.Children) == 0 {
		t.Fatalf("after %s: no blocks", after)
	}
	seen := map[string]bool{}
	e.Content.Walk(func(n, _ *doc.Node) {
		if seen[n.ID] {
			t.Fatalf("after %s: two blocks %s", after, n.ID)
		}
		seen[n.ID] = true
	})
	for _, p := range []Pos{e.anchor, e.focus} {
		if n := e.node(p.ID); n == nil || p.Off < 0 || p.Off > textLen(n) {
			t.Fatalf("after %s: the selection at %+v", after, p)
		}
	}
	for id, l := range e.layouts {
		if n := e.node(id); n != nil && l.text == textIDOf(n) && !sameRuns(l.runs, n.Delta()) {
			t.Fatalf("after %s: %s laid out as text it no longer holds", after, id)
		}
	}
}

func TestRandomEditsKeepThePageWhole(t *testing.T) {
	words := []string{"a", "hé", "👍", "👨‍👩‍👧", "日本", "**b**", "x ", " ", "/", "@", "# ", "- ", "1. ", "[] ", "> ", "```", "---", "*i*", "`c`", "é"}
	pastes := []string{"one\ntwo", "- a\n  - b\n- c", "| a | b |\n| - | - |\n| 1 | 2 |", "```\ncode\n```\nafter", "# h\npara", "x"}
	kinds := []string{"heading", "bulleted_list", "todo_list", "toggle_list", "code", "quote", "paragraph"}
	for seed := range uint64(24) {
		e, tt, _ := setup(t)
		rng := rand.New(rand.NewPCG(seed, 7)) // #nosec G404
		pick := func(n int) int { return rng.IntN(n) }
		var done []string
		anyBlock := func() *doc.Node {
			var all []*doc.Node
			e.Content.Walk(func(n, _ *doc.Node) {
				if n.Type != "table/cell" {
					all = append(all, n)
				}
			})
			return all[pick(len(all))]
		}
		for range 120 {
			var op string
			switch pick(24) {
			case 0, 1, 2:
				w := words[pick(len(words))]
				op = "typing " + w
				tt.Type(w)
			case 3:
				op = "Enter"
				tt.Key(0, ui.KeyEnter)
			case 4:
				op = "Backspace"
				tt.Key(0, ui.KeyBackspace)
			case 5:
				op = "Delete"
				tt.Key(0, ui.KeyDelete)
			case 6:
				us := e.withSections()
				a, b := us[pick(len(us))], us[pick(len(us))]
				from, to := Pos{a.ID, pick(textLen(a) + 1)}, Pos{b.ID, pick(textLen(b) + 1)}
				op = fmt.Sprintf("selecting %v to %v", from, to)
				e.Select(from, to)
			case 7:
				op = "bold"
				e.ToggleBold()
			case 8:
				op = "indenting"
				e.Indent()
			case 9:
				op = "outdenting"
				e.Outdent()
			case 10:
				kind := kinds[pick(len(kinds))]
				op = "turning into " + kind
				e.SetType(kind, 1+pick(3))
			case 11:
				text := pastes[pick(len(pastes))]
				op = "pasting " + text
				e.paste(text)
			case 12:
				op = "cutting"
				tt.Command("copy")
				tt.Command("cut")
			case 13:
				op = "pasting the clipboard"
				tt.Command("paste")
			case 14, 15:
				op = "undo"
				e.Undo()
			case 16:
				op = "redo"
				e.Redo()
			case 17:
				op = "folding"
				if hs := e.Headings(); len(hs) > 0 {
					e.ToggleFold(hs[pick(len(hs))].ID)
				}
				for _, n := range e.withSections() {
					if n.Type == "toggle_list" {
						e.ToggleCollapsed(n.ID)
						break
					}
				}
			case 18:
				next := e.Content.Copy()
				var all []*doc.Node
				next.Walk(func(n, _ *doc.Node) {
					if n.Type != "table/cell" {
						all = append(all, n)
					}
				})
				n := all[pick(len(all))]
				switch pick(3) {
				case 0:
					if n.IsText() {
						n.SetDelta(doc.Delta{{Text: "from outside"}})
					}
				case 1:
					next.Remove(n.ID)
				case 2:
					_ = next.Insert(doc.Position{After: n.ID}, newText("paragraph", doc.Delta{{Text: "new"}}))
				}
				op = "a change from outside"
				e.Replace(next)
			case 19:
				op = "an agent's insert"
				_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, newText("paragraph", doc.Delta{{Text: "agent"}})), pick(2) == 0)
			case 20:
				n := anyBlock()
				op = "an agent's delete of " + n.ID
				_ = e.ApplyOp(doc.Op{Op: "delete", IDs: []string{n.ID}}, pick(2) == 0)
			case 21:
				n := anyBlock()
				op = "an agent's replace of " + n.ID
				_ = e.ApplyOps([]doc.Op{e.Content.ReplaceOp(n.ID, newText("heading", doc.Delta{{Text: "agent"}}))})
			case 22:
				op = "composing"
				tt.Compose("ni", 2)
				if pick(2) == 0 {
					_ = e.Doc()
				}
				switch pick(3) {
				case 0:
					tt.Type("你好")
				case 1:
					e.UnmarkText()
				}
			case 23:
				op = "arrows"
				keys := []ui.Key{ui.KeyLeft, ui.KeyRight, ui.KeyUp, ui.KeyDown}
				mods := []ui.Modifiers{0, ui.Shift, ui.Alt, ui.Cmd, ui.Shift | ui.Alt}
				for range 1 + pick(3) {
					tt.Key(mods[pick(len(mods))], keys[pick(len(keys))])
				}
			}
			done = append(done, op)
			tt.Frame()
			wholePage(t, e, fmt.Sprintf("seed %d: %s", seed, strings.Join(done, ", ")))
		}
		pages := []string{shapeOf(e.Content)}
		for len(e.undo) > 0 {
			kept := len(e.redo)
			e.Undo()
			tt.Frame()
			wholePage(t, e, fmt.Sprintf("seed %d: undo", seed))
			if len(e.redo) > kept {
				pages = append(pages, shapeOf(e.Content))
			}
		}
		for i := len(pages) - 2; i >= 0; i-- {
			e.Redo()
			tt.Frame()
			if got := shapeOf(e.Content); got != pages[i] {
				t.Fatalf("seed %d: redo %d of %d made %s, want %s", seed, len(pages)-1-i, len(pages)-1, got, pages[i])
			}
		}
	}
}

func BenchmarkUndoStepOnLongPage(b *testing.B) {
	e, _, nodes := longPage(b, 6000)
	e.PlaceCaret(nodes[100].ID, 3)
	for b.Loop() {
		e.InsertText("w")
		e.BreakUndo()
	}
}

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
