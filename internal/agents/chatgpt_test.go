package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/keychain"
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

// ChatGPT's answer may send nothing for longer than other models' while
// the model reasons: it is waited for, though not without end.
func TestChatGPTWaitsOutReasoning(t *testing.T) {
	savedIdle, savedStream := idleTimeout, streamIdleTimeout
	idleTimeout, streamIdleTimeout = 50*time.Millisecond, time.Second
	t.Cleanup(func() { idleTimeout, streamIdleTimeout = savedIdle, savedStream })
	var quiet atomic.Int64
	quiet.Store(int64(300 * time.Millisecond))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-time.After(time.Duration(quiet.Load())):
		case <-r.Context().Done():
			return
		}
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n")
	}))
	defer srv.Close()
	g := &ChatGPT{access: "tok", expires: time.Now().Add(time.Hour), base: srv.URL}
	res, err := g.Chat(context.Background(), "m", []map[string]any{{"role": "user", "content": "go"}}, nil, 0)
	if err != nil || res.Content != "Done." {
		t.Errorf("answer %+v %v", res, err)
	}
	quiet.Store(int64(time.Minute))
	if _, err := g.Chat(context.Background(), "m", []map[string]any{{"role": "user", "content": "go"}}, nil, 0); err == nil {
		t.Error("waited for an answer that stopped")
	}
}

func TestRecordingViewsCarryTranscript(t *testing.T) {
	c := doc.FromBlocks([]*doc.Node{{ID: "r1", Type: "recording", Data: map[string]any{"recording_id": "rec", "title": "Standup"}}})
	tc := ToolContext{Content: c, PageID: "page", Transcript: func(pageID, id string) string {
		if pageID != "page" || id != "rec" {
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

// fakeDiscovery has OpenAI's sign-in answer as d for the test.
func fakeDiscovery(t *testing.T, d *chatgptDiscovery) {
	discoveryMu.Lock()
	saved := discoveryCache
	discoveryCache = d
	discoveryMu.Unlock()
	t.Cleanup(func() {
		discoveryMu.Lock()
		discoveryCache = saved
		discoveryMu.Unlock()
	})
}

// An access token the API refuses before it said it would expire is
// renewed, and the request made again, once.
func TestChatGPTRenewsARefusedToken(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"new","refresh_token":"r2","expires_in":3600}`)
			return
		}
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
	}))
	defer srv.Close()
	fakeDiscovery(t, &chatgptDiscovery{TokenEndpoint: srv.URL + "/token"})
	g := &ChatGPT{acct: ChatGPTAccount{ClientID: "c", RefreshToken: "r1"}, access: "old", expires: time.Now().Add(time.Hour), base: srv.URL}
	models, err := g.ListModels(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("models %v, %v", models, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 2 || auths[0] != "Bearer old" || auths[1] != "Bearer new" {
		t.Errorf("requests %v", auths)
	}
}

// A renewal outlasts the run that asked for it, stopped meanwhile: the
// refresh token OpenAI rotated to is kept, rather than lost with the old
// one spent.
func TestChatGPTRenewalOutlastsAStoppedRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"new","refresh_token":"r2","expires_in":3600}`)
	}))
	defer srv.Close()
	fakeDiscovery(t, &chatgptDiscovery{TokenEndpoint: srv.URL + "/token"})
	var kept atomic.Value
	g := &ChatGPT{acct: ChatGPTAccount{ClientID: "c", RefreshToken: "r1"}, OnRefresh: func(a ChatGPTAccount) { kept.Store(a.RefreshToken) }}
	_, _ = g.token(ctx)
	if got := g.Account().RefreshToken; got != "r2" || kept.Load() != "r2" {
		t.Errorf("refresh token %q, kept %v", got, kept.Load())
	}
}

// A sign-in that another replaced, or that was signed out, keeps its
// renewals to itself: they do not overwrite the one kept, or bring back
// the one signed out.
func TestOldChatGPTSignInKeepsNothing(t *testing.T) {
	var mu sync.Mutex
	kept := map[string]string{}
	keep, forget := keepSecret, forgetSecret
	keepSecret = func(name string, v []byte) error {
		mu.Lock()
		defer mu.Unlock()
		kept[name] = string(v)
		return nil
	}
	forgetSecret = func(name string) error {
		mu.Lock()
		defer mu.Unlock()
		delete(kept, name)
		return nil
	}
	t.Cleanup(func() { keepSecret, forgetSecret = keep, forget })
	fakeDiscovery(t, &chatgptDiscovery{})
	refresh := func() string {
		mu.Lock()
		defer mu.Unlock()
		var a ChatGPTAccount
		_ = json.Unmarshal([]byte(kept["chatgpt.p"]), &a)
		return a.RefreshToken
	}

	old := NewChatGPT(ChatGPTAccount{RefreshToken: "old"})
	if err := UseChatGPT("p", old); err != nil {
		t.Fatal(err)
	}
	now := NewChatGPT(ChatGPTAccount{RefreshToken: "new"})
	if err := UseChatGPT("p", now); err != nil {
		t.Fatal(err)
	}
	old.OnRefresh(ChatGPTAccount{RefreshToken: "old2"})
	if got := refresh(); got != "new" {
		t.Errorf("kept %q after an old sign-in renewed", got)
	}
	now.OnRefresh(ChatGPTAccount{RefreshToken: "new2"})
	if got := refresh(); got != "new2" {
		t.Errorf("kept %q after the sign-in renewed", got)
	}
	if err := ForgetChatGPT(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	now.OnRefresh(ChatGPTAccount{RefreshToken: "new3"})
	mu.Lock()
	defer mu.Unlock()
	if _, ok := kept["chatgpt.p"]; ok {
		t.Errorf("a sign-out came back: %v", kept)
	}
}

// What is drawn asks for a sign-in without waiting on the keychain, which
// may be waiting on the user while another goroutine reads it.
func TestKnownChatGPTDoesNotWaitOnTheKeychain(t *testing.T) {
	reading, answer := make(chan struct{}), make(chan struct{})
	was := readSecret
	readSecret = func(string) ([]byte, error) {
		close(reading)
		<-answer
		return []byte(`{"refresh_token":"r"}`), nil
	}
	t.Cleanup(func() {
		readSecret = was
		chatgptMu.Lock()
		defer chatgptMu.Unlock()
		delete(chatgptClients, "slow")
	})
	h := &Hub{}
	read := make(chan *ChatGPT, 1)
	go func() {
		g, _ := h.ChatGPT("slow")
		read <- g
	}()
	<-reading
	known := make(chan *ChatGPT, 1)
	go func() { known <- h.KnownChatGPT("slow") }()
	select {
	case g := <-known:
		if g != nil {
			t.Errorf("known before it was read: %v", g)
		}
	case <-time.After(5 * time.Second):
		t.Error("KnownChatGPT waited on the keychain")
	}
	close(answer)
	if g := <-read; g == nil || h.KnownChatGPT("slow") != g {
		t.Errorf("read %v, known %v", g, h.KnownChatGPT("slow"))
	}
}

// A sign-in signed out while the keychain was reading it does not come
// back from what the keychain gave.
func TestChatGPTSignedOutAsItIsRead(t *testing.T) {
	useMemKeys(t)
	reading, answer := make(chan struct{}), make(chan struct{})
	was := readSecret
	first := true
	readSecret = func(name string) ([]byte, error) {
		if first {
			first = false
			close(reading)
			<-answer
			return []byte(`{"refresh_token":"r"}`), nil
		}
		return nil, keychain.ErrNotFound
	}
	t.Cleanup(func() { readSecret = was })
	h := &Hub{}
	failed := make(chan error, 1)
	go func() {
		_, err := h.ChatGPT("gone")
		failed <- err
	}()
	<-reading
	if err := ForgetChatGPT(context.Background(), "gone"); err != nil {
		t.Fatal(err)
	}
	close(answer)
	if err := <-failed; err == nil || h.KnownChatGPT("gone") != nil {
		t.Errorf("signed out, and kept: %v", err)
	}
}

// A sign-in that leaves the keychain slowly, as a prompt can make it, is
// not kept by a read that came in the meantime: one that ended before it
// has gone hands on what will be no sign-in, whose renewals go nowhere, and
// one that ends after finds it gone.
func TestChatGPTReadWhileItGoes(t *testing.T) {
	keys := useMemKeys(t)
	fakeDiscovery(t, &chatgptDiscovery{})
	if err := SaveChatGPT("going", ChatGPTAccount{RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	dropping, goOn, reading, late := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	was := readSecret
	var reads atomic.Int32
	readSecret = func(name string) ([]byte, error) {
		switch reads.Add(1) {
		case 1: // the sign-out's
			close(dropping)
			<-goOn
		case 2: // the read that ends after it
			close(reading)
			<-late
		}
		return was(name)
	}
	t.Cleanup(func() { readSecret = was })
	h := &Hub{}
	gone := make(chan error, 1)
	go func() { gone <- ForgetChatGPT(context.Background(), "going") }()
	<-dropping
	failed := make(chan error, 1)
	go func() {
		_, err := h.ChatGPT("going")
		failed <- err
	}()
	<-reading
	g, err := h.ChatGPT("going")
	if err != nil {
		t.Fatal(err)
	}
	close(goOn)
	if err := <-gone; err != nil {
		t.Fatal(err)
	}
	if h.KnownChatGPT("going") != nil {
		t.Error("kept a sign-in read as it went")
	}
	close(late)
	if err := <-failed; err == nil || h.KnownChatGPT("going") != nil {
		t.Errorf("read after it went, and kept: %v", err)
	}
	g.OnRefresh(ChatGPTAccount{RefreshToken: "r2"})
	if len(keys.keys) != 0 {
		t.Errorf("a renewal brought it back: %v", keys.keys)
	}
}

// One provider's sign-ins coming and going are no change to another's: a
// read of it under way goes on.
func TestChatGPTReadsKeepToTheirProvider(t *testing.T) {
	useMemKeys(t)
	fakeDiscovery(t, &chatgptDiscovery{})
	for _, id := range []string{"mine", "other"} {
		if err := SaveChatGPT(id, ChatGPTAccount{RefreshToken: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	reading, answer := make(chan struct{}), make(chan struct{})
	was := readSecret
	readSecret = func(name string) ([]byte, error) {
		if name == "chatgpt.mine" {
			close(reading)
			<-answer
		}
		return was(name)
	}
	t.Cleanup(func() {
		readSecret = was
		chatgptMu.Lock()
		defer chatgptMu.Unlock()
		delete(chatgptClients, "mine")
		delete(chatgptClients, "new")
	})
	h := &Hub{}
	failed := make(chan error, 1)
	go func() {
		_, err := h.ChatGPT("mine")
		failed <- err
	}()
	<-reading
	if err := ForgetChatGPT(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if err := UseChatGPT("new", NewChatGPT(ChatGPTAccount{RefreshToken: "n"})); err != nil {
		t.Fatal(err)
	}
	close(answer)
	if err := <-failed; err != nil || h.KnownChatGPT("mine") == nil {
		t.Errorf("read of another provider's: %v", err)
	}
}

// Signing a provider out tells OpenAI, though no run has read its sign-in
// from the keychain yet: its refresh token goes on working otherwise, kept
// nowhere.
func TestForgetChatGPTRevokesOneNotRead(t *testing.T) {
	keys := useMemKeys(t)
	revoked := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		revoked <- r.FormValue("token")
	}))
	defer srv.Close()
	fakeDiscovery(t, &chatgptDiscovery{RevocationEndpoint: srv.URL + "/revoke"})
	if err := SaveChatGPT("p", ChatGPTAccount{ClientID: "c", RefreshToken: "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := ForgetChatGPT(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-revoked:
		if got != "r1" {
			t.Errorf("revoked %q", got)
		}
	default:
		t.Error("the sign-in was not revoked")
	}
	if len(keys.keys) != 0 {
		t.Errorf("kept %v", keys.keys)
	}
}

// A second answer to the sign-in's callback, such as the tab loaded
// again, is answered too, rather than waiting for ever.
func TestSignInCallbackAnsweredTwice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer srv.Close()
	fakeDiscovery(t, &chatgptDiscovery{AuthorizationEndpoint: srv.URL + "/authorize", TokenEndpoint: srv.URL + "/token"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	open := func(link string) {
		u, err := url.Parse(link)
		if err != nil {
			t.Error(err)
			return
		}
		q := u.Query()
		back := q.Get("redirect_uri") + "?code=x&client_id=client.1&state=" + url.QueryEscape(q.Get("state"))
		for range 2 {
			req, _ := http.NewRequestWithContext(ctx, "GET", back, nil)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			_ = res.Body.Close()
		}
	}
	if _, err := SignInChatGPT(ctx, "host", "", open); err == nil || ctx.Err() != nil {
		t.Errorf("sign-in %v, %v", err, ctx.Err())
	}
}

// ChatGPT's function calls stream too, their blocks going in the page as
// they come.
func TestChatGPTInsertStreams(t *testing.T) {
	args := `{"position":{"after":"p1"},"blocks":[{"type":"paragraph","text":"Once upon a time."}]}`
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(v map[string]any) {
			raw, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		if n > 1 {
			event(map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "Done."}}}}}})
			return
		}
		event(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "call_id": "c1", "name": "insert_blocks"}})
		for i := 0; i < len(args); i += 6 {
			event(map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": args[i:min(i+6, len(args))]})
		}
		event(map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{map[string]any{"type": "function_call", "call_id": "c1", "name": "insert_blocks", "arguments": args}}}})
	}))
	defer srv.Close()
	saved := draftEvery
	t.Cleanup(func() { draftEvery = saved })
	draftEvery = 0
	c := page()
	revisions := 0
	g := &ChatGPT{access: "tok", expires: time.Now().Add(time.Hour), base: srv.URL}
	res, err := RunLocal(context.Background(), g, Request{Model: "m", SupportsTools: true, Instruction: "go"}, executor(c).Ctx,
		Events{Revise: func(int, *doc.Op, string, []doc.Op) { revisions++ }})
	if err != nil {
		t.Fatal(err)
	}
	if revisions == 0 || len(res.Ops) != 1 || c.Children[2].Delta().ToMarkdown() != "Once upon a time." {
		t.Errorf("%d revisions, %d ops, page %s", revisions, len(res.Ops), c.Encode())
	}
}

// A recording block carries the transcript only of a recording of its
// page: a block can name any recording's ID, another page's among them,
// whose transcript the run, or an MCP client, would read through it. The
// other page's own read gives its own.
func TestRecordingTranscriptsOfThePageAlone(t *testing.T) {
	st := openStore(t)
	here, err := st.CreatePage(store.NewPageOptions{Title: "Here"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreatePage(store.NewPageOptions{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	for id, page := range map[string]string{"mine": here.ID, "theirs": other.ID} {
		if err := st.PutRecording(store.Recording{ID: id, PageID: page, Status: "ready", Transcript: []store.Segment{{Text: "Words of " + id + "."}}}); err != nil {
			t.Fatal(err)
		}
	}
	block := func(id string) *doc.Node {
		return &doc.Node{ID: "b-" + id, Type: "recording", Data: map[string]any{"recording_id": id}}
	}
	c := doc.FromBlocks([]*doc.Node{block("mine"), block("theirs")})
	tc := PageTools(st, here.ID, "Here", "Test", c, true)
	views := tc.Views(c)
	if rec := views[0]["recording"].(map[string]any); rec["transcript"] != "[0:00] Words of mine." {
		t.Errorf("the page's recording reads %v", rec)
	}
	if rec := views[1]["recording"].(map[string]any); rec["transcript"] != nil {
		t.Errorf("another page's recording reads %v", rec)
	}
	if _, _, err := st.SaveContent(other.ID, doc.FromBlocks([]*doc.Node{block("theirs")})); err != nil {
		t.Fatal(err)
	}
	read := tc.ReadOtherPage(other.ID)["blocks"].([]map[string]any)
	if rec := read[0]["recording"].(map[string]any); rec["transcript"] != "[0:00] Words of theirs." {
		t.Errorf("the other page read as %v", rec)
	}
}
