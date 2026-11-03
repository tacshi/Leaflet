package live

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// textEditor is an editor with no view, as the iOS app's is to the core.
// onDoc, when set, runs as the content is next asked for, once.
type textEditor struct {
	c        *doc.Content
	readOnly bool
	onDoc    func()
}

func (e *textEditor) Doc() *doc.Content {
	if f := e.onDoc; f != nil {
		e.onDoc = nil
		f()
	}
	return e.c
}
func (e *textEditor) Replace(c *doc.Content)          { e.c = c }
func (e *textEditor) SetReadOnly(readOnly bool)       { e.readOnly = readOnly }
func (e *textEditor) ApplyOp(op doc.Op, _ bool) error { return e.c.Apply(op) }
func (e *textEditor) ApplyOps(ops []doc.Op) error {
	for _, op := range ops {
		if err := e.c.Apply(op); err != nil {
			return err
		}
	}
	return nil
}

// typeIn sets the text of block p1, as typing it would.
func (e *textEditor) typeIn(text string) { e.c.Find("p1").SetDelta(doc.FromMarkdown(text)) }
func para(id, text string) *doc.Node {
	n := &doc.Node{ID: id, Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown(text))
	return n
}

// What is typed is saved shortly after, through Post; an edit stored from
// outside meanwhile merges into it, and both are kept.
func TestTypedAndOutsideEditsAreBothKept(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	pg, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{para("p1", "one"), para("p2", "two")})})
	if err != nil {
		t.Fatal(err)
	}
	posted := make(chan func(), 1)
	saved := 0
	p, content := Open(st, pg.ID, Options{Post: func(fn func()) { posted <- fn }, Saved: func() { saved++ }})
	ed := &textEditor{c: content}
	p.Attach(ed)

	ed.typeIn("one, typed")
	p.Edited()
	outside := st.Content(pg.ID)
	outside.Find("p2").SetDelta(doc.FromMarkdown("two, by the agent"))
	if _, _, err := st.SaveContent(pg.ID, outside); err != nil {
		t.Fatal(err)
	}
	select {
	case fn := <-posted:
		fn()
	case <-time.After(5 * SaveDelay):
		t.Fatal("the edit was never saved")
	}
	text := st.Content(pg.ID).PlainText()
	if !strings.Contains(text, "one, typed") || !strings.Contains(text, "two, by the agent") || p.Dirty() || saved != 1 {
		t.Errorf("stored %q, dirty %v, saved %d times", text, p.Dirty(), saved)
	}
	// Closed, a save still waiting does not run.
	ed.typeIn("one, after closing")
	p.Edited()
	p.Close()
	(<-posted)()
	if strings.Contains(st.Content(pg.ID).PlainText(), "after closing") {
		t.Error("a closed page saved")
	}
}

// Dirty hears edits come to wait and stop waiting, whoever saves them: a
// save called directly, as a version named or an export runs one, stops
// the timer, and must still say so.
func TestDirtyHearsEverySave(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	pg, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{para("p1", "one")})})
	if err != nil {
		t.Fatal(err)
	}
	var heard []bool
	p, content := Open(st, pg.ID, Options{Post: func(func()) {}, Dirty: func(dirty bool) { heard = append(heard, dirty) }})
	ed := &textEditor{c: content}
	p.Attach(ed)

	ed.typeIn("one, typed")
	p.Edited()
	p.Edited()
	if !p.Save() {
		t.Fatal("not saved")
	}
	p.Changed()
	p.Save()
	if got := fmt.Sprint(heard); got != "[true false true false]" {
		t.Errorf("heard %s", got)
	}
}

// openStore is a store in memory with one page of blocks.
func openStore(t *testing.T, blocks ...*doc.Node) (*store.Store, store.Page) {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	pg, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	return st, pg
}

// storeRow puts content in the page's row as it is, read or not.
func storeRow(t *testing.T, st *store.Store, pageID, content string) {
	t.Helper()
	if _, err := st.DB().Exec(`UPDATE page_contents SET content = ? WHERE page_id = ?`, content, pageID); err != nil {
		t.Fatal(err)
	}
}

const damaged = `{"document": {"children": [`

// Content that can't be read opens as an empty page that is never saved
// over it, and opens as it is once a sync brings content that can.
func TestUnreadableContentOpensOnceReadable(t *testing.T) {
	st, pg := openStore(t, para("p1", "one"))
	storeRow(t, st, pg.ID, damaged)
	p, content := Open(st, pg.ID, Options{Post: func(func()) {}, CanEdit: func() bool { return true }})
	ed := &textEditor{c: content, readOnly: true}
	p.Attach(ed)
	if !p.Unreadable() {
		t.Fatal("damaged content opened as readable")
	}
	p.Changed()
	if !p.Save() {
		t.Error("a page that can't be read waits to be saved")
	}
	if row, _ := st.ContentRow(pg.ID); row.Content != damaged {
		t.Errorf("saved over the damaged content: %s", row.Content)
	}
	storeRow(t, st, pg.ID, doc.FromBlocks([]*doc.Node{para("p1", "one, synced")}).Encode())
	p.External()
	if p.Unreadable() || ed.readOnly || !strings.Contains(ed.c.PlainText(), "one, synced") {
		t.Errorf("unreadable %v, read-only %v, showing %q", p.Unreadable(), ed.readOnly, ed.c.PlainText())
	}
}

// Content that can't be read coming in leaves the page as it shows, read
// only and never saved over, its edits not yet saved kept in history.
func TestUnreadableContentComingIn(t *testing.T) {
	st, pg := openStore(t, para("p1", "one"))
	p, content := Open(st, pg.ID, Options{Post: func(func()) {}})
	ed := &textEditor{c: content}
	p.Attach(ed)
	ed.typeIn("one, typed")
	p.Changed()
	storeRow(t, st, pg.ID, damaged)
	p.External()
	if !p.Unreadable() || !p.ReadOnly() || !ed.readOnly || p.Dirty() {
		t.Errorf("unreadable %v, read-only %v, dirty %v", p.Unreadable(), p.ReadOnly(), p.Dirty())
	}
	history, err := st.History(pg.ID)
	if err != nil || len(history) != 1 || history[0].Reason != "autosave" || !strings.Contains(history[0].Content, "one, typed") {
		t.Errorf("history %+v, %v", history, err)
	}
	p.Changed()
	p.Save()
	if row, _ := st.ContentRow(pg.ID); row.Content != damaged {
		t.Errorf("saved over the damaged content: %s", row.Content)
	}
}

// A block typed in and stored from outside both merges to one side, the
// other kept as a conflict, and the merge waits to be saved.
func TestEditsMergedKeepTheSideThatLost(t *testing.T) {
	st, pg := openStore(t, para("p1", "one"))
	p, content := Open(st, pg.ID, Options{Post: func(func()) {}})
	ed := &textEditor{c: content}
	p.Attach(ed)
	ed.typeIn("one, typed")
	p.Changed()
	outside := st.Content(pg.ID)
	outside.Find("p1").SetDelta(doc.FromMarkdown("one, from outside"))
	if _, _, err := st.SaveContent(pg.ID, outside); err != nil {
		t.Fatal(err)
	}
	p.External()
	conflicts, err := st.Conflicts()
	if err != nil || len(conflicts) != 1 || conflicts[0].BlockID != "p1" || !p.Dirty() {
		t.Errorf("conflicts %+v, %v, dirty %v", conflicts, err, p.Dirty())
	}
}

// A save that finds the page stored over since it merged tries again,
// merging that in, rather than writing over it.
func TestSaveTriesAgainOverAStoreInBetween(t *testing.T) {
	st, pg := openStore(t, para("p1", "one"), para("p2", "two"))
	p, content := Open(st, pg.ID, Options{Post: func(func()) {}})
	ed := &textEditor{c: content}
	p.Attach(ed)
	ed.typeIn("one, typed")
	p.Changed()
	// The page is stored from outside just as the save reads what to save.
	ed.onDoc = func() {
		outside := st.Content(pg.ID)
		outside.Find("p2").SetDelta(doc.FromMarkdown("two, from outside"))
		if _, _, err := st.SaveContent(pg.ID, outside); err != nil {
			t.Fatal(err)
		}
	}
	if !p.Save() {
		t.Fatal("not saved")
	}
	if text := st.Content(pg.ID).PlainText(); !strings.Contains(text, "one, typed") || !strings.Contains(text, "two, from outside") || p.Dirty() {
		t.Errorf("stored %q, dirty %v", text, p.Dirty())
	}
}

// A change from outside the editor shows and is stored at once, with what
// was typed before it; a page the user can't change takes none.
func TestChangeIsShownAndStored(t *testing.T) {
	st, pg := openStore(t, para("p1", "one"))
	p, content := Open(st, pg.ID, Options{Post: func(func()) {}})
	ed := &textEditor{c: content}
	p.Attach(ed)
	ed.typeIn("one, typed")
	p.Changed()
	c := ed.c.Copy()
	_ = c.Insert(doc.Position{At: "end"}, para("p2", "two"))
	p.Change(c)
	if got := st.Content(pg.ID).PlainText(); got != "one, typed\ntwo" || ed.c.PlainText() != got || p.Dirty() {
		t.Errorf("stored %q, showing %q, dirty %v", got, ed.c.PlainText(), p.Dirty())
	}
	p.SetReadOnly(true)
	p.Change(doc.FromBlocks([]*doc.Node{para("p3", "three")}))
	if got := st.Content(pg.ID).PlainText(); got != "one, typed\ntwo" || ed.c.PlainText() != got {
		t.Errorf("read-only: stored %q, showing %q", got, ed.c.PlainText())
	}
}

// An agent run's ops go into the open page, all of them.
func TestRunsOpsReachTheOpenPage(t *testing.T) {
	st, pg := openStore(t, para("p1", "one"))
	p, content := Open(st, pg.ID, Options{Post: func(func()) {}})
	p.Attach(&textEditor{c: content})
	ops := []doc.Op{doc.InsertOp(doc.Position{After: "p1"}, para("a1", "first")), doc.InsertOp(doc.Position{After: "a1"}, para("a2", "second"))}
	if err := p.ApplyOps(ops); err != nil {
		t.Fatal(err)
	}
	if got := p.Doc().PlainText(); got != "one\nfirst\nsecond" {
		t.Errorf("page %q", got)
	}
}

// A page whose content goes from this device, as when its share is
// withdrawn, waits to be saved no more, and says the edits not saved are
// lost: nothing is left to save over. Its content back, it opens again.
func TestARemovedPageWaitsNoMore(t *testing.T) {
	st, pg := openStore(t, para("p1", "one"))
	var told []string
	p, content := Open(st, pg.ID, Options{Post: func(func()) {}, Notify: func(m string) { told = append(told, m) }})
	ed := &textEditor{c: content}
	p.Attach(ed)
	ed.typeIn("one, typed")
	p.Edited()
	if _, err := st.DB().Exec(`DELETE FROM page_contents WHERE page_id = ?`, pg.ID); err != nil {
		t.Fatal(err)
	}
	if !p.Save() || p.Dirty() || !p.ReadOnly() || len(told) != 1 {
		t.Errorf("dirty %v, read-only %v, told %q", p.Dirty(), p.ReadOnly(), told)
	}
	if _, err := st.ContentRow(pg.ID); err == nil {
		t.Error("the content came back")
	}
	// Back, as a page shared again, it opens as it is.
	if _, _, err := st.SaveContent(pg.ID, doc.FromBlocks([]*doc.Node{para("p1", "one, shared again")})); err != nil {
		t.Fatal(err)
	}
	p.External()
	if p.ReadOnly() || !strings.Contains(ed.c.PlainText(), "shared again") {
		t.Errorf("read-only %v, showing %q", p.ReadOnly(), ed.c.PlainText())
	}
}

// racingEditor stores the page from outside each time its content is
// asked for, while race is set, so every save finds it stored over.
type racingEditor struct {
	textEditor
	st   *store.Store
	id   string
	race bool
	n    int
}

func (e *racingEditor) Doc() *doc.Content {
	if e.race {
		e.n++
		outside := e.st.Content(e.id)
		outside.Find("p2").SetDelta(doc.FromMarkdown(fmt.Sprintf("two, from outside %d", e.n)))
		if _, _, err := e.st.SaveContent(e.id, outside); err != nil {
			panic(err)
		}
	}
	return e.c
}

// A save stored over on every try saves again shortly after, rather than
// leaving the edits for an edit to come.
func TestSaveLosingEveryTryComesBack(t *testing.T) {
	st, pg := openStore(t, para("p1", "one"), para("p2", "two"))
	posted := make(chan func(), 10)
	p, content := Open(st, pg.ID, Options{Post: func(fn func()) { posted <- fn }})
	ed := &racingEditor{textEditor: textEditor{c: content}, st: st, id: pg.ID, race: true}
	p.Attach(ed)
	ed.typeIn("one, typed")
	p.Changed()
	if p.Save() {
		t.Fatal("saved over a store every try")
	}
	ed.race = false
	select {
	case fn := <-posted:
		fn()
	case <-time.After(5 * SaveDelay):
		t.Fatal("the edits were never saved again")
	}
	if text := st.Content(pg.ID).PlainText(); !strings.Contains(text, "one, typed") || !strings.Contains(text, "from outside") || p.Dirty() {
		t.Errorf("stored %q, dirty %v", text, p.Dirty())
	}
}
