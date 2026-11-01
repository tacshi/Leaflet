package mobile

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"leaflet/internal/agents"
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
		in, _ := json.Marshal(provider)
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

func TestSaveCloudProviderSignedOut(t *testing.T) {
	c, _ := open(t)
	save := func(p providerJSON) error {
		in, _ := json.Marshal(p)
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
	if ps[0].DefaultModel != "deepseek-v4-pro" || !ps[0].HasKey {
		t.Fatalf("changed: %+v", ps)
	}
}

func TestRemoveProviderStopsBeingTheDefault(t *testing.T) {
	c, _ := open(t)
	in, _ := json.Marshal(providerJSON{Preset: "deepseek", BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-v4-flash", APIKey: "sk-test"})
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
	// Nothing to save before signing in.
	in, _ := json.Marshal(providerJSON{Preset: "chatgpt", Name: "ChatGPT", DefaultModel: "gpt-6-astra"})
	if err := c.SaveProvider(string(in)); err == nil {
		t.Fatal("saved ChatGPT without a sign-in")
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
		in, _ := json.Marshal(p)
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
