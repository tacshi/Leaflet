package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	mapsvc "leaflet/internal/maps"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
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
	for _, n := range []string{"list_pages", "search_pages", "current_page", "read_page", "write_markdown", "insert_blocks", "replace_block", "move_block", "delete_blocks", "create_page", "rename_page", "set_page_tags", "move_page", "create_folder", "rename_folder", "move_folder", "upload_files"} {
		if names[n] == nil {
			t.Errorf("no tool %s", n)
		}
	}
	createProps := names["create_page"]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if desc, _ := names["create_page"]["description"].(string); !strings.Contains(desc, "at the top level unless") {
		t.Errorf("create_page description %q", desc)
	}
	if desc, _ := createProps["parent_id"].(map[string]any)["description"].(string); !strings.Contains(desc, "not because it is open") {
		t.Errorf("create_page parent_id description %q", desc)
	}
	if desc, _ := createProps["folder_id"].(map[string]any)["description"].(string); !strings.Contains(desc, "the user asks for it in") {
		t.Errorf("create_page folder_id description %q", desc)
	}
	if inst, _ := init["instructions"].(string); !strings.Contains(inst, "at the top level unless") {
		t.Errorf("instructions %q", inst)
	}
	required := names["insert_blocks"]["inputSchema"].(map[string]any)["required"].([]any)
	if required[0] != "page_id" {
		t.Errorf("insert_blocks requires %v", required)
	}
	for name, want := range map[string][3]any{
		"read_page": {true, nil, false}, "insert_blocks": {false, false, true}, "delete_blocks": {false, true, true},
		"move_page": {false, true, true}, "rename_page": {false, true, true}, "move_block": {false, true, true},
		"find_place": {true, nil, true}, "find_route": {true, nil, true}, "list_pages": {true, nil, false},
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
	if len(history) != 1 || core.HistoryLabel(history[0]) != locales.DialogHistoryBeforeAgent("Claude Code") || strings.Contains(history[0].Content, "Founded") {
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
	if history, _ := st.History(id); len(history) != 2 || core.HistoryLabel(history[0]) != locales.DialogHistoryBeforeAgent("Codex") {
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

// An edit saved again, as the page changed while it was saved, keeps the
// page as it was just before the edit that went in: going back to it keeps
// what someone wrote in between.
func TestEditRunAgainKeepsThePageJustBefore(t *testing.T) {
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
	h, err := st.History(p.ID)
	if err != nil || len(h) == 0 || h[0].Reason != "agent_run" {
		t.Fatalf("history %+v, %v", h, err)
	}
	if kept, err := doc.Decode(h[0].Content); err != nil || kept.Find("theirs") == nil || strings.Contains(kept.PlainText(), "Inserted") {
		t.Errorf("the snapshot before the edit holds %s, %v", h[0].Content, err)
	}
	c := st.Content(p.ID)
	if saves != 2 || c.Find("theirs") == nil || !strings.Contains(c.PlainText(), "Inserted") {
		t.Errorf("%d saves, stored %q", saves, c.PlainText())
	}
	// Saved again as the page stood, the edit keeps no second snapshot.
	saves = 1
	if out := s.pages.call("insert_blocks", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"},
		"blocks": []any{map[string]any{"type": "paragraph", "text": "More"}}}); out["error"] != nil {
		t.Fatal(out)
	}
	if again, _ := st.History(p.ID); len(again) != len(h) {
		t.Errorf("history %d, then %d", len(h), len(again))
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
		if got := core.HistoryLabel(h[0]); got != locales.DialogHistoryConflict() {
			t.Errorf("%s: the conflict shows as %q", op, got)
		}
		if n := len(slices.DeleteFunc(slices.Clone(h), func(e store.HistoryEntry) bool { return e.Content != h[0].Content })); n != 1 {
			t.Errorf("%s: the version with the user's fix is kept %d times", op, n)
		}
	}
}

func TestEditRunAgainOverTheSameChangeKeepsOneConflict(t *testing.T) {
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
	c := st.Content(p.ID)
	c.Find("b1").SetDelta(doc.Delta{{Text: "the user's fix"}})
	if _, _, err := st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	saves := 0
	was := beforeSave
	beforeSave = func(id string) {
		if saves++; saves > 1 {
			return
		}
		if _, err := st.DB().Exec(`UPDATE page_contents SET content = content || ' ' WHERE page_id = ?`, id); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeSave = was })
	out := s.pages.call("replace_block", map[string]any{"page_id": p.ID, "id": "b1", "block": map[string]any{"type": "paragraph", "text": "agent's"}})
	if out["error"] != nil {
		t.Fatal(out)
	}
	h, _ := st.History(p.ID)
	conflicts := 0
	for _, e := range h {
		if e.Reason == "conflict" {
			conflicts++
		}
	}
	if saves != 2 || conflicts != 1 {
		t.Errorf("%d saves, %d conflict snapshots", saves, conflicts)
	}
}

// TestChangeNotReadStaysAConflict has the user change b2 after the agent read
// it, then the agent read or edit something else before it rewrites b2: only
// reading b2 again makes its rewrite no conflict.
func TestChangeNotReadStaysAConflict(t *testing.T) {
	text := func(id, s string) map[string]any {
		return map[string]any{"id": id, "block": map[string]any{"type": "paragraph", "text": s}}
	}
	type step struct {
		tool string
		args map[string]any
	}
	for _, c := range []struct {
		name     string
		between  []step
		conflict bool
	}{
		{"an edit of another block", []step{{"replace_block", text("b1", "agent's b1")}}, true},
		{"a part without it", []step{{"read_page", map[string]any{"offset": 0.0, "limit": 1.0}}}, true},
		{"edits of other blocks", []step{
			{"replace_block", text("b1", "agent's b1")},
			{"insert_blocks", map[string]any{"position": "end", "blocks": []any{map[string]any{"type": "paragraph", "text": "new"}}}},
		}, true},
		{"a part with it", []step{{"read_page", map[string]any{"offset": 1.0, "limit": 1.0}}}, false},
		{"the whole page", []step{{"read_page", map[string]any{}}}, false},
	} {
		st := newStore(t)
		var blocks []*doc.Node
		for _, id := range []string{"b1", "b2"} {
			b := &doc.Node{ID: id, Type: "paragraph"}
			b.SetDelta(doc.Delta{{Text: "draft " + id}})
			blocks = append(blocks, b)
		}
		p, err := st.CreatePage(store.NewPageOptions{Content: doc.FromBlocks(blocks)})
		if err != nil {
			t.Fatal(err)
		}
		s := NewServer(st, "test")
		call := func(name string, args map[string]any) map[string]any {
			args = maps.Clone(args)
			args["page_id"] = p.ID
			out := s.pages.call(name, args)
			if out["error"] != nil {
				t.Fatalf("%s: %s: %v", c.name, name, out)
			}
			return out
		}
		call("read_page", map[string]any{})
		content := st.Content(p.ID)
		content.Find("b2").SetDelta(doc.Delta{{Text: "the user's fix"}})
		if _, _, err := st.SaveContent(p.ID, content); err != nil {
			t.Fatal(err)
		}
		for _, s := range c.between {
			call(s.tool, s.args)
		}
		out := call("replace_block", text("b2", "agent's b2"))
		h, _ := st.History(p.ID)
		if got := h[0].Reason == "conflict"; got != c.conflict || !strings.Contains(h[0].Content, "the user's fix") {
			t.Errorf("after %s: newest history %q (%s), want conflict %v", c.name, h[0].Content, h[0].Reason, c.conflict)
		}
		if note, _ := out["conflict"].(string); (note != "") != c.conflict || c.conflict && !strings.Contains(note, "b2") {
			t.Errorf("after %s: the answer's conflict note is %q", c.name, note)
		}
	}
}

func TestPartReturnsATablesCellsButNotNestedBlocks(t *testing.T) {
	table, err := doc.TableNode([][]string{{"a", "b"}}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	item := &doc.Node{ID: "l1", Type: "bulleted_list", Children: []*doc.Node{{ID: "l2", Type: "bulleted_list"}}}
	c := doc.FromBlocks([]*doc.Node{{ID: "p1", Type: "paragraph"}, table, item})
	views := doc.BlockViews(c)
	var part []map[string]any
	for _, v := range views {
		if v["id"] == "t1" || v["id"] == "l1" {
			part = append(part, v)
		}
	}
	got := returned(c, part)
	want := map[string]bool{"t1": true, "l1": true}
	doc.FromBlocks([]*doc.Node{table}).Walk(func(n, _ *doc.Node) { want[n.ID] = true })
	if !maps.Equal(got, want) {
		t.Errorf("returned %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
}

// versionPage makes a page of paragraphs b1 and b2 and reads it over s,
// returning each block's version.
func versionPage(t *testing.T) (*store.Store, *Server, store.Page, map[string]string) {
	t.Helper()
	st := newStore(t)
	var blocks []*doc.Node
	for _, id := range []string{"b1", "b2"} {
		b := &doc.Node{ID: id, Type: "paragraph"}
		b.SetDelta(doc.Delta{{Text: "draft " + id}})
		blocks = append(blocks, b)
	}
	p, err := st.CreatePage(store.NewPageOptions{Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "test")
	return st, s, p, readVersions(t, s, p.ID)
}

func readVersions(t *testing.T, s *Server, pageID string) map[string]string {
	t.Helper()
	out := s.pages.call("read_page", map[string]any{"page_id": pageID})
	if out["error"] != nil {
		t.Fatal(out)
	}
	got := map[string]string{}
	for _, v := range out["blocks"].([]map[string]any) {
		version, _ := v["version"].(string)
		if len(version) != 10 {
			t.Fatalf("block %v has version %q", v["id"], version)
		}
		got[v["id"].(string)] = version
	}
	return got
}

func userEdits(t *testing.T, st *store.Store, pageID, id, text string) {
	t.Helper()
	c := st.Content(pageID)
	c.Find(id).SetDelta(doc.Delta{{Text: text}})
	if _, _, err := st.SaveContent(pageID, c); err != nil {
		t.Fatal(err)
	}
}

func TestVersionLeavesABlockChangedSinceRead(t *testing.T) {
	st, s, p, read := versionPage(t)
	userEdits(t, st, p.ID, "b2", "the user's fix")
	history := func() int { h, _ := st.History(p.ID); return len(h) }
	had := history()
	replace := func(version string) map[string]any {
		return s.pages.call("replace_block", map[string]any{"page_id": p.ID, "id": "b2", "version": version,
			"block": map[string]any{"type": "paragraph", "text": "agent's b2"}})
	}
	out := replace(read["b2"])
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "has changed since you read it") || !strings.Contains(msg, "the user's fix") {
		t.Fatalf("answer %v", out)
	}
	if got := st.Content(p.ID).Find("b2").Delta().Plain(); got != "the user's fix" || history() != had {
		t.Fatalf("b2 %q, %d history entries, had %d", got, history(), had)
	}
	now := readVersions(t, s, p.ID)
	if now["b2"] == read["b2"] || now["b1"] != read["b1"] || !strings.Contains(msg, now["b2"]) {
		t.Fatalf("versions %v, then %v; answer %q", read, now, msg)
	}
	if out := replace(now["b2"]); out["error"] != nil || out["conflict"] != nil {
		t.Fatalf("with the new version: %v", out)
	}
	if got := st.Content(p.ID).Find("b2").Delta().Plain(); got != "agent's b2" {
		t.Errorf("b2 %q", got)
	}
}

func TestVersionReadElsewhereIsNoConflict(t *testing.T) {
	st, s, p, _ := versionPage(t)
	userEdits(t, st, p.ID, "b2", "the user's fix")
	other := readVersions(t, NewServer(st, "test"), p.ID)
	out := s.pages.call("replace_block", map[string]any{"page_id": p.ID, "id": "b2", "version": other["b2"],
		"block": map[string]any{"type": "paragraph", "text": "agent's b2"}})
	if out["error"] != nil || out["conflict"] != nil {
		t.Fatalf("answer %v", out)
	}
	if h, _ := st.History(p.ID); h[0].Reason == "conflict" {
		t.Errorf("a conflict kept for a block read as it is")
	}
}

func TestDeleteWithAChangedVersionDeletesNothing(t *testing.T) {
	st, s, p, read := versionPage(t)
	userEdits(t, st, p.ID, "b2", "the user's fix")
	out := s.pages.call("delete_blocks", map[string]any{"page_id": p.ID, "ids": []any{"b1", "b2"},
		"versions": map[string]any{"b1": read["b1"], "b2": read["b2"]}})
	if msg, _ := out["error"].(string); !strings.Contains(msg, "Block b2 has changed") {
		t.Fatalf("answer %v", out)
	}
	c := st.Content(p.ID)
	if c.Find("b1") == nil || c.Find("b2") == nil {
		t.Errorf("blocks deleted: %s", c.PlainText())
	}
	for _, bad := range []any{"v", map[string]any{"b1": 1.0}} {
		if out := s.pages.call("delete_blocks", map[string]any{"page_id": p.ID, "ids": []any{"b1"}, "versions": bad}); out["error"] == nil {
			t.Errorf("versions %v taken: %v", bad, out)
		}
	}
	if out := s.pages.call("replace_block", map[string]any{"page_id": p.ID, "id": "b1", "version": 1.0,
		"block": map[string]any{"type": "paragraph", "text": "x"}}); out["error"] == nil {
		t.Errorf("a number taken as a version: %v", out)
	}
}

// TestNestedChangeNotReadIsAConflict has the user add or change a list item
// nested in l1 after the agent read the page. An edit that takes the item out
// with l1 replaces that change, though l1's version, of l1's own view, still
// matches, and though the agent edited another block since; a rewrite of l1's
// text, which keeps its nested items as they are, does not.
func TestNestedChangeNotReadIsAConflict(t *testing.T) {
	addEggs := func(c *doc.Content) {
		eggs := &doc.Node{ID: "l3", Type: "bulleted_list"}
		eggs.SetDelta(doc.Delta{{Text: "the user's eggs"}})
		c.Find("l1").Children = append(c.Find("l1").Children, eggs)
	}
	oatMilk := func(c *doc.Content) { c.Find("l2").SetDelta(doc.Delta{{Text: "the user's oat milk"}}) }
	rewrite := map[string]any{"id": "l1", "block": map[string]any{"type": "bulleted_list", "text": "shopping"}}
	withItems := map[string]any{"id": "l1", "block": map[string]any{"type": "bulleted_list", "text": "shopping",
		"children": []any{map[string]any{"type": "bulleted_list", "text": "bread"}}}}
	for _, c := range []struct {
		name     string
		user     func(*doc.Content)
		tool     string
		args     map[string]any
		conflict string
	}{
		{"deleting l1 after an item was added under it", addEggs, "delete_blocks", map[string]any{"ids": []any{"l1"}}, "l3"},
		{"rewriting l1's text after its item changed", oatMilk, "replace_block", rewrite, ""},
		{"rewriting l1 with new items after its item changed", oatMilk, "replace_block", withItems, "l2"},
	} {
		for _, version := range []bool{false, true} {
			for _, between := range []bool{false, true} {
				name := fmt.Sprintf("%s (version %v, an edit between %v)", c.name, version, between)
				st := newStore(t)
				item := &doc.Node{ID: "l1", Type: "bulleted_list", Children: []*doc.Node{{ID: "l2", Type: "bulleted_list"}}}
				item.SetDelta(doc.Delta{{Text: "groceries"}})
				item.Children[0].SetDelta(doc.Delta{{Text: "milk"}})
				p, err := st.CreatePage(store.NewPageOptions{Content: doc.FromBlocks([]*doc.Node{{ID: "p1", Type: "paragraph"}, item})})
				if err != nil {
					t.Fatal(err)
				}
				s := NewServer(st, "test")
				read := readVersions(t, s, p.ID)
				content := st.Content(p.ID)
				c.user(content)
				if _, _, err := st.SaveContent(p.ID, content); err != nil {
					t.Fatal(err)
				}
				if between {
					if out := s.pages.call("insert_blocks", map[string]any{"page_id": p.ID, "position": "end",
						"blocks": []any{map[string]any{"type": "paragraph", "text": "new"}}}); out["error"] != nil {
						t.Fatal(out)
					}
				}
				args := maps.Clone(c.args)
				args["page_id"] = p.ID
				if version && c.tool == "delete_blocks" {
					args["versions"] = map[string]any{"l1": read["l1"]}
				} else if version {
					args["version"] = read["l1"]
				}
				out := s.pages.call(c.tool, args)
				if out["error"] != nil {
					t.Fatalf("%s: %v", name, out)
				}
				h, _ := st.History(p.ID)
				note, _ := out["conflict"].(string)
				if kept := h[0].Reason == "conflict"; kept != (c.conflict != "") || (note != "") != (c.conflict != "") || !strings.Contains(note, c.conflict) {
					t.Errorf("%s: newest history %s, the answer's conflict note %q", name, h[0].Reason, note)
				}
				if c.conflict != "" && !strings.Contains(h[0].Content, "the user's") {
					t.Errorf("%s: the conflict kept %s", name, h[0].Content)
				}
			}
		}
	}
}

func TestChangedCellIsAChangeOfItsTable(t *testing.T) {
	st := newStore(t)
	table, err := doc.TableNode([][]string{{"Phase", "Owner"}, {"Editor", "Ana"}}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePage(store.NewPageOptions{Content: doc.FromBlocks([]*doc.Node{{ID: "p1", Type: "paragraph"}, table})})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "test")
	readVersions(t, s, p.ID)
	c := st.Content(p.ID)
	cell := doc.TableCells(c.Find("t1"))[1][1]
	cell.Children[0].SetDelta(doc.Delta{{Text: "the user's Sam"}})
	if _, _, err := st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	out := s.pages.call("replace_block", map[string]any{"page_id": p.ID, "id": "t1",
		"block": map[string]any{"type": "table", "rows": []any{[]any{"Phase", "Owner"}, []any{"Editor", "Bo"}}}})
	if note, _ := out["conflict"].(string); out["error"] != nil || !strings.HasPrefix(note, "Block t1 had changed") {
		t.Errorf("the answer %v", out)
	}
}

func TestMovingABlockKeepsItsVersion(t *testing.T) {
	st := newStore(t)
	var items []*doc.Node
	for _, id := range []string{"l1", "l2"} {
		n := &doc.Node{ID: id, Type: "bulleted_list"}
		n.SetDelta(doc.Delta{{Text: "item " + id}})
		items = append(items, n)
	}
	p, err := st.CreatePage(store.NewPageOptions{Content: doc.FromBlocks(items)})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "test")
	read := readVersions(t, s, p.ID)
	if out := s.pages.call("move_block", map[string]any{"page_id": p.ID, "id": "l2", "position": map[string]any{"parent": "l1"}}); out["error"] != nil {
		t.Fatal(out)
	}
	if st.Content(p.ID).Find("l1").Children == nil {
		t.Fatal("l2 was not moved into l1")
	}
	if now := readVersions(t, s, p.ID); !maps.Equal(now, read) {
		t.Errorf("versions %v, after the move %v", read, now)
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
		if size := len(jsonText(out)); size > 60_000 {
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

func TestPartsWithVersionsKeepToTheirSize(t *testing.T) {
	st := newStore(t)
	var blocks []*doc.Node
	for i := range 3000 {
		b := &doc.Node{ID: fmt.Sprintf("b%04d", i), Type: "paragraph"}
		b.SetDelta(doc.Delta{{Text: "x"}})
		blocks = append(blocks, b)
	}
	p, err := st.CreatePage(store.NewPageOptions{Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "test")
	read := 0
	for offset := 0; ; {
		out := s.pages.call("read_page", map[string]any{"page_id": p.ID, "offset": float64(offset)})
		if out["error"] != nil {
			t.Fatal(out)
		}
		if size := len(jsonText(out)); size > 58_000 {
			t.Errorf("the part from %d is %d bytes", offset, size)
		}
		for _, b := range out["blocks"].([]map[string]any) {
			if v, _ := b["version"].(string); len(v) != 10 {
				t.Fatalf("block %v has version %q", b["id"], v)
			}
			read++
		}
		next, ok := out["next_offset"].(int)
		if !ok {
			break
		}
		offset = next
	}
	if read != len(blocks) {
		t.Errorf("read %d of %d blocks", read, len(blocks))
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
	out := tc.ReadPart(p.ID, map[string]any{"id": p.ID}, c, 0, 0)
	part := out["blocks"].([]map[string]any)
	if len(part) == 0 || len(part) > 10 || len(asked) > len(part)+1 || out["next_offset"] != len(part) {
		t.Fatalf("a part of %d blocks read %d transcripts, next %v", len(part), len(asked), out["next_offset"])
	}
	asked = nil
	last := tc.ReadPart(p.ID, map[string]any{"id": p.ID}, c, len(blocks)-1, 0)
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

func TestNestedFolders(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize,
		call(2, "create_folder", map[string]any{"name": "Work"}),
		call(3, "create_folder", map[string]any{"name": "Home"}))
	out, _ := result(t, got[2])
	work := out["folder_id"].(string)
	out, _ = result(t, got[3])
	home := out["folder_id"].(string)
	got = session(t, s,
		call(4, "create_folder", map[string]any{"name": "Plans", "parent_folder_id": work}),
		call(5, "create_folder", map[string]any{"name": "Lost", "parent_folder_id": "nope"}),
		call(6, "move_folder", map[string]any{"folder_id": home, "parent_folder_id": work}))
	out, failed := result(t, got[4])
	if failed {
		t.Fatalf("create_folder in a folder %v", out)
	}
	plans := out["folder_id"].(string)
	if out, failed := result(t, got[5]); !failed || !strings.Contains(out["error"].(string), "nope") {
		t.Errorf("create_folder in no folder %v", out)
	}
	if out, failed := result(t, got[6]); failed {
		t.Errorf("move_folder %v", out)
	}
	got = session(t, s,
		call(7, "move_folder", map[string]any{"folder_id": work, "parent_folder_id": plans}),
		call(8, "move_folder", map[string]any{"folder_id": plans}),
		call(9, "list_pages", nil))
	if out, failed := result(t, got[7]); !failed || !strings.Contains(out["error"].(string), "inside it") {
		t.Errorf("move_folder into its own folder %v", out)
	}
	if out, failed := result(t, got[8]); failed {
		t.Errorf("move_folder to the top %v", out)
	}
	out, _ = result(t, got[9])
	parents := map[string]any{}
	for _, f := range out["folders"].([]any) {
		row := f.(map[string]any)
		parents[row["name"].(string)] = row["parent_folder_id"]
	}
	if parents["Home"] != work || parents["Plans"] != nil || parents["Work"] != nil {
		t.Errorf("folders %v", out["folders"])
	}
}

func TestFindRouteThenAMapAcrossCalls(t *testing.T) {
	st := newStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Trip"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "0.1.0")
	s.pages.findPlaces = func(q string, _ *doc.LatLng) ([]mapsvc.Place, error) {
		return []mapsvc.Place{{Name: q, Lat: 22.55206, Lng: 114.15382}}, nil
	}
	path := doc.EncodePath([]doc.LatLng{{Lat: 22.3825, Lng: 114.1873}, {Lat: 22.55206, Lng: 114.15382}}, doc.PathPrecision)
	s.pages.findRoutes = func(from, to doc.LatLng, mode string, _ time.Time) ([]doc.MapRoute, error) {
		return []doc.MapRoute{{Mode: mode, Source: doc.RouteFromOSRM, DurationS: 1600, DistanceM: 30_000, Legs: []doc.MapLeg{{Mode: mode, DurationS: 1600, Path: path}}}}, nil
	}
	got := session(t, s, initialize,
		call(2, "find_route", map[string]any{"from": map[string]any{"name": "Sha Tin", "lat": 22.3825, "lng": 114.1873}, "to": "Heung Yuen Wai", "mode": "drive"}))
	found, failed := result(t, got[2])
	if failed || found["routes"].([]any)[0].(map[string]any)["id"] != "r1" {
		t.Fatalf("find_route %v", found)
	}
	got = session(t, s, call(3, "insert_blocks", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"},
		"blocks": []any{map[string]any{"type": "map", "map": map[string]any{"title": "By car", "route": "r1"}}}}))
	if out, failed := result(t, got[3]); failed {
		t.Fatalf("insert_blocks %v", out)
	}
	var made *doc.Node
	st.Content(p.ID).Walk(func(n, _ *doc.Node) {
		if n.Type == "map" {
			made = n
		}
	})
	if m := doc.MapOf(made); made == nil || m.Route == nil || len(m.Places) != 2 || m.Places[1].Name != "Heung Yuen Wai" {
		t.Fatalf("map %+v", made)
	}
	got = session(t, s, call(4, "create_page", map[string]any{"title": "Again", "markdown": "```map\n{\"route\": \"r1\"}\n```"}))
	out, failed := result(t, got[4])
	if failed {
		t.Fatalf("create_page %v", out)
	}
	if n := st.Content(out["page_id"].(string)).Children; len(n) != 1 || n[0].Type != "map" {
		t.Errorf("the new page holds %+v", n)
	}
}

// A view the app left as it crashed is of no app: current_page answers as it
// does with the app closed.
func TestCurrentPageOfAnAppThatEnded(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "1.0.0")
	p, err := st.CreatePage(store.NewPageOptions{Title: "Roadmap"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue(store.LastPageKey, p.ID); err != nil {
		t.Fatal(err)
	}
	ended := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204
	if err := ended.Run(); err != nil {
		t.Fatal(err)
	}
	began := time.Now().Add(-time.Hour)
	for _, c := range []struct {
		pid   int
		began time.Time
		open  bool
	}{
		{os.Getpid(), time.Now(), true},
		{ended.Process.Pid, began, false},
		{os.Getpid(), began, false},
		{0, time.Time{}, true},
	} {
		raw, _ := json.Marshal(map[string]any{"page_id": p.ID, "selection": map[string]any{"text": "it"}, "updated_at": time.Now(), "pid": c.pid, "began": c.began})
		if err := st.SetValue("open.view", string(raw)); err != nil {
			t.Fatal(err)
		}
		out, isErr := result(t, session(t, s, initialize, call(2, "current_page", nil))[2])
		if isErr || out["open"] != c.open || out["page_id"] != p.ID || (out["selection"] != nil) != c.open {
			t.Errorf("the app of process %d, begun %v: %v", c.pid, c.began, out)
		}
	}
}

// list_pages answers in parts a client reads whole, however many pages the
// user has.
func TestListPagesInParts(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "test")
	parent, _ := st.CreatePage(store.NewPageOptions{Title: "Projects"})
	if _, err := st.CreateFolderIn("Work", ""); err != nil {
		t.Fatal(err)
	}
	for i := range 800 {
		if _, err := st.CreatePage(store.NewPageOptions{ParentID: parent.ID, Title: fmt.Sprintf("Meeting notes %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	next := any(nil)
	for parts := 0; ; parts++ {
		if parts > 10 {
			t.Fatal("list_pages never ends")
		}
		args := map[string]any{}
		if next != nil {
			args["offset"] = next
		}
		got := session(t, s, initialize, call(2, "list_pages", args))
		r := got[2]["result"].(map[string]any)
		text := r["content"].([]any)[0].(map[string]any)["text"].(string)
		if len(text) > listBytes+1_000 {
			t.Errorf("part %d is %d bytes", parts, len(text))
		}
		out, failed := result(t, got[2])
		if failed || out["total_pages"] != 801.0 || (out["folders"] != nil) != (parts == 0) {
			t.Fatalf("part %d: %v", parts, out["error"])
		}
		for _, p := range out["pages"].([]any) {
			seen[p.(map[string]any)["id"].(string)] = true
		}
		if next = out["next_offset"]; next == nil {
			break
		}
	}
	if len(seen) != 801 {
		t.Errorf("listed %d pages of 801", len(seen))
	}
	if out := s.Call("list_pages", map[string]any{"offset": -1.0}); out["error"] == nil {
		t.Errorf("offset -1 listed %v", out)
	}
	all, err := s.Listing()
	if err != nil || len(all["pages"].([]map[string]any)) != 801 || len(all["folders"].([]map[string]any)) != 1 {
		t.Errorf("the whole listing: %v", err)
	}
}

// An edit of a page read so long ago that what was read of it is let go
// replaces what may have changed since: it is kept as a conflict, and the
// agent told.
func TestEditOfAPageReadLongAgoKeepsAConflict(t *testing.T) {
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
	for i := range keptReads {
		q, _ := st.CreatePage(store.NewPageOptions{Title: fmt.Sprint("P", i), Content: doc.FromBlocks(nil)})
		s.pages.call("read_page", map[string]any{"page_id": q.ID})
	}
	if s.pages.read[p.ID] != nil {
		t.Fatal("the first page's sight is still kept")
	}
	c := st.Content(p.ID)
	c.Find("b1").SetDelta(doc.Delta{{Text: "the user's fix"}})
	if _, _, err := st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
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
	out := s.pages.call("replace_block", map[string]any{"page_id": p.ID, "id": "b1", "block": map[string]any{"type": "paragraph", "text": "agent's"}})
	if note, _ := out["conflict"].(string); out["error"] != nil || !strings.Contains(note, "too long ago") || conflicts() != 1 {
		t.Fatalf("after its sight was let go: %v, %d conflicts", out, conflicts())
	}
	// Read again, the page is seen as it is.
	if out := s.pages.call("read_page", map[string]any{"page_id": p.ID}); out["error"] != nil {
		t.Fatal(out)
	}
	if out := s.pages.call("replace_block", map[string]any{"page_id": p.ID, "id": "b1", "block": map[string]any{"type": "paragraph", "text": "again"}}); out["conflict"] != nil || out["error"] != nil {
		t.Errorf("after reading again: %v", out)
	}
}

// A sight keeps each block as a hash, so a session keeps many pages': no
// fewer than a long session reads.
func TestSightsOfManyPagesAreKept(t *testing.T) {
	if keptReads < 200 {
		t.Errorf("a session keeps the sights of %d pages", keptReads)
	}
	tools := pageTools{read: map[string]sight{}, forgot: map[string]bool{}}
	big := sight{}
	for i := range keptBlocks / 2 {
		big[fmt.Sprint(i)] = 1
	}
	tools.keep("a", big)
	tools.keep("b", big)
	tools.keep("c", sight{"x": 1})
	if tools.read["a"] != nil || !tools.forgot["a"] || tools.read["b"] == nil || tools.read["c"] == nil || tools.kept != len(big)+1 {
		t.Errorf("kept %v, forgot %v, %d blocks", slices.Collect(maps.Keys(tools.read)), tools.forgot, tools.kept)
	}
}

func TestRenameTakesTheSpaceAround(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "test")
	p, _ := st.CreatePage(store.NewPageOptions{Title: "x"})
	if out := s.pages.call("rename_page", map[string]any{"page_id": p.ID, "title": "  Plan\n"}); out["error"] != nil {
		t.Fatal(out)
	}
	if q, _ := st.Page(p.ID); q.Title != "Plan" {
		t.Errorf("renamed %q", q.Title)
	}
	if out := s.pages.call("rename_page", map[string]any{"page_id": p.ID, "title": " \n"}); out["error"] == nil {
		t.Errorf("renamed to nothing: %v", out)
	}
}

// The page and folder tools read their arguments as the workspace agent's do.
func TestPageToolsReadTheirArgumentsAsTheWorkspaceDoes(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "test")
	p, _ := st.CreatePage(store.NewPageOptions{Title: "x"})
	if out := s.pages.call("set_page_tags", map[string]any{"page_id": p.ID, "tags": []any{"work", 3.0}}); out["error"] != "tags must be a list of words." {
		t.Errorf("a tag that is no word: %v", out)
	}
	if q, _ := st.Page(p.ID); len(q.Tags) != 0 {
		t.Errorf("tags %v", q.Tags)
	}
	out := s.pages.call("create_folder", map[string]any{"name": " Work "})
	f, err := st.Folder(fmt.Sprint(out["folder_id"]))
	if err != nil || f.Name != "Work" {
		t.Fatalf("folder %+v, %v", f, err)
	}
	if out := s.pages.call("create_page", map[string]any{"title": "y", "parent_id": p.ID, "folder_id": f.ID}); !strings.Contains(fmt.Sprint(out["error"]), "not both") {
		t.Errorf("both: %v", out)
	}
	// A write takes no offset, which read_page alone does.
	if out := s.pages.call("write_markdown", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"}, "markdown": "More.", "offset": -1.0}); out["error"] != nil {
		t.Errorf("a write with a stray offset: %v", out)
	}
}

func TestRequestIDs(t *testing.T) {
	s := NewServer(newStore(t), "test")
	var out bytes.Buffer
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":{"a":1},"method":"ping"}`,
		`{"jsonrpc":"2.0","id":[1],"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":true,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"a","method":"ping"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	}, "\n")
	if err := s.Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 7 {
		t.Fatalf("answers %q", lines)
	}
	for _, l := range lines[:5] {
		var m map[string]any
		_ = json.Unmarshal([]byte(l), &m)
		if e, _ := m["error"].(map[string]any); e == nil || e["code"] != -32600.0 || m["id"] != nil || m["result"] != nil {
			t.Errorf("a request of a bad id answered %s", l)
		}
	}
	if lines[5] != `{"jsonrpc":"2.0","id":"a","result":{}}` || lines[6] != `{"jsonrpc":"2.0","id":7,"result":{}}` {
		t.Errorf("answers %q", lines[5:])
	}
}
