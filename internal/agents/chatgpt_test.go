package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// A page where the sign-in should answer with JSON, such as a region's
// block page or a network's own page when the proxy is not used, says
// the sign-in can't be reached rather than failing to read JSON.
func TestSignInJSONNotAPage(t *testing.T) {
	cases := []struct {
		status int
		ctype  string
		body   string
		ok     bool
	}{
		{403, "text/html; charset=UTF-8", "<!DOCTYPE html><title>Unsupported region</title>", false},
		{200, "text/html", "<html>Sign in to the Wi-Fi</html>", false},
		{200, "application/json; charset=utf-8", `{"issuer":"x"}`, true},
		{200, "application/jwk-set+json", `{"issuer":"x"}`, true},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", c.ctype)
			w.WriteHeader(c.status)
			_, _ = fmt.Fprint(w, c.body)
		}))
		var d chatgptDiscovery
		err := getSignInJSON(context.Background(), srv.URL, &d)
		srv.Close()
		switch {
		case c.ok && (err != nil || d.Issuer != "x"):
			t.Errorf("%d %s: %v %+v", c.status, c.ctype, err, d)
		case !c.ok && (err == nil || !strings.Contains(err.Error(), "couldn't reach ChatGPT's sign-in")):
			t.Errorf("%d %s: %v", c.status, c.ctype, err)
		}
	}
}

// Renewing the access token waits on the network without holding the
// account, which Settings reads on the main thread, and calls that need a
// token at once share one renewal.
func TestChatGPTRenewsOutsideTheAccount(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"new","refresh_token":"r2","expires_in":3600}`)
	}))
	defer srv.Close()
	discoveryMu.Lock()
	saved := discoveryCache
	discoveryCache = &chatgptDiscovery{TokenEndpoint: srv.URL + "/token"}
	discoveryMu.Unlock()
	t.Cleanup(func() {
		discoveryMu.Lock()
		discoveryCache = saved
		discoveryMu.Unlock()
	})
	g := NewChatGPT(ChatGPTAccount{ClientID: "c", Email: "me@example.com", RefreshToken: "r1"})
	toks := make(chan string, 2)
	for range 2 {
		go func() {
			tok, err := g.token(context.Background())
			if err != nil {
				t.Error(err)
			}
			toks <- tok
		}()
	}
	// Both are waiting on the renewal now, or about to.
	time.Sleep(50 * time.Millisecond)
	read := make(chan ChatGPTAccount, 1)
	go func() { read <- g.Account() }()
	select {
	case a := <-read:
		if a.Email != "me@example.com" {
			t.Errorf("account %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reading the account waited on the renewal")
	}
	close(release)
	for range 2 {
		if tok := <-toks; tok != "new" {
			t.Errorf("token %q", tok)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || g.Account().RefreshToken != "r2" {
		t.Errorf("%d renewals, refresh token %q", posts, g.Account().RefreshToken)
	}
}
