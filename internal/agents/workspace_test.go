package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/web"
)

type linkHost struct {
	readingHost
	st     *store.Store
	linked *[]string
	main   *sync.Mutex
}

func (linkHost) Online() bool { return true }

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
	return &Executor{Ctx: w.h.workspaceTools(context.Background(), "Test", store.Settings{AgentsReadOtherPages: reading}, true)}
}

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
	if err := w.st.PutProvider(store.Provider{ID: "local", Preset: "custom", Name: "Local", BaseURL: srv.URL, AuthKind: "none", DefaultModel: "m"}); err != nil {
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

func TestWorkspaceListsAtMostSoManyPages(t *testing.T) {
	list := func(title string, n int) map[string]any {
		var pages []WorkspacePage
		for i := range n {
			p := WorkspacePage{ID: fmt.Sprint("p-", i), Title: title, Tags: []string{}}
			if len(title) > 10 {
				p.ID, p.ParentID, p.Tags = fmt.Sprintf("%08d-0000-0000-0000-000000000000", i), "00000000-0000-0000-0000-000000000000", []string{"work", "plans"}
			}
			pages = append(pages, p)
		}
		x := &Executor{Ctx: ToolContext{Content: doc.Empty(), PageID: WorkspaceID, Workspace: &Workspace{Reading: true,
			Pages:   func() ([]WorkspacePage, error) { return pages, nil },
			Folders: func() []map[string]any { return []map[string]any{{"id": "f-1", "name": "Clippings"}} }}}}
		return x.Call("list_pages", map[string]any{})
	}
	if out := list("Page", maxListedPages+3); len(out["pages"].([]map[string]any)) != maxListedPages || out["more_pages"] != 3 {
		t.Errorf("listed %d, more %v", len(out["pages"].([]map[string]any)), out["more_pages"])
	}
	out := list(strings.Repeat("Quarterly planning <notes> & ", 3), maxListedPages)
	listed := len(out["pages"].([]map[string]any))
	if listed == 0 || listed == maxListedPages || out["more_pages"] != maxListedPages-listed {
		t.Errorf("long titles: listed %d, more %v", listed, out["more_pages"])
	}
	raw, _ := json.Marshal(out)
	if units := len(utf16.Encode([]rune(string(raw)))); units > toolResultLimit {
		t.Errorf("the list takes %d units, past %d", units, toolResultLimit)
	}
}

func TestWorkspaceSearchShowsNoTextOfPeoplesPages(t *testing.T) {
	world := map[string]WorkspacePage{"p-1": {ID: "p-1", Title: "Plan"}, "p-3": {ID: "p-3", Title: "Shared", Shared: true, People: true}}
	x := &Executor{Ctx: ToolContext{Content: doc.Empty(), PageID: WorkspaceID, Workspace: &Workspace{Reading: true,
		Page: func(id string) (WorkspacePage, bool) {
			p, ok := world[id]
			return p, ok
		},
		Search: func(string) ([]map[string]any, error) {
			return []map[string]any{{"page_id": "p-1", "title": "Plan", "snippet": "mine"}, {"page_id": "p-3", "title": "Shared", "snippet": "theirs"},
				{"page_id": "p-new", "title": "Made since", "snippet": "unknown"}}, nil
		},
	}}}
	results, _ := x.Call("search_pages", map[string]any{"query": "x"})["results"].([]map[string]any)
	if len(results) != 3 || results[0]["snippet"] != "mine" || results[1]["snippet"] != nil || results[2]["snippet"] != nil {
		t.Errorf("results %v", results)
	}
}

func TestWorkspaceSearchShowsNoTextOfSites(t *testing.T) {
	w := newWorkspaceWorld(t)
	card := &doc.Node{Type: "link_preview", Data: map[string]any{"url": "https://example.com/a", "title": "Kiwi growers, read this first", "description": "Kiwi"}}
	video := &doc.Node{Type: "video", Data: map[string]any{"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "title": "Kiwi harvest", "notes": "0:00 Picking"}}
	note := &doc.Node{Type: "paragraph"}
	note.SetDelta(doc.FromMarkdown("Ask about the kiwi crop."))
	links, err := w.st.CreatePage(store.NewPageOptions{Title: "Links", Content: doc.FromBlocks([]*doc.Node{card, video})})
	if err != nil {
		t.Fatal(err)
	}
	notes, err := w.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{card.Clone(), note})})
	if err != nil {
		t.Fatal(err)
	}
	results, _ := w.executor(true).Call("search_pages", map[string]any{"query": "kiwi"})["results"].([]map[string]any)
	snippets := map[any]any{}
	for _, r := range results {
		snippets[r["page_id"]] = r["snippet"]
	}
	if s, found := snippets[links.ID]; !found || s != nil {
		t.Errorf("found %v, snippet %v", found, s)
	}
	if s, _ := snippets[notes.ID].(string); !strings.Contains(s, "Ask about the kiwi crop") || strings.Contains(s, "growers") {
		t.Errorf("snippet %q", s)
	}
}

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

	for _, reading := range []bool{true, false} {
		x := w.executor(reading)
		x.Ctx.ReadWeb = func(link string) (web.Page, error) { return web.Page{URL: link, Title: "Page"}, nil }
		x.Ctx.Request = "Save https://example.com/given.png"
		x.Call("list_folders", map[string]any{})
		if out := x.Call("read_web_page", map[string]any{"url": "https://evil.example/?f=Clippings"}); !strings.Contains(toolText(out), "read only a page the user gave") {
			t.Errorf("reading %v: read a page of the run's own pick after listing the folders: %v", reading, out)
		}
		if out := x.Call("create_page", map[string]any{"title": "A", "blocks": image("https://evil.example/x.png")}); out["error"] == nil {
			t.Errorf("reading %v: wrote a URL the user did not give: %v", reading, out)
		}
		if out := x.Call("read_web_page", map[string]any{"url": "https://example.com/given.png"}); out["error"] != nil {
			t.Errorf("reading %v: refused the user's page: %v", reading, out)
		}
	}
}

func toolText(out map[string]any) string {
	s, _ := out["error"].(string)
	return s
}

func TestWorkspaceRun(t *testing.T) {
	w := newWorkspaceWorld(t)
	f := &fakeModel{replies: []map[string]any{
		{"content": "", "tool_calls": []any{call("create_page", map[string]any{"title": "Article", "folder_id": w.folder.ID, "markdown": "# Article\n\nBody"})}},
		{"content": "Saved it in Clippings."},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if err := w.st.PutProvider(store.Provider{ID: "local", Preset: "custom", Name: "Local", BaseURL: srv.URL, AuthKind: "none", DefaultModel: "m"}); err != nil {
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

func TestWorkspaceJobRequest(t *testing.T) {
	got := Request{Instruction: "File it", OpenPage: &OpenPage{ID: "p1", Title: "Plan"}}.JobRequest()
	if got["open_page_id"] != "p1" {
		t.Errorf("request %v", got)
	}
	if got := (Request{Instruction: "Sum it up"}).JobRequest(); got["open_page_id"] != nil {
		t.Errorf("a page's run's request %v", got)
	}
}

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

type cardlessHost struct {
	readingHost
	st *store.Store
}

func (h cardlessHost) Pages() Pages { return cardless{storePages{h.st}} }

type cardless struct{ storePages }

func (cardless) LinkSubpage(string, string, string) error {
	return errors.New("the page cannot be changed")
}

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

func TestWorkspaceDraftsNoBlocks(t *testing.T) {
	w := newWorkspaceWorld(t)
	x := w.executor(true)
	var ops []doc.Op
	x.OnOp = func(op doc.Op, _ string) { ops = append(ops, op) }
	raw := `{"position":{"at":"end"},"markdown":"# Hi\n\nBody"}`
	x.Draft("write_markdown", raw)
	if _, drafted := x.FinishDraft("write_markdown", raw); drafted || len(ops) != 0 {
		t.Fatalf("drafted %v, ops %+v", drafted, ops)
	}
	if out := x.CallRaw("write_markdown", raw); out["error"] == nil || len(x.Ops) != 0 {
		t.Errorf("write_markdown %v, ops %+v", out, x.Ops)
	}
}

func TestWorkspaceRunKeepsItsOpsAsTheyCome(t *testing.T) {
	w := newWorkspaceWorld(t)
	s := w.h.Session(WorkspaceID)
	reply := &Message{Text: "Working…", Pending: true, RunID: "r1"}
	run := &Run{ID: "r1", Status: "running", Local: true, At: time.Now()}
	s.Runs, s.Messages = []*Run{run}, []*Message{{Mine: true, Text: "File them"}, reply}
	option := Option{Provider: store.Provider{Name: "Local"}}
	d := w.h.newDelivery(WorkspaceID, run, option)
	o := RunOptions{Surface: "workspace", Reply: reply}
	for i, op := range []doc.Op{
		{Op: "rename_page", PageID: w.mine.ID, Title: "Mine 2", PrevTitle: "Mine"},
		{Op: "set_page_tags", PageID: w.mine.ID, Title: "Mine 2", Tags: []string{"a"}, PrevTags: []string{"plan"}},
	} {
		w.h.arrived(s, run, option, doc.Empty(), d, o, op, "edit")
		kept := ""
		if i == 0 {
			kept, _ = w.st.Value(chatKey(WorkspaceID))
		} else {
			kept, _ = w.st.Value(runningKey(WorkspaceID))
		}
		if !strings.Contains(kept, op.Op) {
			t.Errorf("after op %d the chat keeps %q", i, kept)
		}
	}
}

func TestUndoOfALocalRunPassesOverTheUsersChange(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1", Email: "u@example.com"}); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "New", SpaceID: st.Session().SpaceID})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub(st, nil, readingHost{})
	run := &Run{ID: "r1", Status: "done", Local: true, At: time.Now(),
		Ops: []doc.Op{{Op: "rename_page", PageID: p.ID, Title: "New", PrevTitle: "Old"}}}
	h.Session(WorkspaceID).Runs = []*Run{run}
	if err := st.RenamePage(p.ID, "Mine"); err != nil {
		t.Fatal(err)
	}
	if err := h.UndoRun(WorkspaceID, run.ID); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if got, _ := st.Page(p.ID); got.Title != "Mine" || !run.Undone {
		t.Errorf("title %q, undone %v", got.Title, run.Undone)
	}
	cloud := &Run{ID: "r2", Status: "done", At: time.Now(),
		Ops: []doc.Op{{Op: "rename_page", PageID: p.ID, Title: "New", PrevTitle: "Old"}}}
	h.Session(WorkspaceID).Runs = append(h.Session(WorkspaceID).Runs, cloud)
	if err := h.UndoRun(WorkspaceID, cloud.ID); err == nil {
		t.Error("a cloud run's Undo did not wait for its change")
	}
}

func TestRedoLeavesWhatTheUserDeleted(t *testing.T) {
	w := newWorkspaceWorld(t)
	x := w.executor(true)
	folder := x.Call("create_folder", map[string]any{"name": "Trips"})["folder_id"].(string)
	page := x.Call("create_page", map[string]any{"title": "Lisbon"})["page_id"].(string)
	kept := x.Call("create_page", map[string]any{"title": "Porto"})["page_id"].(string)
	run := &Run{ID: "r1", Status: "done", Local: true, At: time.Now(), Ops: x.Ops, Edits: x.Edits}
	w.h.Session(WorkspaceID).Runs = []*Run{run}
	if _, err := w.st.DeletePage(page); err != nil {
		t.Fatal(err)
	}
	f, _ := w.st.Folder(folder)
	if err := w.st.DeleteFolder(f); err != nil {
		t.Fatal(err)
	}
	if err := w.h.UndoRun(WorkspaceID, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.h.RedoRun(WorkspaceID, run.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.st.Page(page); p.DeletedAt.IsZero() {
		t.Error("the page the user deleted is back")
	}
	if f, _ := w.st.Folder(folder); f.DeletedAt.IsZero() {
		t.Error("the folder the user deleted is back")
	}
	if p, _ := w.st.Page(kept); !p.DeletedAt.IsZero() {
		t.Error("Redo did not bring back the page the user kept")
	}
}

type movingPages struct {
	storePages
	moved *[]*doc.Position
}

func (p movingPages) MovePageAt(_, _ string, at *doc.Position, move func() error) (*doc.Position, error) {
	*p.moved = append(*p.moved, at)
	return &doc.Position{After: "b0"}, move()
}

type movingHost struct {
	readingHost
	pages movingPages
}

func (h movingHost) Pages() Pages { return h.pages }

func TestWorkspaceMovesKeepTheirCards(t *testing.T) {
	w := newWorkspaceWorld(t)
	var moved []*doc.Position
	w.h = NewHub(w.st, nil, movingHost{pages: movingPages{storePages{w.st}, &moved}})
	inside, err := w.st.CreatePage(store.NewPageOptions{Title: "Inside", ParentID: w.mine.ID})
	if err != nil {
		t.Fatal(err)
	}
	x := w.executor(true)
	before, _ := w.st.Page(w.inside.ID)
	if out := x.Call("move_page", map[string]any{"page_id": w.inside.ID, "folder_id": w.folder.ID}); out["error"] != nil || len(x.Ops) != 0 {
		t.Fatalf("a move where it is: %v, ops %+v", out, x.Ops)
	}
	if after, _ := w.st.Page(w.inside.ID); after.SortKey != before.SortKey || len(moved) != 0 {
		t.Errorf("moved where it is: key %q, was %q; moves %d", after.SortKey, before.SortKey, len(moved))
	}
	if out := x.Call("move_page", map[string]any{"page_id": inside.ID}); out["error"] != nil || len(x.Ops) != 1 {
		t.Fatalf("a move out: %v, ops %+v", out, x.Ops)
	}
	if card := x.Ops[0].PrevPlace.Card; card == nil || card.After != "b0" {
		t.Fatalf("the card was at %+v", card)
	}
	w.h.Session(WorkspaceID).Runs = []*Run{{ID: "r1", Status: "done", Local: true, Ops: x.Ops, Edits: x.Edits}}
	if err := w.h.UndoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.st.Page(inside.ID); p.ParentID != w.mine.ID || len(moved) != 2 || moved[1] == nil || moved[1].After != "b0" {
		t.Errorf("after Undo inside %q, moves %+v", p.ParentID, moved)
	}
}

func TestWorkspaceLeavesPagesWhoseCardsOthersRead(t *testing.T) {
	w := newWorkspaceWorld(t)
	guested, err := w.st.CreatePage(store.NewPageOptions{Title: "Guested"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.AddComment(guested.ID, "b1", "Nice", "", "", "guest", "Visitor"); err != nil {
		t.Fatal(err)
	}
	child, err := w.st.CreatePage(store.NewPageOptions{Title: "Child", ParentID: guested.ID})
	if err != nil {
		t.Fatal(err)
	}
	pasted, err := w.st.CreatePage(store.NewPageOptions{Title: "Pasted"})
	if err != nil {
		t.Fatal(err)
	}
	mentioned, err := w.st.CreatePage(store.NewPageOptions{Title: "Mentioned"})
	if err != nil {
		t.Fatal(err)
	}
	mention := &doc.Node{Type: "paragraph"}
	mention.SetDelta(doc.Delta{{Text: "See "}, {Text: "Mentioned", Attrs: doc.PageMention(mentioned.ID, "Mentioned")}})
	c := w.st.Content(guested.ID)
	c.Children = append(c.Children, doc.SubpageNode(pasted.ID), mention)
	if _, _, err := w.st.SaveContent(guested.ID, c); err != nil {
		t.Fatal(err)
	}
	x := w.executor(true)
	for _, id := range []string{child.ID, pasted.ID, mentioned.ID} {
		if out := x.Call("rename_page", map[string]any{"page_id": id, "title": "Secret"}); out["error"] == nil {
			t.Errorf("renamed %s: %v", id, out)
		}
		if out := x.Call("move_page", map[string]any{"page_id": id, "folder_id": w.folder.ID}); out["error"] == nil {
			t.Errorf("moved %s: %v", id, out)
		}
	}
	listed, _ := x.Call("list_pages", map[string]any{})["pages"].([]map[string]any)
	for _, p := range listed {
		if (p["id"] == child.ID || p["id"] == pasted.ID || p["id"] == mentioned.ID) && p["shared"] != true {
			t.Errorf("listed %v", p)
		}
	}
	if len(x.Ops) != 0 {
		t.Errorf("ops %+v", x.Ops)
	}
}

func TestWorkspaceReadsNoUnreadablePage(t *testing.T) {
	w := newWorkspaceWorld(t)
	if _, err := w.st.DB().Exec(`UPDATE page_contents SET content = '{"version": 99' WHERE page_id = ?`, w.mine.ID); err != nil {
		t.Fatal(err)
	}
	if out := w.executor(true).Call("read_page", map[string]any{"page_id": w.mine.ID}); out["error"] == nil {
		t.Errorf("read %v", out)
	}
}

func TestLongTitlesAreCutAsTheStoreKeepsThem(t *testing.T) {
	long := strings.Repeat("👨‍👩‍👧", 1000)
	want := store.ClampTitle(long)
	if n := utf8.RuneCountInString(want); n >= utf8.RuneCountInString(long) || !utf8.ValidString(want) {
		t.Fatalf("the store keeps %d characters", n)
	}
	x := executor(page())
	var titles []string
	x.Ctx.CreateSubpage = func(title string, _ []*doc.Node) (string, error) {
		titles = append(titles, title)
		return "page-2", nil
	}
	x.Ctx.CreatePage = func(title string, _ []*doc.Node, _ string) (string, error) {
		titles = append(titles, title)
		return "page-3", nil
	}
	for _, tool := range []string{"create_subpage", "create_page"} {
		if out := x.Call(tool, map[string]any{"title": " " + long + " "}); out["error"] != nil {
			t.Fatalf("%s: %v", tool, out)
		}
	}
	if !slices.Equal(titles, []string{want, want}) {
		t.Errorf("made pages titled %d and %d characters", utf8.RuneCountInString(titles[0]), utf8.RuneCountInString(titles[1]))
	}
	for _, op := range x.Ops {
		if op.Op == "create_page" && op.Page.Title != want {
			t.Errorf("an op titled %d characters", utf8.RuneCountInString(op.Page.Title))
		}
	}

	w := newWorkspaceWorld(t)
	wx := w.executor(true)
	if out := wx.Call("rename_page", map[string]any{"page_id": w.mine.ID, "title": long}); out["error"] != nil {
		t.Fatalf("rename_page: %v", out)
	}
	if p, _ := w.st.Page(w.mine.ID); p.Title != want || len(wx.Ops) != 1 || wx.Ops[0].Title != want || !strings.Contains(wx.Edits[0].Summary, want) {
		t.Fatalf("renamed to %d characters, ops %d", utf8.RuneCountInString(p.Title), len(wx.Ops))
	}
	w.h.Session(WorkspaceID).Runs = []*Run{{ID: "r1", Status: "done", Local: true, Ops: wx.Ops}}
	if err := w.h.UndoRun(WorkspaceID, "r1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.st.Page(w.mine.ID); p.Title != "Mine" {
		t.Errorf("after Undo the page is titled %d characters", utf8.RuneCountInString(p.Title))
	}
}
