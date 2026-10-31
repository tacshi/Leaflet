package editor

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

// foldPage is "# A", a paragraph and a list item with a child under it,
// "## B" with a paragraph, then "# C".
func foldPage(t *testing.T) (*Editor, *ui.Tester, *fakeHost, map[string]*doc.Node) {
	t.Helper()
	// Room on the left for the headings' buttons, as the app's page has.
	host := &fakeHost{}
	e := New(doc.Empty(), host)
	e.SetWidth(440)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20, 20, 20, 80).Children(func() { e.View(c) })
	}, 600, 400)
	if err := tt.Click("Page"); err != nil {
		t.Fatal(err)
	}
	heading := func(text string, level int) *doc.Node {
		n := newText("heading", doc.FromMarkdown(text))
		n.Set("level", level)
		return n
	}
	b := map[string]*doc.Node{
		"A": heading("A", 1), "a1": newText("paragraph", doc.FromMarkdown("a1")),
		"a2": newText("bulleted_list", doc.FromMarkdown("a2")), "kid": newText("bulleted_list", doc.FromMarkdown("kid")),
		"B": heading("B", 2), "b1": newText("paragraph", doc.FromMarkdown("b1")), "C": heading("C", 1),
	}
	b["a2"].Children = []*doc.Node{b["kid"]}
	e.Edit(func(c *doc.Content) {
		c.Children = []*doc.Node{b["A"], b["a1"], b["a2"], b["B"], b["b1"], b["C"]}
	})
	tt.Frame()
	return e, tt, host, b
}

func TestFoldHeadingHidesItsSection(t *testing.T) {
	e, tt, _, b := foldPage(t)
	e.PlaceCaret(b["b1"].ID, 1)
	if err := tt.Click("Fold section"); err != nil { // the first heading's
		t.Fatal(err)
	}
	if !b["A"].Bool("collapsed") {
		t.Fatal("A did not fold")
	}
	if got := texts(e); len(got) != 2 || got[0] != "heading:A" || got[1] != "heading:C" {
		t.Errorf("units %v", got)
	}
	if p := e.Caret(); p.ID != b["A"].ID || p.Off != 1 {
		t.Errorf("the caret stayed in what folded away: %v", p)
	}
	if !tt.HasText("C") || tt.HasText("b1") {
		t.Errorf("shown %v", tt.Texts())
	}
	// Down from A goes past its section.
	tt.Key(0, ui.KeyDown)
	if e.Caret().ID != b["C"].ID {
		t.Errorf("down went to %v", e.Caret())
	}
	// Revealing a block in it unfolds A.
	e.Reveal(b["kid"].ID)
	if b["A"].Bool("collapsed") || len(texts(e)) != 7 {
		t.Errorf("after reveal %v", texts(e))
	}
	// Enter at the end of a folded heading starts its section, which shows.
	e.ToggleFold(b["A"].ID)
	e.PlaceCaret(b["A"].ID, -1)
	tt.Key(0, ui.KeyEnter)
	tt.Type("new")
	if got := texts(e); len(got) != 8 || got[1] != "paragraph:new" || b["A"].Bool("collapsed") {
		t.Errorf("after Enter %v", got)
	}
}

func TestFoldedSectionGoesWithItsHeading(t *testing.T) {
	e, _, _, b := foldPage(t)
	e.ToggleFold(b["B"].ID)
	e.ToggleFold(b["A"].ID)
	e.Select(Pos{b["A"].ID, 0}, Pos{b["C"].ID, 0})
	blocks := e.copyBlocks()
	var copied []string
	for _, n := range blocks {
		copied = append(copied, n.Delta().Plain())
	}
	if len(blocks) != 6 || copied[3] != "B" || len(blocks[2].Children) != 1 {
		t.Errorf("copied %v", copied)
	}
	e.InsertText("x")
	if got := texts(e); len(got) != 1 || got[0] != "heading:xC" {
		t.Errorf("after deleting over the fold %v", got)
	}
}

// A selection from inside a folded heading leaves the heading, and so its
// section: Backspace over it does as Delete at the heading's end does.
func TestSelectionFromInsideAFoldedHeadingKeepsItsSection(t *testing.T) {
	for _, key := range []ui.Key{ui.KeyBackspace, ui.KeyDelete} {
		e, tt, _, b := foldPage(t)
		e.ToggleFold(b["A"].ID)
		e.PlaceCaret(b["A"].ID, -1)
		if key == ui.KeyBackspace {
			tt.Key(ui.Shift, ui.KeyRight)
			if a, z := e.Range(); a != (Pos{b["A"].ID, 1}) || z != (Pos{b["C"].ID, 0}) {
				t.Fatalf("selected %v to %v", a, z)
			}
			// Copying it takes no section either.
			if blocks := e.copyBlocks(); len(blocks) != 2 {
				t.Errorf("copied %d blocks", len(blocks))
			}
		}
		tt.Key(0, key)
		var top []string
		for _, n := range e.Content.Children {
			top = append(top, n.Delta().Plain())
		}
		if !slices.Equal(top, []string{"AC", "a1", "a2", "B", "b1"}) || e.node(b["kid"].ID) == nil {
			t.Errorf("key %v left %v", key, top)
		}
		if !foldedHeading(b["A"]) || len(texts(e)) != 1 {
			t.Errorf("key %v: A folded %v, shown %v", key, foldedHeading(b["A"]), texts(e))
		}
	}
}

// Nothing goes out of sight into a folded section as the heading that
// ended it goes, or becomes another kind of block: the section unfolds.
func TestNothingFoldsAwayAsAHeadingGoes(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(e *Editor, tt *ui.Tester, b map[string]*doc.Node)
		want []string
	}{
		{"Backspace at the start of the heading", func(e *Editor, tt *ui.Tester, b map[string]*doc.Node) {
			e.PlaceCaret(b["C"].ID, 0)
			tt.Key(0, ui.KeyBackspace)
		}, []string{"heading:A", "paragraph:C", "paragraph:c1"}},
		{"a lower level", func(e *Editor, tt *ui.Tester, b map[string]*doc.Node) {
			e.PlaceCaret(b["C"].ID, 0)
			e.SetType("heading", 2)
		}, []string{"heading:A", "heading:C", "paragraph:c1"}},
		{"Delete joining it", func(e *Editor, tt *ui.Tester, b map[string]*doc.Node) {
			e.PlaceCaret(b["A"].ID, -1)
			tt.Key(0, ui.KeyDelete)
		}, []string{"heading:AC", "paragraph:c1"}},
		{"a selection taking it", func(e *Editor, tt *ui.Tester, b map[string]*doc.Node) {
			e.Select(Pos{b["A"].ID, 1}, Pos{b["C"].ID, 1})
			tt.Key(0, ui.KeyBackspace)
		}, []string{"heading:A", "paragraph:c1"}},
	} {
		e, tt, _, b := foldPage(t)
		c1 := para(doc.NewBlockID(), "c1")
		e.Edit(func(c *doc.Content) { c.Children = append(c.Children, c1) })
		e.ToggleFold(b["A"].ID)
		tc.edit(e, tt, b)
		got := texts(e)
		if foldedHeading(b["A"]) || len(got) < len(tc.want) || !slices.Equal(got[len(got)-len(tc.want)+1:], tc.want[1:]) {
			t.Errorf("%s: A folded %v, shown %v", tc.name, foldedHeading(b["A"]), got)
		}
		if got[0] != tc.want[0] {
			t.Errorf("%s: first %v", tc.name, got)
		}
		// Undo folds it again, as it was.
		e.Undo()
		if !foldedHeading(e.node(b["A"].ID)) || len(texts(e)) != 3 {
			t.Errorf("%s: undo left A unfolded", tc.name)
		}
	}
}

// Nothing goes in out of sight either: blocks put before the heading that
// ends a folded section, or after a folded heading, would join the
// section, which unfolds.
func TestNothingFoldsAwayAsBlocksGoIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(e *Editor, tt *ui.Tester, b map[string]*doc.Node)
		want string
	}{
		{"a table pasted at the start of the heading", func(e *Editor, tt *ui.Tester, b map[string]*doc.Node) {
			e.PlaceCaret(b["C"].ID, 0)
			tbl, _ := doc.TableNode([][]string{{"h1", "h2"}, {"1", "2"}}, "")
			e.pasteBlocks([]*doc.Node{tbl})
		}, "table:"},
		{"a list pasted at the start of the heading", func(e *Editor, tt *ui.Tester, b map[string]*doc.Node) {
			e.PlaceCaret(b["C"].ID, 0)
			e.paste("- x\n- y")
		}, "bulleted_list:x"},
		{"Enter at the start of the heading", func(e *Editor, tt *ui.Tester, b map[string]*doc.Node) {
			e.PlaceCaret(b["C"].ID, 0)
			tt.Key(0, ui.KeyEnter)
		}, "paragraph:"},
		{"a block put after the folded heading", func(e *Editor, tt *ui.Tester, b map[string]*doc.Node) {
			e.PlaceCaret(b["A"].ID, -1)
			e.InsertBlocks(&doc.Node{ID: doc.NewBlockID(), Type: "divider"})
		}, "divider:"},
	} {
		e, tt, _, b := foldPage(t)
		e.ToggleFold(b["A"].ID)
		tc.edit(e, tt, b)
		tt.Frame()
		if foldedHeading(b["A"]) || len(e.hidden()) > 0 || !slices.Contains(texts(e), tc.want) {
			t.Errorf("%s: A folded %v, shown %v", tc.name, foldedHeading(b["A"]), texts(e))
		}
		if e.Caret().ID == b["A"].ID {
			t.Errorf("%s: the caret went to A", tc.name)
		}
	}
}

func TestBlockLinks(t *testing.T) {
	e, tt, host, b := foldPage(t)
	if err := tt.Click("Copy link to heading"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != doc.BlockLink("this", b["A"].ID) {
		t.Errorf("clipboard %q", got)
	}
	tt.Frame()
	if _, ok := tt.Find("Copied"); !ok {
		t.Error("the button does not say it copied")
	}
	// A link to a block opens the page there.
	b["a1"].SetDelta(doc.Delta{{Text: "see B", Attrs: doc.Attrs{Href: doc.BlockLink("prd", "b9")}}})
	e.invalidate(b["a1"].ID)
	tt.Frame()
	r := e.geom[b["a1"].ID]
	tt.ClickAt(r.X+5, r.Y+r.H/2)
	if len(host.opened) != 1 || host.opened[0] != "prd#b9" {
		t.Errorf("opened %v", host.opened)
	}
	// Right-click a block for a link to it.
	r = e.geom[b["b1"].ID]
	tt.RightClickAt(r.X+5, r.Y+r.H/2)
	if err := chooseDrawn(tt, "Copy link to block"); err != nil {
		t.Fatalf("%v; shown %v", err, tt.Texts())
	}
	if got := tt.Clipboard(); got != doc.BlockLink("this", b["b1"].ID) {
		t.Errorf("clipboard %q", got)
	}
}

func para(id, text string) *doc.Node {
	n := &doc.Node{ID: id, Type: "paragraph"}
	n.SetDelta(doc.Delta{{Text: text}})
	return n
}

// A change from outside can fold away the block the caret is in; the caret
// then goes to the block that folds it, and editing there is safe.
func TestCaretLeavesWhatAChangeFolds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fold   string
		anchor Pos
		focus  Pos
		edit   func(e *Editor)
		want   Pos
	}{
		{"backspace over toggle child", "T", Pos{"K", 1}, Pos{"Y", 2}, func(e *Editor) { e.Backspace(false) }, Pos{"T", 6}},
		{"delete at end of toggle child", "T", Pos{"K", 3}, Pos{"K", 3}, func(e *Editor) { e.Delete(false) }, Pos{"T", 6}},
		{"type in toggle child", "T", Pos{"K", 3}, Pos{"K", 3}, func(e *Editor) { e.InsertText("!") }, Pos{"T", 7}},
		{"copy from folded section", "H", Pos{"S", 1}, Pos{"Y", 1}, func(e *Editor) { e.copyText() }, Pos{"H", 7}},
		{"type in folded section", "H", Pos{"S", 1}, Pos{"S", 1}, func(e *Editor) { e.InsertText("!") }, Pos{"H", 8}},
	} {
		tg := &doc.Node{ID: "T", Type: "toggle_list", Children: []*doc.Node{para("K", "kid")}}
		tg.SetDelta(doc.Delta{{Text: "toggle"}})
		h := &doc.Node{ID: "H", Type: "heading", Data: map[string]any{"level": 2}}
		h.SetDelta(doc.Delta{{Text: "heading"}})
		c := doc.FromBlocks([]*doc.Node{para("F", "first"), tg, h, para("S", "section"), para("Y", "after")})
		for _, apply := range []string{"Replace", "ApplyOps", "ApplyOp"} {
			e := New(c.Copy(), &fakeHost{})
			e.Select(tc.anchor, tc.focus)
			next := e.Content.Copy()
			folded := next.Find(tc.fold)
			folded.Set("collapsed", true)
			op := e.Content.ReplaceOp(tc.fold, folded.Clone())
			switch apply {
			case "Replace":
				e.Replace(next)
			case "ApplyOps":
				_ = e.ApplyOps([]doc.Op{op})
			case "ApplyOp":
				_ = e.ApplyOp(op, true)
			}
			tc.edit(e)
			if got := e.Caret(); got != tc.want {
				t.Errorf("%s after %s: caret %v, want %v", tc.name, apply, got, tc.want)
			}
			if got := e.node("F").Delta().Plain(); got != "first" {
				t.Errorf("%s after %s: first block is %q", tc.name, apply, got)
			}
		}
	}
}

// Undo steps last over content that comes in from outside: going back one
// takes back what it changed, and the change from outside stays. A block
// the step deleted comes back, one it made goes, and redo goes forward
// again over the change.
func TestReplaceKeepsUndo(t *testing.T) {
	e := New(doc.FromBlocks([]*doc.Node{para("A", "a"), para("B", "b"), para("X", "x")}), &fakeHost{})
	e.Select(Pos{"A", 1}, Pos{"A", 1})
	e.InsertText("1")
	e.Select(Pos{"X", 0}, Pos{"X", 1})
	e.Backspace(false)
	e.Backspace(false) // X joins B
	e.Enter()
	e.InsertText("new")
	outside := func(fn func(c *doc.Content)) {
		next := e.Content.Copy()
		fn(next)
		e.Replace(next)
	}
	outside(func(c *doc.Content) {
		c.Find("B").SetDelta(doc.Delta{{Text: "B from outside"}})
		c.Children = append(c.Children, para("Q", "from outside"))
	})
	if !e.CanUndo() {
		t.Fatal("undo steps went")
	}
	got := func() string { return strings.Join(texts(e), " | ") }
	e.Undo() // the typing
	e.Undo() // the Enter
	if want := "paragraph:a1 | paragraph:B from outside | paragraph:from outside"; got() != want {
		t.Errorf("after undoing the new line %q, want %q", got(), want)
	}
	outside(func(c *doc.Content) { c.Find("Q").SetDelta(doc.Delta{{Text: "again"}}) })
	e.Undo() // X joining B, which brings X back after B, as Q is
	e.Undo() // emptying X
	e.Undo() // the 1
	if want := "paragraph:a | paragraph:B from outside | paragraph:again | paragraph:x"; got() != want {
		t.Errorf("after undoing all %q, want %q", got(), want)
	}
	if e.CanUndo() {
		t.Error("steps left")
	}
	e.Redo()
	if want := "paragraph:a1 | paragraph:B from outside | paragraph:again | paragraph:x"; got() != want {
		t.Errorf("after redo %q, want %q", got(), want)
	}
}

// A change from outside to other blocks keeps a composition: MyGo never
// tells the input method it ended, so it composes on, and its next update
// replaces it as before. A change to its block gives it up and takes its
// text out, leaving none behind.
func TestChangesFromOutsideKeepComposition(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("a")
	tt.Compose("ni", 2)
	_ = e.ApplyOps([]doc.Op{doc.InsertOp(doc.Position{At: "end"}, para("X", "agent"))})
	if !e.marked {
		t.Error("ApplyOps ended the composition")
	}
	tt.Compose("nih", 3)
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("Y", "more")), true)
	tt.Compose("niha", 4)
	e.Replace(e.Content.Copy())
	tt.Compose("nihao", 5)
	tt.Type("你好")
	want := []string{"paragraph:a你好", "paragraph:agent", "paragraph:more"}
	if got := texts(e); !slices.Equal(got, want) {
		t.Fatalf("composing on over changes elsewhere %v, want %v", got, want)
	}
	// The composition and what it became are a step of their own, and the
	// changes' steps never hold what it was as it went.
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "paragraph:agent", "paragraph:more"}) {
		t.Errorf("after undoing the composition %v", got)
	}
	e.Undo()
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a"}) {
		t.Errorf("after undoing the changes %v", got)
	}

	// A change to the composition's block gives it up.
	id := e.Content.Children[0].ID
	e.PlaceCaret(id, -1)
	tt.Compose("ni", 2)
	repl := para("", "b")
	_ = e.ApplyOp(e.Content.ReplaceOp(id, repl), true)
	if e.marked {
		t.Error("a change to its block kept the composition")
	}
	e.PlaceCaret(id, -1)
	tt.Compose("nih", 3)
	tt.Type("你好")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:b你好"}) {
		t.Errorf("composing on after a change to its block %v", got)
	}
}

// foldedToggle is a folded toggle with a child hidden in it.
func foldedToggle(id, text, kid string) *doc.Node {
	n := &doc.Node{ID: id, Type: "toggle_list", Children: []*doc.Node{para(id+"K", kid)}}
	n.SetDelta(doc.Delta{{Text: text}})
	n.Set("collapsed", true)
	return n
}

// count counts the blocks of a page with a text.
func count(c *doc.Content, text string) int {
	k := 0
	c.Walk(func(n, _ *doc.Node) {
		if n.Delta().Plain() == text {
			k++
		}
	})
	return k
}

// Cutting and pasting back a folded toggle first or last in the
// selection leaves one of its hidden children: cutting keeps them in the
// page, so copying leaves them out.
func TestCutPasteFoldedToggleKeepsOneOfItsChildren(t *testing.T) {
	for _, tc := range []struct {
		name     string
		blocks   func() []*doc.Node
		from, to Pos
	}{
		{"last", func() []*doc.Node {
			return []*doc.Node{para("A", "aaa"), foldedToggle("T", "Toggle", "hidden"), para("B", "bbb")}
		}, Pos{"A", 1}, Pos{"T", 3}},
		{"first", func() []*doc.Node {
			return []*doc.Node{foldedToggle("T", "Toggle", "hidden"), para("B", "bbb")}
		}, Pos{"T", 3}, Pos{"B", 1}},
	} {
		e := New(doc.FromBlocks(tc.blocks()), &fakeHost{})
		e.Select(tc.from, tc.to)
		text := e.copyText()
		clip.text, clip.blocks, clip.delta = text, e.copyBlocks(), nil
		e.Backspace(false)
		e.PlaceCaret("B", -1)
		e.paste(text)
		if k := count(e.Content, "hidden"); k != 1 {
			t.Errorf("%s: %d hidden children after cut and paste, want 1", tc.name, k)
		}
	}
}

// Shift-Tab on a folded toggle nested in a list item unfolds it, as the
// siblings after it become its children, which would be hidden in it.
func TestOutdentFoldedToggleShowsSiblings(t *testing.T) {
	item := func(id, text string, kids ...*doc.Node) *doc.Node {
		n := &doc.Node{ID: id, Type: "bulleted_list", Children: kids}
		n.SetDelta(doc.Delta{{Text: text}})
		return n
	}
	e := New(doc.FromBlocks([]*doc.Node{item("L", "parent", foldedToggle("T", "Toggle", "kid"), item("S", "sibling"))}), &fakeHost{})
	e.PlaceCaret("T", 0)
	e.Outdent()
	want := []string{"bulleted_list:parent", "toggle_list:Toggle", "paragraph:kid", "bulleted_list:sibling"}
	if got := texts(e); !slices.Equal(got, want) {
		t.Errorf("after Shift-Tab %v, want %v", got, want)
	}
	// Without siblings after it, it stays folded.
	e = New(doc.FromBlocks([]*doc.Node{item("L", "parent", foldedToggle("T", "Toggle", "kid"))}), &fakeHost{})
	e.PlaceCaret("T", 0)
	e.Outdent()
	if !collapsed(e.node("T")) {
		t.Error("Shift-Tab unfolded a toggle that took no siblings")
	}
}

// Pasting blocks that end in a folded toggle into the middle of a line
// puts the rest of the line, and the caret, where they show: on a line
// after the toggle, never among its hidden children.
func TestPasteEndingInFoldedToggleShowsTheRest(t *testing.T) {
	kid := para("C", "child")
	b := para("B", "abcdef")
	b.Type, b.Children = "bulleted_list", []*doc.Node{kid}
	e := New(doc.FromBlocks([]*doc.Node{b}), &fakeHost{})
	e.PlaceCaret("B", 3)
	e.pasteBlocks(freshBlocks([]*doc.Node{para("P", "xyz"), foldedToggle("T", "Toggle", "hidden")}))
	shown := texts(e)
	if !slices.Contains(shown, "bulleted_list:def") || !slices.Contains(shown, "paragraph:child") {
		t.Errorf("shown %v; the rest of the line or its child is hidden", shown)
	}
	if c := e.Caret(); e.node(c.ID).Delta().Plain() != "def" || c.Off != 0 {
		t.Errorf("caret %v in %q", c, e.node(c.ID).Delta().Plain())
	}
	if k := count(e.Content, "hidden"); k != 1 {
		t.Errorf("%d hidden children, want 1", k)
	}
}
