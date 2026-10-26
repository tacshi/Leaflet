package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
)

func TestOutlineAndBlockLinks(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	heading := func(id, text string, level int, folded bool) *doc.Node {
		n := &doc.Node{ID: id, Type: "heading"}
		n.SetDelta(doc.Delta{{Text: text}})
		n.Set("level", level)
		n.Set("collapsed", folded)
		return n
	}
	para := &doc.Node{ID: "p1", Type: "paragraph"}
	para.SetDelta(doc.Delta{{Text: "Retry with backoff"}})
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "RFC", Content: doc.FromBlocks([]*doc.Node{
		heading("h1", "Goals", 1, false), heading("h2", "Design", 1, true), para, heading("h3", "Rollout", 2, false),
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.panelOpen, a.panelTab = true, 2
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	for _, h := range []string{"Goals", "Design"} {
		if _, ok := tt.Find(h); !ok {
			t.Errorf("the outline has no %s", h)
		}
	}
	// Rollout is in Design's folded section, out of the page but in the
	// outline; going to it unfolds Design.
	if _, shown := a.page.editor.BlockTop("h3"); shown {
		t.Fatal("Rollout shows in the page while Design is folded")
	}
	if err := tt.Click("Rollout"); err != nil {
		t.Fatal(err)
	}
	ed := a.page.editor
	if ed.Caret().ID != "h3" || ed.Content.Find("h2").Bool("collapsed") || !a.page.tinted["h3"] {
		t.Errorf("caret %v, Design folded %v, tinted %v", ed.Caret(), ed.Content.Find("h2").Bool("collapsed"), a.page.tinted)
	}

	// A link to a block opens its page there.
	other, _ := a.st.CreatePage(store.NewPageOptions{Title: "Other"})
	a.reloadAll()
	a.openPage(other.ID)
	a.openBlock(p.ID, "p1")
	if a.page == nil || a.page.id != p.ID || a.page.editor.Caret().ID != "p1" {
		t.Errorf("open block: page %v caret %v", a.page != nil && a.page.id == p.ID, a.page.editor.Caret())
	}
	if got := (&host{a: a}).BlockLink("p1"); got != doc.BlockLink(p.ID, "p1") {
		t.Errorf("block link %q", got)
	}
}

func TestCurrentHeading(t *testing.T) {
	heads := []*doc.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	tops := map[string]float32{"a": -400, "b": 90, "c": 300}
	top := func(id string) (float32, bool) { y, ok := tops[id]; return y, ok }
	if got := currentHeading(heads, top, 140); got != "b" {
		t.Errorf("current %q", got)
	}
	tops = map[string]float32{"a": 500, "c": 900}
	if got := currentHeading(heads, top, 140); got != "a" {
		t.Errorf("none scrolled past: current %q", got)
	}
}

// Choosing a heading in the outline brings it to the top of the page, and
// the outline then marks it as the one the page is at.
func TestOutlineScrollsHeadingToTop(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	var blocks []*doc.Node
	for i, title := range []string{"Overview", "Products", "Customers"} {
		h := &doc.Node{ID: fmt.Sprintf("h%d", i), Type: "heading"}
		h.SetDelta(doc.Delta{{Text: title}})
		h.Set("level", 2)
		blocks = append(blocks, h)
		for j := 0; j < 20; j++ {
			p := &doc.Node{Type: "paragraph"}
			p.SetDelta(doc.Delta{{Text: fmt.Sprintf("Line %d of %s.", j, title)}})
			blocks = append(blocks, p)
		}
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Cognex", Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.panelOpen, a.panelTab = true, 2
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 700)
	tt.Frame()
	if err := tt.Click("Products"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	ed := a.page.editor
	top, ok := ed.BlockTop("h1")
	if !ok || top-a.page.viewTop < 0 || top-a.page.viewTop > 2*scrollMargin {
		t.Errorf("Products is %v below the top of the page (shown %v)", top-a.page.viewTop, ok)
	}
	if got := currentHeading(ed.Headings(), ed.BlockTop, a.page.viewTop+2*scrollMargin); got != "h1" {
		t.Errorf("current heading %q", got)
	}
}

func TestHeadingTree(t *testing.T) {
	h := func(id string, level int) *doc.Node {
		n := &doc.Node{ID: id, Type: "heading"}
		n.Set("level", level)
		return n
	}
	roots, kids := headingTree([]*doc.Node{h("a", 2), h("b", 3), h("c", 3), h("d", 1), h("e", 3), h("f", 2)})
	if fmt.Sprint(roots) != "[a d]" || fmt.Sprint(kids["a"]) != "[b c]" || fmt.Sprint(kids["d"]) != "[e f]" || kids["b"] != nil {
		t.Errorf("roots %v kids %v", roots, kids)
	}
}
