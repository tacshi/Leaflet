package core

import (
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

func (f *pagesFixture) conflict(t *testing.T, id string, c doc.Conflict) store.ConflictRow {
	t.Helper()
	if err := f.st.AddConflict(id, c); err != nil {
		t.Fatal(err)
	}
	rows, err := f.st.Conflicts()
	if err != nil || len(rows) == 0 {
		t.Fatalf("conflicts %+v, %v", rows, err)
	}
	return rows[0]
}

func TestSidesOf(t *testing.T) {
	long := strings.Repeat("word ", 20)
	s := SidesOf(store.ConflictRow{LocalNode: block("b1", "Mine\nhere"), RemoteNode: block("b1", long), Kept: "remote"})
	if s.Kept != strings.Repeat("word ", 8)[:39]+"…" || s.Other != "Mine here" || s.Deleted != "" || s.KeptHere {
		t.Errorf("%+v", s)
	}
	s = SidesOf(store.ConflictRow{RemoteNode: block("b1", "Edited there"), Kept: "remote"})
	if s.Kept != "Edited there" || s.Other != "" || s.Deleted != "here" {
		t.Errorf("%+v", s)
	}
	s = SidesOf(store.ConflictRow{LocalNode: block("b1", "Edited here"), Kept: "local"})
	if s.Kept != "Edited here" || s.Deleted != "there" || !s.KeptHere {
		t.Errorf("%+v", s)
	}
}

func TestResolveConflict(t *testing.T) {
	for _, open := range []bool{false, true} {
		f := newPagesFixture(t)
		p := f.page(t, "Plans", "", block("b1", "Theirs"), block("b2", "Edited there"))
		var ed *textEditor
		if open {
			ed = f.openPage(p.ID)
			ed.c.Find("b2").SetDelta(doc.FromMarkdown("Edited there, typed"))
			f.open[p.ID].Changed()
		}
		cf := f.conflict(t, p.ID, doc.Conflict{BlockID: "b1", Local: block("b1", "Mine"), Remote: block("b1", "Theirs"), LocalEditedAt: 1, RemoteEditedAt: 2})
		if err := f.ResolveConflict(cf.ID, UseThisDevice); err != nil {
			t.Fatal(err)
		}
		if got := f.st.Content(p.ID).PlainText(); !strings.HasPrefix(got, "Mine\nEdited there") {
			t.Errorf("open %v: put back %q", open, got)
		}
		if open && !strings.HasPrefix(ed.c.PlainText(), "Mine") {
			t.Errorf("the editor shows %q", ed.c.PlainText())
		}
		h := versions(t, f, p.ID, "conflict")
		if len(h) != 1 || !strings.HasPrefix(doc.MustDecode(h[0].Content).PlainText(), "Theirs\nEdited there") {
			t.Errorf("open %v: kept %+v", open, h)
		}

		cf = f.conflict(t, p.ID, doc.Conflict{BlockID: "b2", Remote: block("b2", "Edited there")})
		if err := f.ResolveConflict(cf.ID, DeleteBlock); err != nil {
			t.Fatal(err)
		}
		if got := f.st.Content(p.ID).PlainText(); got != "Mine" {
			t.Errorf("open %v: deleted again, the page is %q", open, got)
		}
		if rows, _ := f.st.Conflicts(); len(rows) != 0 {
			t.Errorf("open %v: still %+v", open, rows)
		}
	}
}

func item(id, text string, kids ...*doc.Node) *doc.Node {
	n := block(id, text)
	n.Type = "bulleted_list"
	n.Children = kids
	return n
}

func TestResolveConflictKeepsWhatIsUnder(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Plans", "", item("x", "Theirs", item("a1", "child"), item("r1", "added there")))
	cf := f.conflict(t, p.ID, doc.Conflict{BlockID: "x", Local: item("x", "Mine", item("a1", "child")),
		Remote: item("x", "Theirs", item("a1", "child"), item("r1", "added there")), LocalEditedAt: 1, RemoteEditedAt: 2})
	if err := f.ResolveConflict(cf.ID, UseThisDevice); err != nil {
		t.Fatal(err)
	}
	if got := f.st.Content(p.ID).PlainText(); got != "Mine\nchild\nadded there" {
		t.Errorf("put back %q", got)
	}

	if err := f.EditPage(p.ID, func(c *doc.Content) bool {
		_ = c.Move("a1", doc.Position{At: "start"})
		c.Remove("x")
		return true
	}); err != nil {
		t.Fatal(err)
	}
	cf = f.conflict(t, p.ID, doc.Conflict{BlockID: "x", Local: item("x", "Mine", item("a1", "child")), Remote: item("x", "Theirs"), LocalEditedAt: 1, RemoteEditedAt: 2})
	if err := f.ResolveConflict(cf.ID, UseThisDevice); err != nil {
		t.Fatal(err)
	}
	if got := f.st.Content(p.ID).PlainText(); got != "child\nMine" {
		t.Errorf("put back after a delete %q", got)
	}
}

func TestResolveConflictWithTheOtherDevicesVersion(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Plans", "", block("b1", "Mine"))
	cf := f.conflict(t, p.ID, doc.Conflict{BlockID: "b1", Local: block("b1", "Mine"), Remote: block("b1", "Theirs"), LocalEditedAt: 2, RemoteEditedAt: 1})
	if !SidesOf(cf).KeptHere {
		t.Fatalf("kept %+v", cf)
	}
	if err := f.ResolveConflict(cf.ID, UseOtherDevice); err != nil {
		t.Fatal(err)
	}
	if got := f.st.Content(p.ID).PlainText(); got != "Theirs" {
		t.Errorf("put back %q", got)
	}
}

func TestResolveConflictRefused(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Plans", "", block("b1", "Theirs"))
	cf := f.conflict(t, p.ID, doc.Conflict{BlockID: "b1", Local: block("b1", "Mine"), Remote: block("b1", "Theirs"), LocalEditedAt: 1, RemoteEditedAt: 2})
	if err := f.ResolveConflict(cf.ID, "this-device"); err == nil {
		t.Error("resolved a way it doesn't know")
	}
	if err := f.ResolveConflict("gone", KeepMerged); err == nil {
		t.Error("resolved a conflict that is gone")
	}
	if _, err := f.st.DB().Exec(`UPDATE pages SET space_id = 'someone-elses' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.ResolveConflict(cf.ID, UseThisDevice); err == nil {
		t.Error("put back a block in a page only viewed")
	}
	if rows, _ := f.st.Conflicts(); len(rows) != 1 || f.st.Content(p.ID).PlainText() != "Theirs" {
		t.Errorf("conflicts %+v, page %q", rows, f.st.Content(p.ID).PlainText())
	}
}

func TestResolveConflictWithNoContentHere(t *testing.T) {
	f := newPagesFixture(t)
	p := f.page(t, "Plans", "", block("b1", "Theirs"))
	cf := f.conflict(t, p.ID, doc.Conflict{BlockID: "b1", Local: block("b1", "Mine"), Remote: block("b1", "Theirs"), LocalEditedAt: 1, RemoteEditedAt: 2})
	if _, err := f.st.DB().Exec(`DELETE FROM page_contents WHERE page_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.ResolveConflict(cf.ID, UseThisDevice); err != nil {
		t.Fatal(err)
	}
	if c := f.st.Content(p.ID); c == nil || !strings.Contains(c.PlainText(), "Mine") {
		t.Errorf("the page is %v", c)
	}
	if rows, _ := f.st.Conflicts(); len(rows) != 0 {
		t.Errorf("still %+v", rows)
	}
}

func TestResolveConflictNotSaved(t *testing.T) {
	for _, typed := range []bool{false, true} {
		f := newPagesFixture(t)
		p := f.page(t, "Plans", "", block("b1", "Theirs"), block("b2", "Edited there"))
		ed := f.openPage(p.ID)
		if typed {
			ed.c.Find("b2").SetDelta(doc.FromMarkdown("Edited there, typed"))
			f.open[p.ID].Changed()
		}
		cf := f.conflict(t, p.ID, doc.Conflict{BlockID: "b1", Local: block("b1", "Mine"), Remote: block("b1", "Theirs"), LocalEditedAt: 1, RemoteEditedAt: 2})
		f.fail(t)
		if err := f.ResolveConflict(cf.ID, UseThisDevice); err == nil {
			t.Errorf("typed %v: resolved", typed)
		}
		if rows, _ := f.st.Conflicts(); len(rows) != 1 {
			t.Errorf("typed %v: conflicts %+v", typed, rows)
		}
		if got := f.st.Content(p.ID).PlainText(); got != "Theirs\nEdited there" {
			t.Errorf("typed %v: stored %q", typed, got)
		}
	}
}
