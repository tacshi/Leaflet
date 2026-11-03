package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/web"
)

// Searches read here as the agent function reads them (supabase/functions/
// _shared/testdata/search.json, which its tests write).
func TestSearchesAsTheServerReadsThem(t *testing.T) {
	raw, err := os.ReadFile("../../supabase/functions/_shared/testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Responses struct {
			Output []any  `json:"output"`
			Search Search `json:"search"`
		} `json:"responses"`
		Anthropic struct {
			Content []any  `json:"content"`
			Search  Search `json:"search"`
		} `json:"anthropic"`
		Chat struct {
			Data   chatSearchAnswer `json:"data"`
			Search Search           `json:"search"`
		} `json:"chat"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string][2]Search{
		"responses": {responsesSearch(f.Responses.Output), f.Responses.Search},
		"anthropic": {anthropicSearch(f.Anthropic.Content), f.Anthropic.Search},
		"chat":      {f.Chat.Data.search(), f.Chat.Search},
	} {
		if len(c[1].Sources) == 0 || !reflect.DeepEqual(c[0], c[1]) {
			t.Errorf("%s found\n%+v\nthe server's\n%+v", name, c[0], c[1])
		}
	}
}

// The web's tools are offered as the server offers them, and take and
// refuse the calls of supabase/functions/_shared/testdata/web_calls.json as
// its do: the URLs a run may read and write after reading the web.
func TestWebToolsAsTheServerHasThem(t *testing.T) {
	raw, err := os.ReadFile("../../supabase/functions/_shared/testdata/tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var schemas struct {
		Web map[string]any `json:"web"`
	}
	if err := json.Unmarshal(raw, &schemas); err != nil {
		t.Fatal(err)
	}
	defs := WebToolDefinitions(true)
	if len(defs) != len(schemas.Web) || len(defs) != 2 {
		t.Fatalf("%d web tools here, %d on the server", len(defs), len(schemas.Web))
	}
	for _, d := range defs {
		fn := d["function"].(map[string]any)
		got, _ := json.Marshal(contract(jsonOf(t, fn["parameters"])))
		want, _ := json.Marshal(schemas.Web[fn["name"].(string)])
		if string(got) != string(want) {
			t.Errorf("%s takes\n%s\nthe server's\n%s", fn["name"], got, want)
		}
	}
	if defs := WebToolDefinitions(false); len(defs) != 1 || defs[0]["function"].(map[string]any)["name"] != "read_web_page" {
		t.Errorf("without a search %v", defs)
	}

	raw, err = os.ReadFile("../../supabase/functions/_shared/testdata/web_calls.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Request string `json:"request"`
		Search  Search `json:"search"`
		Seqs    []struct {
			Armed bool `json:"armed"`
			Calls []struct {
				Tool string `json:"tool"`
				Args string `json:"args"`
				OK   bool   `json:"ok"`
			} `json:"calls"`
			KeptToPage bool `json:"kept_to_page"`
		} `json:"sequences"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Seqs) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for i, seq := range f.Seqs {
		x := webExecutor(f.Request, f.Search)
		x.armed = seq.Armed
		for _, c := range seq.Calls {
			out := x.CallRaw(c.Tool, c.Args)
			if _, refused := out["error"]; refused == c.OK {
				t.Errorf("sequence %d: %s %s: refused %v (%v), the server's ok %v", i, c.Tool, c.Args, refused, out["error"], c.OK)
			}
		}
		if !x.readPages != seq.KeptToPage {
			t.Errorf("sequence %d kept to its page %v, the server's %v", i, !x.readPages, seq.KeptToPage)
		}
	}
}

// webExecutor is an executor on page() with the web: its searches find
// found, and its pages each link https://found.example/b.
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

func jsonOf(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// toServer sends a client's requests to srv, whatever host they name, so
// a test can be each provider.
type toServer struct{ srv *httptest.Server }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(t.srv.URL)
	r = r.Clone(r.Context())
	r.Header.Set("X-Host", r.URL.Host)
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r)
}

// Each provider is asked in its own way, as the agent function asks it:
// the Responses API's web_search, Claude's server tool with its key in
// x-api-key alone, OpenRouter's and GLM's tools; another is not asked.
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

// A ChatGPT plan searches with the Responses API's web_search, its answer
// streamed; an error it streams fails the search.
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

// A run that may use the web is offered its tools and told of them, says
// what it does while a call reads the web, and keeps to its page; one that
// follows turns that read other pages, or may not, is offered none.
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
		"pages before it": {Model: "m", SupportsTools: true, Web: true, History: []map[string]string{{"role": "user", "content": "hi"}}},
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
	req := Request{Model: "m", SupportsTools: true, Web: true, History: []map[string]string{{"role": "user", "content": "hi"}}, KeptToPage: true}
	if _, err := RunLocal(context.Background(), NewClient(srv2.URL, ""), req, webExecutor("", Search{}).Ctx, Events{}); err != nil {
		t.Fatal(err)
	}
	if names := tools(f, 0); !slices.Contains(names, "read_web_page") {
		t.Errorf("after turns kept to the page: tools %v", names)
	}
}

// A conversation's turns kept to the page while each reply came from a
// run that read nothing beyond it.
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

// A cloud run says what it does as the server calls the web, and ends
// with whether it kept to its page.
func TestCloudRunSaysWhatItDoes(t *testing.T) {
	var did []string
	res, err := RunCloud(context.Background(), fakeStream{events: []map[string]any{
		{"type": "call", "tool": "web_search", "args": map[string]any{"query": "AI videos"}},
		{"type": "call", "tool": "read_web_page", "args": map[string]any{"url": "https://news.ycombinator.com/"}},
		{"type": "done", "reply": "Found them.", "kept_to_page": true},
	}}, "j1", Events{Activity: func(s string) { did = append(did, s) }})
	if err != nil || !res.KeptToPage || len(did) != 2 || !strings.Contains(did[0], "AI videos") || !strings.Contains(did[1], "news.ycombinator.com") {
		t.Errorf("run %+v %v, said %q", res, err, did)
	}
}
