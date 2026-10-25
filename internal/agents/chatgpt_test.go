package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

func TestChatGPTResponses(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/responses" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		var resp map[string]any
		if len(bodies) == 1 {
			resp = map[string]any{"output": []any{map[string]any{"type": "function_call", "call_id": "c1", "name": "read_page", "arguments": "{}"}}}
		} else {
			resp = map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "Done."}}}}}
		}
		raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": resp})
		_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\nevent: response.completed\ndata: %s\n\n", raw)
	}))
	defer srv.Close()
	g := &ChatGPT{access: "tok", expires: time.Now().Add(time.Hour), base: srv.URL}
	res, err := RunLocal(context.Background(), g, Request{Model: "m", SupportsTools: true, Instruction: "go"}, executor(page()).Ctx, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "Done." {
		t.Errorf("reply %q", res.Reply)
	}
	first := bodies[0]
	if first["instructions"] == nil || first["store"] != false || first["stream"] != true {
		t.Errorf("first request %v", first)
	}
	if tools, _ := first["tools"].([]any); len(tools) == 0 || tools[0].(map[string]any)["name"] != "read_page" {
		t.Errorf("tools %v", first["tools"])
	}
	input := bodies[1]["input"].([]any)
	call := input[len(input)-2].(map[string]any)
	out := input[len(input)-1].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "c1" || out["type"] != "function_call_output" || out["call_id"] != "c1" {
		t.Errorf("second input ends %v %v", call, out)
	}
}

// The backend may send the items as they finish and end with an empty
// output: the call and the text still count.
func TestChatGPTStreamedItems(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(ev map[string]any) {
			raw, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], raw)
		}
		if calls == 1 {
			send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": "c1", "name": "ping", "arguments": `{"value":"ok"}`}})
		} else {
			send(map[string]any{"type": "response.output_text.delta", "delta": "All "})
			send(map[string]any{"type": "response.output_text.delta", "delta": "set."})
		}
		send(map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{}}})
	}))
	defer srv.Close()
	g := &ChatGPT{access: "tok", expires: time.Now().Add(time.Hour), base: srv.URL}
	res, err := g.Chat(context.Background(), "m", []map[string]any{{"role": "user", "content": "go"}}, nil, 0)
	if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "ping" {
		t.Fatalf("first answer %+v %v", res, err)
	}
	res, err = g.Chat(context.Background(), "m", []map[string]any{{"role": "user", "content": "go"}}, nil, 0)
	if err != nil || res.Content != "All set." {
		t.Errorf("second answer %+v %v", res, err)
	}
}

func TestRecordingViewsCarryTranscript(t *testing.T) {
	c := doc.FromBlocks([]*doc.Node{{ID: "r1", Type: "recording", Data: map[string]any{"recording_id": "rec", "title": "Standup"}}})
	tc := ToolContext{Content: c, Transcript: func(id string) string {
		if id != "rec" {
			return ""
		}
		return TranscriptText([]store.Segment{{StartMs: 1000, Text: "Ship on Friday"}, {StartMs: 26000, Text: "."}})
	}}
	views := tc.Views(c)
	rec, _ := views[0]["recording"].(map[string]any)
	if rec["transcript"] != "[0:01] Ship on Friday." {
		t.Errorf("view %v", views[0])
	}
}
