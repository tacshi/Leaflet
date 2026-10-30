package mcp

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"leaflet/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	return st
}

// session sends lines to a server and returns its answers by ID.
func session(t *testing.T, s *Server, lines ...string) map[float64]map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")), &out); err != nil {
		t.Fatal(err)
	}
	answers := map[float64]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("answer %q: %v", l, err)
		}
		id, _ := m["id"].(float64)
		answers[id] = m
	}
	return answers
}

func call(id int, name string, args map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	return string(raw)
}

// result is a tool call's answer, decoded, and whether it was an error.
func result(t *testing.T, answer map[string]any) (map[string]any, bool) {
	t.Helper()
	r, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", answer)
	}
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out, r["isError"].(bool)
}

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.0"}}}`

func TestHandshake(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
		`not json`)
	init := got[1]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || s.agent != "Claude Code" {
		t.Errorf("initialize %v, agent %q", init, s.agent)
	}
	if len(got) != 4 {
		t.Errorf("answered %d messages, want 4 (no answer to a notification)", len(got))
	}
	if seen := st.ValuesWithPrefix(SeenPrefix); len(seen) != 0 {
		t.Errorf("seen %v before a tool call", seen)
	}
	session(t, s, call(4, "list_pages", nil))
	if seen := st.ValuesWithPrefix(SeenPrefix); seen["claude-code"] == "" {
		t.Errorf("seen %v after a tool call, want claude-code", seen)
	}
	names := map[string]map[string]any{}
	for _, tool := range got[2]["result"].(map[string]any)["tools"].([]any) {
		m := tool.(map[string]any)
		names[m["name"].(string)] = m
	}
	for _, n := range []string{"list_pages", "search_pages", "read_page", "insert_blocks", "replace_block", "move_block", "delete_blocks", "create_page", "rename_page", "set_page_tags", "move_page", "create_folder", "rename_folder"} {
		if names[n] == nil {
			t.Errorf("no tool %s", n)
		}
	}
	required := names["insert_blocks"]["inputSchema"].(map[string]any)["required"].([]any)
	if required[0] != "page_id" {
		t.Errorf("insert_blocks requires %v", required)
	}
	for name, want := range map[string][2]any{"read_page": {true, nil}, "insert_blocks": {false, false}, "delete_blocks": {false, true}} {
		a := names[name]["annotations"].(map[string]any)
		if a["readOnlyHint"] != want[0] || a["destructiveHint"] != want[1] || a["openWorldHint"] != false || a["title"] == "" {
			t.Errorf("%s annotations %v", name, a)
		}
	}
	if got[3]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("resources/list %v", got[3])
	}
	if got[0]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Errorf("not json %v", got[0])
	}
	// An unknown tool is the client's mistake, as the spec has it: a
	// protocol error, not a tool's.
	got = session(t, s, call(4, "no_such_tool", map[string]any{}))
	if e, _ := got[4]["error"].(map[string]any); e == nil || e["code"].(float64) != -32602 || !strings.Contains(e["message"].(string), "no_such_tool") {
		t.Errorf("unknown tool %v", got[4])
	}
}

// Pages are full of & < > (embeds, code): the agent reads them as they
// are, not as \u0026 and \u003c.
func TestResultsKeepHTMLCharacters(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "0.1.0")
	got := session(t, s, call(2, "create_page", map[string]any{"title": "R&D <notes>",
		"blocks": []any{map[string]any{"type": "paragraph", "text": "a < b && c > d"}}}))
	out, _ := result(t, got[2])
	var raw bytes.Buffer
	if err := s.Serve(strings.NewReader(call(3, "read_page", map[string]any{"page_id": out["page_id"]})+"\n"+call(4, "list_pages", nil)), &raw); err != nil {
		t.Fatal(err)
	}
	text := raw.String()
	if !strings.Contains(text, "R&D <notes>") || !strings.Contains(text, "a < b && c > d") || strings.Contains(text, `\u00`) {
		t.Errorf("results escape HTML characters: %s", text)
	}
}

func TestCreateAndEditPage(t *testing.T) {
	st := newStore(t)
	parent, err := st.CreatePage(store.NewPageOptions{Title: "Companies"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize, call(2, "create_page", map[string]any{
		"title": "Cognex", "parent_id": parent.ID,
		"blocks": []any{
			map[string]any{"type": "heading", "level": 2, "text": "Overview"},
			map[string]any{"type": "paragraph", "text": "Machine vision for **factories**."},
		},
	}))
	out, failed := result(t, got[2])
	id, _ := out["page_id"].(string)
	if failed || id == "" {
		t.Fatalf("create_page %v", out)
	}
	p, err := st.Page(id)
	if err != nil || p.ParentID != parent.ID || p.Title != "Cognex" {
		t.Errorf("page %+v, %v", p, err)
	}
	if text := st.Content(id).PlainText(); !strings.Contains(text, "Machine vision for factories.") {
		t.Errorf("content %q", text)
	}
	link := st.Content(parent.ID).Children
	if len(link) == 0 || link[len(link)-1].Type != "subpage" || link[len(link)-1].Str("page_id") != id {
		t.Errorf("the parent does not link the page: %v", link)
	}

	got = session(t, s, call(3, "read_page", map[string]any{"page_id": id}))
	read, _ := result(t, got[3])
	blocks := read["blocks"].([]any)
	first := blocks[0].(map[string]any)["id"].(string)
	got = session(t, s,
		call(4, "insert_blocks", map[string]any{"page_id": id, "position": map[string]any{"after": first}, "blocks": []any{map[string]any{"type": "bulleted_list", "text": "Founded 1981"}}}),
		call(5, "replace_block", map[string]any{"page_id": id, "id": first, "block": map[string]any{"type": "heading", "level": 2, "text": "About Cognex"}}),
		call(6, "delete_blocks", map[string]any{"page_id": id, "ids": []any{"nope"}}),
		call(7, "read_page", map[string]any{"page_id": "nope"}))
	if _, failed := result(t, got[4]); failed {
		t.Errorf("insert_blocks %v", got[4])
	}
	if out, failed := result(t, got[6]); !failed || !strings.Contains(out["error"].(string), "nope") {
		t.Errorf("delete_blocks of no block %v", out)
	}
	if _, failed := result(t, got[7]); !failed {
		t.Errorf("read_page of no page succeeded")
	}
	text := st.Content(id).PlainText()
	if !strings.Contains(text, "About Cognex") || !strings.Contains(text, "Founded 1981") {
		t.Errorf("content %q", text)
	}
	// Edits close together keep one snapshot, from before the first.
	history, _ := st.History(id)
	if len(history) != 1 || history[0].Label != "Before Claude Code" || strings.Contains(history[0].Content, "Founded") {
		t.Errorf("history %+v", history)
	}
}

// links are the pages page id links to as subpages.
func links(st *store.Store, id string) []string {
	var out []string
	for _, n := range st.Content(id).Children {
		if n.Type == "subpage" {
			out = append(out, n.Str("page_id"))
		}
	}
	return out
}

func TestFolders(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize,
		call(2, "create_folder", map[string]any{"name": " Work "}),
		call(3, "create_folder", map[string]any{"name": " "}))
	out, failed := result(t, got[2])
	folder, _ := out["folder_id"].(string)
	if failed || folder == "" {
		t.Fatalf("create_folder %v", out)
	}
	if _, failed := result(t, got[3]); !failed {
		t.Errorf("create_folder with no name succeeded")
	}
	got = session(t, s,
		call(4, "rename_folder", map[string]any{"folder_id": folder, "name": "Projects"}),
		call(5, "rename_folder", map[string]any{"folder_id": "nope", "name": "X"}),
		call(6, "create_page", map[string]any{"title": "Plan", "folder_id": folder}),
		call(7, "create_page", map[string]any{"title": "Notes"}),
		call(8, "create_page", map[string]any{"title": "Lost", "folder_id": "nope"}))
	if _, failed := result(t, got[4]); failed {
		t.Errorf("rename_folder %v", got[4])
	}
	if out, failed := result(t, got[5]); !failed || !strings.Contains(out["error"].(string), "nope") {
		t.Errorf("rename_folder of no folder %v", out)
	}
	if out, failed := result(t, got[8]); !failed || !strings.Contains(out["error"].(string), "nope") {
		t.Errorf("create_page in no folder %v", out)
	}
	out, _ = result(t, got[6])
	plan := out["page_id"].(string)
	out, _ = result(t, got[7])
	notes := out["page_id"].(string)
	got = session(t, s,
		call(9, "create_page", map[string]any{"title": "Both", "parent_id": notes, "folder_id": folder}),
		call(10, "list_pages", nil))
	if out, failed := result(t, got[9]); !failed || !strings.Contains(out["error"].(string), "not both") {
		t.Errorf("create_page with a parent and a folder %v", out)
	}
	listed, _ := result(t, got[10])
	if f := listed["folders"].([]any); len(f) != 1 || f[0].(map[string]any)["name"] != "Projects" || f[0].(map[string]any)["id"] != folder {
		t.Errorf("folders %v", f)
	}
	inFolder := map[string]any{}
	for _, p := range listed["pages"].([]any) {
		m := p.(map[string]any)
		inFolder[m["title"].(string)] = m["folder_id"]
	}
	if inFolder["Plan"] != folder || inFolder["Notes"] != nil {
		t.Errorf("pages' folders %v", inFolder)
	}

	// Into a page and out to a folder, the links follow the page.
	got = session(t, s,
		call(11, "move_page", map[string]any{"page_id": plan, "parent_id": notes}),
		call(12, "move_page", map[string]any{"page_id": notes, "parent_id": plan}))
	if _, failed := result(t, got[11]); failed {
		t.Errorf("move_page inside a page %v", got[11])
	}
	if p, _ := st.Page(plan); p.ParentID != notes || p.FolderID != "" || !slices.Equal(links(st, notes), []string{plan}) {
		t.Errorf("moved inside: page %+v, links %v", p, links(st, notes))
	}
	if out, failed := result(t, got[12]); !failed || !strings.Contains(out["error"].(string), "inside itself") {
		t.Errorf("move_page inside its own subpage %v", out)
	}
	got = session(t, s,
		call(13, "move_page", map[string]any{"page_id": plan, "folder_id": folder}),
		call(14, "move_page", map[string]any{"page_id": notes, "parent_id": plan}))
	if p, _ := st.Page(plan); p.ParentID != "" || p.FolderID != folder || len(links(st, notes)) != 0 {
		t.Errorf("moved to a folder: page %+v, links %v", p, links(st, notes))
	}
	if _, failed := result(t, got[14]); failed {
		t.Errorf("move_page %v", got[14])
	}
	got = session(t, s, call(15, "move_page", map[string]any{"page_id": notes}))
	if _, failed := result(t, got[15]); failed {
		t.Errorf("move_page to the top level %v", got[15])
	}
	if p, _ := st.Page(notes); p.ParentID != "" || p.FolderID != "" || len(links(st, plan)) != 0 {
		t.Errorf("moved to the top level: page %+v, links %v", p, links(st, plan))
	}
}

// A page stays in its space, as the server has it, and out of templates.
// The server keeps its own store on the app's database, from before the
// app signed in: its pages go into the space the app has now.
func TestFollowsTheAppToAnotherAccount(t *testing.T) {
	dir := t.TempDir()
	app, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if _, err := app.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	// What sync does on sign-in: adopt the server's space.
	local := app.Session().SpaceID
	if err := app.PutSpace("remote-a", "My space", "A"); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"space.A": "remote-a", "space.local": "remote-a", "account": "A"} {
		_ = app.SetValue(k, v)
	}
	if err := app.MoveSpace(local, "remote-a"); err != nil {
		t.Fatal(err)
	}

	got := session(t, NewServer(st, "0.1.0"), initialize, call(2, "create_page", map[string]any{"title": "Plan"}))
	out, failed := result(t, got[2])
	if failed {
		t.Fatalf("create_page %v", out)
	}
	p, err := app.Page(out["page_id"].(string))
	if err != nil || p.SpaceID != "remote-a" || p.CreatedBy != "A" {
		t.Errorf("page %+v, %v", p, err)
	}
}

func TestMovePageStaysInItsSpace(t *testing.T) {
	st := newStore(t)
	mine, err := st.CreatePage(store.NewPageOptions{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := st.CreatePage(store.NewPageOptions{Title: "Theirs", SpaceID: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	inside, err := st.CreatePage(store.NewPageOptions{Title: "Inside", ParentID: theirs.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.Page{theirs, inside} {
		if err := st.SetMembers(p.ID, []store.Member{{PageID: p.ID, UserID: st.Session().UserID, Role: "editor"}}); err != nil {
			t.Fatal(err)
		}
	}
	template, err := st.CreatePage(store.NewPageOptions{Title: "PRD", Template: true})
	if err != nil {
		t.Fatal(err)
	}
	folder, err := st.CreateFolder("Work")
	if err != nil {
		t.Fatal(err)
	}
	got := session(t, NewServer(st, "0.1.0"),
		call(2, "move_page", map[string]any{"page_id": mine.ID, "parent_id": theirs.ID}),
		call(3, "move_page", map[string]any{"page_id": inside.ID}),
		call(4, "move_page", map[string]any{"page_id": inside.ID, "folder_id": folder.ID}),
		call(5, "move_page", map[string]any{"page_id": mine.ID, "parent_id": template.ID}))
	for id, want := range map[float64]string{2: "another space", 3: "top level", 4: "top level", 5: "template"} {
		if out, failed := result(t, got[id]); !failed || !strings.Contains(out["error"].(string), want) {
			t.Errorf("move %v: %v, want an error saying %q", id, out, want)
		}
	}
	if p, _ := st.Page(mine.ID); p.ParentID != "" || p.SpaceID != mine.SpaceID {
		t.Errorf("mine moved: %+v", p)
	}
	if p, _ := st.Page(inside.ID); p.ParentID != theirs.ID || p.FolderID != "" {
		t.Errorf("inside moved: %+v", p)
	}
}

func TestSharedPageToView(t *testing.T) {
	st := newStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Theirs", SpaceID: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembers(p.ID, []store.Member{{PageID: p.ID, UserID: st.Session().UserID, Role: "viewer"}}); err != nil {
		t.Fatal(err)
	}
	got := session(t, NewServer(st, "0.1.0"),
		call(2, "insert_blocks", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"}, "blocks": []any{map[string]any{"type": "paragraph", "text": "x"}}}),
		call(3, "read_page", map[string]any{"page_id": p.ID}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "view") {
		t.Errorf("edit of a page to view %v", out)
	}
	if _, failed := result(t, got[3]); failed {
		t.Errorf("read of a page to view failed")
	}
}

// A page shared with the user is read-only until its members say they
// may edit it, as in the app.
func TestSharedPageBeforeItsMembers(t *testing.T) {
	st := newStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Theirs", SpaceID: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	got := session(t, NewServer(st, "0.1.0"),
		call(2, "insert_blocks", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"}, "blocks": []any{map[string]any{"type": "paragraph", "text": "x"}}}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "view") {
		t.Errorf("edit before the members loaded %v", out)
	}
}

func TestAgentName(t *testing.T) {
	for _, c := range [][3]string{{"", "claude-code", "Claude Code"}, {"Cursor", "cursor-vscode", "Cursor"}, {"", "codex_mcp", "Codex Mcp"}, {"", "", ""}} {
		if got := agentName(c[0], c[1]); got != c[2] {
			t.Errorf("agentName(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// Templates stay out of what agents outside the app find.
func TestSearchLeavesOutTemplates(t *testing.T) {
	st := newStore(t)
	if _, err := st.CreatePage(store.NewPageOptions{Title: "PRD: Sync"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePage(store.NewPageOptions{Title: "PRD", Template: true}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize, call(2, "search_pages", map[string]any{"query": "PRD"}))
	out, failed := result(t, got[2])
	results, _ := out["results"].([]any)
	if failed || len(results) != 1 || results[0].(map[string]any)["title"] != "PRD: Sync" {
		t.Errorf("results %v", out)
	}
}

// A page whose stored content cannot be read is left as it is, rather
// than edited as an empty page and saved over.
func TestUnreadablePageIsLeftAlone(t *testing.T) {
	st := newStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Broken"})
	if err != nil {
		t.Fatal(err)
	}
	const broken = `{"children": [`
	if _, err := st.DB().Exec(`UPDATE page_contents SET content = ? WHERE page_id = ?`, broken, p.ID); err != nil {
		t.Fatal(err)
	}
	got := session(t, NewServer(st, "0.1.0"),
		call(2, "insert_blocks", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"}, "blocks": []any{map[string]any{"type": "paragraph", "text": "x"}}}),
		call(3, "create_page", map[string]any{"title": "Inside", "parent_id": p.ID}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "could not be read") {
		t.Errorf("insert_blocks on an unreadable page %v", out)
	}
	if out, failed := result(t, got[3]); failed {
		t.Errorf("create_page inside an unreadable page %v", out)
	}
	if row, err := st.ContentRow(p.ID); err != nil || row.Content != broken {
		t.Errorf("content %q, %v", row.Content, err)
	}
}

// set_page_tags replaces a page's tags, which list_pages, read_page and a
// #tag search show.
func TestPageTags(t *testing.T) {
	st := newStore(t)
	p, _ := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize,
		call(2, "set_page_tags", map[string]any{"page_id": p.ID, "tags": []string{"#work", "Q4", "work"}}),
		call(3, "list_pages", nil),
		call(4, "read_page", map[string]any{"page_id": p.ID}),
		call(5, "search_pages", map[string]any{"query": "#q4"}))
	if out, failed := result(t, got[2]); failed || !slices.Equal(toStrings(out["tags"]), []string{"work", "Q4"}) {
		t.Errorf("set %v", out)
	}
	if out, _ := result(t, got[3]); !slices.Equal(toStrings(out["pages"].([]any)[0].(map[string]any)["tags"]), []string{"work", "Q4"}) {
		t.Errorf("listed %v", out)
	}
	if out, _ := result(t, got[4]); !slices.Equal(toStrings(out["page"].(map[string]any)["tags"]), []string{"work", "Q4"}) {
		t.Errorf("read %v", out["page"])
	}
	if out, _ := result(t, got[5]); len(out["results"].([]any)) != 1 {
		t.Errorf("found %v", out)
	}
}

func toStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// An edit whose history snapshot cannot be kept is not saved either, so
// trying it again does not make it twice.
func TestEditNotSavedWithoutItsSnapshot(t *testing.T) {
	st := newStore(t)
	p, err := st.CreatePage(store.NewPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER reject_history BEFORE INSERT ON page_history BEGIN SELECT RAISE(ABORT, 'history failed'); END`); err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "test")
	args := map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"},
		"blocks": []any{map[string]any{"type": "paragraph", "text": "Inserted once"}}}
	if out := s.pages.call("insert_blocks", args); out["error"] == nil {
		t.Fatalf("insert with no snapshot: %v", out)
	}
	if strings.Contains(st.Content(p.ID).PlainText(), "Inserted once") {
		t.Error("insert saved without its snapshot")
	}
	if _, err := st.DB().Exec(`DROP TRIGGER reject_history`); err != nil {
		t.Fatal(err)
	}
	if out := s.pages.call("insert_blocks", args); out["error"] != nil {
		t.Fatalf("insert again: %v", out)
	}
	if h, err := st.History(p.ID); err != nil || len(h) != 1 {
		t.Errorf("history %d, %v", len(h), err)
	}
}
