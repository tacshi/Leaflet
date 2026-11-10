package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/web"
)

func TestFollowedLinksEnd(t *testing.T) {
	x := webExecutor("Read https://given.example/r", Search{Sources: []Source{{URL: "https://found.example/a"}}})
	read := x.Ctx.ReadWeb
	x.Ctx.ReadWeb = func(link string) (web.Page, error) {
		if link == "https://found.example/b#fails" {
			return web.Page{}, errors.New("gone")
		}
		return read(link)
	}
	must := func(link string, refused bool) {
		t.Helper()
		out := x.Call("read_web_page", map[string]any{"url": link})
		if got := out["error"] != nil && !strings.Contains(out["error"].(string), "gone"); got != refused {
			t.Errorf("%s: %v", link, out)
		}
	}
	must("https://any.example/", false)
	must("https://found.example/b#fails", false)
	for range maxFollowedLinks - 1 {
		must("https://found.example/b", false)
	}
	must("https://found.example/b", true)
	must("https://given.example/r", false)
	if _, err := x.searchWeb(map[string]any{"query": "more"}); err != nil {
		t.Fatal(err)
	}
	must("https://found.example/a", false)
}

func webExecutor(request string, found Search) *Executor {
	x := executor(page())
	x.Ctx.Request = request
	x.Ctx.ListPages = func() []map[string]any { return []map[string]any{} }
	x.Ctx.SearchWeb = func(string) (Search, error) { return found, nil }
	x.Ctx.ReadWeb = func(link string) (web.Page, error) {
		link, _, _ = strings.Cut(link, "#")
		return web.Page{URL: link, Title: "T", Text: "[next](https://found.example/b)", Links: []string{"https://found.example/b"}}, nil
	}
	return x
}

type toServer struct{ srv *httptest.Server }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(t.srv.URL)
	r = r.Clone(r.Context())
	r.Header.Set("X-Host", r.URL.Host)
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestClientSearchesAsItsProviderDoes(t *testing.T) {
	type asked struct {
		host, path, auth, key string
		body                  map[string]any
	}
	var mu sync.Mutex
	var got []asked
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, asked{r.Header.Get("X-Host"), r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), body})
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/responses":
			_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"Hi","annotations":[{"type":"url_citation","url":"https://a.example/","title":"A"}]}]}]}`))
		case "/v1/messages":
			_, _ = w.Write([]byte(`{"content":[{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://a.example/","title":"A"}]},{"type":"text","text":"Hi"}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Hi"}}],"web_search":[{"title":"A","link":"https://a.example/"}]}`))
		}
	}))
	defer srv.Close()
	for _, base := range []string{"https://api.openai.com/v1", "https://api.x.ai/v1", "https://api.anthropic.com/v1", "https://openrouter.ai/api/v1", "https://open.bigmodel.cn/api/paas/v4"} {
		c := NewClient(base, "sk")
		c.HTTP = &http.Client{Transport: toServer{srv}}
		if !c.CanSearch() {
			t.Errorf("%s does not search", base)
		}
		s, err := c.SearchWeb(context.Background(), "m", "AI videos")
		if err != nil || s.Answer != "Hi" || len(s.Sources) != 1 || s.Sources[0].URL != "https://a.example/" {
			t.Errorf("%s found %+v, %v", base, s, err)
		}
	}
	paths := []string{}
	for _, a := range got {
		paths = append(paths, a.host+a.path)
	}
	if !slices.Equal(paths, []string{"api.openai.com/v1/responses", "api.x.ai/v1/responses", "api.anthropic.com/v1/messages", "openrouter.ai/api/v1/chat/completions", "open.bigmodel.cn/api/paas/v4/chat/completions"}) {
		t.Errorf("asked %v", paths)
	}
	tool := func(a asked) map[string]any { return a.body["tools"].([]any)[0].(map[string]any) }
	if tool(got[0])["type"] != "web_search" || got[0].body["include"] == nil || got[1].body["include"] != nil || got[0].auth != "Bearer sk" {
		t.Errorf("Responses asked %+v %+v", got[0], got[1])
	}
	if tool(got[2])["type"] != "web_search_20250305" || got[2].key != "sk" || got[2].auth != "" {
		t.Errorf("Claude asked %+v", got[2])
	}
	if tool(got[3])["type"] != "openrouter:web_search" || tool(got[4])["web_search"].(map[string]any)["enable"] != true {
		t.Errorf("Chat Completions asked %v %v", tool(got[3]), tool(got[4]))
	}
	if c := NewClient("https://api.deepseek.com/v1", "sk"); c.CanSearch() {
		t.Error("DeepSeek searches")
	}
}

func TestChatGPTSearches(t *testing.T) {
	var body map[string]any
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		if fail {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"error\",\"message\":\"web search is off for this workspace\"}\n\n")
			return
		}
		item := `{"type":"web_search_call","action":{"type":"search","sources":[{"type":"url","url":"https://a.example/"}]}}`
		msg := `{"type":"message","content":[{"type":"output_text","text":"Hi","annotations":[]}]}`
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"item\":%s}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n", item, msg)
	}))
	defer srv.Close()
	g := &ChatGPT{access: "tok", expires: time.Now().Add(time.Hour), base: srv.URL}
	s, err := g.SearchWeb(context.Background(), "m", "AI videos")
	if err != nil || s.Answer != "Hi" || len(s.Sources) != 1 || s.Sources[0].URL != "https://a.example/" {
		t.Errorf("found %+v, %v", s, err)
	}
	if tools := body["tools"].([]any); tools[0].(map[string]any)["type"] != "web_search" || body["stream"] != true || body["store"] != false {
		t.Errorf("asked %v", body)
	}
	fail = true
	if _, err := g.SearchWeb(context.Background(), "m", "q"); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("failed search: %v", err)
	}
}

func TestRunOffersTheWeb(t *testing.T) {
	tools := func(f *fakeModel, i int) []string {
		var names []string
		for _, d := range f.requests[i]["tools"].([]any) {
			names = append(names, d.(map[string]any)["function"].(map[string]any)["name"].(string))
		}
		return names
	}
	f := &fakeModel{replies: []map[string]any{
		{"content": "", "tool_calls": []any{call("read_web_page", map[string]any{"url": "https://www.example.com/a"})}},
		{"content": "Read it."},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	x := webExecutor("", Search{})
	var did []string
	res, err := RunLocal(context.Background(), NewClient(srv.URL, ""), Request{Model: "m", SupportsTools: true, Web: true}, x.Ctx,
		Events{Activity: func(s string) { did = append(did, s) }})
	if err != nil || res.Reply != "Read it." || !res.KeptToPage {
		t.Fatalf("run %+v, %v", res, err)
	}
	if names := tools(f, 0); !slices.Contains(names, "web_search") || !slices.Contains(names, "read_web_page") {
		t.Errorf("tools %v", names)
	}
	system := f.requests[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "web_search") {
		t.Errorf("the prompt says nothing of the web: %s", system)
	}
	if len(did) != 1 || !strings.Contains(did[0], "example.com") || strings.Contains(did[0], "www.") {
		t.Errorf("said %q", did)
	}

	for name, req := range map[string]Request{
		"no web":          {Model: "m", SupportsTools: true},
		"pages before it": {Model: "m", SupportsTools: true, Web: true, History: []Turn{{Role: "user", Content: "hi"}}},
	} {
		f := &fakeModel{replies: []map[string]any{{"content": "Hi."}}}
		srv := httptest.NewServer(f)
		if _, err := RunLocal(context.Background(), NewClient(srv.URL, ""), req, webExecutor("", Search{}).Ctx, Events{}); err != nil {
			t.Fatal(err)
		}
		if names := tools(f, 0); slices.Contains(names, "read_web_page") {
			t.Errorf("%s: tools %v", name, names)
		}
		srv.Close()
	}
	f = &fakeModel{replies: []map[string]any{{"content": "Hi."}}}
	srv2 := httptest.NewServer(f)
	defer srv2.Close()
	req := Request{Model: "m", SupportsTools: true, Web: true, History: []Turn{{Role: "user", Content: "hi"}}, KeptToPage: true}
	if _, err := RunLocal(context.Background(), NewClient(srv2.URL, ""), req, webExecutor("", Search{}).Ctx, Events{}); err != nil {
		t.Fatal(err)
	}
	if names := tools(f, 0); !slices.Contains(names, "read_web_page") {
		t.Errorf("after turns kept to the page: tools %v", names)
	}
}

func TestConversationKeptToPage(t *testing.T) {
	s := &Session{Runs: []*Run{{ID: "r1", Status: "done", KeptToPage: true}, {ID: "r2", Status: "done"}, {ID: "r3", Status: "failed"}}}
	s.Messages = []*Message{{Mine: true, Text: "a"}, {Text: "A", RunID: "r1"}}
	if !keptToPage(s) {
		t.Error("a run kept to the page")
	}
	s.Messages = append(s.Messages, &Message{Mine: true, Text: "b"}, &Message{Text: "B", RunID: "r3", Error: true})
	if !keptToPage(s) {
		t.Error("a failed run's reply counts")
	}
	s.Messages = append(s.Messages, &Message{Mine: true, Text: "c"}, &Message{Text: "C", RunID: "r2"})
	if keptToPage(s) {
		t.Error("a run that read beyond the page did not count")
	}
	s.Messages = []*Message{{Mine: true, Text: "a"}, {Text: "A", RunID: "gone"}}
	if keptToPage(s) {
		t.Error("a reply whose run is unknown kept to the page")
	}
}

func TestPagesPickedFromPagesAreNoSources(t *testing.T) {
	x := webExecutor("Read https://given.example/r", Search{Sources: []Source{{URL: "https://found.example/a"}}})
	card := func(u string) map[string]any {
		return map[string]any{"position": map[string]any{"at": "end"}, "blocks": []any{map[string]any{"type": "link_preview", "link": map[string]any{"url": u}}}}
	}
	for _, c := range []struct {
		tool string
		args map[string]any
		ok   bool
	}{
		{"read_web_page", map[string]any{"url": "https://first.example/"}, true},
		{"read_web_page", map[string]any{"url": "https://found.example/b"}, true},
		{"insert_blocks", card("https://found.example/b"), false},
		{"insert_blocks", card("https://first.example/"), true},
		{"read_web_page", map[string]any{"url": "https://given.example/r"}, true},
		{"insert_blocks", card("https://given.example/r"), true},
		{"web_search", map[string]any{"query": "AI videos"}, true},
		{"read_web_page", map[string]any{"url": "https://found.example/a"}, true},
		{"insert_blocks", card("https://found.example/a"), true},
	} {
		if out := x.Call(c.tool, c.args); (out["error"] == nil) != c.ok {
			t.Errorf("%s %v: %v", c.tool, c.args, out)
		}
	}
}

func TestFirstReadTakesNoMadeUpQuery(t *testing.T) {
	for link, ok := range map[string]bool{
		"https://any.example/docs/page.html":                           true,
		"https://given.example/r?x=1":                                  true,
		"https://evil.example/c?d=the+rest+of+the+page":                false,
		"https://evil.example/c#the-rest-of-the-page":                  false,
		"https://user:pass@evil.example/":                              false,
		"https://evil.example/" + strings.Repeat("a", maxPlainAddress): false,
	} {
		x := webExecutor("Read https://given.example/r?x=1", Search{})
		if out := x.Call("read_web_page", map[string]any{"url": link}); (out["error"] == nil) != ok {
			t.Errorf("%s: %v", link, out)
		}
	}
}

func TestStreamsEndAtTheCap(t *testing.T) {
	r := &cappedReader{r: strings.NewReader(strings.Repeat("data: {}\n", 10)), left: 20}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
	}
	if !errors.Is(sc.Err(), errTooLong) {
		t.Errorf("a stream past its cap ended with %v", sc.Err())
	}
}
