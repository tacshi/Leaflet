package store

import (
	"testing"

	"leaflet/internal/doc"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestKeyBetween(t *testing.T) {
	a := KeyBetween("", "")
	b := KeyBetween(a, "")
	c := KeyBetween(a, b)
	if !(a < c && c < b) {
		t.Errorf("keys %q %q %q", a, c, b)
	}
	first := KeyBetween("", a)
	if !(first < a) {
		t.Errorf("first %q not before %q", first, a)
	}
	for i := 0; i < 50; i++ {
		next := KeyBetween(a, c)
		if !(a < next && next < c) {
			t.Fatalf("step %d: %q not between %q and %q", i, next, a, c)
		}
		c = next
	}
}

func TestPagesAndContent(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := s.CreatePage(NewPageOptions{ParentID: p.ID, Title: "Notes"})
	c := s.Content(p.ID)
	c.Children[0].SetDelta(doc.Delta{{Text: "Ship the editor"}})
	if _, changed, err := s.SaveContent(p.ID, c); err != nil || !changed {
		t.Fatalf("save %v %v", changed, err)
	}
	if _, changed, _ := s.SaveContent(p.ID, s.Content(p.ID)); changed {
		t.Error("saving the same content changed it")
	}
	hits, _ := s.Search("editor", 10)
	if len(hits) != 1 || hits[0].PageID != p.ID {
		t.Errorf("hits %+v", hits)
	}
	// One outbox entry per changed row.
	n := 0
	entries, _ := s.Pending()
	for _, e := range entries {
		if e.Entity == "page_content" && e.EntityID == p.ID {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d page_content entries", n)
	}
	if err := s.MovePage(p.ID, kid.ID, "", ""); err != ErrMoveIntoSelf {
		t.Errorf("move into own child: %v", err)
	}
	ids, _ := s.DeletePage(p.ID)
	if len(ids) != 2 {
		t.Errorf("deleted %v", ids)
	}
	if pages, _ := s.Pages(); len(pages) != 0 {
		t.Errorf("pages left %d", len(pages))
	}
	s.RestorePages(ids)
	if pages, _ := s.Pages(); len(pages) != 2 {
		t.Errorf("restored %d", len(pages))
	}
}

func TestComments(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "x"})
	root, _ := s.AddComment(p.ID, "b1", "Is this right?", "", "quote", "", "")
	s.AddComment(p.ID, "b1", "Yes", root.ID, "", "agent", "Claude")
	threads, _ := s.Threads(p.ID)
	if len(threads) != 1 || len(threads[0].Replies) != 1 || threads[0].Replies[0].AuthorName != "Claude" {
		t.Fatalf("threads %+v", threads)
	}
	s.SetResolved(root.ID, true)
	threads, _ = s.Threads(p.ID)
	if !threads[0].Root.Resolved {
		t.Error("not resolved")
	}
}
