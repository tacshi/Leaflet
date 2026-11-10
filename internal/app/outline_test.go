package app

import (
	"fmt"
	"strings"
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
	var ctx *ui.Context
	tt := ui.NewTester(func(c *ui.Context) {
		ctx = c
		a.view(c)
	}, 1300, 700)
	tt.Frame()
	if err := tt.Click("Products"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	ed := a.page.editor
	top, ok := ed.BlockTop("h1")
	viewTop := a.page.view.Bounds(ctx).Y
	if !ok || viewTop == 0 || top-viewTop < 0 || top-viewTop > 2*scrollMargin {
		t.Errorf("Products is %v below the top of the page at %v (shown %v)", top-viewTop, viewTop, ok)
	}
	if got := currentHeading(ed.Headings(), ed.BlockTop, viewTop+2*scrollMargin); got != "h1" {
		t.Errorf("current heading %q", got)
	}
	if ed.Focused() {
		t.Fatal("a click in the outline gave the page the keyboard")
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	tt.Key(0, ui.KeyUp)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if ed.Focused() || a.page.outlineRow != 0 || ed.Caret().ID != "h0" {
		t.Errorf("after Down, Up, Up: page focused %v, row %d, caret in %s", ed.Focused(), a.page.outlineRow, ed.Caret().ID)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if !ed.Focused() {
		t.Error("Enter left the keyboard in the outline")
	}
}

func TestGoToBlockHoldsAsThePageGrows(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	var blocks []*doc.Node
	for i := range 60 {
		n := textNode(fmt.Sprintf("Line %d.", i))
		n.ID = fmt.Sprintf("p%d", i)
		blocks = append(blocks, n)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	var ctx *ui.Context
	tt := ui.NewTester(func(c *ui.Context) {
		ctx = c
		a.view(c)
	}, 1300, 700)
	tt.Frame()
	a.goToBlock("p40", false)
	for range 3 {
		tt.Frame()
	}
	below := func() float32 {
		top, _ := a.page.editor.BlockTop("p40")
		return top - a.page.view.Bounds(ctx).Y
	}
	grow := func(id string) {
		a.page.editor.SetData(id, "delta", doc.Delta{{Text: strings.Repeat("A picture's worth of words. ", 200)}}.JSON())
		for range 3 {
			tt.Frame()
		}
	}
	grow("p0")
	if d := below(); d < 0 || d > 2*scrollMargin {
		t.Errorf("the block went %v below the top of the page as the page above it grew", d)
	}
	view := a.page.view.Bounds(ctx)
	tt.Move(view.X+view.W/2, view.Y+view.H/2)
	tt.Scroll(view.X+view.W/2, view.Y+view.H/2, 0, -100)
	tt.Frame()
	was := below()
	grow("p1")
	if below() <= was {
		t.Errorf("the page went back to the block after it was scrolled: %v below the top, was %v", below(), was)
	}
}

func TestSidebarSplit(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	a.sidebarW = 260
	a.route = Route{Kind: "home"}
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1200, 700)
	tt.Frame()
	clickOn(t, tt, "Divider")
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if a.sidebarW <= 260 {
		t.Errorf("width %v after Right", a.sidebarW)
	}
	for i := 0; i < 40; i++ {
		tt.Key(0, ui.KeyRight)
	}
	tt.Frame()
	if a.sidebarW != sidebarMax {
		t.Errorf("width %v, want at most %v", a.sidebarW, float32(sidebarMax))
	}
}

func TestBlockLinkOpensToggle(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	kid := &doc.Node{ID: "kid", Type: "paragraph"}
	kid.SetDelta(doc.Delta{{Text: "hidden inside"}})
	tog := &doc.Node{ID: "tog", Type: "toggle_list", Children: []*doc.Node{kid}}
	tog.SetDelta(doc.Delta{{Text: "Toggle"}})
	tog.Set("collapsed", true)
	outer := &doc.Node{ID: "outer", Type: "toggle_list", Children: []*doc.Node{tog}}
	outer.SetDelta(doc.Delta{{Text: "Outer"}})
	outer.Set("collapsed", true)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "T", Content: doc.FromBlocks([]*doc.Node{outer})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	a.openBlock(p.ID, "kid")
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	c := a.page.editor.Content
	if _, ok := a.page.editor.BlockTop("kid"); !ok {
		t.Error("the block is not shown")
	}
	if c.Find("tog").Bool("collapsed") || c.Find("outer").Bool("collapsed") || a.page.scrollTo != "" {
		t.Errorf("toggles closed %v %v, still to scroll to %q", c.Find("outer").Bool("collapsed"), c.Find("tog").Bool("collapsed"), a.page.scrollTo)
	}
	a.page.scrollTo = "nowhere"
	for i := 0; i <= scrollWaitMax; i++ {
		tt.Frame()
	}
	if a.page.scrollTo != "" {
		t.Error("still waiting for a block that never shows")
	}
}

func TestGoToBlockKeepsAgentTint(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	agent, other := textNode("Changed by an agent"), textNode("Not changed")
	agent.ID, other.ID = "p1", "p2"
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan", Content: doc.FromBlocks([]*doc.Node{agent, other})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	a.goToBlock("p2", true)
	tt.Frame()
	tt.Advance(blockLit / 2)
	if !a.page.tinted["p2"] {
		t.Error("the block went dark before its moment was over")
	}
	tt.Advance(blockLit)
	if a.page.tinted["p2"] {
		t.Error("the block stayed lit after its moment")
	}

	a.goToBlock("p1", true)
	a.Tint(p.ID, []string{"p1"})
	tt.Frame()
	tt.Advance(2 * blockLit)
	if !a.page.tinted["p1"] {
		t.Error("the agent run's tint went with the moment")
	}
	a.goToBlock("p1", true)
	if len(a.page.lit) != 0 {
		t.Error("going to a tinted block lit it for a moment of its own")
	}
	tt.Frame()
	tt.Advance(2 * blockLit)
	if !a.page.tinted["p1"] {
		t.Error("the agent run's tint went")
	}
}
