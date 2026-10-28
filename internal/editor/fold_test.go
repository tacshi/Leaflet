package editor

import (
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
	if err := tt.ChooseMenuItem("Copy link to block"); err != nil {
		t.Fatalf("%v; menu %v", err, tt.Menu())
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

// Undo steps are whole pages, so after content comes in from outside they
// would undo it too; they go.
func TestReplaceDropsUndo(t *testing.T) {
	e := New(doc.FromBlocks([]*doc.Node{para("A", "a"), para("B", "b")}), &fakeHost{})
	e.Select(Pos{"A", 1}, Pos{"A", 1})
	e.InsertText("x")
	next := e.Content.Copy()
	next.Children = append(next.Children, para("Q", "from outside"))
	e.Replace(next)
	if e.CanUndo() || e.CanRedo() {
		t.Error("undo steps kept")
	}
	e.Undo()
	if e.node("Q") == nil || e.node("A").Delta().Plain() != "ax" {
		t.Errorf("undo reverted the change from outside: %v", texts(e))
	}
}

// An agent's ops end a composition, as content from outside does.
func TestOpsEndComposition(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Compose("ni", 2)
	if !e.marked {
		t.Fatal("not composing")
	}
	id := e.Content.Children[0].ID
	_ = e.ApplyOps([]doc.Op{doc.InsertOp(doc.Position{After: id}, para("", "agent"))})
	if e.marked {
		t.Error("ApplyOps kept the composition")
	}
	tt.Compose("hao", 3)
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("", "more")), true)
	if e.marked {
		t.Error("ApplyOp kept the composition")
	}
}
