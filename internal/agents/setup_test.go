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

type memKeys struct {
	mu     sync.Mutex
	keys   map[string][]byte
	locked bool
	denied bool
}

func (k *memKeys) Get(name string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.denied {
		return nil, keychain.ErrDenied
	}
	v, ok := k.keys[name]
	if !ok {
		return nil, keychain.ErrNotFound
	}
	return v, nil
}

func (k *memKeys) Set(name string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.locked {
		return errors.New("the keychain is locked")
	}
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

func useMemKeys(t *testing.T) *memKeys {
	t.Helper()
	k := &memKeys{keys: map[string][]byte{}}
	keychain.Use(k)
	t.Cleanup(func() { keychain.Use(nil) })
	return k
}

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

func (s *providersServer) calls() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.heard
	s.heard = nil
	return out
}

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

func TestTestProviderNeedsItsKeyHere(t *testing.T) {
	s := &providersServer{answer: `{"ok":true}`}
	h := setupHub(t, s)
	if res := h.TestProvider(Form{ID: "p1", Preset: "openai", Model: "gpt-6"}); res.OK || res.Message != locales.SettingsPasteKeyToTest() {
		t.Errorf("signed in: %+v", res)
	}
	if res := setupHub(t, nil).TestProvider(Form{Preset: "openai", Model: "gpt-6"}); res.OK || res.Message != locales.SettingsPasteKeyToTest() {
		t.Errorf("signed out: %+v", res)
	}
	if calls := s.calls(); len(calls) != 0 {
		t.Errorf("asked the server %v", calls)
	}
}

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

func TestSaveProviderKeepsItsKeyHere(t *testing.T) {
	useMemKeys(t)
	server := &providersServer{answer: `{}`}
	h := setupHub(t, server)
	t.Cleanup(func() {
		if calls := server.calls(); len(calls) != 0 {
			t.Errorf("asked the server %v", calls)
		}
	})
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
	if err != nil || kept.AuthKind != "api_key" || kept.Name != "DeepSeek" || kept.SupportsTools == nil || !*kept.SupportsTools {
		t.Errorf("kept %+v, %v", kept, err)
	}
	if got := LocalKey(p.ID); got != "sk-1" {
		t.Errorf("key %q", got)
	}
}

func TestSaveProviderWhoseKeyIsNotKept(t *testing.T) {
	keys := useMemKeys(t)
	h := setupHub(t, nil)
	form := Form{Preset: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4", APIKey: "sk-1", KeyTyped: true}
	keys.locked = true
	if _, err := h.SaveProvider(form); err == nil || !strings.Contains(err.Error(), "the keychain is locked") {
		t.Fatalf("saved with no key kept: %v", err)
	}
	if all, err := h.st.Providers(); err != nil || len(all) != 0 {
		t.Errorf("kept %+v, %v", all, err)
	}
	keys.locked = false
	p, err := h.SaveProvider(form)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.st.Provider(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	keys.locked = true
	form.ID, form.Name, form.Model, form.APIKey = p.ID, "Renamed", "deepseek-v5", "sk-2"
	if _, err := h.SaveProvider(form); err == nil {
		t.Fatal("changed with no key kept")
	}
	after, err := h.st.Provider(p.ID)
	if err != nil || after.Name != before.Name || after.DefaultModel != before.DefaultModel || after.AuthKind != "api_key" || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("after %+v, before %+v, %v", after, before, err)
	}
	if got := LocalKey(p.ID); got != "sk-1" {
		t.Errorf("key %q", got)
	}
}

func TestKeptKeyStaysWithItsAddress(t *testing.T) {
	useMemKeys(t)
	h := setupHub(t, nil)
	heard := make(chan string, 4)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case heard <- r.Header.Get("Authorization"):
		default:
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer other.Close()
	cloud, err := h.SaveProvider(Form{Preset: "custom", BaseURL: "https://one.example/v1", Model: "m", APIKey: "sk-one", KeyTyped: true})
	if err != nil {
		t.Fatal(err)
	}
	moved := Form{ID: cloud.ID, Preset: "custom", BaseURL: other.URL + "/v1", Model: "m", APIKey: "sk-one"}
	if res := h.TestProvider(moved); res.Message != locales.SettingsPasteKeyToTest() {
		t.Errorf("tested with %q", res.Message)
	}
	select {
	case got := <-heard:
		t.Errorf("the key went to another server: %q", got)
	default:
	}
	if _, err := h.SaveProvider(moved); err == nil || err.Error() != locales.SettingsKeyStaysBehind() {
		t.Errorf("moved with its old key: %v", err)
	}
	if p, _ := h.st.Provider(cloud.ID); p.BaseURL != "https://one.example/v1" || LocalKey(cloud.ID) != "sk-one" {
		t.Errorf("refused, and changed: %+v, key %q", p, LocalKey(cloud.ID))
	}
	if _, err := h.SaveProvider(Form{ID: cloud.ID, Preset: "custom", BaseURL: "https://one.example/v2", Model: "m", APIKey: "sk-one"}); err != nil || LocalKey(cloud.ID) != "sk-one" {
		t.Errorf("same address: %v, key %q", err, LocalKey(cloud.ID))
	}
	moved.KeyTyped = true
	if res := h.TestProvider(moved); !strings.Contains(<-heard, "sk-one") {
		t.Errorf("typed key not tried: %+v", res)
	}
	if _, err := h.SaveProvider(moved); err != nil || LocalKey(cloud.ID) != "sk-one" {
		t.Errorf("typed key: %v, key %q", err, LocalKey(cloud.ID))
	}

	local, err := h.SaveProvider(Form{Preset: "lmstudio", BaseURL: "http://127.0.0.1:1234/v1", Model: "m", APIKey: "sk-two", KeyTyped: true})
	if err != nil {
		t.Fatal(err)
	}
	away := Form{ID: local.ID, Preset: "lmstudio", BaseURL: "http://127.0.0.1:1235/v1", Model: "m"}
	if _, err := h.SaveProvider(away); err == nil || err.Error() != locales.SettingsKeyStaysBehind() {
		t.Errorf("moved without a word: %v", err)
	}
	if p, _ := h.st.Provider(local.ID); p.BaseURL != "http://127.0.0.1:1234/v1" || LocalKey(local.ID) != "sk-two" {
		t.Errorf("refused, and changed: %+v, key %q", p, LocalKey(local.ID))
	}
	away.APIKey, away.KeyTyped = "sk-three", true
	if p, err := h.SaveProvider(away); err != nil || p.AuthKind != "api_key" || LocalKey(local.ID) != "sk-three" {
		t.Errorf("typed: %+v, %v, key %q", p, err, LocalKey(local.ID))
	}
	away.BaseURL, away.APIKey = "http://127.0.0.1:1236/v1", ""
	if p, err := h.SaveProvider(away); err != nil || p.AuthKind != "none" || LocalKey(local.ID) != "" {
		t.Errorf("shown gone: %+v, %v, key %q", p, err, LocalKey(local.ID))
	}
	away.BaseURL, away.KeyTyped = "http://127.0.0.1:1237/v1", false
	if p, err := h.SaveProvider(away); err != nil || p.BaseURL != away.BaseURL {
		t.Errorf("no key: %+v, %v", p, err)
	}
}

func TestKeyThatCouldNotBeRead(t *testing.T) {
	keys := useMemKeys(t)
	h := setupHub(t, nil)
	p, err := h.SaveProvider(Form{Preset: "local", BaseURL: "http://127.0.0.1:8080/v1", Model: "m", APIKey: "sk-1", KeyTyped: true})
	if err != nil {
		t.Fatal(err)
	}
	keychain.Use(keys)
	keys.denied = true
	if _, err := h.ModelFor(p); err == nil || err.Error() != locales.AgentKeyUnread(p.Name) {
		t.Errorf("model with a key unread: %v", err)
	}
	if key := LocalKey(p.ID); key != "" {
		t.Fatalf("read %q", key)
	}
	saved, err := h.SaveProvider(Form{ID: p.ID, Preset: "local", BaseURL: p.BaseURL, Model: "m2", APIKey: LocalKey(p.ID)})
	if err != nil || saved.AuthKind != "api_key" || saved.DefaultModel != "m2" {
		t.Errorf("saved %+v, %v", saved, err)
	}
	if _, err := h.SaveProvider(Form{ID: p.ID, Preset: "local", BaseURL: "http://127.0.0.1:9090/v1", Model: "m2"}); err == nil || err.Error() != locales.SettingsKeyStaysBehind() {
		t.Errorf("moved with a key unread: %v", err)
	}
	keys.denied = false
	keychain.Use(keys)
	if key := LocalKey(p.ID); key != "sk-1" {
		t.Errorf("key %q after a save that could not read it", key)
	}
	if _, err := h.ModelFor(p); err != nil {
		t.Error(err)
	}
	if _, err := h.SaveProvider(Form{ID: p.ID, Preset: "local", BaseURL: p.BaseURL, Model: "m2", KeyTyped: true}); err != nil || LocalKey(p.ID) != "" {
		t.Errorf("emptied: %v, key %q", err, LocalKey(p.ID))
	}
}

func TestForgetProviders(t *testing.T) {
	keys := useMemKeys(t)
	h := setupHub(t, nil)
	revoked := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		revoked <- r.FormValue("token")
	}))
	defer srv.Close()
	fakeDiscovery(t, &chatgptDiscovery{RevocationEndpoint: srv.URL + "/revoke"})
	for _, p := range []store.Provider{{ID: "key", Preset: "openai", AuthKind: "api_key"}, {ID: "plan", Preset: "chatgpt", AuthKind: "account"}, {ID: "plain", Preset: "ollama", AuthKind: "none"}} {
		if err := h.st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetLocalKey("key", "sk-1"); err != nil {
		t.Fatal(err)
	}
	if err := UseChatGPT("plan", NewChatGPT(ChatGPTAccount{RefreshToken: "r1"})); err != nil {
		t.Fatal(err)
	}
	h.ForgetProviders(context.Background())
	if len(keys.keys) != 0 || h.KnownChatGPT("plan") != nil {
		t.Errorf("kept %v, sign-in %v", keys.keys, h.KnownChatGPT("plan"))
	}
	select {
	case got := <-revoked:
		if got != "r1" {
			t.Errorf("revoked %q", got)
		}
	default:
		t.Error("the sign-in was not revoked")
	}
}

func TestRemoveProvider(t *testing.T) {
	keys := useMemKeys(t)
	s := &providersServer{down: true}
	h := setupHub(t, s)
	if err := h.st.PutProvider(store.Provider{ID: "here", Preset: "openai", AuthKind: "api_key"}); err != nil {
		t.Fatal(err)
	}
	if err := SetLocalKey("here", "sk-1"); err != nil {
		t.Fatal(err)
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
	if calls := s.calls(); len(calls) != 0 {
		t.Errorf("asked the server %v", calls)
	}
}

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

func TestAProviderWithoutItsKeyAsksForIt(t *testing.T) {
	useMemKeys(t)
	h := setupHub(t, nil)
	keyed := store.Provider{ID: "cloud", Preset: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", AuthKind: "api_key"}
	if _, err := h.ModelFor(keyed); err == nil || err.Error() != locales.AgentKeyMissing("OpenAI") {
		t.Errorf("without its key: %v", err)
	}
	if err := SetLocalKey("cloud", "sk-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ModelFor(keyed); err != nil {
		t.Errorf("with its key: %v", err)
	}
	if _, err := h.ModelFor(store.Provider{ID: "ollama", Preset: "ollama", BaseURL: "http://localhost:11434/v1", AuthKind: "none"}); err != nil {
		t.Errorf("a model server: %v", err)
	}
}
