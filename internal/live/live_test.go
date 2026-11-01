package live

import (
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// textEditor is an editor with no view, as the iOS app's is to the core.
type textEditor struct {
	c        *doc.Content
	readOnly bool
}

func (e *textEditor) Doc() *doc.Content               { return e.c }
func (e *textEditor) Replace(c *doc.Content)          { e.c = c }
func (e *textEditor) SetReadOnly(readOnly bool)       { e.readOnly = readOnly }
func (e *textEditor) ApplyOp(op doc.Op, _ bool) error { return e.c.Apply(op) }
func (e *textEditor) ApplyOps(ops []doc.Op) error     { return e.ApplyOp(ops[0], true) }
func (e *textEditor) set(id, text string)             { e.c.Find(id).SetDelta(doc.FromMarkdown(text)) }
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

	ed.set("p1", "one, typed")
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
	ed.set("p1", "one, after closing")
	p.Edited()
	p.Close()
	(<-posted)()
	if strings.Contains(st.Content(pg.ID).PlainText(), "after closing") {
		t.Error("a closed page saved")
	}
}
