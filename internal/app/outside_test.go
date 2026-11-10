package app

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func TestSaveKeepsOutsideEdits(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	para := func(id, text string) *doc.Node {
		n := &doc.Node{ID: id, Type: "paragraph"}
		n.SetDelta(doc.FromMarkdown(text))
		return n
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{para("p1", "one"), para("p2", "two")})})
	if err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	a.openPage(p.ID)
	if a.page == nil {
		t.Fatal("the page did not open")
	}

	a.page.editor.Content.Find("p1").SetDelta(doc.FromMarkdown("one, typed"))
	a.page.live.Changed()
	outside := st.Content(p.ID)
	outside.Find("p2").SetDelta(doc.FromMarkdown("two, by the agent"))
	if _, _, err := st.SaveContent(p.ID, outside); err != nil {
		t.Fatal(err)
	}

	a.savePage()
	text := st.Content(p.ID).PlainText()
	if !strings.Contains(text, "one, typed") || !strings.Contains(text, "two, by the agent") {
		t.Errorf("stored %q, want both edits", text)
	}
}

func TestDeletedAndEditedConflict(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	para := func(id, text string) *doc.Node {
		n := &doc.Node{ID: id, Type: "paragraph"}
		n.SetDelta(doc.FromMarkdown(text))
		return n
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{para("p1", "one"), para("p2", "two")})})
	if err != nil {
		t.Fatal(err)
	}
	base := st.Content(p.ID)
	mine := base.Copy()
	mine.Remove("p1")
	mine = mine.Stamped(base, base.Blocks["p1"]+5)
	theirs := base.Copy()
	theirs.Find("p1").SetDelta(doc.FromMarkdown("one, edited"))
	theirs = theirs.Stamped(base, base.Blocks["p1"]+1)
	merged, conflicts := doc.Merge(base, mine, theirs)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts %+v", conflicts)
	}
	if _, _, err := st.SaveContent(p.ID, merged); err != nil {
		t.Fatal(err)
	}
	if err := st.AddConflict(p.ID, conflicts[0]); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Conflicts()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	cf := rows[0]
	if cf.LocalNode != nil || cf.RemoteNode == nil || cf.Kept != "remote" {
		t.Fatalf("stored %+v", cf)
	}

	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	a.resolveConflict(cf.ID, core.DeleteBlock)
	if text := st.Content(p.ID).PlainText(); text != "two" {
		t.Errorf("after Delete it the page holds %q", text)
	}
	if rows, _ := st.Conflicts(); len(rows) != 0 {
		t.Errorf("still listed: %+v", rows)
	}
}

func TestFailedSaveKeepsTheEdits(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	para := func(id, text string) *doc.Node {
		n := &doc.Node{ID: id, Type: "paragraph"}
		n.SetDelta(doc.FromMarkdown(text))
		return n
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{para("p1", "one"), para("p2", "two")})})
	if err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()

	a.openPage(p.ID)
	a.page.editor.Content.Find("p1").SetDelta(doc.FromMarkdown("one, typed"))
	a.page.live.Changed()
	_ = st.Close()
	a.savePage()
	if !a.page.live.Dirty() {
		t.Error("a save that failed dropped the edits")
	}
}

func TestLeavingAPageThatCannotSave(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreatePage(store.NewPageOptions{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	a.openPage(p.ID)
	if a.page == nil {
		t.Fatal("the page did not open")
	}
	typed := &doc.Node{ID: "t1", Type: "paragraph"}
	typed.SetDelta(doc.FromMarkdown("typed"))
	a.page.editor.Content.Children = append(a.page.editor.Content.Children, typed)
	a.page.live.Changed()

	full := []string{
		`CREATE TRIGGER full_insert BEFORE INSERT ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
		`CREATE TRIGGER full_update BEFORE UPDATE ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
	}
	for _, q := range full {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a.openPage(other.ID)
	if a.page == nil || a.page.id != p.ID || a.route.ID != p.ID || a.page.editor.Content.Find("t1") == nil {
		t.Fatalf("left the page with its edits unsaved: route %+v", a.route)
	}

	for _, q := range []string{`DROP TRIGGER full_insert`, `DROP TRIGGER full_update`} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a.openPage(other.ID)
	if a.page == nil || a.page.id != other.ID {
		t.Fatal("did not leave the page once it saved")
	}
	if st.Content(p.ID).Find("t1") == nil {
		t.Error("the edits were not stored")
	}
}

func TestUnreadablePageOpensReadOnly(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	const broken = `{"children": [{"id": "p1", "type": "paragraph", "delta": `
	if _, err := a.st.DB().Exec(`UPDATE page_contents SET content = ? WHERE page_id = ?`, broken, p.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	if a.page == nil || !a.page.live.Unreadable() || !a.page.editor.ReadOnly || a.Live(p.ID) != nil {
		t.Fatalf("opened as %+v", a.page)
	}
	tt := ui.NewTester(func(c *ui.Context) { a.pageNotices(c, p.ID) }, 800, 200)
	tt.Frame()
	if !tt.HasText(locales.PageUnreadable()) {
		t.Errorf("no notice in %v", tt.Texts())
	}
	a.page.editor.Content.Children[0].SetDelta(doc.FromMarkdown("typed"))
	a.page.live.Changed()
	a.reload(store.Change{Table: "page_members", PageID: p.ID})
	if !a.page.editor.ReadOnly {
		t.Error("editable again after its members were read")
	}
	if !a.closePage() {
		t.Fatal("the page would not close")
	}
	if row, _ := a.st.ContentRow(p.ID); row.Content != broken {
		t.Errorf("saved over: %q", row.Content)
	}

	a.loadPage(p.ID)
	good := doc.FromBlocks([]*doc.Node{textNode("restored")})
	if _, err := a.st.DB().Exec(`UPDATE page_contents SET content = ? WHERE page_id = ?`, good.Encode(), p.ID); err != nil {
		t.Fatal(err)
	}
	a.externalContent(p.ID)
	if a.page.live.Unreadable() || a.page.editor.ReadOnly || !strings.Contains(a.page.editor.Content.PlainText(), "restored") {
		t.Errorf("unreadable %v, read-only %v, shows %q", a.page.live.Unreadable(), a.page.editor.ReadOnly, a.page.editor.Content.PlainText())
	}
}

func TestUnreadableContentBroughtInIsNotSavedOver(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{textNode("mine")})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.page.editor.Content.Children[0].SetDelta(doc.FromMarkdown("typed"))
	a.page.live.Changed()
	const newer = `{"children": [{"id": "p1", "type": "paragraph", "delta": `
	if _, err := a.st.DB().Exec(`UPDATE page_contents SET content = ? WHERE page_id = ?`, newer, p.ID); err != nil {
		t.Fatal(err)
	}
	a.externalContent(p.ID)
	if got := a.page.editor.Content.PlainText(); got != "typed" {
		t.Errorf("the editor shows %q", got)
	}
	if !a.page.live.Unreadable() || !a.page.editor.ReadOnly {
		t.Errorf("unreadable %v, read-only %v", a.page.live.Unreadable(), a.page.editor.ReadOnly)
	}
	if !a.closePage() {
		t.Fatal("the page would not close")
	}
	if row, _ := a.st.ContentRow(p.ID); row.Content != newer {
		t.Errorf("saved over: %q", row.Content)
	}
	h, err := a.st.History(p.ID)
	if err != nil || len(h) == 0 || !strings.Contains(h[0].Content, "typed") {
		t.Errorf("what was typed is not in the history: %+v %v", h, err)
	}
}

func TestLeavingAPageWhoseTitleCannotSave(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.page.title = "Typed title"
	if _, err := a.st.DB().Exec(`CREATE TRIGGER full BEFORE UPDATE ON pages BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if a.closePage() || a.page == nil || a.page.title != "Typed title" {
		t.Fatal("the page closed without its title saved")
	}
}

func TestReloadKeepsATitleNotYetSaved(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.page.title = "Typed title"
	a.reload(store.Change{Table: "page_contents", PageID: p.ID})
	if a.page.title != "Typed title" {
		t.Fatalf("the title went back to %q", a.page.title)
	}
	if !a.saveTitle() {
		t.Fatal("the title did not save")
	}
	if q, err := a.st.Page(p.ID); err != nil || q.Title != "Typed title" {
		t.Fatalf("stored %q, %v", q.Title, err)
	}
	a.reload(store.Change{Table: "pages"})
	if err := a.st.RenamePage(p.ID, "Renamed elsewhere"); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "pages"})
	if a.page.title != "Renamed elsewhere" {
		t.Errorf("the title shows %q", a.page.title)
	}
}

func TestRenameElsewhereShowsInAFocusedTitle(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	var ctx *ui.Context
	tt := ui.NewTester(func(c *ui.Context) {
		ctx = c
		a.view(c)
	}, 1300, 800)
	a.focusTitle(false)
	for range 3 {
		tt.Frame()
	}
	if !a.page.titleIn.Focused(ctx) {
		t.Fatal("the title didn't take the focus")
	}
	if err := a.st.RenamePage(p.ID, "Renamed elsewhere"); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "pages"})
	tt.Frame()
	if a.page.title != "Renamed elsewhere" || !tt.HasText("Renamed elsewhere") {
		t.Errorf("the title shows %q", a.page.title)
	}
	if !a.saveTitle() {
		t.Fatal("the title did not save")
	}
	if q, err := a.st.Page(p.ID); err != nil || q.Title != "Renamed elsewhere" {
		t.Errorf("stored %q, %v", q.Title, err)
	}
}

func TestPageDeletedElsewhereKeepsWhatWasTyped(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := a.st.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := a.st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	a.openPage(p.ID)
	typed := &doc.Node{ID: "t1", Type: "paragraph"}
	typed.SetDelta(doc.FromMarkdown("typed"))
	a.page.editor.Content.Children = append(a.page.editor.Content.Children, typed)
	a.page.live.Changed()
	row := store.PageToRemote(p)
	row["deleted_at"] = "2026-10-30T00:00:00Z"
	if err := a.st.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "pages", PageID: p.ID})
	if a.page != nil || a.route.Kind != "home" {
		t.Fatalf("the deleted page stayed open: %+v", a.route)
	}
	if a.st.Content(p.ID).Find("t1") == nil {
		t.Error("what was typed went with the page")
	}
}

func TestPageDeletedElsewhereKeepsWhatCouldNotSave(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := a.st.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := a.st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	a.openPage(p.ID)
	typed := &doc.Node{ID: "t1", Type: "paragraph"}
	typed.SetDelta(doc.FromMarkdown("typed"))
	a.page.editor.Content.Children = append(a.page.editor.Content.Children, typed)
	a.page.live.Changed()
	for _, q := range []string{
		`CREATE TRIGGER full_insert BEFORE INSERT ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
		`CREATE TRIGGER full_update BEFORE UPDATE ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
	} {
		if _, err := a.st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	row := store.PageToRemote(p)
	row["deleted_at"] = "2026-10-30T00:00:00Z"
	if err := a.st.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "pages", PageID: p.ID})
	if a.page != nil {
		t.Fatal("the deleted page stayed open")
	}
	hist, err := a.st.History(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, h := range hist {
		if c, err := doc.Decode(h.Content); err == nil && c.Find("t1") != nil {
			kept = true
		}
	}
	if !kept {
		t.Errorf("what was typed went with the page: history %+v", hist)
	}
}

func TestPageDeletedElsewhereKeepsTheTypedTitle(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := a.st.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := a.st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.page.title = "New"
	row := store.PageToRemote(p)
	row["deleted_at"] = "2026-10-30T00:00:00Z"
	if err := a.st.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "pages", PageID: p.ID})
	if a.page != nil {
		t.Fatal("the deleted page stayed open")
	}
	if got, err := a.st.Page(p.ID); err != nil || got.Title != "New" {
		t.Errorf("title %q, %v", got.Title, err)
	}
}

func TestClosingAPageDropsWhatWasStartedOnIt(t *testing.T) {
	a := templatesApp(t)
	first, err := a.st.CreatePage(store.NewPageOptions{Title: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.st.CreatePage(store.NewPageOptions{Title: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(first.ID)
	(&host{a: a}).Comment("b1", "quoted")
	a.dialogs.inline = &inlinePrompt{blockID: "b1"}
	a.dialogs.embedOpen, a.dialogs.linkOpen, a.dialogs.sourceOpen, a.dialogs.chartOpen = true, true, true, true
	a.dialogs.chartEditor = a.page.editor
	a.openPage(second.ID)
	if a.page == nil || a.page.id != second.ID {
		t.Fatal("the second page did not open")
	}
	d := a.dialogs
	if d.newComment != nil || d.inline != nil {
		t.Error("the comment or the prompt started on the first page stayed")
	}
	if d.embedOpen || d.linkOpen || d.sourceOpen || d.chartOpen || d.chartEditor != nil {
		t.Error("a dialog changing the first page's blocks stayed open")
	}
}

func TestOpeningTheOpenPageClosesSettings(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.settingsOpen = true
	a.openPage(p.ID)
	if a.settingsOpen {
		t.Error("Settings stayed open")
	}
}

func TestRemovedSharedPageCloses(t *testing.T) {
	a := templatesApp(t)
	old := "2026-01-01T00:00:00Z"
	if err := a.st.PutRemotePage(map[string]any{"id": "shared", "space_id": "theirs", "title": "Shared", "sort_key": "V",
		"created_at": old, "updated_at": old}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage("shared")
	if a.page == nil {
		t.Fatal("the page did not open")
	}
	a.page.live.Changed()
	if _, err := a.st.RemoveSharedPages("shared"); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "*"})
	if a.page != nil || a.route.Kind != "home" {
		t.Fatalf("the removed page stayed open: %+v", a.route)
	}
	if _, err := a.st.ContentRow("shared"); err == nil || a.st.HasPending("page_content", "shared") {
		t.Error("the removed page was saved back")
	}
}

func TestTitleSavesWithoutATimer(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	a.focusTitle(true)
	for range 3 {
		tt.Frame()
	}
	tt.Type("After")
	tt.Frame()
	stored := func() string {
		q, err := a.st.Page(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return q.Title
	}
	if got := stored(); got != "After" || a.page.titleTimer != nil {
		t.Errorf("stored %q, a timer to come %v", got, a.page.titleTimer != nil)
	}
	time.Sleep(700 * time.Millisecond)
	tt.Type(" all")
	tt.Frame()
	if got := stored(); got != "After all" || a.page.savedTitle != "After all" {
		t.Errorf("stored %q, saved %q", got, a.page.savedTitle)
	}
}

func TestLaterWaitsWithAWindow(t *testing.T) {
	a := templatesApp(t)
	posted := make(chan func(), 2)
	a.post = func(fn func()) { posted <- fn }
	ran := 0
	stopped := a.later(time.Millisecond, func() { ran += 10 })
	if !stopped.Stop() {
		<-posted
	}
	if a.later(20*time.Millisecond, func() { ran++ }) == nil || ran != 0 {
		t.Fatalf("ran %d before the wait", ran)
	}
	select {
	case fn := <-posted:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("never posted")
	}
	if ran != 1 {
		t.Errorf("ran %d", ran)
	}
}

func TestViewIsPublished(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "p1", Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown("ship it now"))
	p, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{n})})
	if err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	a.openPage(p.ID)
	if a.page == nil {
		t.Fatal("the page did not open")
	}
	view := func() *store.OpenView {
		t.Helper()
		v, err := st.OpenView()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := view(); v == nil || v.PageID != p.ID || v.Selection.Text != "" {
		t.Fatalf("opened: %+v", v)
	}

	a.page.editor.Select(editor.Pos{ID: "p1", Off: 5}, editor.Pos{ID: "p1", Off: 7})
	if v := view(); v == nil || v.Selection.Text != "it" || v.Selection.EndOffset != 7 {
		t.Fatalf("selected: %+v", v)
	}
	a.page.editor.SetColor("#ef4444ff")
	if v := view(); v == nil || v.Selection.Color != "#ef4444ff" {
		t.Fatalf("colored: %+v", v)
	}

	if !a.closePage() {
		t.Fatal("the page did not close")
	}
	if v := view(); v != nil {
		t.Fatalf("closed: %+v", v)
	}
	if last, _ := st.Value(store.LastPageKey); last != p.ID {
		t.Fatalf("last page %q", last)
	}
}

func TestLinkToADeletedPageStaysHere(t *testing.T) {
	a := templatesApp(t)
	here, _ := a.st.CreatePage(store.NewPageOptions{Title: "Here"})
	gone, _ := a.st.CreatePage(store.NewPageOptions{Title: "Gone"})
	if _, err := a.st.DeletePage(gone.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(here.ID)
	for _, id := range []string{gone.ID, "no-such-page"} {
		a.toasts = nil
		a.openLink(id, "", -1)
		if a.page == nil || a.page.id != here.ID || a.route != (Route{Kind: "page", ID: here.ID}) {
			t.Fatalf("a link to %s left the page: %+v", id, a.route)
		}
		if len(a.toasts) != 1 || a.toasts[0].text != locales.EditorPageNotFound() {
			t.Errorf("said %+v", a.toasts)
		}
	}
	made, _ := a.st.CreatePage(store.NewPageOptions{Title: "Made elsewhere"})
	if a.openPage(made.ID); a.page == nil || a.page.id != made.ID {
		t.Errorf("a page not listed yet does not open: %+v", a.route)
	}
}

func TestTitleRenamedElsewhereIsKept(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Before"})
	a.reloadAll()
	a.openPage(p.ID)
	if err := a.st.RenamePage(p.ID, "Renamed elsewhere"); err != nil {
		t.Fatal(err)
	}
	a.newPage("", "")
	if q, _ := a.st.Page(p.ID); q.Title != "Renamed elsewhere" {
		t.Errorf("a new page renamed the open one back to %q", q.Title)
	}

	a.openPage(p.ID)
	a.page.title = "Typed here"
	if err := a.st.RenamePage(p.ID, "Renamed again"); err != nil {
		t.Fatal(err)
	}
	a.newPage("", "")
	if q, _ := a.st.Page(p.ID); q.Title != "Typed here" {
		t.Errorf("the title typed was not saved: %q", q.Title)
	}

	a.openPage(p.ID)
	a.page.title = "Typed again"
	a.saveTitle()
	if a.saveTitle(); a.page.title != "Typed again" {
		t.Errorf("saved twice before the pages were read again, the title is %q", a.page.title)
	}
}
