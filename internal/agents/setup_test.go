package agents

import (
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

	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// memKeys is a keychain in memory; while locked, nothing is removed from
// it.
type memKeys struct {
	mu     sync.Mutex
	keys   map[string][]byte
	locked bool
}

func (k *memKeys) Get(name string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.keys[name]
	if !ok {
		return nil, keychain.ErrNotFound
	}
	return v, nil
}

func (k *memKeys) Set(name string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys[name] = value
	return nil
}

func (k *memKeys) Delete(name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.locked {
		return errors.New("the keychain is locked")
	}
	if _, ok := k.keys[name]; !ok {
		return keychain.ErrNotFound
	}
	delete(k.keys, name)
	return nil
}

// useMemKeys keeps the secrets in memory for the test.
func useMemKeys(t *testing.T) *memKeys {
	t.Helper()
	k := &memKeys{keys: map[string][]byte{}}
	keychain.Use(k)
	t.Cleanup(func() { keychain.Use(nil) })
	return k
}

// providersServer is Leaflet's server as Settings' providers reach it: it
// keeps what it heard, and answers with answer, or refuses while down.
type providersServer struct {
	mu     sync.Mutex
	heard  []map[string]any
	answer string
	down   bool
}

func (s *providersServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heard = append(s.heard, body)
	if s.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"down for maintenance"}`))
		return
	}
	_, _ = w.Write([]byte(s.answer))
}

// calls returns what the server heard, and forgets it.
func (s *providersServer) calls() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.heard
	s.heard = nil
	return out
}

// setupHub is a hub signed in as u1 to the server s, or signed out for a
// nil one.
func setupHub(t *testing.T, s *providersServer) *Hub {
	t.Helper()
	st := openStore(t)
	if s == nil {
		return NewHub(st, remote.New(remote.Settings{}, st), stubHost{})
	}
	hs := httptest.NewServer(s)
	t.Cleanup(hs.Close)
	if _, err := st.LoadSession(&store.User{ID: "u1", Email: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}
	return NewHub(st, remote.SignedInForTest(hs.URL, st, "u1"), stubHost{})
}

// A cloud provider is kept here signed out, or once kept here; signed in,
// a new one goes to the server. One on this device's network, and
// ChatGPT's, are this device's anyway.
func TestKeptHere(t *testing.T) {
	out := setupHub(t, nil)
	in := setupHub(t, &providersServer{})
	if !out.KeptHere(Form{Preset: "openai"}) || in.KeptHere(Form{Preset: "openai"}) {
		t.Error("a new cloud provider is kept in the wrong place")
	}
	for _, p := range []store.Provider{{ID: "here", Preset: "openai", IsLocal: true}, {ID: "server", Preset: "openai"}} {
		if err := in.st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	if !in.KeptHere(Form{ID: "here", Preset: "openai"}) || in.KeptHere(Form{ID: "server", Preset: "openai"}) {
		t.Error("a saved provider moved")
	}
	if out.KeptHere(Form{Preset: "ollama"}) || out.KeptHere(Form{Preset: "chatgpt"}) {
		t.Error("a local or ChatGPT provider counts as a cloud one kept here")
	}
}

// A saved cloud provider is tried by the server, whose models come back
// as the dialog lists them: chat models, the preset's suggestions first.
// Signed out, or with no key typed for a new one, there is nothing to try
// it with.
func TestTestProviderOnTheServer(t *testing.T) {
	s := &providersServer{answer: `{"ok":true,"supports_tools":true,"message":"Works","models":["text-embedding-3","gpt-6","gpt-6-luna"]}`}
	h := setupHub(t, s)
	res := h.TestProvider(Form{ID: "p1", Preset: "openai", Model: "gpt-6"})
	if !res.OK || !res.SupportsTools || res.Message != "Works" || !slices.Equal(res.Models, []string{"gpt-6-luna", "gpt-6"}) {
		t.Errorf("tested %+v", res)
	}
	if calls := s.calls(); len(calls) != 1 || calls[0]["action"] != "test" || calls[0]["provider_id"] != "p1" || calls[0]["model"] != "gpt-6" {
		t.Errorf("asked %v", calls)
	}
	if res := setupHub(t, nil).TestProvider(Form{ID: "p1", Preset: "openai", Model: "gpt-6"}); res.OK || res.Message != locales.SettingsPasteKeyToTest() {
		t.Errorf("signed out: %+v", res)
	}
	if res := h.TestProvider(Form{Preset: "openai", Model: "gpt-6"}); res.OK || res.Message != locales.SettingsPasteKeyToTest() {
		t.Errorf("no key: %+v", res)
	}
}

// A ChatGPT provider is tried through its sign-in, with the models its
// plan lists; none yet, it asks to sign in first.
func TestTestProviderOnChatGPT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = fmt.Fprint(w, `{"models":[{"slug":"gpt-6","visibility":"list"},{"slug":"gpt-6-mini","visibility":"list"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
			"output": []any{map[string]any{"type": "function_call", "call_id": "c1", "name": "ping", "arguments": `{"value":"ok"}`}},
		}})
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", raw)
	}))
	defer srv.Close()
	h := setupHub(t, nil)
	g := &ChatGPT{access: "tok", expires: time.Now().Add(time.Hour), base: srv.URL}
	res := h.TestProvider(Form{Preset: "chatgpt", Model: "gpt-6", ChatGPT: g})
	if !res.OK || !res.SupportsTools || res.Message != locales.AgentTestModels(2) || !slices.Equal(res.Models, []string{"gpt-6", "gpt-6-mini"}) {
		t.Errorf("tested %+v", res)
	}
	if res := h.TestProvider(Form{Preset: "chatgpt", Model: "gpt-6"}); res.OK || res.Message != locales.SettingsChatGPTSignInFirst() {
		t.Errorf("signed out of ChatGPT: %+v", res)
	}
}

// A provider kept here is saved with its key in the keychain, which it
// needs; a missing address or model says so.
func TestSaveProviderKeptHere(t *testing.T) {
	useMemKeys(t)
	h := setupHub(t, nil)
	if _, err := h.SaveProvider(Form{Preset: "deepseek", BaseURL: "https://api.deepseek.com"}); err == nil || err.Error() != locales.SettingsNeedURLAndModel() {
		t.Errorf("no model: %v", err)
	}
	if _, err := h.SaveProvider(Form{Preset: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4"}); err == nil || err.Error() != locales.SettingsNeedKey() {
		t.Errorf("no key: %v", err)
	}
	tools := true
	p, err := h.SaveProvider(Form{Preset: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4", APIKey: " sk-1 ", SupportsTools: &tools})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := h.st.Provider(p.ID)
	if err != nil || !kept.IsLocal || kept.AuthKind != "api_key" || kept.Name != "DeepSeek" || kept.SupportsTools == nil || !*kept.SupportsTools {
		t.Errorf("kept %+v, %v", kept, err)
	}
	if got := LocalKey(p.ID); got != "sk-1" {
		t.Errorf("key %q", got)
	}
}

// Signed in, a cloud provider goes to the server, which keeps its key; it
// is kept here as the server has it, with what a test in the form found
// of its tools, and the models listed there while the server lists none.
func TestSaveProviderOnTheServer(t *testing.T) {
	s := &providersServer{answer: `{"provider":{"id":"p1","preset":"openai","name":"OpenAI","base_url":"https://api.openai.com/v1","auth_kind":"api_key","default_model":"gpt-6","supports_tools":true}}`}
	h := setupHub(t, s)
	tools := false
	p, err := h.SaveProvider(Form{Preset: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-6", APIKey: "sk-1", SupportsTools: &tools, Models: []string{"gpt-6"}})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := h.st.Provider(p.ID)
	if err != nil || kept.IsLocal || kept.SupportsTools == nil || *kept.SupportsTools || !slices.Equal(kept.Models, []string{"gpt-6"}) {
		t.Errorf("kept %+v, %v", kept, err)
	}
	calls := s.calls()
	if len(calls) != 1 || calls[0]["action"] != "save" {
		t.Fatalf("sent %v", calls)
	}
	if sent, _ := calls[0]["provider"].(map[string]any); sent["api_key"] != "sk-1" || sent["name"] != "OpenAI" {
		t.Errorf("sent %v", sent)
	}
}

// A provider kept on the server is removed here only once the server has
// deleted it; one kept here goes with its key, and stays while the key
// can't be removed.
func TestRemoveProvider(t *testing.T) {
	keys := useMemKeys(t)
	s := &providersServer{down: true}
	h := setupHub(t, s)
	for _, p := range []store.Provider{{ID: "server", Preset: "openai"}, {ID: "here", Preset: "openai", IsLocal: true, AuthKind: "api_key"}} {
		if err := h.st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetLocalKey("here", "sk-1"); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveProvider("server"); err == nil || !strings.Contains(err.Error(), "down for maintenance") {
		t.Errorf("removed while the server is down: %v", err)
	}
	if _, err := h.st.Provider("server"); err != nil {
		t.Error("removed here though the server kept it")
	}
	keys.locked = true
	if err := h.RemoveProvider("here"); err == nil || !strings.HasPrefix(err.Error(), locales.SettingsDeleteKeyFailed("")) {
		t.Errorf("removed with its key left: %v", err)
	}
	if _, err := h.st.Provider("here"); err != nil || LocalKey("here") != "sk-1" {
		t.Errorf("gone though its key stays: %v", err)
	}
	keys.locked = false
	if err := h.RemoveProvider("here"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Provider("here"); err == nil || LocalKey("here") != "" {
		t.Errorf("kept here: %v, key %q", err, LocalKey("here"))
	}
	if calls := s.calls(); len(calls) != 1 || calls[0]["action"] != "delete" || calls[0]["id"] != "server" {
		t.Errorf("asked %v", calls)
	}
}

// A ChatGPT sign-in asks for the client of the provider's own sign-in,
// else the one this device signed in as last.
func TestSignInWithChatGPTAsksForTheClient(t *testing.T) {
	fakeDiscovery(t, &chatgptDiscovery{AuthorizationEndpoint: "https://auth.example.com/authorize"})
	h := setupHub(t, nil)
	asked := func(had *ChatGPT) string {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		var client string
		_, err := h.SignInWithChatGPT(ctx, had, func(link string) {
			u, _ := url.Parse(link)
			client = u.Query().Get("client_id")
			cancel()
		})
		if err == nil {
			t.Fatal("signed in with no answer")
		}
		return client
	}
	if got := asked(nil); got != "dynamic_agent_client" {
		t.Errorf("first sign-in asked for %q", got)
	}
	if err := h.st.SetValue(chatgptClientKey, "client.1"); err != nil {
		t.Fatal(err)
	}
	if got := asked(nil); got != "client.1" {
		t.Errorf("asked for %q, want this device's", got)
	}
	if got := asked(NewChatGPT(ChatGPTAccount{ClientID: "client.2"})); got != "client.2" {
		t.Errorf("asked for %q, want the provider's", got)
	}
}
