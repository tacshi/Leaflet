package mcp

import (
	"bytes"
	"encoding/json"
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
	if seen := st.ValuesWithPrefix(SeenPrefix); seen["claude-code"] == "" {
		t.Errorf("seen %v, want claude-code", seen)
	}
	names := map[string]map[string]any{}
	for _, tool := range got[2]["result"].(map[string]any)["tools"].([]any) {
		m := tool.(map[string]any)
		names[m["name"].(string)] = m
	}
	for _, n := range []string{"list_pages", "search_pages", "read_page", "insert_blocks", "replace_block", "move_block", "delete_blocks", "create_page", "rename_page"} {
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
