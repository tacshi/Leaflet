package agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/web"
)

// linkHost is a host whose pages keep the cards of their subpages in the
// store, as the app's do, and which counts them. What it is given to run
// on the main thread runs one at a time, as there: a reply streamed and
// the run's end, which share the reply.
type linkHost struct {
	readingHost
	st     *store.Store
	linked *[]string
	main   *sync.Mutex
}

func (h linkHost) Update(_ string, fn func()) {
	h.main.Lock()
	defer h.main.Unlock()
	fn()
}

func (h linkHost) Pages() Pages { return linkPages{storePages{h.st}, h.linked} }

type linkPages struct {
	storePages
	linked *[]string
}

func (p linkPages) LinkSubpage(parent, id, _ string) error {
	*p.linked = append(*p.linked, parent+">"+id)
	return nil
}

// workspaceWorld is the user's pages for the workspace agent: one of
// theirs, one in a folder, one they share with a friend, a template, and
// one of someone else's space.
type workspaceWorld struct {
	st                                  *store.Store
	h                                   *Hub
	linked                              []string
	mine, shared, inside, theirs, model store.Page
	folder                              store.Folder
}

func newWorkspaceWorld(t *testing.T) *workspaceWorld {
	t.Helper()
	st := openStore(t)
	w := &workspaceWorld{st: st}
	me := st.Session().UserID
	var err error
	if w.folder, err = st.CreateFolder("Clippings"); err != nil {
		t.Fatal(err)
	}
	if w.mine, err = st.CreatePage(store.NewPageOptions{Title: "Mine", Content: page(), Tags: []string{"plan"}}); err != nil {
		t.Fatal(err)
	}
	if w.inside, err = st.CreatePage(store.NewPageOptions{Title: "Filed", FolderID: w.folder.ID}); err != nil {
		t.Fatal(err)
	}
	if w.shared, err = st.CreatePage(store.NewPageOptions{Title: "Shared"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembers(w.shared.ID, []store.Member{{PageID: w.shared.ID, UserID: me, Role: "owner"}, {PageID: w.shared.ID, UserID: "friend", Role: "editor"}}); err != nil {
		t.Fatal(err)
	}
	if w.theirs, err = st.CreatePage(store.NewPageOptions{Title: "Theirs", SpaceID: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	if w.model, err = st.CreatePage(store.NewPageOptions{Title: "Template", Template: true}); err != nil {
		t.Fatal(err)
	}
	w.h = NewHub(st, nil, linkHost{st: st, linked: &w.linked, main: &sync.Mutex{}})
	return w
}

func (w *workspaceWorld) executor(reading bool) *Executor {
	return &Executor{Ctx: w.h.workspaceTools(context.Background(), "Test", store.Settings{AgentsReadOtherPages: reading})}
}

// The workspace agent sees the user's own pages, templates and others'
// spaces left out, reads none that other people share, and changes none
// that someone else shares, nor puts a page in one; what it does on the
// others is one undo step.
func TestWorkspaceKeepsToTheUsersPages(t *testing.T) {
	w := newWorkspaceWorld(t)
	x := w.executor(true)
	listed, _ := x.Call("list_pages", map[string]any{})["pages"].([]map[string]any)
	var titles []string
	for _, p := range listed {
		titles = append(titles, p["title"].(string))
		if p["title"] == "Shared" && (p["shared"] != true || p["shared_with_people"] != true) {
			t.Errorf("the shared page lists as %v", p)
		}
	}
	slices.Sort(titles)
	if want := []string{"Filed", "Mine", "Shared"}; !slices.Equal(titles, want) {
		t.Errorf("listed %q", titles)
	}
	for _, id := range []string{w.theirs.ID, w.model.ID, "nope"} {
		if out := x.Call("read_page", map[string]any{"page_id": id}); out["error"] == nil {
			t.Errorf("read %s: %v", id, out)
		}
	}
	// Refused, it read nothing: a run that asks it first keeps its web.
	if y := w.executor(true); y.Call("read_page", map[string]any{"page_id": w.shared.ID})["error"] == nil || y.armed {
		t.Errorf("a page other people write in was read, or armed the run's guard: %v", y.armed)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"create_page", map[string]any{"title": "In it", "parent_id": w.shared.ID}},
		{"move_page", map[string]any{"page_id": w.shared.ID, "folder_id": w.folder.ID}},
		{"move_page", map[string]any{"page_id": w.mine.ID, "parent_id": w.shared.ID}},
		{"rename_page", map[string]any{"page_id": w.shared.ID, "title": "Ours"}},
		{"set_page_tags", map[string]any{"page_id": w.shared.ID, "tags": []any{"x"}}},
		{"move_page", map[string]any{"page_id": w.theirs.ID}},
		{"move_page", map[string]any{"page_id": w.mine.ID, "parent_id": w.mine.ID}},
		{"create_page", map[string]any{"title": "Both", "parent_id": w.mine.ID, "folder_id": w.folder.ID}},
		{"create_page", map[string]any{"title": "Lost", "folder_id": "nope"}},
		{"set_page_tags", map[string]any{"page_id": w.mine.ID, "tags": "plan"}},
	} {
		if out := x.Call(c.tool, c.args); out["error"] == nil {
			t.Errorf("%s %v: %v", c.tool, c.args, out)
		}
	}
	if len(x.Ops) != 0 {
		t.Fatalf("refused calls made %+v", x.Ops)
	}

	made := x.Call("create_page", map[string]any{"title": "Notes", "parent_id": w.mine.ID, "markdown": "- one"})
	notes, _ := made["page_id"].(string)
	folder, _ := x.Call("create_folder", map[string]any{"name": " Reading "})["folder_id"].(string)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"move_page", map[string]any{"page_id": w.inside.ID, "folder_id": folder}},
		{"rename_page", map[string]any{"page_id": w.inside.ID, "title": "Filed away"}},
		{"set_page_tags", map[string]any{"page_id": w.inside.ID, "tags": []any{"Read", "read", " later "}}},
	} {
		if out := x.Call(c.tool, c.args); out["error"] != nil {
			t.Fatalf("%s: %v", c.tool, out)
		}
	}
	if p, _ := w.st.Page(notes); p.ParentID != w.mine.ID || !slices.Equal(w.linked, []string{w.mine.ID + ">" + notes}) {
		t.Errorf("subpage %+v, linked %q", p, w.linked)
	}
	filed, _ := w.st.Page(w.inside.ID)
	if filed.FolderID != folder || filed.Title != "Filed away" || len(filed.Tags) != 2 {
		t.Errorf("filed %+v", filed)
	}
	var summaries []string
	for _, e := range x.Edits {
		summaries = append(summaries, e.Summary)
	}
	if want := []string{"Created subpage “Notes”", "Created folder “Reading”", "Moved “Filed”", "Renamed “Filed” to “Filed away”", "Tagged “Filed away”"}; !slices.Equal(summaries, want) {
		t.Errorf("edits %q", summaries)
	}
	if x.Edits[2].PageID != w.inside.ID {
		t.Errorf("the move opens %q", x.Edits[2].PageID)
	}

	s := w.h.Session(WorkspaceID)
	s.Runs = []*Run{{ID: "r1", Status: "done", Ops: x.Ops, Edits: x.Edits}}
	if err := w.h.UndoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	back, _ := w.st.Page(w.inside.ID)
	gone, _ := w.st.Page(notes)
	f, _ := w.st.Folder(folder)
	if back.FolderID != w.folder.ID || back.Title != "Filed" || len(back.Tags) != 0 || gone.DeletedAt.IsZero() || f.DeletedAt.IsZero() {
		t.Errorf("after Undo: filed %+v, notes deleted %v, folder deleted %v", back, !gone.DeletedAt.IsZero(), !f.DeletedAt.IsZero())
	}
}

// A page with a public link another device serves, as the server says by
// its own clock, is left as it is, as one shared, and the open page is
// told of as shared; the server gone, the lease it gave stands. One the
// server no longer lists has ended, offline after too.
func TestWorkspaceLeavesPagesPublicElsewhere(t *testing.T) {
	w := newWorkspaceWorld(t)
	var mu sync.Mutex
	listed, down := true, false
	hs := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case down:
			http.Error(rw, `{"message":"down"}`, http.StatusInternalServerError)
		case listed && r.URL.Path == "/rest/v1/pages" && strings.Contains(r.URL.RawQuery, "public_link_until=gt.now"):
			_, _ = fmt.Fprintf(rw, `[{"id":%q,"public_link_until":%q}]`, w.mine.ID, time.Now().Add(time.Hour).Format(time.RFC3339Nano))
		default:
			_, _ = rw.Write([]byte(`[]`))
		}
	}))
	defer hs.Close()
	set := func(l, d bool) {
		mu.Lock()
		defer mu.Unlock()
		listed, down = l, d
	}
	w.h = NewHub(w.st, remote.SignedInForTest(hs.URL, w.st, w.st.Session().UserID), linkHost{st: w.st, linked: &w.linked, main: &sync.Mutex{}})
	for _, c := range []struct {
		listed, down, public bool
		title                string
	}{{true, false, true, "Ours"}, {true, true, true, "Ours"}, {false, false, false, "Mine again"}, {false, true, false, "Mine"}} {
		set(c.listed, c.down)
		x := w.executor(true)
		out := x.Call("rename_page", map[string]any{"page_id": w.mine.ID, "title": c.title})
		if refused := strings.Contains(fmt.Sprint(out["error"]), "shared with others"); refused != c.public {
			t.Errorf("listed %v, down %v: renaming refused %v: %v", c.listed, c.down, refused, out)
		}
		if out := x.Call("set_page_tags", map[string]any{"page_id": w.inside.ID, "tags": []any{c.title}}); out["error"] != nil {
			t.Errorf("listed %v, down %v: a page of no public link: %v", c.listed, c.down, out)
		}
	}

	set(true, false)
	f := &fakeModel{replies: []map[string]any{{"content": "It is shared."}}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if err := w.st.PutProvider(store.Provider{ID: "local", Preset: "custom", Name: "Local", BaseURL: srv.URL, AuthKind: "none", DefaultModel: "m", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	done := make(chan *Run, 1)
	w.h.Start(WorkspaceID, RunOptions{Surface: "workspace", Instruction: "Is this page shared?", OpenPage: w.mine.ID, Done: func(r *Run) { done <- r }})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not end")
	}
	if user := f.requests[0]["messages"].([]any)[1].(map[string]any)["content"].(string); !strings.Contains(user, `"shared":true`) {
		t.Errorf("the open page is told of as private: %q", user)
	}
}

// The workspace agent lists at most maxListedPages of the user's pages,
// saying how many more there are, for search_pages to find.
func TestWorkspaceListsAtMostSoManyPages(t *testing.T) {
	var pages []WorkspacePage
	for i := range maxListedPages + 3 {
		pages = append(pages, WorkspacePage{ID: fmt.Sprint("p-", i), Title: "Page", Tags: []string{}})
	}
	x := &Executor{Ctx: ToolContext{Content: doc.Empty(), PageID: WorkspaceID, Workspace: &Workspace{Reading: true,
		Pages: func() []WorkspacePage { return pages }, Folders: func() []map[string]any { return []map[string]any{} }}}}
	out := x.Call("list_pages", map[string]any{})
	if listed, _ := out["pages"].([]map[string]any); len(listed) != maxListedPages || out["more_pages"] != 3 {
		t.Errorf("listed %d, more %v", len(listed), out["more_pages"])
	}
}

// What other people wrote in a page shared with them is not read: a
// search finds the page, but shows none of its text.
func TestWorkspaceSearchShowsNoTextOfPeoplesPages(t *testing.T) {
	world := map[string]WorkspacePage{"p-1": {ID: "p-1", Title: "Plan"}, "p-3": {ID: "p-3", Title: "Shared", Shared: true, People: true}}
	x := &Executor{Ctx: ToolContext{Content: doc.Empty(), PageID: WorkspaceID, Workspace: &Workspace{Reading: true,
		Page: func(id string) (WorkspacePage, bool) {
			p, ok := world[id]
			return p, ok
		},
		Search: func(string) []map[string]any {
			return []map[string]any{{"page_id": "p-1", "title": "Plan", "snippet": "mine"}, {"page_id": "p-3", "title": "Shared", "snippet": "theirs"}}
		},
	}}}
	results, _ := x.Call("search_pages", map[string]any{"query": "x"})["results"].([]map[string]any)
	if len(results) != 2 || results[0]["snippet"] != "mine" || results[1]["snippet"] != nil {
		t.Errorf("results %v", results)
	}
}

// With reading off the agent reads none of the user's pages, but makes
// and files them.
func TestWorkspaceReadingOffReadsNoPage(t *testing.T) {
	w := newWorkspaceWorld(t)
	x := w.executor(false)
	for _, tool := range []string{"list_pages", "search_pages", "read_page"} {
		if out := x.Call(tool, map[string]any{"query": "plan", "page_id": w.mine.ID}); out["error"] == nil {
			t.Errorf("%s with reading off: %v", tool, out)
		}
	}
	if out := x.Call("create_page", map[string]any{"title": "Notes"}); out["error"] != nil {
		t.Errorf("create_page: %v", out)
	}
	var names []string
	for _, d := range WorkspaceToolDefinitions(false) {
		names = append(names, d["function"].(map[string]any)["name"].(string))
	}
	if slices.Contains(names, "list_pages") || slices.Contains(names, "read_page") || !slices.Contains(names, "create_page") {
		t.Errorf("tools %q", names)
	}
}

// Once the agent reads the user's pages, the web is closed to the run,
// and what it writes keeps to the URLs the user gave: what it read could
// go out in a request, or in a link the app loads.
func TestWorkspaceWebClosesAfterReadingPages(t *testing.T) {
	w := newWorkspaceWorld(t)
	x := w.executor(true)
	x.Ctx.ReadWeb = func(link string) (web.Page, error) { return web.Page{URL: link, Title: "Page"}, nil }
	x.Ctx.Request = "Save https://example.com/given.png"
	x.Call("search_pages", map[string]any{"query": "plan"})
	if out := x.Call("read_web_page", map[string]any{"url": "https://example.com/a"}); !strings.Contains(toolText(out), "cannot use the web") {
		t.Errorf("read the web after reading pages: %v", out)
	}
	image := func(url string) []any {
		return []any{map[string]any{"type": "image", "image": map[string]any{"url": url}}}
	}
	if out := x.Call("create_page", map[string]any{"title": "A", "blocks": image("https://evil.example/x.png")}); out["error"] == nil {
		t.Errorf("wrote a URL the user did not give: %v", out)
	}
	if out := x.Call("create_page", map[string]any{"title": "B", "blocks": image("https://example.com/given.png")}); out["error"] != nil {
		t.Errorf("refused the user's URL: %v", out)
	}
}

func toolText(out map[string]any) string {
	s, _ := out["error"].(string)
	return s
}

// A workspace run asked for in the chat makes its pages and ends, its job
// of no page, and no page's history changed; Undo takes its pages back.
func TestWorkspaceRun(t *testing.T) {
	w := newWorkspaceWorld(t)
	f := &fakeModel{replies: []map[string]any{
		{"content": "", "tool_calls": []any{call("create_page", map[string]any{"title": "Article", "folder_id": w.folder.ID, "markdown": "# Article\n\nBody"})}},
		{"content": "Saved it in Clippings."},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if err := w.st.PutProvider(store.Provider{ID: "local", Preset: "custom", Name: "Local", BaseURL: srv.URL, AuthKind: "none", DefaultModel: "m", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	done := make(chan *Run, 1)
	w.h.Start(WorkspaceID, RunOptions{Surface: "workspace", Instruction: "Save this in Clippings", OpenPage: w.mine.ID, Done: func(r *Run) { done <- r }})
	var run *Run
	select {
	case run = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not end")
	}
	if run.Status != "done" || run.Reply != "Saved it in Clippings." || len(run.Ops) != 1 {
		t.Fatalf("run %+v", run)
	}
	user := f.requests[0]["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.HasPrefix(user, `Open page (JSON): {"id":"`+w.mine.ID+`","title":"Mine"}`) || strings.Contains(user, "Page title") {
		t.Errorf("user turn %q", user)
	}
	id := run.Ops[0].Page.ID
	if p, err := w.st.Page(id); err != nil || p.FolderID != w.folder.ID {
		t.Errorf("page %+v, %v", p, err)
	}
	var jobPage string
	if err := w.st.DB().QueryRow(`SELECT page_id FROM ai_jobs WHERE id = ?`, run.ID).Scan(&jobPage); err != nil || jobPage != "" {
		t.Errorf("job of page %q, %v", jobPage, err)
	}
	var history int
	if err := w.st.DB().QueryRow(`SELECT COUNT(*) FROM page_history WHERE page_id = ?`, WorkspaceID).Scan(&history); err != nil || history != 0 {
		t.Errorf("%d history rows of the workspace, %v", history, err)
	}
	if err := w.h.UndoRun(WorkspaceID, run.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.st.Page(id); p.DeletedAt.IsZero() {
		t.Error("Undo left the page")
	}
}

// The workspace prompt has the rules the server's has: no page's blocks
// changed, shared pages left as they are and those of other people's
// writing unread, text saved whole, and the web read before the user's
// pages.
func TestWorkspacePromptHasTheServersRules(t *testing.T) {
	p := WorkspacePrompt("Claude", true, true, true, true)
	for _, rule := range []string{"You do not change what a page says", "you leave as it is", "you cannot read either", "not a summary, unless", "Read the web first", "list_pages and search_pages"} {
		if !strings.Contains(p, rule) {
			t.Errorf("no %q in the prompt", rule)
		}
	}
	if p := WorkspacePrompt("Claude", false, true, false, false); !strings.Contains(p, "does not let agents read their pages") || strings.Contains(p, "read_web_page") {
		t.Errorf("reading and the web off:\n%s", p)
	}
}

// A cloud workspace run's job names the page open as the user asked, and
// those the app has a public link open to, whose leases its server may not
// have yet.
func TestWorkspaceJobRequest(t *testing.T) {
	got := Request{Instruction: "File it", OpenPage: &OpenPage{ID: "p1", Title: "Plan"}, PublicPages: []string{"p2"}}.JobRequest()
	if got["open_page_id"] != "p1" || !slices.Equal(got["public_page_ids"].([]string), []string{"p2"}) {
		t.Errorf("request %v", got)
	}
	if got := (Request{Instruction: "Sum it up"}).JobRequest(); got["open_page_id"] != nil || got["public_page_ids"] != nil {
		t.Errorf("a page's run's request %v", got)
	}
}

// Undo and Redo take each of a run's changes by what those before it in
// the same step did: a page made, then tagged, renamed and filed, comes
// back with all of them, and a page moved and renamed twice goes back to
// where it was, as it was called.
func TestUndoAndRedoFollowTheRunsSteps(t *testing.T) {
	w := newWorkspaceWorld(t)
	x := w.executor(true)
	made, _ := x.Call("create_page", map[string]any{"title": "Daily"})["page_id"].(string)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"set_page_tags", map[string]any{"page_id": made, "tags": []any{"daily"}}},
		{"rename_page", map[string]any{"page_id": made, "title": "Today"}},
		{"move_page", map[string]any{"page_id": made, "folder_id": w.folder.ID}},
		{"move_page", map[string]any{"page_id": w.mine.ID, "folder_id": w.folder.ID}},
		{"move_page", map[string]any{"page_id": w.mine.ID, "parent_id": w.inside.ID}},
		{"rename_page", map[string]any{"page_id": w.mine.ID, "title": "Mine, filed"}},
		{"rename_page", map[string]any{"page_id": w.mine.ID, "title": "Mine, filed again"}},
	} {
		if out := x.Call(c.tool, c.args); out["error"] != nil {
			t.Fatalf("%s: %v", c.tool, out)
		}
	}
	state := func() string {
		d, _ := w.st.Page(made)
		m, _ := w.st.Page(w.mine.ID)
		return fmt.Sprintf("made %q in %q tagged %q, deleted %v; mine %q in %q, %q",
			d.Title, d.FolderID, d.Tags, !d.DeletedAt.IsZero(), m.Title, m.ParentID, m.FolderID)
	}
	after := state()
	w.h.Session(WorkspaceID).Runs = []*Run{{ID: "r1", Status: "done", Ops: x.Ops, Edits: x.Edits}}

	if err := w.h.UndoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, want := state(), `made "Daily" in "" tagged [], deleted true; mine "Mine" in "", ""`; got != want {
		t.Errorf("after Undo: %s, want %s", got, want)
	}
	if err := w.h.RedoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != after {
		t.Errorf("after Redo: %s, want %s", got, after)
	}
}

// Undo leaves a folder the run made where the user filed a page of theirs
// in it since, the page with it: the run's own pages leave it all the
// same.
func TestUndoKeepsAFolderTheUserFiledIn(t *testing.T) {
	w := newWorkspaceWorld(t)
	x := w.executor(true)
	folder, _ := x.Call("create_folder", map[string]any{"name": "Reading"})["folder_id"].(string)
	x.Call("move_page", map[string]any{"page_id": w.mine.ID, "folder_id": folder})
	made, _ := x.Call("create_page", map[string]any{"title": "Saved", "folder_id": folder})["page_id"].(string)
	if len(x.Ops) != 3 {
		t.Fatalf("ops %+v", x.Ops)
	}
	w.h.Session(WorkspaceID).Runs = []*Run{{ID: "r1", Status: "done", Ops: x.Ops, Edits: x.Edits}}
	if err := w.st.MovePage(w.inside.ID, "", folder, ""); err != nil {
		t.Fatal(err)
	}

	if err := w.h.UndoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	f, _ := w.st.Folder(folder)
	m, _ := w.st.Page(w.mine.ID)
	filed, _ := w.st.Page(w.inside.ID)
	saved, _ := w.st.Page(made)
	if !f.DeletedAt.IsZero() || m.FolderID != "" || filed.FolderID != folder || saved.DeletedAt.IsZero() {
		t.Errorf("folder deleted %v, mine in %q, the user's in %q, saved deleted %v",
			!f.DeletedAt.IsZero(), m.FolderID, filed.FolderID, !saved.DeletedAt.IsZero())
	}
}

// Redo of a run that made a folder and a page in it puts the page back in
// the folder, which Undo deleted after it.
func TestRedoPutsAPageBackInItsFolder(t *testing.T) {
	w := newWorkspaceWorld(t)
	x := w.executor(true)
	folder, _ := x.Call("create_folder", map[string]any{"name": "Reading"})["folder_id"].(string)
	made, _ := x.Call("create_page", map[string]any{"title": "Saved", "folder_id": folder})["page_id"].(string)
	w.h.Session(WorkspaceID).Runs = []*Run{{ID: "r1", Status: "done", Ops: x.Ops, Edits: x.Edits}}
	if err := w.h.UndoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	if f, _ := w.st.Folder(folder); f.DeletedAt.IsZero() {
		t.Fatal("Undo left the folder")
	}
	if err := w.h.RedoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	f, _ := w.st.Folder(folder)
	p, _ := w.st.Page(made)
	if !f.DeletedAt.IsZero() || !p.DeletedAt.IsZero() || p.FolderID != folder {
		t.Errorf("after Redo: folder deleted %v, page %+v", !f.DeletedAt.IsZero(), p)
	}
}

// Undo of a rename gives an untitled page back no title, not the word the
// app shows for one.
func TestUndoOfARenameKeepsAPageUntitled(t *testing.T) {
	w := newWorkspaceWorld(t)
	blank, err := w.st.CreatePage(store.NewPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	x := w.executor(true)
	if out := x.Call("rename_page", map[string]any{"page_id": blank.ID, "title": "Trip"}); out["error"] != nil {
		t.Fatal(out)
	}
	if len(x.Ops) != 1 || x.Ops[0].PrevTitle != "" || x.Edits[0].Summary != "Renamed “Untitled” to “Trip”" {
		t.Fatalf("ops %+v, edits %+v", x.Ops, x.Edits)
	}
	w.h.Session(WorkspaceID).Runs = []*Run{{ID: "r1", Status: "done", Ops: x.Ops, Edits: x.Edits}}
	if err := w.h.UndoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.st.Page(blank.ID); p.Title != "" {
		t.Errorf("after Undo titled %q", p.Title)
	}
}

// cardlessHost is a host whose pages take no subpage's card.
type cardlessHost struct {
	readingHost
	st *store.Store
}

func (h cardlessHost) Pages() Pages { return cardless{storePages{h.st}} }

type cardless struct{ storePages }

func (cardless) LinkSubpage(string, string, string) error {
	return errors.New("the page cannot be changed")
}

// A subpage whose card cannot go in its parent is made all the same, and
// kept for Undo: the agent is not told to make it again.
func TestASubpageWithoutItsCardIsKept(t *testing.T) {
	w := newWorkspaceWorld(t)
	w.h = NewHub(w.st, nil, cardlessHost{st: w.st})
	x := w.executor(true)
	out := x.Call("create_page", map[string]any{"title": "Notes", "parent_id": w.mine.ID})
	made, _ := out["page_id"].(string)
	if out["error"] != nil || made == "" || len(x.Ops) != 1 {
		t.Fatalf("made %v, ops %+v", out, x.Ops)
	}
	if p, err := w.st.Page(made); err != nil || p.ParentID != w.mine.ID {
		t.Errorf("subpage %+v, %v", p, err)
	}
}

// What a run asks of the main thread and gives up on before its turn
// comes is never done: the run no longer keeps it, to undo.
func TestWorkGivenUpOnIsNotDone(t *testing.T) {
	host := queueHost{q: make(chan func(), 1)}
	h := NewHub(openStore(t), nil, host)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	if err := h.onMain(ctx, func() error { ran = true; return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("given up on: %v", err)
	}
	(<-host.q)()
	if ran {
		t.Error("it ran once given up on")
	}
}

// Who else has the pages, read once for them all as list_pages reads
// them, is what a page's own check finds: a page shared, one inside it,
// and one a guest commented on, of which only the first two are had by
// other people.
func TestPrivacyOfAllPagesIsEachPagesOwn(t *testing.T) {
	w := newWorkspaceWorld(t)
	under, err := w.st.CreatePage(store.NewPageOptions{Title: "Under", ParentID: w.shared.ID})
	if err != nil {
		t.Fatal(err)
	}
	guested, err := w.st.CreatePage(store.NewPageOptions{Title: "Guested"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.AddComment(guested.ID, "b1", "Nice", "", "", "guest", "Visitor"); err != nil {
		t.Fatal(err)
	}
	pages, _ := w.st.Pages()
	private, people := w.h.privacy(pages)
	for _, p := range pages {
		if got, want := private(p), w.h.private(p); got != want {
			t.Errorf("%q private %v, alone %v", p.Title, got, want)
		}
		if got, want := people(p), sharedWithOthers(w.st, p); got != want {
			t.Errorf("%q had by others %v, alone %v", p.Title, got, want)
		}
	}
	if private(under) || private(guested) || private(w.shared) || !private(w.mine) {
		t.Errorf("under %v, guested %v, shared %v, mine %v", private(under), private(guested), private(w.shared), private(w.mine))
	}
	if !people(under) || people(guested) || !people(w.shared) || people(w.mine) {
		t.Errorf("had by others: under %v, guested %v, shared %v, mine %v", people(under), people(guested), people(w.shared), people(w.mine))
	}
}
