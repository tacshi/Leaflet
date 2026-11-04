package agents

import (
	"fmt"
	"slices"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/web"
)

// A page the agent makes of its own is the user's, at the top level of
// their space or in their folder, wherever the page the run is on is: one
// shared from another space is its owner's.
func TestCreatePageMakesAPageOfTheUsersOwn(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	own := st.Session().SpaceID
	f, err := st.CreateFolder("Clippings")
	if err != nil {
		t.Fatal(err)
	}
	theirs := store.Folder{ID: "f-theirs", SpaceID: "space-other", Name: "Theirs"}
	if err := st.PutFolder(theirs); err != nil {
		t.Fatal(err)
	}
	shared, err := st.CreatePage(store.NewPageOptions{SpaceID: "space-other", Title: "Shared", Content: page()})
	if err != nil {
		t.Fatal(err)
	}
	x := &Executor{Ctx: PageTools(st, shared.ID, "Shared", "Test", st.Content(shared.ID), true)}

	listed, _ := x.Call("list_folders", map[string]any{})["folders"].([]map[string]any)
	if len(listed) != 1 || listed[0]["id"] != f.ID || listed[0]["name"] != "Clippings" {
		t.Errorf("folders %v", listed)
	}
	out := x.Call("create_page", map[string]any{"title": " Article ", "folder_id": f.ID, "markdown": "# Title\n\nBody"})
	id, _ := out["page_id"].(string)
	p, err := st.Page(id)
	if err != nil {
		t.Fatalf("out %v: %v", out, err)
	}
	if p.SpaceID != own || p.ParentID != "" || p.FolderID != f.ID || p.Title != "Article" || p.Template {
		t.Errorf("page %+v", p)
	}
	if c := st.Content(id); len(c.Children) != 2 || c.Children[0].Type != "heading" {
		t.Errorf("content %s", c.Encode())
	}
	if last := x.Edits[len(x.Edits)-1]; last.PageID != id || last.Summary != "Created page “Article”" {
		t.Errorf("edit %+v", last)
	}
	if op := x.Ops[len(x.Ops)-1]; op.Page == nil || op.Page.FolderID != f.ID || op.Page.ParentID != "" {
		t.Errorf("op %+v", op)
	}
	// Nothing changed on the page the run is on.
	if len(x.Ctx.Content.Children) != len(page().Children) {
		t.Errorf("the page has %d blocks", len(x.Ctx.Content.Children))
	}

	for _, args := range []map[string]any{
		{"title": "A", "folder_id": theirs.ID},
		{"title": "A", "folder_id": "nope"},
		{"title": "A", "folder_id": 5},
		{"title": "A", "markdown": "x", "blocks": []any{map[string]any{"type": "paragraph", "text": "y"}}},
	} {
		if out := x.Call("create_page", args); out["error"] == nil {
			t.Errorf("%v made %v", args, out)
		}
	}
	x.Ctx.ReadOnly = true
	if out := x.Call("create_page", map[string]any{"title": "A"}); out["error"] == nil {
		t.Errorf("a viewer's run made %v", out)
	}
}

// A subpage's card goes at the end of the page it is in, as the app puts
// one there, before the op that made it: Undo takes both away.
func TestCreateSubpageLinksItHere(t *testing.T) {
	x := executor(page())
	x.Ctx.CreateSubpage = func(string, []*doc.Node) (string, error) { return "page-2", nil }
	out := x.Call("create_subpage", map[string]any{"title": "Notes", "markdown": "- a\n- b"})
	if out["page_id"] != "page-2" {
		t.Fatalf("out %v", out)
	}
	last := x.Ctx.Content.Children[len(x.Ctx.Content.Children)-1]
	if last.Type != "subpage" || last.Str("page_id") != "page-2" {
		t.Errorf("last block %s %v", last.Type, last.Data)
	}
	if len(x.Ops) != 2 || x.Ops[0].Op != "insert" || x.Ops[1].Op != "create_page" || x.Ops[1].Page.ParentID != "page" {
		t.Errorf("ops %+v", x.Ops)
	}
	var summaries []string
	for _, e := range x.Edits {
		summaries = append(summaries, e.Summary)
	}
	if want := []string{"Inserted a subpage link", "Created subpage “Notes”"}; !slices.Equal(summaries, want) {
		t.Errorf("edits %q", summaries)
	}
	if x.Edits[0].PageID != "" || x.Edits[1].PageID != "page-2" || !slices.Equal(x.Edits[0].BlockIDs, []string{last.ID}) {
		t.Errorf("edits %+v", x.Edits)
	}
}

// Listing the folders reads nothing someone else wrote: the web's links
// stay the run's to write, as they are before it reads another page.
func TestListFoldersKeepsTheWebsLinks(t *testing.T) {
	x := executor(page())
	x.Ctx.ReadWeb = func(link string) (web.Page, error) {
		return web.Page{URL: link, Title: "Article", Text: "See https://example.com/found.png", Links: []string{"https://example.com/found.png"}}, nil
	}
	x.Ctx.ListFolders = func() []map[string]any { return []map[string]any{{"id": "f1", "name": "Clippings"}} }
	x.Ctx.CreatePage = func(string, []*doc.Node, string) (string, error) { return "page-3", nil }
	x.Call("read_web_page", map[string]any{"url": "https://example.com/article"})
	if out := x.Call("list_folders", map[string]any{}); out["error"] != nil || x.readPages {
		t.Fatalf("out %v, read pages %v", out, x.readPages)
	}
	link := map[string]any{"type": "paragraph", "text": "See https://example.com/found.png"}
	if out := x.Call("create_page", map[string]any{"title": "Article", "folder_id": "f1", "blocks": []any{link}}); out["error"] != nil {
		t.Errorf("out %v", out)
	}
	x.Ctx.ListFolders = nil
	if out := x.Call("list_folders", map[string]any{}); out["error"] == nil {
		t.Errorf("listed folders with reading other pages off: %v", out)
	}
}

// Undo takes a subpage and its card away together, and Redo brings both
// back.
func TestUndoOfASubpageTakesItsCardAway(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan", Content: page()})
	if err != nil {
		t.Fatal(err)
	}
	x := &Executor{Ctx: PageTools(st, p.ID, "Plan", "Test", st.Content(p.ID), false)}
	out := x.Call("create_subpage", map[string]any{"title": "Notes"})
	sub, _ := out["page_id"].(string)
	if _, _, err := st.SaveContent(p.ID, x.Ctx.Content); err != nil {
		t.Fatal(err)
	}
	h := NewHub(st, nil, stubHost{})
	s := h.Session(p.ID)
	s.Runs = []*Run{{ID: "r1", Status: "done", Ops: x.Ops, Edits: x.Edits}}
	card := func() bool {
		found := false
		st.Content(p.ID).Walk(func(n, _ *doc.Node) { found = found || n.Type == "subpage" && n.Str("page_id") == sub })
		return found
	}
	gone := func() bool {
		made, err := st.Page(sub)
		return err != nil || !made.DeletedAt.IsZero()
	}
	if !card() || gone() {
		t.Fatalf("card %v, subpage gone %v", card(), gone())
	}

	if err := h.UndoRun(p.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if card() || !gone() {
		t.Errorf("after Undo: card %v, subpage gone %v", card(), gone())
	}
	if err := h.RedoRun(p.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if !card() || gone() {
		t.Errorf("after Redo: card %v, subpage gone %v", card(), gone())
	}
}

// Undo puts back what a run did to the pages around its page, and Redo
// does it again: a folder made, a page moved into it, renamed and tagged,
// and a page made, with the subpage added to it since, which goes with it
// and comes back with it.
func TestUndoOfPageOps(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	home, _ := st.CreatePage(store.NewPageOptions{Title: "Home"})
	f, _ := st.CreateFolder("Clippings")
	article, _ := st.CreatePage(store.NewPageOptions{Title: "Saved article", FolderID: f.ID, Tags: []string{"read"}})
	made, _ := st.CreatePage(store.NewPageOptions{Title: "Made"})
	later, _ := st.CreatePage(store.NewPageOptions{ParentID: made.ID, Title: "Later"})
	h := NewHub(st, nil, stubHost{})
	s := h.Session(home.ID)
	s.Runs = []*Run{{ID: "r1", Status: "done", Ops: []doc.Op{
		{Op: "create_folder", Folder: &doc.NewFolder{ID: f.ID, Name: "Clippings"}},
		{Op: "move_page", PageID: article.ID, Title: "Article", Place: &doc.PagePlace{FolderID: f.ID}, PrevPlace: &doc.PagePlace{}},
		{Op: "rename_page", PageID: article.ID, Title: "Saved article", PrevTitle: "Article"},
		{Op: "set_page_tags", PageID: article.ID, Title: "Saved article", Tags: []string{"read"}},
		{Op: "create_page", Page: &doc.NewPage{ID: made.ID, Title: "Made"}},
	}}}
	state := func() string {
		folder, _ := st.Folder(f.ID)
		a, _ := st.Page(article.ID)
		m, _ := st.Page(made.ID)
		l, _ := st.Page(later.ID)
		return fmt.Sprintf("folder %v, article %q in %q tagged %q, made %v, later %v",
			folder.DeletedAt.IsZero(), a.Title, a.FolderID, a.Tags, m.DeletedAt.IsZero(), l.DeletedAt.IsZero())
	}
	after := state()

	if err := h.UndoRun(home.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, want := state(), `folder false, article "Article" in "" tagged [], made false, later false`; got != want {
		t.Errorf("after Undo: %s, want %s", got, want)
	}
	if err := h.RedoRun(home.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != after {
		t.Errorf("after Redo: %s, want %s", got, after)
	}
}

// A page the user changed since the run keeps their change: Undo leaves
// it, and does the rest.
func TestUndoKeepsWhatTheUserChangedSince(t *testing.T) {
	st := openStore(t)
	home, _ := st.CreatePage(store.NewPageOptions{Title: "Home"})
	article, _ := st.CreatePage(store.NewPageOptions{Title: "Article", Tags: []string{"read"}})
	h := NewHub(st, nil, stubHost{})
	h.Session(home.ID).Runs = []*Run{{ID: "r1", Status: "done", Ops: []doc.Op{
		{Op: "rename_page", PageID: article.ID, Title: "Saved", PrevTitle: "Article"},
		{Op: "set_page_tags", PageID: article.ID, Title: "Saved", Tags: []string{"read"}},
	}}}
	// The user named it again since.
	if err := st.RenamePage(article.ID, "Mine"); err != nil {
		t.Fatal(err)
	}
	if err := h.UndoRun(home.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Page(article.ID); got.Title != "Mine" || len(got.Tags) != 0 {
		t.Errorf("page %q tagged %q", got.Title, got.Tags)
	}
}
