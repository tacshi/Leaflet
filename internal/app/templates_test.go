package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func templatesApp(t *testing.T) *App {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session(), rec: recording.NewController(st, be)}
	a.reloadAll()
	return a
}

func textNode(text string) *doc.Node {
	n := &doc.Node{Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown(text))
	return n
}

// Duplicate puts the copy after the page, links it after the page's link
// in the parent, and opens it with its title selected.
func TestDuplicatePage(t *testing.T) {
	a := templatesApp(t)
	product, _ := a.st.CreatePage(store.NewPageOptions{Title: "Product"})
	prd, _ := a.st.CreatePage(store.NewPageOptions{ParentID: product.ID, Title: "PRD", Content: doc.FromBlocks([]*doc.Node{textNode("Goals")})})
	roadmap, _ := a.st.CreatePage(store.NewPageOptions{ParentID: product.ID, Title: "Roadmap"})
	link := func(id string) *doc.Node {
		return &doc.Node{Type: "subpage", Data: map[string]any{"page_id": id}}
	}
	if _, _, err := a.st.SaveContent(product.ID, doc.FromBlocks([]*doc.Node{link(prd.ID), link(roadmap.ID)})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()

	a.duplicatePage(*a.pageByID(prd.ID))
	kids := a.children(product.ID, "", false)
	if len(kids) != 3 || kids[0].ID != prd.ID || kids[1].Title != "PRD copy" || kids[2].ID != roadmap.ID {
		t.Fatalf("pages under Product %+v", kids)
	}
	cp := kids[1]
	if a.page == nil || a.page.id != cp.ID || !a.page.selectTitle {
		t.Error("the copy did not open with its title selected")
	}
	if got := a.st.Content(cp.ID).PlainText(); got != "Goals" {
		t.Errorf("copy holds %q", got)
	}
	var links []string
	for _, n := range a.st.Content(product.ID).Children {
		links = append(links, n.Str("page_id"))
	}
	if len(links) != 3 || links[0] != prd.ID || links[1] != cp.ID || links[2] != roadmap.ID {
		t.Errorf("Product links %v", links)
	}
}

// A page, or one under it, with a recording still being transcribed is not
// copied until it is done: the copy would keep the recording unfinished.
func TestDuplicateWaitsForRecording(t *testing.T) {
	a := templatesApp(t)
	notes, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	call, _ := a.st.CreatePage(store.NewPageOptions{ParentID: notes.ID, Title: "Call"})
	rec, err := a.st.CreateRecording(call.ID, "b1", "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	count := func() int { all, _ := a.st.Pages(); templates, _ := a.st.Templates(); return len(all) + len(templates) }
	before := count()

	a.rec.Transcribing[rec.ID] = true
	for _, p := range []store.Page{notes, call} {
		a.toasts = nil
		a.duplicatePage(*a.pageByID(p.ID))
		a.saveAsTemplate(*a.pageByID(p.ID))
		if n := count(); n != before || len(a.toasts) != 2 || a.toasts[0].text != locales.TemplateWaitForRecording() {
			t.Fatalf("%s: %d pages, want %d; toasts %+v", p.Title, n, before, a.toasts)
		}
	}
	// A page beside it is copied.
	other, _ := a.st.CreatePage(store.NewPageOptions{Title: "Other"})
	a.reloadAll()
	before = count()
	a.duplicatePage(*a.pageByID(other.ID))
	if n := count(); n != before+1 {
		t.Errorf("a page without the recording: %d pages, want %d", n, before+1)
	}

	delete(a.rec.Transcribing, rec.ID)
	before = count()
	a.duplicatePage(*a.pageByID(notes.ID))
	if n := count(); n != before+2 {
		t.Errorf("once transcribed: %d pages, want %d", n, before+2)
	}
}

// A page saved as a template leaves the page list, and a blank page lists
// it under Start with; choosing it fills the page and takes its title.
func TestStartFromTemplate(t *testing.T) {
	a := templatesApp(t)
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{textNode("Problem"), textNode("Goals")})})
	if _, err := a.st.CreatePage(store.NewPageOptions{ParentID: prd.ID, Title: "FAQ"}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.saveAsTemplate(*a.pageByID(prd.ID))
	a.reloadAll()
	roots := a.templateRoots()
	if len(roots) != 1 || roots[0].Title != "PRD" || roots[0].ID == prd.ID {
		t.Fatalf("templates %+v", roots)
	}
	for _, p := range a.pages {
		if p.Template {
			t.Errorf("template %q in the page list", p.Title)
		}
	}

	// A template's row shows its first line, or, with no text, its
	// subpages: a subpage made under a template is one too.
	if got := a.templateHint(roots[0]); got != "Problem" {
		t.Errorf("hint %q", got)
	}
	empty, _ := a.st.CreatePage(store.NewPageOptions{Title: "Kit", Template: true})
	if _, err := a.st.CreatePage(store.NewPageOptions{ParentID: empty.ID, Title: "Notes"}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	if got := a.templateHint(*a.pageByID(empty.ID)); got != "1 subpage" {
		t.Errorf("hint of a template with only a subpage %q", got)
	}

	a.newPage("", "")
	blank := a.page.id
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { a.emptyActions(c) })
	}, 600, 500)
	tt.Frame()
	if _, ok := tt.Find("Problem"); !ok {
		t.Error("the template's row has no hint of what it holds")
	}
	if err := tt.Click("PRD"); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	if got := a.page.editor.Content.PlainText(); got != "Problem\nGoals" {
		t.Errorf("page holds %q", got)
	}
	if p := a.pageByID(blank); p.Title != "PRD" || p.Template {
		t.Errorf("page %+v", p)
	}
	kids := a.children(blank, "", false)
	if len(kids) != 1 || kids[0].Title != "FAQ" || kids[0].Template {
		t.Errorf("subpages %+v", kids)
	}

	// A template's own blank page offers no templates.
	a.openPage(roots[0].ID)
	a.page.editor.Edit(func(c *doc.Content) { c.Children = doc.Empty().Children })
	tt.Frame()
	if _, ok := tt.Find("Templates"); ok {
		t.Error("a template offers templates")
	}
}

// Deleting the open page from its own menu, as a template opened from
// search is deleted, closes it in the middle of drawing it.
func TestDeleteOpenPageFromItsMenu(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	tpl, err := a.st.CreatePage(store.NewPageOptions{Title: "Postmortem", Template: true})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(tpl.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1200, 800)
	tt.Frame()
	if err := tt.Click("Page actions"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.page != nil || a.route.Kind != "home" {
		t.Errorf("route %+v", a.route)
	}
	if p, _ := a.st.Page(tpl.ID); p.DeletedAt.IsZero() {
		t.Error("the template was not deleted")
	}
}
