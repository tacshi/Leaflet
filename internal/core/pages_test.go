package core

import (
	"errors"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/live"
	"leaflet/internal/store"
	"leaflet/locales"
)

// textEditor is an editor with no view, as the iOS app's is to the core.
type textEditor struct {
	c *doc.Content
	// replaced counts the changes from outside it shows.
	replaced int
}

func (e *textEditor) Doc() *doc.Content      { return e.c }
func (e *textEditor) Replace(c *doc.Content) { e.c, e.replaced = c, e.replaced+1 }
func (e *textEditor) SetReadOnly(bool)       {}
func (e *textEditor) ApplyOp(op doc.Op, _ bool) error {
	return e.c.Apply(op)
}
func (e *textEditor) ApplyOps(ops []doc.Op) error {
	for _, op := range ops {
		if err := e.c.Apply(op); err != nil {
			return err
		}
	}
	return nil
}

// pagesFixture is Pages over a store of its own, with the pages openPage
// opened open in it, and what it told the user.
type pagesFixture struct {
	Pages
	st   *store.Store
	open map[string]*live.Page
	told []string
}

func newPagesFixture(t *testing.T) *pagesFixture {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	f := &pagesFixture{st: st, open: map[string]*live.Page{}}
	f.Pages = Pages{
		Core:   &Core{Store: st},
		Open:   func(id string) *live.Page { return f.open[id] },
		Notify: func(m string) { f.told = append(f.told, m) },
	}
	return f
}

// page makes a page of blocks, under parent ("" for none).
func (f *pagesFixture) page(t *testing.T, title, parent string, blocks ...*doc.Node) store.Page {
	t.Helper()
	o := store.NewPageOptions{Title: title, ParentID: parent}
	if len(blocks) > 0 {
		o.Content = doc.FromBlocks(blocks)
	}
	p, err := f.st.CreatePage(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// openPage opens page id as the app does, returning its editor.
func (f *pagesFixture) openPage(id string) *textEditor {
	lp, c := live.Open(f.st, id, live.Options{Post: func(func()) {}})
	ed := &textEditor{c: c}
	lp.Attach(ed)
	f.open[id] = lp
	return ed
}

// cards are the pages page id has cards for, as it is open or stored.
func (f *pagesFixture) cards(id string) []string {
	c := f.content(id)
	if c == nil {
		return nil
	}
	var out []string
	c.Walk(func(n, _ *doc.Node) {
		if n.Type == "subpage" {
			out = append(out, n.Str("page_id"))
		}
	})
	return out
}

func block(id, text string) *doc.Node {
	n := &doc.Node{ID: id, Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown(text))
	return n
}

// A page not open is changed over what is stored: content stored while
// the change is made, as by sync, gets the change made again over it,
// keeping both.
func TestEditPageStoredGoesOverWhatItRead(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Notes", "", block("q1", "one"))
	tries := 0
	err := f.EditPage(p.ID, func(c *doc.Content) bool {
		if tries++; tries == 1 {
			synced := f.st.Content(p.ID)
			synced.Find("q1").SetDelta(doc.FromMarkdown("one, synced"))
			if _, _, err := f.st.SaveContent(p.ID, synced); err != nil {
				t.Fatal(err)
			}
		}
		return c.Insert(doc.Position{At: "end"}, block("q2", "two, added")) == nil
	})
	if err != nil || tries != 2 {
		t.Fatalf("saved with %v after %d tries", err, tries)
	}
	if text := f.st.Content(p.ID).PlainText(); text != "one, synced\ntwo, added" {
		t.Errorf("stored %q, want both", text)
	}
}

// A page open is changed through its editor, which shows the change, and
// is stored with what was typed in it.
func TestEditPageOpenGoesThroughTheEditor(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Notes", "", block("p1", "one"))
	ed := f.openPage(p.ID)
	ed.c.Find("p1").SetDelta(doc.FromMarkdown("one, typed"))
	f.open[p.ID].Changed()
	err := f.EditPage(p.ID, func(c *doc.Content) bool {
		return c.Insert(doc.Position{At: "end"}, block("p2", "two")) == nil
	})
	if err != nil || ed.replaced != 1 || ed.c.PlainText() != "one, typed\ntwo" {
		t.Fatalf("%v: the editor shows %q, %d changes", err, ed.c.PlainText(), ed.replaced)
	}
	if got := f.st.Content(p.ID).PlainText(); got != "one, typed\ntwo" {
		t.Errorf("stored %q", got)
	}
	// An edit that changes nothing gives the editor nothing.
	if err := f.EditPage(p.ID, func(*doc.Content) bool { return false }); err != nil || ed.replaced != 1 {
		t.Errorf("%v, %d changes", err, ed.replaced)
	}
}

// A page the user can't change is left as it is, and content that can't
// be read is not saved over, open or not, but said to be unreadable.
func TestEditPageLeavesWhatItCannotChange(t *testing.T) {
	f := newPagesFixture(t)
	viewed := f.page(t, "Theirs", "", block("p1", "one"))
	if _, err := f.st.DB().Exec(`UPDATE pages SET space_id = 'someone-elses' WHERE id = ?`, viewed.ID); err != nil {
		t.Fatal(err)
	}
	edited := false
	if err := f.EditPage(viewed.ID, func(*doc.Content) bool { edited = true; return true }); err != nil || edited {
		t.Errorf("a page only viewed: %v, edited %v", err, edited)
	}

	p := f.page(t, "Newer", "", block("p1", "one"))
	if _, err := f.st.DB().Exec(`UPDATE page_contents SET content = '{"document": [' WHERE page_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, open := range []bool{false, true} {
		if open {
			f.openPage(p.ID)
		}
		err := f.EditPage(p.ID, func(c *doc.Content) bool { return c.Insert(doc.Position{At: "end"}, block("p2", "two")) == nil })
		if !errors.Is(err, store.ErrUnreadable) {
			t.Errorf("open %v: %v", open, err)
		}
		if row, _ := f.st.ContentRow(p.ID); !strings.HasPrefix(row.Content, `{"document": [`) {
			t.Errorf("open %v: saved over with %s", open, row.Content)
		}
	}
}

func fileBlock(id, notes string) *doc.Node {
	n := &doc.Node{ID: id, Type: "file", Data: map[string]any{"name": "call.m4a", "file_id": "f1"}}
	if notes != "" {
		n.Set("notes", notes)
	}
	return n
}

// A transcript goes in a closed page's file over what is stored, and in an
// open page's as one step of its own, which the app is told of.
func TestSetFileNotes(t *testing.T) {
	f := newPagesFixture(t)
	closed := f.page(t, "Closed", "", fileBlock("a", ""))
	if err := f.SetFileNotes(closed.ID, "a", "0:05 Hello", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.st.Content(closed.ID).Find("a").Str("notes"); got != "0:05 Hello" {
		t.Errorf("closed page: %q", got)
	}
	open := f.page(t, "Open", "", fileBlock("b", "old"))
	ed := f.openPage(open.ID)
	var steps []string
	f.Step = func(page, block string) { steps = append(steps, page+"/"+block) }
	if err := f.SetFileNotes(open.ID, "b", "0:05 Hello", "old"); err != nil {
		t.Fatal(err)
	}
	if got := ed.c.Find("b").Str("notes"); got != "0:05 Hello" || ed.replaced != 0 {
		t.Errorf("open page: %q, %d outside changes", got, ed.replaced)
	}
	if len(steps) != 1 || steps[0] != open.ID+"/b" {
		t.Errorf("steps %q", steps)
	}
}

// Notes written while a file transcribed stay, the transcript after them.
func TestSetFileNotesKeepsNewWriting(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Notes", "", fileBlock("a", "written since"))
	if err := f.SetFileNotes(p.ID, "a", "0:05 Hello", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.st.Content(p.ID).Find("a").Str("notes"); got != "written since\n\n0:05 Hello" {
		t.Errorf("notes %q", got)
	}
}

// A block gone by the time a transcript is done takes nothing, and says so,
// open or not.
func TestSetFileNotesBlockGone(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Notes", "", block("p1", "one"))
	if err := f.SetFileNotes(p.ID, "gone", "0:05 Hello", ""); err != nil {
		t.Fatal(err)
	}
	f.openPage(p.ID)
	if err := f.SetFileNotes(p.ID, "gone", "0:05 Hello", ""); err != nil {
		t.Fatal(err)
	}
	if len(f.told) != 2 || f.told[0] != locales.RecFileBlockGone() {
		t.Errorf("told %q", f.told)
	}
}
