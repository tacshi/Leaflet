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

type textEditor struct {
	c        *doc.Content
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

func (f *pagesFixture) openPage(id string) *textEditor {
	lp, c := live.Open(f.st, id, live.Options{Post: func(func()) {}})
	ed := &textEditor{c: c}
	lp.Attach(ed)
	f.open[id] = lp
	return ed
}

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
	if err := f.EditPage(p.ID, func(*doc.Content) bool { return false }); err != nil || ed.replaced != 1 {
		t.Errorf("%v, %d changes", err, ed.replaced)
	}
}

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

func TestSetFileNotes(t *testing.T) {
	f := newPagesFixture(t)
	closed := f.page(t, "Closed", "", fileBlock("a", ""))
	if written, cut, err := f.SetFileNotes(closed.ID, "a", "f1", "0:05 Hello", "", "moss"); written != NotesSaved || cut || err != nil {
		t.Fatal(written, cut, err)
	}
	if n := f.st.Content(closed.ID).Find("a"); n.Str("notes") != "0:05 Hello" || n.Str(doc.TranscribedBy) != "moss" {
		t.Errorf("closed page: %v", n.Data)
	}
	open := f.page(t, "Open", "", fileBlock("b", "old"))
	ed := f.openPage(open.ID)
	var steps []string
	f.Step = func(page, block string) { steps = append(steps, page+"/"+block) }
	if written, cut, err := f.SetFileNotes(open.ID, "b", "f1", "0:05 Hello", "old", "moss"); written != NotesSaved || cut || err != nil {
		t.Fatal(written, cut, err)
	}
	if got := ed.c.Find("b").Str("notes"); got != "0:05 Hello" || ed.replaced != 0 {
		t.Errorf("open page: %q, %d outside changes", got, ed.replaced)
	}
	if n := f.st.Content(open.ID).Find("b"); n.Str("notes") != "0:05 Hello" || n.Str(doc.TranscribedBy) != "moss" {
		t.Errorf("open page stored %v", n.Data)
	}
	if len(steps) != 1 || steps[0] != open.ID+"/b" {
		t.Errorf("steps %q", steps)
	}
}

func TestSetFileNotesKeepsNewWriting(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Notes", "", fileBlock("a", "written since"))
	if written, cut, err := f.SetFileNotes(p.ID, "a", "f1", "0:05 Hello", "", "moss"); written != NotesSaved || cut || err != nil {
		t.Fatal(written, cut, err)
	}
	if got := f.st.Content(p.ID).Find("a").Str("notes"); got != "written since\n\n0:05 Hello" {
		t.Errorf("notes %q", got)
	}
}

func TestSetFileNotesBlockGone(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Notes", "", block("p1", "one"), fileBlock("a", ""))
	for _, open := range []bool{false, true} {
		if open {
			f.openPage(p.ID)
		}
		for _, at := range []struct{ block, file string }{{"gone", "f1"}, {"a", "f2"}} {
			f.told = nil
			if written, _, err := f.SetFileNotes(p.ID, at.block, at.file, "0:05 Hello", "", "moss"); written != NotesGone || err != nil {
				t.Errorf("open %v, %v: %v, %v", open, at, written, err)
			}
			if len(f.told) != 1 || f.told[0] != locales.RecFileBlockGone() {
				t.Errorf("open %v, %v: told %q", open, at, f.told)
			}
		}
	}
	if got := f.content(p.ID).Find("a").Str("notes"); got != "" {
		t.Errorf("another file's transcript went in: %q", got)
	}
}

func (f *pagesFixture) fail(t *testing.T) {
	t.Helper()
	for _, q := range []string{
		`CREATE TRIGGER full_insert BEFORE INSERT ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
		`CREATE TRIGGER full_update BEFORE UPDATE ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`,
	} {
		if _, err := f.st.DB().Exec(q); err != nil {
			t.Error(err)
		}
	}
}

func TestSetFileNotesNotSaved(t *testing.T) {
	for _, open := range []bool{false, true} {
		f := newPagesFixture(t)
		p := f.page(t, "Notes", "", fileBlock("a", ""))
		if open {
			f.openPage(p.ID)
		}
		f.fail(t)
		if written, _, err := f.SetFileNotes(p.ID, "a", "f1", "0:05 Hello", "", "moss"); written != NotesFailed || err == nil {
			t.Errorf("open %v: %v, %v", open, written, err)
		}
		if got := f.st.Content(p.ID).Find("a").Str("notes"); got != "" {
			t.Errorf("open %v: stored %q", open, got)
		}
	}
}

func TestSetFileNotesFitsAfterNewWriting(t *testing.T) {
	for _, open := range []bool{false, true} {
		f := newPagesFixture(t)
		since := strings.Repeat("x", doc.MaxNotes-20)
		p := f.page(t, "Notes", "", fileBlock("a", since), fileBlock("b", strings.Repeat("x", doc.MaxNotes)))
		if open {
			f.openPage(p.ID)
		}
		written, cut, err := f.SetFileNotes(p.ID, "a", "f1", "0:05 Hello\n0:06 And a line too long to fit", "", "moss")
		got := f.st.Content(p.ID).Find("a").Str("notes")
		if written != NotesSaved || !cut || err != nil || got != since+"\n\n0:05 Hello" {
			t.Errorf("open %v: %v, cut %v, %v, %d characters ending %q", open, written, cut, err, len(got), got[len(got)-20:])
		}
		written, _, err = f.SetFileNotes(p.ID, "b", "f1", "0:05 Hello", "", "moss")
		if written != NotesFailed || err == nil || err.Error() != locales.RecFileNotesFull() {
			t.Errorf("open %v: notes full: %v, %v", open, written, err)
		}
		if got := f.st.Content(p.ID).Find("b").Str("notes"); len(got) != doc.MaxNotes {
			t.Errorf("open %v: full notes now %d characters", open, len(got))
		}
	}
}

func TestEditPageOpenNotSaved(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Notes", "", block("p1", "one"))
	ed := f.openPage(p.ID)
	f.fail(t)
	err := f.EditPage(p.ID, func(c *doc.Content) bool {
		return c.Insert(doc.Position{At: "end"}, block("p2", "two")) == nil
	})
	if err == nil || err.Error() != locales.AppErrorNotSaved() {
		t.Errorf("saved with %v", err)
	}
	if ed.c.PlainText() != "one\ntwo" || f.st.Content(p.ID).PlainText() != "one" {
		t.Errorf("the editor shows %q, stored %q", ed.c.PlainText(), f.st.Content(p.ID).PlainText())
	}
}
