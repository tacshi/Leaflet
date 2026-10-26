package app

import (
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
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
