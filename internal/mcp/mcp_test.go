package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
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
	for _, n := range []string{"list_pages", "search_pages", "current_page", "read_page", "write_markdown", "insert_blocks", "replace_block", "move_block", "delete_blocks", "create_page", "rename_page", "set_page_tags", "move_page", "create_folder", "rename_folder"} {
		if names[n] == nil {
			t.Errorf("no tool %s", n)
		}
	}
	required := names["insert_blocks"]["inputSchema"].(map[string]any)["required"].([]any)
	if required[0] != "page_id" {
		t.Errorf("insert_blocks requires %v", required)
	}
	for name, want := range map[string][3]any{
		"read_page": {true, nil, false}, "insert_blocks": {false, false, true}, "delete_blocks": {false, true, true},
		"move_page": {false, true, true}, "rename_page": {false, true, true}, "move_block": {false, true, true},
	} {
		a := names[name]["annotations"].(map[string]any)
		if a["readOnlyHint"] != want[0] || a["destructiveHint"] != want[1] || a["openWorldHint"] != want[2] || a["title"] == "" {
			t.Errorf("%s annotations %v", name, a)
		}
	}
	if got[3]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("resources/list %v", got[3])
	}
	if got[0]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Errorf("not json %v", got[0])
	}
	got = session(t, s, call(4, "no_such_tool", map[string]any{}))
	if e, _ := got[4]["error"].(map[string]any); e == nil || e["code"].(float64) != -32602 || !strings.Contains(e["message"].(string), "no_such_tool") {
		t.Errorf("unknown tool %v", got[4])
	}
}

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
		call(7, "read_page", map[string]any{"page_id": "nope"}),
		call(8, "write_markdown", map[string]any{"page_id": id, "position": map[string]any{"at": "end"}, "markdown": "## History\nStarted in a garage."}))
	if _, failed := result(t, got[4]); failed {
		t.Errorf("insert_blocks %v", got[4])
	}
	if out, failed := result(t, got[8]); failed || len(out["inserted_ids"].([]any)) != 2 {
		t.Errorf("write_markdown %v", got[8])
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
	history, _ := st.History(id)
	if len(history) != 1 || history[0].Label != "Before Claude Code" || strings.Contains(history[0].Content, "Founded") {
		t.Errorf("history %+v", history)
	}

	edit := func(agent string) {
		s := NewServer(st, "0.1.0")
		s.SetAgent(agent)
		if out := s.Call("write_markdown", map[string]any{"page_id": id, "position": map[string]any{"at": "end"}, "markdown": "More."}); out["error"] != nil {
			t.Fatal(out["error"])
		}
	}
	edit("Claude Code")
	edit("Codex")
	edit("Codex")
	if history, _ := st.History(id); len(history) != 2 || history[0].Label != "Before Codex" {
		t.Errorf("history after clients started again %+v", history)
	}
}

func TestSnapshotKeepsWhatTheUserTyped(t *testing.T) {
	for _, again := range []bool{false, true} {
		st := newStore(t)
		p, err := st.CreatePage(store.NewPageOptions{Title: "Notes"})
		if err != nil {
			t.Fatal(err)
		}
		s := NewServer(st, "0.1.0")
		s.SetAgent("Claude Code")
		if out := s.Call("write_markdown", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"}, "markdown": "Its own."}); out["error"] != nil {
			t.Fatal(out["error"])
		}
		c := st.Content(p.ID)
		typed := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		typed.SetDelta(doc.FromMarkdown("Typed in the app."))
		if err := c.Insert(doc.Position{At: "end"}, typed); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.SaveContent(p.ID, c); err != nil {
			t.Fatal(err)
		}
		if err := st.MaybeAutosave(p.ID, c); err != nil {
			t.Fatal(err)
		}
		if again {
			s = NewServer(st, "0.1.0")
			s.SetAgent("Claude Code")
		}
		if out := s.Call("read_page", map[string]any{"page_id": p.ID}); out["error"] != nil {
			t.Fatal(out["error"])
		}
		if out := s.Call("delete_blocks", map[string]any{"page_id": p.ID, "ids": []any{typed.ID}}); out["error"] != nil {
			t.Fatal(out["error"])
		}
		history, _ := st.History(p.ID)
		if !slices.ContainsFunc(history, func(h store.HistoryEntry) bool { return strings.Contains(h.Content, "Typed in the app.") }) {
			t.Errorf("started again %v: the user's text is in none of %d snapshots", again, len(history))
		}
		s = NewServer(st, "0.1.0")
		s.SetAgent("Claude Code")
		if out := s.Call("write_markdown", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"}, "markdown": "More."}); out["error"] != nil {
			t.Fatal(out["error"])
		}
		if after, _ := st.History(p.ID); len(after) != len(history) {
			t.Errorf("started again %v: %d snapshots became %d", again, len(history), len(after))
		}
	}
}

func TestMoveIntoAPageOthersHave(t *testing.T) {
	st := newStore(t)
	newPage := func(title string) string {
		p, err := st.CreatePage(store.NewPageOptions{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	secret, team, also := newPage("Secret"), newPage("Team"), newPage("Also the team's")
	for _, id := range []string{team, also} {
		if err := st.SetMembers(id, []store.Member{{UserID: "sam", Role: "editor"}}); err != nil {
			t.Fatal(err)
		}
	}
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize,
		call(2, "move_page", map[string]any{"page_id": secret, "parent_id": team}),
		call(3, "move_page", map[string]any{"page_id": also, "parent_id": team}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "Other people may have that page") {
		t.Errorf("moved into a page others have: %v", out)
	}
	if p, _ := st.Page(secret); p.ParentID != "" {
		t.Errorf("the page is inside %q", p.ParentID)
	}
	if out, failed := result(t, got[3]); failed {
		t.Errorf("moved between pages others have: %v", out)
	}
	if out := NewServer(st, "0.1.0").Call("move_page", map[string]any{"page_id": secret, "parent_id": team}); out["error"] != nil {
		t.Errorf("the user's own command: %v", out)
	}
	if p, _ := st.Page(secret); p.ParentID != team {
		t.Errorf("the page is inside %q", p.ParentID)
	}
}

func TestMoveBeforeMembersAreKnown(t *testing.T) {
	st := newStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("account", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSpace("theirs", "Theirs", "sam"); err != nil {
		t.Fatal(err)
	}
	newPage := func(title, space string) string {
		p, err := st.CreatePage(store.NewPageOptions{Title: title, SpaceID: space})
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	secret, mine := newPage("Secret", ""), newPage("Mine", "")
	report, plans := newPage("Report", "theirs"), newPage("Plans", "theirs")
	for _, id := range []string{report, plans} {
		if err := st.SetMembers(id, []store.Member{{UserID: "u1", Role: "editor"}, {UserID: "sam", Role: "owner"}}); err != nil {
			t.Fatal(err)
		}
	}
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize,
		call(2, "move_page", map[string]any{"page_id": secret, "parent_id": mine}),
		call(3, "move_page", map[string]any{"page_id": report, "parent_id": plans}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "Other people may have that page") {
		t.Errorf("moved before members were known: %v", out)
	}
	if p, _ := st.Page(secret); p.ParentID != "" {
		t.Errorf("the page is inside %q", p.ParentID)
	}
	if out, failed := result(t, got[3]); failed {
		t.Errorf("moved within someone else's space: %v", out)
	}
	if err := st.SetValue(syncsvc.OwnMembersKey, "1"); err != nil {
		t.Fatal(err)
	}
	got = session(t, s, call(4, "move_page", map[string]any{"page_id": secret, "parent_id": mine}))
	if out, failed := result(t, got[4]); failed {
		t.Errorf("moved once no one else has it: %v", out)
	}
}

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

func TestEditRunAgainKeepsOneSnapshot(t *testing.T) {
	st := newStore(t)
	p, err := st.CreatePage(store.NewPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	saves := 0
	was := beforeSave
	beforeSave = func(id string) {
		if saves++; saves > 1 {
			return
		}
		c := st.Content(id)
		c.Children = append(c.Children, &doc.Node{ID: "theirs", Type: "paragraph"})
		if _, _, err := st.SaveContent(id, c); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeSave = was })
	s := NewServer(st, "test")
	out := s.pages.call("insert_blocks", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"},
		"blocks": []any{map[string]any{"type": "paragraph", "text": "Inserted"}}})
	if out["error"] != nil {
		t.Fatal(out)
	}
	if h, err := st.History(p.ID); err != nil || len(h) != 1 {
		t.Errorf("history %d, %v", len(h), err)
	}
	c := st.Content(p.ID)
	if saves != 2 || c.Find("theirs") == nil || !strings.Contains(c.PlainText(), "Inserted") {
		t.Errorf("%d saves, stored %q", saves, c.PlainText())
	}
}

func TestEditOverAChangeNotReadKeepsIt(t *testing.T) {
	for _, op := range []string{"replace_block", "delete_blocks"} {
		st := newStore(t)
		b1 := &doc.Node{ID: "b1", Type: "paragraph"}
		b1.SetDelta(doc.Delta{{Text: "draft"}})
		p, err := st.CreatePage(store.NewPageOptions{Content: doc.FromBlocks([]*doc.Node{b1})})
		if err != nil {
			t.Fatal(err)
		}
		s := NewServer(st, "test")
		if out := s.pages.call("read_page", map[string]any{"page_id": p.ID}); out["error"] != nil {
			t.Fatal(out)
		}
		args := map[string]any{"page_id": p.ID, "id": "b1", "block": map[string]any{"type": "paragraph", "text": "agent's"}}
		if op == "delete_blocks" {
			args = map[string]any{"page_id": p.ID, "ids": []any{"b1"}}
		}
		if out := s.pages.call("replace_block", map[string]any{"page_id": p.ID, "id": "b1", "block": map[string]any{"type": "paragraph", "text": "first"}}); out["error"] != nil {
			t.Fatal(out)
		}
		conflicts := func() int {
			h, _ := st.History(p.ID)
			n := 0
			for _, e := range h {
				if e.Reason == "conflict" {
					n++
				}
			}
			return n
		}
		if n := conflicts(); n != 0 {
			t.Fatalf("%s: %d conflict snapshots after its own edit", op, n)
		}
		c := st.Content(p.ID)
		c.Find("b1").SetDelta(doc.Delta{{Text: "the user's fix"}})
		if _, _, err := st.SaveContent(p.ID, c); err != nil {
			t.Fatal(err)
		}
		if out := s.pages.call(op, args); out["error"] != nil {
			t.Fatal(out)
		}
		h, _ := st.History(p.ID)
		if conflicts() != 1 || !strings.Contains(h[0].Content, "the user's fix") {
			t.Errorf("%s: %d conflict snapshots, newest %q", op, conflicts(), h[0].Content)
		}
	}
}

func TestLongPageComesInParts(t *testing.T) {
	st := newStore(t)
	var blocks []*doc.Node
	for i := range 40 {
		n := &doc.Node{ID: fmt.Sprintf("p%d", i), Type: "paragraph"}
		n.SetDelta(doc.Delta{{Text: strings.Repeat("words ", 50)}})
		blocks = append(blocks, n)
		if i%8 == 0 {
			blocks = append(blocks, &doc.Node{ID: fmt.Sprintf("e%d", i), Type: "embed", Data: map[string]any{"title": "Sim", "html": strings.Repeat("<p>x</p>", 3000)}})
		}
	}
	blocks = append(blocks, &doc.Node{ID: "huge", Type: "embed", Data: map[string]any{"title": "Huge", "html": strings.Repeat("y", 40_000)}})
	p, err := st.CreatePage(store.NewPageOptions{Title: "Long", Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "test")
	var read []string
	for offset, parts := 0.0, 0; ; parts++ {
		out := s.pages.call("read_page", map[string]any{"page_id": p.ID, "offset": offset})
		if out["error"] != nil || parts > len(blocks) {
			t.Fatal(out)
		}
		if size := len(jsonText(out)); size > maxRead+2_000 {
			t.Errorf("part %d is %d bytes", parts, size)
		}
		if out["total_blocks"] != len(blocks) {
			t.Errorf("total_blocks %v, want %d", out["total_blocks"], len(blocks))
		}
		for _, b := range out["blocks"].([]map[string]any) {
			read = append(read, b["id"].(string))
			if b["id"] == "huge" {
				e := b["embed"].(map[string]any)
				if _, ok := e["html"]; ok || e["html_length"] != 40_000 {
					t.Errorf("the huge embed reads %v", e["html_length"])
				}
			}
		}
		next, ok := out["next_offset"].(int)
		if !ok {
			break
		}
		if parts == 0 && out["comments"] == nil || parts > 0 && out["comments"] != nil {
			t.Errorf("part %d: comments %v", parts, out["comments"])
		}
		offset = float64(next)
	}
	var want []string
	for _, b := range blocks {
		want = append(want, b.ID)
	}
	if !slices.Equal(read, want) {
		t.Errorf("read %v\nwant %v", read, want)
	}
	if out := s.pages.call("read_page", map[string]any{"page_id": p.ID, "offset": 3.0, "limit": 2.0}); len(out["blocks"].([]map[string]any)) != 2 || out["next_offset"] != 5 {
		t.Errorf("limit 2 from 3: %v blocks, next %v", len(out["blocks"].([]map[string]any)), out["next_offset"])
	}
	if out := s.pages.call("read_page", map[string]any{"page_id": p.ID, "offset": -1.0}); out["error"] == nil {
		t.Error("read from offset -1")
	}
}

func TestInvalidRequestsAndNotificationWithAnID(t *testing.T) {
	got := session(t, NewServer(newStore(t), "0.1.0"),
		`{"jsonrpc":"2.0","id":3,"method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4}`,
		`{"jsonrpc":"1.0","id":5,"method":"ping"}`,
		`{"id":6,"method":"ping"}`)
	for _, id := range []float64{4, 5, 6} {
		if e, _ := got[id]["error"].(map[string]any); e == nil || e["code"].(float64) != -32600 {
			t.Errorf("request %v answered %v", id, got[id])
		}
	}
	if r, ok := got[3]["result"].(map[string]any); !ok || r == nil {
		t.Errorf("notification with an ID %v", got[3])
	}
}

func TestBatch(t *testing.T) {
	var out bytes.Buffer
	in := strings.Join([]string{
		`[{"jsonrpc":"2.0","id":2,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":3,"method":"tools/list"},5]`,
		`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`,
		`[]`,
	}, "\n")
	if err := NewServer(newStore(t), "0.1.0").Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d answers: %s", len(lines), out.String())
	}
	var batch []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &batch); err != nil {
		t.Fatalf("batch answer %s: %v", lines[0], err)
	}
	if len(batch) != 3 || batch[0]["id"] != 2.0 || batch[0]["result"] == nil || batch[1]["id"] != 3.0 || batch[1]["result"] == nil {
		t.Fatalf("batch answered %v", batch)
	}
	if e, _ := batch[2]["error"].(map[string]any); e == nil || e["code"] != -32600.0 || batch[2]["id"] != nil {
		t.Errorf("a number in a batch answered %v", batch[2])
	}
	var empty map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &empty); err != nil {
		t.Fatal(err)
	}
	if e, _ := empty["error"].(map[string]any); e == nil || e["code"] != -32600.0 {
		t.Errorf("an empty batch answered %v", empty)
	}
}

func TestTemplatesOutOfReach(t *testing.T) {
	st := newStore(t)
	template, err := st.CreatePage(store.NewPageOptions{Title: "PRD", Template: true})
	if err != nil {
		t.Fatal(err)
	}
	got := session(t, NewServer(st, "0.1.0"),
		call(2, "read_page", map[string]any{"page_id": template.ID}),
		call(3, "insert_blocks", map[string]any{"page_id": template.ID, "position": map[string]any{"at": "end"}, "blocks": []any{map[string]any{"type": "paragraph", "text": "x"}}}),
		call(4, "create_page", map[string]any{"title": "Inside", "parent_id": template.ID}),
		call(5, "rename_page", map[string]any{"page_id": template.ID, "title": "Mine"}))
	for id := 2.0; id <= 5; id++ {
		if out, failed := result(t, got[id]); !failed || !strings.Contains(out["error"].(string), "template") {
			t.Errorf("call %v: %v", id, out)
		}
	}
	if kids, _ := st.Children(template.ID, ""); len(kids) != 0 {
		t.Errorf("a page made inside the template: %v", kids)
	}
}

func TestReadPartReadsItsOwnBlocks(t *testing.T) {
	var blocks []*doc.Node
	for i := range 30 {
		blocks = append(blocks, &doc.Node{ID: fmt.Sprint("r", i), Type: "recording", Data: map[string]any{"recording_id": fmt.Sprint("rec", i)}})
	}
	blocks = append(blocks, &doc.Node{ID: "k", Type: "link_preview", Data: map[string]any{"url": "https://go.dev/", "title": "Go"}})
	c := doc.FromBlocks(blocks)
	var asked []string
	tc := agents.ToolContext{SiteInfo: true,
		Subpages: func() []map[string]any { return nil }, Comments: func() []map[string]any { return nil },
		Transcript: func(_, id string) string {
			asked = append(asked, id)
			return strings.Repeat("words ", 2000)
		}}
	p := store.Page{ID: "p", Title: "Talks"}
	out := readPart(tc, p, c, 0, 0)
	part := out["blocks"].([]map[string]any)
	if len(part) == 0 || len(part) > 10 || len(asked) > len(part)+1 || out["next_offset"] != len(part) {
		t.Fatalf("a part of %d blocks read %d transcripts, next %v", len(part), len(asked), out["next_offset"])
	}
	asked = nil
	last := readPart(tc, p, c, len(blocks)-1, 0)
	if len(asked) != 0 || last["comments"] != nil {
		t.Errorf("the last part read %d transcripts, comments %v", len(asked), last["comments"])
	}
	if link := last["blocks"].([]map[string]any)[0]["link"].(map[string]any); link["title"] != "Go" {
		t.Errorf("the link card reads %v", link)
	}
}

func TestReadsKeptAreTheLatest(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "test")
	var ids []string
	for i := range keptReads + 4 {
		p, err := st.CreatePage(store.NewPageOptions{Title: fmt.Sprint("Page ", i), Content: doc.FromBlocks(nil)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
		if out := s.pages.call("read_page", map[string]any{"page_id": p.ID}); out["error"] != nil {
			t.Fatal(out)
		}
	}
	if len(s.pages.read) != keptReads || len(s.pages.order) != keptReads || s.pages.read[ids[0]] != nil || s.pages.read[ids[len(ids)-1]] == nil {
		t.Errorf("kept %d reads, %d in order", len(s.pages.read), len(s.pages.order))
	}
	s.pages.call("read_page", map[string]any{"page_id": ids[4]})
	if s.pages.order[len(s.pages.order)-1] != ids[4] || len(s.pages.order) != keptReads {
		t.Errorf("order %v", s.pages.order)
	}
}

func TestCurrentPage(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "1.0.0")
	current := func() map[string]any {
		t.Helper()
		out, isErr := result(t, session(t, s, initialize, call(2, "current_page", nil))[2])
		if isErr {
			t.Fatalf("current_page: %v", out)
		}
		return out
	}
	if got := current(); got["open"] != false || got["page_id"] != nil {
		t.Fatalf("no page ever opened: %v", got)
	}

	p, err := st.CreatePage(store.NewPageOptions{Title: "Roadmap"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue(store.LastPageKey, p.ID); err != nil {
		t.Fatal(err)
	}
	if got := current(); got["open"] != false || got["page_id"] != p.ID || got["title"] != "Roadmap" || got["selection"] != nil {
		t.Fatalf("app closed: %v", got)
	}

	view := store.OpenView{PageID: p.ID, At: time.Now(), Selection: store.ViewSelection{
		StartBlock: "b1", StartOffset: 2, EndBlock: "b1", EndOffset: 4, Text: "it", Color: "#ef4444ff", MixedColors: true,
	}}
	if err := st.SetOpenView(&view); err != nil {
		t.Fatal(err)
	}
	got := current()
	sel, _ := got["selection"].(map[string]any)
	if got["open"] != true || got["page_id"] != p.ID || sel["text"] != "it" || sel["color"] != "#ef4444ff" || sel["mixed_colors"] != true || sel["start_block"] != "b1" || sel["end_offset"] != 4.0 {
		t.Fatalf("app open: %v", got)
	}

	tpl, err := st.CreatePage(store.NewPageOptions{Title: "Weekly", Template: true})
	if err != nil {
		t.Fatal(err)
	}
	view.PageID = tpl.ID
	if err := st.SetOpenView(&view); err != nil {
		t.Fatal(err)
	}
	if got := current(); got["open"] != false || got["selection"] != nil || got["page_id"] != nil {
		t.Fatalf("template: %v", got)
	}

	if err := st.SetOpenView(nil); err != nil {
		t.Fatal(err)
	}
	if v, err := st.OpenView(); v != nil || err != nil {
		t.Fatalf("view after it ended: %v, %v", v, err)
	}
}
