package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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

func ownTemplates(a *App) []store.Page {
	var out []store.Page
	for _, t := range a.templateChoices() {
		if t.Page != nil {
			out = append(out, *t.Page)
		}
	}
	return out
}

func textNode(text string) *doc.Node {
	n := &doc.Node{Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown(text))
	return n
}

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
	kids := a.children(product.ID)
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

func TestStartFromTemplate(t *testing.T) {
	a := templatesApp(t)
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{textNode("Problem"), textNode("Goals")})})
	if _, err := a.st.CreatePage(store.NewPageOptions{ParentID: prd.ID, Title: "FAQ"}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.saveAsTemplate(*a.pageByID(prd.ID))
	a.reloadAll()
	roots := ownTemplates(a)
	if len(roots) != 1 || roots[0].Title != "PRD" || roots[0].ID == prd.ID {
		t.Fatalf("templates %+v", roots)
	}
	for _, p := range a.pages {
		if p.Template {
			t.Errorf("template %q in the page list", p.Title)
		}
	}

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
	kids := a.children(blank)
	if len(kids) != 1 || kids[0].Title != "FAQ" || kids[0].Template {
		t.Errorf("subpages %+v", kids)
	}

	a.openPage(roots[0].ID)
	a.page.editor.Edit(func(c *doc.Content) { c.Children = doc.Empty().Children })
	tt.Frame()
	if _, ok := tt.Find("Templates"); ok {
		t.Error("a template offers templates")
	}
}

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
	roots := ownTemplates(a)
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
	if kids := a.children(notes.ID); len(kids) != 1 || kids[0].Title != "FAQ" || kids[0].Template {
		t.Errorf("subpages %+v", kids)
	}
	if p := a.pageByID(notes.ID); p.Title != "Notes" {
		t.Errorf("title %q", p.Title)
	}

	a.openPage(kit.ID)
	if got := keys("kit"); len(got) != 0 {
		t.Errorf("a template lists %v", got)
	}
}

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
	journal := ownTemplates(a)[0]
	if journal.Title != "Journal" || a.pageByID(diary.ID).DefaultTemplateID != journal.ID {
		t.Fatalf("templates %+v, diary %+v", ownTemplates(a), a.pageByID(diary.ID))
	}
	a.addSubpageBlock()
	a.reloadAll()
	if p := a.pageByID(a.page.id); p.ParentID != diary.ID || p.Title != "Journal" || !strings.Contains(a.page.editor.Content.PlainText(), "Tomorrow") {
		t.Errorf("subpage %+v holding %q", p, a.page.editor.Content.PlainText())
	}

	if _, err := a.st.DeletePage(journal.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.newPage(diary.ID, "")
	if a.page.editor.Content.PlainText() != "" {
		t.Error("a deleted template filled a page")
	}
}

func TestDeleteStarter(t *testing.T) {
	a := templatesApp(t)
	a.newPage("", "")
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { a.emptyActions(c) })
	}, 600, 900)
	tt.Frame()
	for range 20 {
		if tt.Focused("Meeting notes") {
			break
		}
		tt.Key(0, ui.KeyTab)
		tt.Frame()
	}
	if !tt.Focused("Meeting notes") {
		t.Fatal("Tab never reached the starter")
	}
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if !tt.Focused("To-do list") {
		t.Error("Tab went from the starter to its hidden menu button")
	}
	tt.Key(0, ui.KeyContextMenu)
	tt.Frame()
	if !tt.HasText("Delete template") {
		t.Errorf("the menu key opened no menu in %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	r, ok := tt.Find("Template actions")
	if !ok {
		t.Fatalf("no menu button in %v", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
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
	a.goneStarters = nil
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
	if kids := a.children(mine.ID); len(kids) != 0 {
		t.Errorf("the template's subpages went under the open page: %+v", kids)
	}
}

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
	if kids := a.children(mine.ID); len(kids) != 0 {
		t.Errorf("subpages %+v", kids)
	}
}

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
	if !a.page.editor.Content.IsEmpty() || len(a.children(blank)) != 0 {
		t.Error("the template was used with its recording unfinished")
	}

	delete(a.rec.Transcribing, rec.ID)
	a.applyTemplate(choice)
	if got := a.page.editor.Content.PlainText(); got != "Goals" {
		t.Errorf("once transcribed the page holds %q", got)
	}
}

func TestEmptyTemplate(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	tp, err := a.st.CreatePage(store.NewPageOptions{Title: "Empty", Template: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(tp.ID, &doc.Content{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{ParentID: tp.ID, Title: "Sub"}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	choice, ok := a.templateByKey("template:" + tp.ID)
	if !ok {
		t.Fatal("no template")
	}
	a.newPage("", "")
	a.applyTemplate(choice)
	if n := len(a.page.editor.Content.Children); n != 1 || !a.page.editor.Content.IsEmpty() {
		t.Fatalf("%d blocks", n)
	}
	if kids := a.children(a.page.id); len(kids) != 1 || a.page.title != "Empty" {
		t.Errorf("subpages %+v, title %q", kids, a.page.title)
	}
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	a.caretAtEnd()
	a.page.titleLeave = true
	tt.Frame()
}

func TestCopiesWaitForTheSave(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	a.reloadAll()
	a.openPage(p.ID)
	a.page.title = "Typed title"
	if _, err := a.st.DB().Exec(`CREATE TRIGGER full BEFORE UPDATE ON pages WHEN OLD.id = '` + p.ID + `' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	pages, templates := len(a.pages), len(a.templates)
	a.duplicatePage(*a.pageByID(p.ID))
	a.saveAsTemplate(*a.pageByID(p.ID))
	a.reloadAll()
	if len(a.pages) != pages || len(a.templates) != templates {
		t.Errorf("%d pages and %d templates, want %d and %d", len(a.pages), len(a.templates), pages, templates)
	}
}

func TestDuplicateBringsFilesFirst(t *testing.T) {
	release := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/storage/v1/object/authenticated/files/") {
			<-release
			_, _ = w.Write([]byte("%PDF"))
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(hs.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	a := templatesApp(t)
	a.backend = remote.SignedInForTest(hs.URL, a.st, "u1")
	a.sync = syncsvc.New(a.st, a.backend)
	posted := make(chan func(), 4)
	a.post = func(fn func()) { posted <- fn }
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Trip"})
	f := store.File{ID: doc.NewRowID(), SpaceID: a.st.Session().SpaceID, PageID: p.ID, Name: "plan.pdf", MimeType: "application/pdf",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.StoragePath = f.SpaceID + "/" + f.ID + "/plan.pdf"
	if err := a.st.PutFile(f); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "b1", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	pages := func() int { all, _ := a.st.Pages(); return len(all) }
	before := pages()
	a.duplicatePage(*a.pageByID(p.ID))
	if pages() != before || len(a.toasts) == 0 || a.toasts[len(a.toasts)-1].text != locales.TemplateFetchingFiles() {
		t.Fatalf("copied before the file came: %d pages; toasts %+v", pages(), a.toasts)
	}
	a.duplicatePage(*a.pageByID(p.ID))
	if pages() != before || a.toasts[len(a.toasts)-1].text != locales.TemplateFetchingOther() {
		t.Fatalf("a second copy while the first downloads: %d pages; toasts %+v", pages(), a.toasts)
	}
	next := func(what string) {
		t.Helper()
		select {
		case fn := <-posted:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal(what)
		}
	}
	close(release)
	next("the file never came")
	next("the copy was never made")
	if pages() != before+1 || a.page == nil || a.page.id == p.ID {
		t.Fatalf("not copied once the file came: %d pages", pages())
	}
	cf, err := a.st.File(a.st.Content(a.page.id).Children[0].Str("file_id"))
	if err != nil || cf.ID == f.ID {
		t.Fatalf("the copy's file %+v, %v", cf, err)
	}
	if data, err := os.ReadFile(cf.LocalPath); err != nil || string(data) != "%PDF" {
		t.Errorf("the copy's file here: %q, %v", data, err)
	}

	q, _ := a.st.CreatePage(store.NewPageOptions{Title: "Call"})
	if _, err := a.st.CreateRecording(q.ID, "r1", "soniox", "m", "", "en"); err != nil {
		t.Fatal(err)
	}
	recs, _ := a.st.Recordings()
	if _, _, err := a.st.SaveContent(q.ID, doc.FromBlocks([]*doc.Node{
		{ID: "r1", Type: "recording", Data: map[string]any{"recording_id": recs[0].ID}},
	})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	before = pages()
	a.duplicatePage(*a.pageByID(q.ID))
	if pages() != before || a.toasts[len(a.toasts)-1].text != locales.TemplateFilesUploading() {
		t.Errorf("a recording still uploading: %d pages, want %d; toasts %+v", pages(), before, a.toasts)
	}
}

func TestCopyKeepsFetchedAudioUntilMade(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/storage/v1/object/authenticated/recordings/") {
			_, _ = w.Write([]byte("RIFF"))
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(hs.Close)
	a := templatesApp(t)
	a.backend = remote.SignedInForTest(hs.URL, a.st, "u1")
	a.sync = syncsvc.New(a.st, a.backend)
	a.settings.KeepAudioOnDevice = "until_uploaded"
	posted := make(chan func(), 4)
	a.post = func(fn func()) { posted <- fn }
	next := func(what string) {
		t.Helper()
		select {
		case fn := <-posted:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal(what)
		}
	}
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Call"})
	r, err := a.st.CreateRecording(p.ID, "r1", "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	r.AudioPath, r.Status, r.UploadedAt = a.st.Session().SpaceID+"/"+r.ID+".wav", "done", time.Now()
	if err := a.st.PutRecording(r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "r1", Type: "recording", Data: map[string]any{"recording_id": r.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	f, _ := a.st.CreateFolder("Work")
	other, _ := a.st.CreatePage(store.NewPageOptions{Title: "Other"})
	a.reloadAll()

	a.duplicatePage(*a.pageByID(p.ID))
	if err := a.st.MovePage(p.ID, "", f.ID, ""); err != nil {
		t.Fatal(err)
	}
	next("the audio never came")
	if got, _ := a.st.Recording(r.ID); got.LocalPath == "" {
		t.Fatal("the audio went before the copy was made")
	}
	a.duplicatePage(*a.pageByID(other.ID))
	if a.toasts[len(a.toasts)-1].text != locales.TemplateFetchingOther() {
		t.Errorf("a second copy while the first is made: toasts %+v", a.toasts)
	}
	if got, _ := a.st.Recording(r.ID); got.LocalPath == "" {
		t.Fatal("the copy refused took the audio from the copy being made")
	}
	next("the copy was never made")
	if a.page == nil || a.page.id == p.ID {
		t.Fatal("no copy opened")
	}
	cp, _ := a.st.Page(a.page.id)
	if cp.FolderID != f.ID {
		t.Errorf("the copy is in folder %q, not where the page is now", cp.FolderID)
	}
	if got, _ := a.st.Recording(r.ID); got.LocalPath != "" {
		t.Errorf("the original's audio stayed at %q", got.LocalPath)
	}
	recs, _ := a.st.Recordings()
	for _, c := range recs {
		if c.ID == r.ID {
			continue
		}
		if data, err := os.ReadFile(c.LocalPath); err != nil || string(data) != "RIFF" {
			t.Errorf("the copy's audio: %q, %v", data, err)
		}
	}
}

func TestCopyOfAPageDeletedMeanwhile(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/storage/v1/object/authenticated/files/") {
			_, _ = w.Write([]byte("%PDF"))
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(hs.Close)
	for _, copyIt := range []func(*App, store.Page){(*App).duplicatePage, (*App).saveAsTemplate} {
		a := templatesApp(t)
		a.backend = remote.SignedInForTest(hs.URL, a.st, "u1")
		a.sync = syncsvc.New(a.st, a.backend)
		posted := make(chan func(), 4)
		a.post = func(fn func()) { posted <- fn }
		p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Trip"})
		f := store.File{ID: doc.NewRowID(), SpaceID: a.st.Session().SpaceID, PageID: p.ID, Name: "plan.pdf", MimeType: "application/pdf",
			CreatedAt: time.Now(), UpdatedAt: time.Now()}
		f.StoragePath = f.SpaceID + "/" + f.ID + "/plan.pdf"
		if err := a.st.PutFile(f); err != nil {
			t.Fatal(err)
		}
		if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
			{ID: "b1", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
		})); err != nil {
			t.Fatal(err)
		}
		a.reloadAll()
		copyIt(a, *a.pageByID(p.ID))
		if _, err := a.st.DeletePage(p.ID); err != nil {
			t.Fatal(err)
		}
		select {
		case fn := <-posted:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("the file never came")
		}
		if a.fetchingCopy {
			t.Error("a page deleted meanwhile is being copied")
		}
		if all, _ := a.st.Pages(); len(all) != 0 {
			t.Errorf("pages %+v", all)
		}
		if ts, _ := a.st.Templates(); len(ts) != 0 {
			t.Errorf("templates %+v", ts)
		}
	}
}

func TestTemplateFillsAPageClosedDuringTheCopy(t *testing.T) {
	a := templatesApp(t)
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Template: true,
		Content: doc.FromBlocks([]*doc.Node{textNode("Problem"), textNode("Goals")})})
	if _, err := a.st.CreatePage(store.NewPageOptions{ParentID: prd.ID, Title: "FAQ"}); err != nil {
		t.Fatal(err)
	}
	other, _ := a.st.CreatePage(store.NewPageOptions{Title: "Other"})
	a.reloadAll()
	a.newPage("", "")
	blank := a.page.id
	posted := make(chan func(), 8)
	a.post = func(fn func()) { posted <- fn }
	tmpl, ok := a.templateByKey("template:" + prd.ID)
	if !ok {
		t.Fatalf("no template in %+v", a.templateChoices())
	}
	if !a.applyTemplate(tmpl) {
		t.Fatal("the template was not taken")
	}
	a.openPage(other.ID)
	for a.fetchingCopy {
		select {
		case fn := <-posted:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("the copy was never made")
		}
	}
	if got := a.st.Content(blank).PlainText(); got != "Problem\nGoals" {
		t.Errorf("the page closed holds %q", got)
	}
	kids, _ := a.st.Pages()
	var under []string
	for _, k := range kids {
		if k.ParentID == blank {
			under = append(under, k.Title)
		}
	}
	if len(under) != 1 || under[0] != "FAQ" {
		t.Errorf("subpages %v", under)
	}
}
