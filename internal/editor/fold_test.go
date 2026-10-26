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
