package editor

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

func foldPage(t *testing.T) (*Editor, *ui.Tester, *fakeHost, map[string]*doc.Node) {
	t.Helper()
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
	if err := tt.Click("Fold section"); err != nil {
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
	tt.Key(0, ui.KeyDown)
	if e.Caret().ID != b["C"].ID {
		t.Errorf("down went to %v", e.Caret())
	}
	e.Reveal(b["kid"].ID)
	if b["A"].Bool("collapsed") || len(texts(e)) != 7 {
		t.Errorf("after reveal %v", texts(e))
	}
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

func TestSelectionToAFoldedHeadingsEndTakesItsSection(t *testing.T) {
	e, tt, _, b := foldPage(t)
	e.ToggleFold(b["C"].ID)
	e.ToggleFold(b["B"].ID)
	e.Select(Pos{b["a1"].ID, 0}, Pos{b["B"].ID, 1})
	if blocks := e.copyBlocks(); len(blocks) != 4 || blocks[3].Delta().Plain() != "b1" {
		var copied []string
		for _, n := range blocks {
			copied = append(copied, n.Delta().Plain())
		}
		t.Errorf("copied %v", copied)
	}
	if text := e.copyText(); !strings.Contains(text, "b1") {
		t.Errorf("copied as text %q", text)
	}
	tt.Key(0, ui.KeyBackspace)
	var top []string
	for _, n := range e.Content.Children {
		top = append(top, n.Delta().Plain())
	}
	if !slices.Equal(top, []string{"A", "", "C"}) {
		t.Errorf("after deleting to B's end %v", top)
	}

	e, tt, _, b = foldPage(t)
	e.ToggleFold(b["B"].ID)
	e.Edit(func(c *doc.Content) { c.Remove(b["C"].ID) })
	tt.Frame()
	e.PlaceCaret(b["a1"].ID, 0)
	e.SelectAll()
	e.SelectAll()
	tt.Key(0, ui.KeyBackspace)
	if got := e.Content.Children; len(got) != 1 || got[0].Delta().Plain() != "" {
		var left []string
		for _, n := range got {
			left = append(left, n.Delta().Plain())
		}
		t.Errorf("after Select All and Backspace %v", left)
	}
}

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
		e.Undo()
		if !foldedHeading(e.node(b["A"].ID)) || len(texts(e)) != 3 {
			t.Errorf("%s: undo left A unfolded", tc.name)
		}
	}
}

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
	b["a1"].SetDelta(doc.Delta{{Text: "see B", Attrs: doc.Attrs{Href: doc.BlockLink("prd", "b9")}}})
	e.invalidate(b["a1"].ID)
	tt.Frame()
	r := boxOf(e, b["a1"].ID)
	tt.ClickAt(r.X+5, r.Y+r.H/2)
	if len(host.opened) != 1 || host.opened[0] != "prd#b9" {
		t.Errorf("opened %v", host.opened)
	}
	tt.Advance(time.Second)
	b["a1"].SetDelta(doc.Delta{{Text: "see 1:23", Attrs: doc.Attrs{Href: doc.MomentLink("prd", "v9", 83)}}})
	e.invalidate(b["a1"].ID)
	tt.Frame()
	r = boxOf(e, b["a1"].ID)
	tt.ClickAt(r.X+5, r.Y+r.H/2)
	if len(host.opened) != 2 || host.opened[1] != "prd#v9@83" {
		t.Errorf("opened %v", host.opened)
	}
	r = boxOf(e, b["b1"].ID)
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

func TestReplaceKeepsUndo(t *testing.T) {
	e := New(doc.FromBlocks([]*doc.Node{para("A", "a"), para("B", "b"), para("X", "x")}), &fakeHost{})
	e.Select(Pos{"A", 1}, Pos{"A", 1})
	e.InsertText("1")
	e.Select(Pos{"X", 0}, Pos{"X", 1})
	e.Backspace(false)
	e.Backspace(false)
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
	if len(e.undo) == 0 {
		t.Fatal("undo steps went")
	}
	got := func() string { return strings.Join(texts(e), " | ") }
	e.Undo()
	e.Undo()
	if want := "paragraph:a1 | paragraph:B from outside | paragraph:from outside"; got() != want {
		t.Errorf("after undoing the new line %q, want %q", got(), want)
	}
	outside(func(c *doc.Content) { c.Find("Q").SetDelta(doc.Delta{{Text: "again"}}) })
	e.Undo()
	e.Undo()
	e.Undo()
	if want := "paragraph:a | paragraph:B from outside | paragraph:again | paragraph:x"; got() != want {
		t.Errorf("after undoing all %q, want %q", got(), want)
	}
	if len(e.undo) > 0 {
		t.Error("steps left")
	}
	e.Redo()
	if want := "paragraph:a1 | paragraph:B from outside | paragraph:again | paragraph:x"; got() != want {
		t.Errorf("after redo %q, want %q", got(), want)
	}
}

func TestUndoPastAChangeFromOutsideKeepsABlock(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("hello")
	tt.Key(0, ui.KeyEnter)
	tt.Type("world")
	next := e.Content.Copy()
	next.Remove(next.Children[0].ID)
	e.Replace(next)
	e.Undo()
	e.Undo()
	tt.Frame()
	if len(e.Content.Children) != 1 || e.node(e.focus.ID) == nil || e.anchor != e.focus {
		t.Fatalf("after undo the page is %v, the caret at %+v", texts(e), e.focus)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyDown)
	tt.Key(ui.Cmd, ui.KeyUp)
	tt.Type("x")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:x"}) {
		t.Errorf("typing after undo %v", got)
	}
}

func TestChangeToTheCompositionsBlockKeepsItsOwnText(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("hello ")
	id := e.Content.Children[0].ID
	tt.Compose("wo", 2)
	earlier, _ := e.Composition()
	_ = e.ApplyOp(e.Content.ReplaceOp(id, para("", "hello world")), true)
	tt.Type("我")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:hello 我world"}) {
		t.Errorf("composing over an agent's rewrite of the line %v", got)
	}

	e.Edit(func(c *doc.Content) { c.Find(id).SetDelta(doc.Delta{{Text: "hello "}}) })
	e.PlaceCaret(id, -1)
	tt.Compose("wo", 2)
	_ = e.Doc()
	_ = e.ApplyOpFrom(e.Content.ReplaceOp(id, para("", "hello world")), true, earlier)
	tt.Type("我")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:hello 我world"}) {
		t.Errorf("composing over an agent's rewrite of the line, saved since %v", got)
	}

	e.PlaceCaret(id, -1)
	tt.Compose("ni", 2)
	read, _ := e.Composition()
	_ = e.ApplyOpFrom(e.Content.ReplaceOp(id, para("", "hello 我worldni.")), true, read)
	tt.Compose("nih", 3)
	tt.Type("你")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:hello 我world你."}) {
		t.Errorf("composing on over an op that held the composition %v", got)
	}

	e.PlaceCaret(id, -1)
	tt.Compose("ni", 2)
	_ = e.Doc()
	next := e.Content.Copy()
	next.Find(id).SetDelta(doc.Delta{{Text: "hello 我world你.ni!"}})
	e.Replace(next)
	tt.Compose("nih", 3)
	tt.Type("你")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:hello 我world你.你!"}) {
		t.Errorf("composing on over a change that held the composition %v", got)
	}

	e.PlaceCaret(id, -1)
	tt.Compose("!", 1)
	next = e.Content.Copy()
	next.Find(id).SetDelta(doc.Delta{{Text: "hello 我world你.你!!."}})
	e.Replace(next)
	tt.Type("？")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:hello 我world你.你!？!."}) {
		t.Errorf("composing over a change made without the composition %v", got)
	}
}

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
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "paragraph:agent", "paragraph:more"}) {
		t.Errorf("after undoing the composition %v", got)
	}
	e.Undo()
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a"}) {
		t.Errorf("after undoing the changes %v", got)
	}

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

func foldedToggle(id, text, kid string) *doc.Node {
	n := &doc.Node{ID: id, Type: "toggle_list", Children: []*doc.Node{para(id+"K", kid)}}
	n.SetDelta(doc.Delta{{Text: text}})
	n.Set("collapsed", true)
	return n
}

func count(c *doc.Content, text string) int {
	k := 0
	c.Walk(func(n, _ *doc.Node) {
		if n.Delta().Plain() == text {
			k++
		}
	})
	return k
}

func TestCutPasteFoldedToggleKeepsOneOfItsChildren(t *testing.T) {
	freshClipboard(t)
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
	e = New(doc.FromBlocks([]*doc.Node{item("L", "parent", foldedToggle("T", "Toggle", "kid"))}), &fakeHost{})
	e.PlaceCaret("T", 0)
	e.Outdent()
	if !collapsed(e.node("T")) {
		t.Error("Shift-Tab unfolded a toggle that took no siblings")
	}
}

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

func TestIndentSelection(t *testing.T) {
	item := func(id, text string, kids ...*doc.Node) *doc.Node {
		n := &doc.Node{ID: id, Type: "bulleted_list", Children: kids}
		n.SetDelta(doc.Delta{{Text: text}})
		return n
	}
	shape := func(e *Editor) string {
		var b strings.Builder
		var visit func([]*doc.Node, string)
		visit = func(nodes []*doc.Node, in string) {
			for _, n := range nodes {
				b.WriteString(in + n.ID + " ")
				visit(n.Children, in+">")
			}
		}
		visit(e.Content.Children, "")
		return strings.TrimSpace(b.String())
	}
	e := New(doc.FromBlocks([]*doc.Node{item("A", "a"), item("B", "b", item("B1", "b1")), item("C", "c")}), &fakeHost{})
	e.Select(Pos{"B", 0}, Pos{"C", 1})
	e.Indent()
	if got := shape(e); got != "A >B >>B1 >C" {
		t.Errorf("after Tab %q", got)
	}
	e.Outdent()
	if got := shape(e); got != "A B >B1 C" {
		t.Errorf("after Shift-Tab %q", got)
	}
	e.Select(Pos{"A", 0}, Pos{"B", 1})
	e.Indent()
	if got := shape(e); got != "A B >B1 C" {
		t.Errorf("Tab with the first at the top %q", got)
	}
}

func TestIndentNotIntoAHiddenToggle(t *testing.T) {
	heading := func(id, text string) *doc.Node {
		n := &doc.Node{ID: id, Type: "heading", Data: map[string]any{"level": 2}}
		n.SetDelta(doc.Delta{{Text: text}})
		return n
	}
	one := heading("H1", "One")
	one.Set("collapsed", true)
	e := New(doc.FromBlocks([]*doc.Node{one, foldedToggle("T", "Toggle", "kid"), heading("H2", "Two")}), &fakeHost{})
	if hid := e.hidden(); !hid["T"] || hid["H2"] {
		t.Fatalf("hidden %v", hid)
	}
	e.PlaceCaret("H2", 0)
	e.Indent()
	if loc := e.Content.Locate("H2"); loc == nil || loc.Parent != nil {
		t.Error("Tab put a heading in a toggle its fold hides")
	}
}

func TestReadOnlyUnfoldsAHeading(t *testing.T) {
	e, tt, _, b := foldPage(t)
	b["B"].Set("collapsed", true)
	e.ReadOnly = true
	tt.Frame()
	if !e.hidden()[b["b1"].ID] {
		t.Fatal("the section isn't folded")
	}
	if err := tt.Click("Unfold section"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if e.hidden()[b["b1"].ID] {
		t.Error("the section stayed folded")
	}
}
