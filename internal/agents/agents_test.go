package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"leaflet/internal/doc"
)

func page() *doc.Content {
	h := &doc.Node{ID: "h1", Type: "heading", Data: map[string]any{"level": 1.0}}
	h.SetDelta(doc.FromMarkdown("Plan"))
	p := &doc.Node{ID: "p1", Type: "paragraph"}
	p.SetDelta(doc.FromMarkdown("Ship the editor first."))
	l := &doc.Node{ID: "l1", Type: "bulleted_list"}
	l.SetDelta(doc.FromMarkdown("item"))
	return &doc.Content{Children: []*doc.Node{h, p, l}, Blocks: map[string]int64{}, Deleted: map[string]int64{}}
}

func executor(c *doc.Content) *Executor {
	return &Executor{Ctx: ToolContext{Content: c, PageID: "page", PageTitle: "Plan", AgentName: "Test"}}
}

func TestRejectUnknownIDs(t *testing.T) {
	c := page()
	before := c.Encode()
	x := executor(c)
	out := x.Call("replace_block", map[string]any{"id": "nope", "block": map[string]any{"type": "paragraph", "text": "x"}})
	if e, _ := out["error"].(string); !strings.Contains(e, "nope") {
		t.Errorf("out %v", out)
	}
	if c.Encode() != before || len(x.Ops) != 0 {
		t.Error("the page changed")
	}
}

func TestRejectNestingUnderParagraph(t *testing.T) {
	x := executor(page())
	out := x.Call("insert_blocks", map[string]any{"position": map[string]any{"parent": "p1"}, "blocks": []any{map[string]any{"type": "paragraph", "text": "x"}}})
	if e, _ := out["error"].(string); !strings.Contains(e, "list") {
		t.Errorf("out %v", out)
	}
}

func TestReadPage(t *testing.T) {
	out := executor(page()).Call("read_page", nil)
	blocks := out["blocks"].([]map[string]any)
	if blocks[0]["id"] != "h1" || blocks[1]["text"] != "Ship the editor first." {
		t.Errorf("blocks %v", blocks)
	}
}

func TestMoveInsideItself(t *testing.T) {
	c := page()
	kid := &doc.Node{ID: "l2", Type: "bulleted_list"}
	c.Children[2].Children = []*doc.Node{kid}
	out := executor(c).Call("move_block", map[string]any{"id": "l1", "position": map[string]any{"after": "l2"}})
	if out["error"] == nil {
		t.Errorf("out %v", out)
	}
}

// fakeModel answers chat requests with scripted responses.
type fakeModel struct {
	mu       sync.Mutex
	replies  []map[string]any
	requests []map[string]any
	status   int
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.requests = append(f.requests, body)
	if f.status != 0 {
		w.WriteHeader(f.status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "model not found"}})
		return
	}
	reply := f.replies[0]
	f.replies = f.replies[1:]
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": reply, "finish_reason": "stop"}}})
}

func call(name string, args map[string]any) map[string]any {
	raw, _ := json.Marshal(args)
	return map[string]any{"id": "c_" + name, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}
}

func TestLoopAppliesAndUndoes(t *testing.T) {
	f := &fakeModel{replies: []map[string]any{
		{"content": "", "tool_calls": []any{call("read_page", map[string]any{})}},
		{"content": "", "tool_calls": []any{
			call("insert_blocks", map[string]any{"position": map[string]any{"after": "p1"}, "blocks": []any{map[string]any{
				"type": "chart", "chart": map[string]any{"chart_type": "bar", "title": "Risks", "labels": []any{"Editor", "Agents"},
					"series": []any{map[string]any{"name": "Open risks", "values": []any{1, 4}}}},
			}}}),
			call("replace_block", map[string]any{"id": "p1", "block": map[string]any{"type": "paragraph", "text": "Ship the **editor** first."}}),
		}},
		{"content": "<think>hmm</think>Added a chart of risks by phase."},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := page()
	original := c.Encode()
	var seen []string
	res, err := RunLocal(context.Background(), NewClient(srv.URL, ""), Request{Model: "m", AgentName: "Test", SupportsTools: true, Instruction: "chart it"},
		executor(c).Ctx, Events{Op: func(op doc.Op, _ string) { seen = append(seen, op.Op) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "Added a chart of risks by phase." {
		t.Errorf("reply %q", res.Reply)
	}
	if strings.Join(seen, ",") != "insert,replace" || len(res.Ops) != 2 {
		t.Errorf("ops %v", seen)
	}
	var types []string
	for _, n := range c.Children {
		types = append(types, n.Type)
	}
	if strings.Join(types, ",") != "heading,paragraph,chart,bulleted_list" {
		t.Errorf("types %v", types)
	}
	if got := c.Children[1].Delta().ToMarkdown(); got != "Ship the **editor** first." {
		t.Errorf("p1 %q", got)
	}
	if tools, _ := f.requests[0]["tools"].([]any); len(tools) == 0 {
		t.Error("no tools sent")
	}
	for _, o := range doc.InvertRun(res.Ops) {
		if err := c.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	if c.Encode() != original {
		t.Errorf("undo left\n%s", c.Encode())
	}
}

func TestBadCallIsReported(t *testing.T) {
	f := &fakeModel{replies: []map[string]any{
		{"tool_calls": []any{call("delete_blocks", map[string]any{"ids": []any{"missing"}})}},
		{"tool_calls": []any{call("delete_blocks", map[string]any{"ids": []any{"p1"}})}},
		{"content": "Deleted it."},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := page()
	res, err := RunLocal(context.Background(), NewClient(srv.URL, ""), Request{Model: "m", SupportsTools: true}, executor(c).Ctx, Events{})
	if err != nil {
		t.Fatal(err)
	}
	msgs := f.requests[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if !strings.Contains(last["content"].(string), "missing") {
		t.Errorf("the model did not see the error: %v", last)
	}
	if len(res.Ops) != 1 || c.Find("p1") != nil {
		t.Errorf("ops %d", len(res.Ops))
	}
}

func TestChatOnlyModels(t *testing.T) {
	f := &fakeModel{replies: []map[string]any{{"content": "Here is a summary."}}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	res, err := RunLocal(context.Background(), NewClient(srv.URL, ""), Request{Model: "m"}, executor(page()).Ctx, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.requests[0]["tools"]; ok {
		t.Error("tools sent to a chat-only model")
	}
	if res.Reply != "Here is a summary." {
		t.Errorf("reply %q", res.Reply)
	}
}

func TestProviderErrors(t *testing.T) {
	f := &fakeModel{status: 404}
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, err := RunLocal(context.Background(), NewClient(srv.URL, ""), Request{Model: "m", SupportsTools: true}, executor(page()).Ctx, Events{})
	if err == nil || err.Error() != "model not found" {
		t.Errorf("err %v", err)
	}
}
