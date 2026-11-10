package app

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

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
	if len(h) != 1 || h[0].Reason != "manual" || core.HistoryLabel(h[0]) != "Sent to legal" {
		t.Fatalf("history %+v", h)
	}
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
	if err := tt.Click("Side by side"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.dialogs.historyDiff.Layout != codeeditor.DiffSplit {
		t.Error("not side by side")
	}
}

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

func TestRestoredVersionUndoes(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	for _, open := range []bool{true, false} {
		p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{textNode("Goals")})})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.st.AddHistory(p.ID, doc.FromBlocks([]*doc.Node{textNode("Plans")}), "manual", "", "v1"); err != nil {
			t.Fatal(err)
		}
		h, _ := a.st.History(p.ID)
		a.reloadAll()
		if open {
			a.openPage(p.ID)
		} else {
			a.page = nil
		}
		a.restoreVersion(p.ID, h[0].ID)
		if got := a.st.Content(p.ID).PlainText(); got != "Plans" {
			t.Fatalf("open %v: restored to %q", open, got)
		}
		if open && a.page.editor.Content.PlainText() != "Plans" {
			t.Errorf("the open page shows %q", a.page.editor.Content.PlainText())
		}
		a.toasts[len(a.toasts)-1].fn()
		if got := a.st.Content(p.ID).PlainText(); got != "Goals" {
			t.Errorf("open %v: undone to %q", open, got)
		}
	}
}

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

func TestHistoryDays(t *testing.T) {
	at := func(day, hour int) time.Time { return time.Date(2026, 3, day, hour, 0, 0, 0, time.Local) }
	got := historyDays([]store.HistoryEntry{{CreatedAt: at(10, 23)}, {CreatedAt: at(10, 1)}, {CreatedAt: at(9, 23)}})
	if want := [][]int{{0, 1}, {2}}; !slices.EqualFunc(got, want, slices.Equal[[]int]) {
		t.Errorf("days %v, want %v", got, want)
	}
}

func TestHistoryDayStaysInView(t *testing.T) {
	a := templatesApp(t)
	para := &doc.Node{ID: "p1", Type: "paragraph"}
	para.SetDelta(doc.Delta{{Text: "Text"}})
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{para})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openHistory(p.ID)
	a.nameVersion("Kept")
	day := a.dialogs.history[0]
	day.CreatedAt = time.Date(2026, 3, 10, 9, 0, 0, 0, time.Local)
	a.dialogs.history = slices.Repeat([]store.HistoryEntry{day}, 30)
	for i := range a.dialogs.history {
		a.dialogs.history[i].ID += strconv.Itoa(i)
		a.dialogs.history[i].Label = "Kept " + strconv.Itoa(i)
	}
	tt := ui.NewTester(func(c *ui.Context) { a.historyDialog(c) }, 1000, 700)
	tt.Frame()
	label := locales.Day(day.CreatedAt, time.Now())
	before, ok := tt.Find(label)
	if !ok {
		t.Fatalf("no %q in %v", label, tt.Texts())
	}
	first, ok := tt.Find("Kept 0")
	if !ok {
		t.Fatalf("no version in %v", tt.Texts())
	}
	tt.Scroll(first.X+20, first.Y+20, 0, 200)
	tt.Frame()
	if after, ok := tt.Find(label); !ok || after.Y != before.Y || after.H != before.H {
		t.Errorf("heading %v, then %v (shown %v)", before, after, ok)
	}
}
