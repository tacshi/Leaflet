package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

// An agent outside the app (leaflet mcp) edits the open page while the
// user types: the app's next save keeps both.
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

	// The user types in p1, not yet saved.
	a.page.editor.Content.Find("p1").SetDelta(doc.FromMarkdown("one, typed"))
	a.page.dirty = true
	// Meanwhile the agent rewrites p2 in the store.
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

// A block deleted here and edited elsewhere comes back as a conflict,
// stored with no node for this device; Delete it removes it again.
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
	a.resolveConflict(cf, nil)
	if text := st.Content(p.ID).PlainText(); text != "two" {
		t.Errorf("after Delete it the page holds %q", text)
	}
	if rows, _ := st.Conflicts(); len(rows) != 0 {
		t.Errorf("still listed: %+v", rows)
	}
}

// A save that fails keeps the edits waiting, for the page closing to try
// again; and an edit of a page not open goes only over what it read.
func TestSaveFailsAndRaces(t *testing.T) {
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
	other, err := st.CreatePage(store.NewPageOptions{Title: "Other", Content: doc.FromBlocks([]*doc.Node{para("q1", "one")})})
	if err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()

	// Sync writes the other page while it is being changed: the change
	// is made again over that, keeping both.
	tries := 0
	err = a.saveStored(other.ID, func(c *doc.Content) *doc.Content {
		if tries++; tries == 1 {
			synced := st.Content(other.ID)
			synced.Find("q1").SetDelta(doc.FromMarkdown("one, synced"))
			if _, _, err := st.SaveContent(other.ID, synced); err != nil {
				t.Fatal(err)
			}
		}
		_ = c.Insert(doc.Position{At: "end"}, para("q2", "two, added"))
		return c
	})
	if err != nil || tries != 2 {
		t.Fatalf("saved with %v after %d tries", err, tries)
	}
	if text := st.Content(other.ID).PlainText(); !strings.Contains(text, "one, synced") || !strings.Contains(text, "two, added") {
		t.Errorf("stored %q, want both", text)
	}

	a.openPage(p.ID)
	a.page.editor.Content.Find("p1").SetDelta(doc.FromMarkdown("one, typed"))
	a.page.dirty = true
	_ = st.Close()
	a.savePage()
	if !a.page.dirty {
		t.Error("a save that failed dropped the edits")
	}
}

// Leaving a page whose edits fail to save keeps it open with them, rather
// than dropping them; once saving works, it closes with them stored.
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
	a.page.dirty = true

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

// Stored content that can't be read opens read-only with a notice, and
// is never saved over; once a readable version comes, it opens as usual.
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
	if a.page == nil || !a.page.unreadable || !a.page.editor.ReadOnly || a.Editor(p.ID) != nil {
		t.Fatalf("opened as %+v", a.page)
	}
	tt := ui.NewTester(func(c *ui.Context) { a.pageNotices(c, p.ID) }, 800, 200)
	tt.Frame()
	if !tt.HasText(locales.PageUnreadable()) {
		t.Errorf("no notice in %v", tt.Texts())
	}
	a.page.editor.Content.Children[0].SetDelta(doc.FromMarkdown("typed"))
	a.page.dirty = true
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
	if a.page.unreadable || a.page.editor.ReadOnly || !strings.Contains(a.page.editor.Content.PlainText(), "restored") {
		t.Errorf("unreadable %v, read-only %v, shows %q", a.page.unreadable, a.page.editor.ReadOnly, a.page.editor.Content.PlainText())
	}
}

// Content brought into the open page that can't be read is not shown as
// an empty page and saved over: the editor keeps what it shows, read-only
// as an unreadable page opens, with what was typed kept in its history,
// and the page still closes.
func TestUnreadableContentBroughtInIsNotSavedOver(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{textNode("mine")})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.page.editor.Content.Children[0].SetDelta(doc.FromMarkdown("typed"))
	a.page.dirty = true
	const newer = `{"children": [{"id": "p1", "type": "paragraph", "delta": `
	if _, err := a.st.DB().Exec(`UPDATE page_contents SET content = ? WHERE page_id = ?`, newer, p.ID); err != nil {
		t.Fatal(err)
	}
	a.externalContent(p.ID)
	if got := a.page.editor.Content.PlainText(); got != "typed" {
		t.Errorf("the editor shows %q", got)
	}
	if !a.page.unreadable || !a.page.editor.ReadOnly {
		t.Errorf("unreadable %v, read-only %v", a.page.unreadable, a.page.editor.ReadOnly)
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

// A title that cannot be saved keeps the page open with it, as its text
// would.
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

// A title typed and not yet saved, its field left before the rename's
// wait is over, stays as other changes reload the pages; one renamed
// elsewhere, with nothing typed, shows the new title.
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

// The open page deleted on another device closes, with what was typed and
// not yet saved stored first, for restoring it to bring back.
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
	a.page.dirty = true
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

// The open page removed from this device, its share withdrawn, closes
// without being saved back.
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
	a.page.dirty = true
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
