package mobile

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

func TestTestProvider(t *testing.T) {
	c, _ := open(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"text-embedding-3"},{"id":"deepseek-v4-pro"},{"id":"deepseek-v4-flash"}]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"ping","arguments":"{\"value\":\"ok\"}"}}]}}]}`))
		}
	}))
	defer srv.Close()

	test := func(provider providerJSON) providerTest {
		t.Helper()
		in, _ := json.Marshal(provider) // #nosec G117 -- test keys only.
		raw, err := c.TestProvider(string(in))
		if err != nil {
			t.Fatal(err)
		}
		var out providerTest
		_ = json.Unmarshal([]byte(raw), &out)
		return out
	}

	// A cloud provider's key typed in is tried from here, signed in or not.
	got := test(providerJSON{Preset: "deepseek", BaseURL: srv.URL + "/v1", APIKey: "sk-test", DefaultModel: "deepseek-v4-flash"})
	if !got.OK || !got.SupportsTools {
		t.Fatalf("test with a key: %+v", got)
	}
	if !slices.Contains(got.Models, "deepseek-v4-pro") || slices.Contains(got.Models, "text-embedding-3") {
		t.Fatalf("chat models: %v", got.Models)
	}

	// Without one there is nothing to try it with.
	if got := test(providerJSON{Preset: "deepseek", BaseURL: srv.URL + "/v1"}); got.OK || got.Message == "" {
		t.Fatalf("test without a key: %+v", got)
	}
}

// The app's form never holds a key kept here: a model server moved to
// another address without one typed is refused, with why, and keeps its
// key and its address, rather than lose the key at the save.
func TestMovedLocalProviderKeepsItsKey(t *testing.T) {
	c, _ := open(t)
	save := func(p providerJSON) error {
		in, _ := json.Marshal(p) // #nosec G117 -- test keys only.
		return c.SaveProvider(string(in))
	}
	providers := func() []providerJSON {
		raw, _ := c.Providers()
		var ps []providerJSON
		_ = json.Unmarshal([]byte(raw), &ps)
		return ps
	}
	if err := save(providerJSON{Preset: "lmstudio", BaseURL: "http://127.0.0.1:1234/v1", DefaultModel: "m", APIKey: "sk-test"}); err != nil {
		t.Fatal(err)
	}
	moved := providers()[0]
	moved.BaseURL = "http://127.0.0.1:1235/v1"
	if err := save(moved); err == nil || err.Error() != locales.SettingsKeyStaysBehind() {
		t.Fatalf("moved: %v", err)
	}
	if p := providers()[0]; p.BaseURL != "http://127.0.0.1:1234/v1" || !p.HasKey {
		t.Fatalf("refused, and changed: %+v", p)
	}
	moved.APIKey = "sk-new"
	if err := save(moved); err != nil {
		t.Fatal(err)
	}
	if p := providers()[0]; p.BaseURL != moved.BaseURL || !p.HasKey {
		t.Fatalf("typed: %+v", p)
	}
}

func TestSaveCloudProviderSignedOut(t *testing.T) {
	c, _ := open(t)
	save := func(p providerJSON) error {
		in, _ := json.Marshal(p) // #nosec G117 -- test keys only.
		return c.SaveProvider(string(in))
	}
	deepseek := providerJSON{Preset: "deepseek", BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-v4-flash"}
	if err := save(deepseek); err == nil {
		t.Fatal("saved a cloud provider kept here without its key")
	}
	deepseek.APIKey = "sk-test"
	if err := save(deepseek); err != nil {
		t.Fatal(err)
	}

	raw, _ := c.Providers()
	var ps []providerJSON
	_ = json.Unmarshal([]byte(raw), &ps)
	if len(ps) != 1 || !ps[0].IsLocal || !ps[0].HasKey || ps[0].Name != "DeepSeek" {
		t.Fatalf("providers: %+v", ps)
	}

	// Changed without its key, it keeps the one it has.
	ps[0].DefaultModel = "deepseek-v4-pro"
	if err := save(ps[0]); err != nil {
		t.Fatal(err)
	}
	raw, _ = c.Providers()
	_ = json.Unmarshal([]byte(raw), &ps)
	// Moved to another server it takes a key of its own, not the one it
	// has, which stays with its address.
	moved := ps[0]
	moved.BaseURL = "https://proxy.example/v1"
	if err := save(moved); err == nil {
		t.Fatal("moved with the key kept for its address")
	}
	moved.APIKey = "sk-proxy"
	if err := save(moved); err != nil {
		t.Fatal(err)
	}
	if err := save(ps[0]); err == nil {
		t.Fatal("moved back with the key kept for the other address")
	}
	if ps[0].DefaultModel != "deepseek-v4-pro" || !ps[0].HasKey {
		t.Fatalf("changed: %+v", ps)
	}
}

func TestRemoveProviderStopsBeingTheDefault(t *testing.T) {
	c, _ := open(t)
	in, _ := json.Marshal(providerJSON{Preset: "deepseek", BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-v4-flash", APIKey: "sk-test"}) // #nosec G117 -- test keys only.
	if err := c.SaveProvider(string(in)); err != nil {
		t.Fatal(err)
	}
	raw, _ := c.Providers()
	var ps []providerJSON
	_ = json.Unmarshal([]byte(raw), &ps)
	if s := c.core.Store.Settings(); s.DefaultModel != ps[0].ID+"|" {
		t.Fatalf("default agent: %q", s.DefaultModel)
	}
	if err := c.RemoveProvider(ps[0].ID); err != nil {
		t.Fatal(err)
	}
	if s := c.core.Store.Settings(); s.DefaultModel != "" {
		t.Fatalf("default agent after removing it: %q", s.DefaultModel)
	}
}

func TestChatGPTPresetAndSave(t *testing.T) {
	c, _ := open(t)
	raw, _ := c.Presets()
	var presets []presetJSON
	_ = json.Unmarshal([]byte(raw), &presets)
	if !slices.ContainsFunc(presets, func(p presetJSON) bool { return p.ID == "chatgpt" && p.Auth == "account" }) {
		t.Fatalf("no ChatGPT preset: %+v", presets)
	}
	// Nothing to save before signing in; the address is the preset's, so
	// the form needs none.
	in, _ := json.Marshal(providerJSON{Preset: "chatgpt", Name: "ChatGPT", DefaultModel: "gpt-6-astra"}) // #nosec G117 -- test keys only.
	if err := c.SaveProvider(string(in)); err == nil || err.Error() != locales.SettingsChatGPTSignInFirst() {
		t.Fatalf("saved ChatGPT without a sign-in: %v", err)
	}
	if _, err := c.FinishChatGPTSignIn(); err == nil {
		t.Fatal("finished a sign-in never begun")
	}
}

func TestClosingTheChatGPTPageKeepsTheSignIn(t *testing.T) {
	c, _ := open(t)
	c.chatgpt.g = agents.NewChatGPT(agents.ChatGPTAccount{Email: "a@example.com"})
	// The page closed after OpenAI answered: the sign-in stays to save.
	c.CancelChatGPTSignIn()
	if c.madeChatGPT() == nil {
		t.Fatal("closing the page forgot the sign-in made")
	}
	// The form closed without saving: it goes.
	c.ForgetChatGPTSignIn()
	if c.madeChatGPT() != nil {
		t.Fatal("the form closed and kept the sign-in")
	}
}

func TestSelectAgentPicksTheChatsAgent(t *testing.T) {
	c, _ := open(t)
	for _, p := range []providerJSON{
		{Preset: "deepseek", BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-v4-flash", APIKey: "sk-a"},
		{Preset: "openai", BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-6-luna", APIKey: "sk-b"},
	} {
		in, _ := json.Marshal(p) // #nosec G117 -- test keys only.
		if err := c.SaveProvider(string(in)); err != nil {
			t.Fatal(err)
		}
	}
	state := func() agentState {
		raw, _ := c.AgentState("p")
		var s agentState
		_ = json.Unmarshal([]byte(raw), &s)
		return s
	}
	s := state()
	if len(s.Options) != 2 {
		t.Fatalf("before picking: %+v", s)
	}
	other := s.Options[1]
	c.SelectAgent("p", other.ProviderID, other.Model)
	if s := state(); s.Current == nil || s.Current.ProviderID != other.ProviderID || s.Current.Model != other.Model {
		t.Fatalf("after picking: %+v", s.Current)
	}
}

// An agent run whose edits can't be taken back, as on content that can't
// be read, says why to the app, and stays as it was.
func TestUndoRunThatFailsSaysWhy(t *testing.T) {
	c, _ := open(t)
	row := create(t, c, `{"title":"Plan"}`)
	if _, err := c.core.Store.DB().Exec(`UPDATE page_contents SET content = '{"children": [' WHERE page_id = ?`, row.ID); err != nil {
		t.Fatal(err)
	}
	var run *agents.Run
	c.q.do(func() {
		run = &agents.Run{ID: "r1", AgentName: "Claude", Status: "done",
			Ops: []doc.Op{doc.InsertOp(doc.Position{At: "end"}, paragraph("x1", "one"))}}
		s := c.core.Hub.Session(row.ID)
		s.Runs = append(s.Runs, run)
	})
	err := c.UndoRun(row.ID, "r1")
	if err == nil || !strings.HasPrefix(err.Error(), "Could not undo Claude's edits: ") {
		t.Fatalf("undo failed with %v", err)
	}
	c.q.do(func() {
		if run.Undone {
			t.Error("the run counts as undone")
		}
	})
}

// Ask says why a run could not start, rather than returning an ID the
// page's runs never have; one that starts is among them.
func TestAskFailsToStart(t *testing.T) {
	c, _ := open(t)
	row := create(t, c, `{"title":"Plan"}`)
	if id, err := c.Ask(row.ID, "inline", "sum it up", `[]`, "", "", ""); err == nil || id != "" {
		t.Fatalf("asked with no agent: %q, %v", id, err)
	}
	// A model server that answers once the test is done with the run.
	answer := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-answer
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := c.SaveProvider(`{"preset":"ollama","name":"Ollama","base_url":"` + srv.URL + `","default_model":"llama3"}`); err != nil {
		t.Fatal(err)
	}
	id, err := c.Ask(row.ID, "inline", "sum it up", `[]`, "", "", "")
	if err != nil || id == "" {
		t.Fatalf("asked: %q, %v", id, err)
	}
	c.q.do(func() {
		if c.core.Hub.Session(row.ID).Run(id) == nil {
			t.Errorf("run %s is not the page's", id)
		}
	})
	if again, err := c.Ask(row.ID, "inline", "and again", `[]`, "", "", ""); err == nil || again != "" {
		t.Errorf("asked while a run works: %q, %v", again, err)
	}
	close(answer)
	deadline := time.Now().Add(30 * time.Second)
	for running := true; running; {
		if time.Now().After(deadline) {
			t.Fatal("the run never ended")
		}
		c.q.do(func() { running = c.core.Hub.Session(row.ID).Running })
		time.Sleep(10 * time.Millisecond)
	}
}

// An answer in words is the blocks of its Markdown, for the editor to put
// after the block it was asked about, as the desktop's Insert puts it; a
// run that edited the page gives none.
func TestReplyBlocksAreTheAnswersMarkdown(t *testing.T) {
	c, _ := open(t)
	row := create(t, c, `{"title":"Plan"}`)
	c.q.do(func() {
		s := c.core.Hub.Session(row.ID)
		s.Runs = append(s.Runs,
			&agents.Run{ID: "r1", Status: "done", Reply: "It means **twice**.\n\n- once\n- again"},
			&agents.Run{ID: "r2", Status: "done", Reply: "Done.", Ops: []doc.Op{doc.InsertOp(doc.Position{At: "end"}, paragraph("x1", "one"))}})
	})
	if got, err := c.ReplyBlocks(row.ID, "r2"); err != nil || got != "[]" {
		t.Errorf("a run's edits give %s, %v", got, err)
	}
	raw, err := c.ReplyBlocks(row.ID, "r1")
	if err != nil {
		t.Fatal(err)
	}
	var nodes []*doc.Node
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		t.Fatal(err)
	}
	got := &doc.Content{Children: nodes}
	if len(nodes) != 3 || nodes[0].Delta().Plain() != "It means twice." || nodes[1].Type != "bulleted_list" ||
		nodes[2].Delta().Plain() != "again" {
		t.Errorf("blocks %s", texts(got))
	}
}

// A run's edits say which pages they made, an edit each, for the app to
// open them from.
func TestAgentStateNamesThePagesMade(t *testing.T) {
	c, _ := open(t)
	c.q.do(func() {
		c.core.Hub.Session("p").Runs = []*agents.Run{{ID: "r1", Status: "done", Edits: []agents.Edit{
			{Summary: "Inserted a subpage link", BlockIDs: []string{"b1"}},
			{Summary: "Created subpage “Notes”", PageID: "page-2"},
		}}}
	})
	raw, err := c.AgentState("p")
	if err != nil {
		t.Fatal(err)
	}
	var s agentState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Runs) != 1 || !slices.Equal(s.Runs[0].EditPages, []string{"", "page-2"}) {
		t.Errorf("runs %+v", s.Runs)
	}
}

// An agent's comment says who else had it written, by their name among
// the page's members, as the desktop's comments do; one the user asked
// for, and any that is no agent's, says nothing.
func TestCommentsSayWhoAskedTheAgent(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	var uid string
	var err error
	c.q.do(func() {
		uid = c.core.Store.Session().UserID
		err = c.core.Store.SetMembers(page.ID, []store.Member{{UserID: "ann", DisplayName: "Ann", Role: "editor", Accepted: true}})
		for id, by := range map[string][2]string{
			"ann":    {"ann", "agent"},
			"gone":   {"gone", "agent"},
			"mine":   {uid, "agent"},
			"person": {"ann", "user"},
		} {
			if err == nil {
				err = c.core.Store.PutComment(store.Comment{ID: id, PageID: page.ID, BlockID: "b", AuthorID: by[0], AuthorKind: by[1], Body: "Hi"})
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Threads(page.ID)
	if err != nil {
		t.Fatal(err)
	}
	var threads []threadJSON
	if err := json.Unmarshal([]byte(raw), &threads); err != nil {
		t.Fatal(err)
	}
	asked := map[string]string{}
	for _, th := range threads {
		asked[th.Root.ID] = th.Root.AskedBy
	}
	want := map[string]string{
		"ann": locales.PanelAskedBy("Ann"), "gone": locales.PanelAskedBySomeone(), "mine": "", "person": "",
	}
	if !maps.Equal(asked, want) {
		t.Errorf("asked by %v, want %v", asked, want)
	}
}
