package app

import (
	"strings"
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

// A blank page offers the starters too, until the user has a template of
// the same title: choosing one fills the page, and editing one makes it
// the user's own.
func TestStarterTemplates(t *testing.T) {
	a := templatesApp(t)
	a.newPage("", "")
	blank := a.page.id
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { a.emptyActions(c) })
	}, 600, 900)
	tt.Frame()
	for _, s := range []string{"Templates", "Meeting notes", "To-do list", "Project brief", "Journal", "Agenda, notes and action items"} {
		if _, ok := tt.Find(s); !ok {
			t.Errorf("no %q on a blank page", s)
		}
	}
	if err := tt.Click("Meeting notes"); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	if got := a.page.editor.Content.PlainText(); !strings.HasPrefix(got, "Agenda") || !strings.Contains(got, "Action items") {
		t.Errorf("page holds %q", got)
	}
	if p := a.pageByID(blank); p.Title != "Meeting notes" {
		t.Errorf("page %+v", p)
	}

	journal, _ := a.templateByKey("starter:journal")
	a.editTemplate(journal)
	a.reloadAll()
	roots := a.templateRoots()
	if len(roots) != 1 || roots[0].Title != "Journal" || a.page.id != roots[0].ID {
		t.Fatalf("templates %+v, open %s", roots, a.page.id)
	}
	if !strings.Contains(a.st.Content(roots[0].ID).PlainText(), "Grateful for") {
		t.Error("the template lacks the starter's blocks")
	}
	if _, ok := a.templateByKey("starter:journal"); ok {
		t.Error("the starter shows beside the user's template of its title")
	}
}

// / lists the templates, by name or for "template"; one chosen puts its
// blocks at the caret, after what the page holds, and its subpages under
// the page.
func TestSlashTemplate(t *testing.T) {
	a := templatesApp(t)
	kit, _ := a.st.CreatePage(store.NewPageOptions{Title: "Kit", Template: true, Content: doc.FromBlocks([]*doc.Node{textNode("Checklist")})})
	if _, err := a.st.CreatePage(store.NewPageOptions{ParentID: kit.ID, Title: "FAQ", Template: true}); err != nil {
		t.Fatal(err)
	}
	notes, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{textNode("Intro")})})
	a.reloadAll()
	a.openPage(notes.ID)
	h := &host{a: a}
	keys := func(q string) []string {
		var out []string
		for _, it := range h.SlashItems(q) {
			if it.Group == "Templates" {
				out = append(out, it.Key)
			}
		}
		return out
	}
	if got := keys("meet"); len(got) != 1 || got[0] != "starter:meeting" {
		t.Errorf("/meet lists %v", got)
	}
	if got := keys("templ"); len(got) != 5 || got[0] != "template:"+kit.ID {
		t.Errorf("/templ lists %v", got)
	}

	tc, _ := a.templateByKey("template:" + kit.ID)
	a.insertTemplate(tc)
	a.reloadAll()
	if got := a.page.editor.Content.PlainText(); got != "Intro\nChecklist" {
		t.Errorf("page holds %q", got)
	}
	if kids := a.children(notes.ID, "", false); len(kids) != 1 || kids[0].Title != "FAQ" || kids[0].Template {
		t.Errorf("subpages %+v", kids)
	}
	if p := a.pageByID(notes.ID); p.Title != "Notes" {
		t.Errorf("title %q", p.Title)
	}

	// A template doesn't list itself.
	a.openPage(kit.ID)
	if got := keys("kit"); len(got) != 0 {
		t.Errorf("a template lists %v", got)
	}
}

// In search, a template or a starter starts a new page; a subpage of a
// template opens.
func TestSearchStartsPageFromTemplate(t *testing.T) {
	a := templatesApp(t)
	kit, _ := a.st.CreatePage(store.NewPageOptions{Title: "Kit", Template: true, Content: doc.FromBlocks([]*doc.Node{textNode("Checklist")})})
	faq, _ := a.st.CreatePage(store.NewPageOptions{ParentID: kit.ID, Title: "Kit FAQ"})
	a.reloadAll()
	hit := func(q, title string) store.SearchHit {
		t.Helper()
		for _, h := range a.searchHits(q) {
			if h.Title == title && h.Kind == "template" {
				return h
			}
		}
		t.Fatalf("%q finds no %q", q, title)
		return store.SearchHit{}
	}

	a.openHit(hit("journ", "Journal"))
	a.reloadAll()
	if a.page == nil || a.pageByID(a.page.id).Title != "Journal" || !strings.Contains(a.page.editor.Content.PlainText(), "Highlights") {
		t.Errorf("Journal did not start a page")
	}
	a.openHit(hit("kit", "Kit"))
	if a.page.id == kit.ID || a.page.editor.Content.PlainText() != "Checklist" {
		t.Errorf("Kit opened %s holding %q", a.page.id, a.page.editor.Content.PlainText())
	}
	a.openHit(hit("kit", "Kit FAQ"))
	if a.page == nil || a.page.id != faq.ID {
		t.Errorf("the template's subpage did not open")
	}
	for _, h := range a.searchHits("#work journal") {
		if h.Title == "Journal" {
			t.Error("a tag search lists a starter")
		}
	}
}

// A folder's new pages, and a page's new subpages, start from the template
// set for them; a page's menu sets it, making a starter the user's own.
func TestDefaultTemplate(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	sync, _ := a.st.CreatePage(store.NewPageOptions{Title: "Weekly sync", Template: true, Content: doc.FromBlocks([]*doc.Node{textNode("Shipped")})})
	f, _ := a.st.CreateFolder("Meetings")
	if err := a.st.SetFolderDefaultTemplate(f, sync.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.newPage("", f.ID)
	a.reloadAll()
	if p := a.pageByID(a.page.id); p.FolderID != f.ID || p.Title != "Weekly sync" || a.page.editor.Content.PlainText() != "Shipped" {
		t.Errorf("new page %+v holding %q", p, a.page.editor.Content.PlainText())
	}
	a.newPage("", "")
	if a.page.editor.Content.PlainText() != "" {
		t.Error("a page outside the folder took its template")
	}

	diary, _ := a.st.CreatePage(store.NewPageOptions{Title: "Diary"})
	a.reloadAll()
	a.openPage(diary.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1200, 800)
	tt.Frame()
	if err := tt.Click("Page actions"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("New subpages start from", "Journal"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	journal := a.templateRoots()[0]
	if journal.Title != "Journal" || a.pageByID(diary.ID).DefaultTemplateID != journal.ID {
		t.Fatalf("templates %+v, diary %+v", a.templateRoots(), a.pageByID(diary.ID))
	}
	a.addSubpageBlock()
	a.reloadAll()
	if p := a.pageByID(a.page.id); p.ParentID != diary.ID || p.Title != "Journal" || !strings.Contains(a.page.editor.Content.PlainText(), "Tomorrow") {
		t.Errorf("subpage %+v holding %q", p, a.page.editor.Content.PlainText())
	}

	// A template deleted leaves new pages blank.
	if _, err := a.st.DeletePage(journal.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.newPage(diary.ID, "")
	if a.page.editor.Content.PlainText() != "" {
		t.Error("a deleted template filled a page")
	}
}

// A starter's menu deletes it on this device, with Undo; a starter made
// the user's own stays gone once that template is deleted.
func TestDeleteStarter(t *testing.T) {
	a := templatesApp(t)
	a.newPage("", "")
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { a.emptyActions(c) })
	}, 600, 900)
	tt.Frame()
	if err := tt.Click("Template actions"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete template"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("Meeting notes"); ok {
		t.Error("the deleted starter still shows")
	}
	if _, ok := a.templateByKey("starter:meeting"); ok {
		t.Error("the deleted starter is still a choice")
	}
	a.goneStarters = nil // as when the app starts again
	if _, ok := a.templateByKey("starter:meeting"); ok {
		t.Error("the deleted starter came back")
	}
	undo := a.toasts[len(a.toasts)-1]
	if undo.text != "Deleted Meeting notes" || undo.fn == nil {
		t.Fatalf("toast %+v", undo)
	}
	undo.fn()
	tt.Frame()
	if _, ok := tt.Find("Meeting notes"); !ok {
		t.Error("Undo did not bring the starter back")
	}

	todo, _ := a.templateByKey("starter:todo")
	a.editTemplate(todo)
	a.reloadAll()
	own, ok := a.templateByKey("template:" + a.page.id)
	if !ok {
		t.Fatal("no template made from the starter")
	}
	a.deleteTemplate(own)
	a.reloadAll()
	if _, ok := a.templateByKey("starter:todo"); ok {
		t.Error("deleting the template made from a starter brought the starter back")
	}
}

// A page made from a template while the open page's edits can't be saved
// is not made: the open page stays as it is, not filled from the
// template, and no blank page is left behind.
func TestNewPageWhileTheOpenPageCannotSave(t *testing.T) {
	a := templatesApp(t)
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Template: true, Content: doc.FromBlocks([]*doc.Node{textNode("Goals")})})
	if _, err := a.st.CreatePage(store.NewPageOptions{ParentID: prd.ID, Title: "FAQ"}); err != nil {
		t.Fatal(err)
	}
	mine, _ := a.st.CreatePage(store.NewPageOptions{Title: "Before", Content: doc.FromBlocks([]*doc.Node{textNode("Mine")})})
	a.reloadAll()
	choice, ok := a.templateByKey("template:" + prd.ID)
	if !ok {
		t.Fatal("no template")
	}
	a.openPage(mine.ID)
	a.page.title = "Typed title"
	if _, err := a.st.DB().Exec(`CREATE TRIGGER full BEFORE UPDATE ON pages WHEN OLD.id = '` + mine.ID + `' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	before := len(a.pages)

	a.newPageFrom("", "", &choice)
	if a.page == nil || a.page.id != mine.ID || a.page.title != "Typed title" {
		t.Fatal("the page that could not save did not stay open")
	}
	if got := a.page.editor.Content.PlainText(); got != "Mine" {
		t.Errorf("the open page holds %q", got)
	}
	a.pages, _ = a.st.Pages()
	if len(a.pages) != before {
		t.Errorf("%d pages, want %d", len(a.pages), before)
	}
	if kids := a.children(mine.ID, "", false); len(kids) != 0 {
		t.Errorf("the template's subpages went under the open page: %+v", kids)
	}
}

// A template fills only an empty page: one with something in it already
// is left as it is.
func TestTemplateFillsOnlyAnEmptyPage(t *testing.T) {
	a := templatesApp(t)
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Template: true, Content: doc.FromBlocks([]*doc.Node{textNode("Goals")})})
	if _, err := a.st.CreatePage(store.NewPageOptions{ParentID: prd.ID, Title: "FAQ"}); err != nil {
		t.Fatal(err)
	}
	mine, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{textNode("Mine")})})
	a.reloadAll()
	choice, _ := a.templateByKey("template:" + prd.ID)
	a.openPage(mine.ID)
	if a.applyTemplate(choice) {
		t.Error("applyTemplate reported it filled a page with text")
	}
	if got := a.page.editor.Content.PlainText(); got != "Mine" || a.page.title != "Notes" {
		t.Errorf("page %q holds %q", a.page.title, got)
	}
	if kids := a.children(mine.ID, "", false); len(kids) != 0 {
		t.Errorf("subpages %+v", kids)
	}
}

// A template with a recording still being transcribed in it is not used
// until it is done, as it is not copied.
func TestTemplateWaitsForRecording(t *testing.T) {
	a := templatesApp(t)
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Template: true, Content: doc.FromBlocks([]*doc.Node{textNode("Goals")})})
	call, _ := a.st.CreatePage(store.NewPageOptions{ParentID: prd.ID, Title: "Call"})
	rec, err := a.st.CreateRecording(call.ID, "b1", "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.rec.Transcribing[rec.ID] = true
	choice, _ := a.templateByKey("template:" + prd.ID)

	a.newPage("", "")
	blank := a.page.id
	a.toasts = nil
	if a.applyTemplate(choice) || len(a.toasts) != 1 || a.toasts[0].text != locales.TemplateWaitForRecording() {
		t.Fatalf("toasts %+v", a.toasts)
	}
	if !a.isEmpty() || len(a.children(blank, "", false)) != 0 {
		t.Error("the template was used with its recording unfinished")
	}

	delete(a.rec.Transcribing, rec.ID)
	a.applyTemplate(choice)
	if got := a.page.editor.Content.PlainText(); got != "Goals" {
		t.Errorf("once transcribed the page holds %q", got)
	}
}
