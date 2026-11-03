package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A named version shows in the page's history, marked, and Changes since
// shows what was edited after it.
func TestNamedVersionAndChanges(t *testing.T) {
	a := templatesApp(t)
	para := &doc.Node{ID: "p1", Type: "paragraph"}
	para.SetDelta(doc.Delta{{Text: "Ship v1 to beta users"}})
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{para})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openHistory(p.ID)
	a.nameVersion("Sent to legal")
	h := a.dialogs.history
	if len(h) != 1 || h[0].Reason != "manual" || historyLabel(h[0]) != "Sent to legal" {
		t.Fatalf("history %+v", h)
	}
	// Edit the page after it.
	c := a.st.Content(p.ID)
	c.Find("p1").SetDelta(doc.Delta{{Text: "Ship v2 to all beta users"}})
	if _, _, err := a.st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	a.dialogs.historyView = 1
	tt := ui.NewTester(func(c *ui.Context) { a.historyDialog(c) }, 1000, 700)
	tt.Frame()
	if !tt.HasText("Sent to legal") {
		t.Error("the named version is not listed")
	}
	added, deleted := a.dialogs.historyDiff.Stats()
	if added != 1 || deleted != 1 {
		t.Errorf("stats +%d -%d", added, deleted)
	}
	if !tt.HasText("+1") || !tt.HasText("−1") {
		t.Errorf("no counts in %v", tt.Texts())
	}
	// Side by side shows the two versions in columns.
	if err := tt.Click("Side by side"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.dialogs.historyDiff.Layout != codeeditor.DiffSplit {
		t.Error("not side by side")
	}
}

// A version that cannot be read says so, and offers no Restore: shown as an
// empty page, restoring it emptied the page.
func TestUnreadableVersionIsNotRestored(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	if err := a.st.AddHistory(p.ID, doc.Empty(), "manual", "", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.DB().Exec(`UPDATE page_history SET content = '{"document": [' WHERE page_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	a.openHistory(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.historyDialog(c) }, 1000, 700)
	tt.Frame()
	if !tt.HasText(locales.DialogHistoryUnreadable()) || tt.HasText(locales.DialogHistoryRestore()) {
		t.Errorf("shown %q", tt.Texts())
	}
}

// A version of no blocks restores as one to type in, whether its page is
// open or not.
func TestEmptyVersionRestoresAParagraph(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	for _, open := range []bool{true, false} {
		p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{textNode("Goals")})})
		if err != nil {
			t.Fatal(err)
		}
		a.reloadAll()
		if open {
			a.openPage(p.ID)
		} else {
			a.page = nil
		}
		a.restoreVersion(p.ID, &doc.Content{})
		c := a.st.Content(p.ID)
		if c == nil || len(c.Children) != 1 || c.Children[0].Type != "paragraph" || c.PlainText() != "" {
			t.Errorf("open %v: restored to %+v", open, c)
		}
	}
}

// In the narrowest window, History fits with a margin, its close button
// too, and the page keeps its room beside a wide sidebar and panel.
func TestNarrowestWindow(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.sidebarW, a.panelW, a.panelOpen = sidebarMax, panelMax, true
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, windowMinW, 600)
	tt.Frame()
	var room, sidebar, panel float32
	probe := ui.NewTester(func(c *ui.Context) {
		room, sidebar, panel = a.pageRoom(c), a.sidebarWidth(c), a.panelWidth(c)
	}, windowMinW, 600)
	probe.Frame()
	if page := room - panel - 1; page < pageMin-1 {
		t.Errorf("the page is %v wide beside a sidebar of %v and a panel of %v", page, sidebar, panel)
	}
	if a.sidebarW != sidebarMax || a.panelW != panelMax {
		t.Errorf("the widths the user chose changed: %v, %v", a.sidebarW, a.panelW)
	}
	a.openHistory(p.ID)
	tt.Frame()
	r, ok := tt.Find(locales.CommonClose())
	if !ok || r.W == 0 || r.X+r.W > windowMinW-16 {
		t.Errorf("History's close button at %+v, shown %v, in a window %v wide", r, ok, windowMinW)
	}
}
