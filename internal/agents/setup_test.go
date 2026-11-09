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

// memKeys is a keychain in memory; while locked, nothing is kept in it or
// removed from it.
type memKeys struct {
	mu     sync.Mutex
	keys   map[string][]byte
	locked bool
	// denied has every read turned down, as the system's prompt can be.
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

// A provider whose key the keychain does not take is not kept without
// it: a new one is not saved, and one changed stays as it was, with the
// key it had.
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

// A key kept here goes only to the address it was saved for: a provider
// moved to another server takes a key typed for it, as the server's do,
// and one at the same address keeps its own. Testing it says so too,
// rather than try the old key out there.
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
	// Another path at the same address keeps it.
	if _, err := h.SaveProvider(Form{ID: cloud.ID, Preset: "custom", BaseURL: "https://one.example/v2", Model: "m", APIKey: "sk-one"}); err != nil || LocalKey(cloud.ID) != "sk-one" {
		t.Errorf("same address: %v, key %q", err, LocalKey(cloud.ID))
	}
	// Typed for the new address, even the same key, it goes there.
	moved.KeyTyped = true
	if res := h.TestProvider(moved); !strings.Contains(<-heard, "sk-one") {
		t.Errorf("typed key not tried: %+v", res)
	}
	if _, err := h.SaveProvider(moved); err != nil || LocalKey(cloud.ID) != "sk-one" {
		t.Errorf("typed key: %v, key %q", err, LocalKey(cloud.ID))
	}

	// One on this device's network is told so too, rather than lose its key
	// without a word, as an app that never holds it would have it.
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
	// A key typed for the new address takes its place.
	away.APIKey, away.KeyTyped = "sk-three", true
	if p, err := h.SaveProvider(away); err != nil || p.AuthKind != "api_key" || LocalKey(local.ID) != "sk-three" {
		t.Errorf("typed: %+v, %v, key %q", p, err, LocalKey(local.ID))
	}
	// Shown gone by the app, it goes.
	away.BaseURL, away.APIKey = "http://127.0.0.1:1236/v1", ""
	if p, err := h.SaveProvider(away); err != nil || p.AuthKind != "none" || LocalKey(local.ID) != "" {
		t.Errorf("shown gone: %+v, %v, key %q", p, err, LocalKey(local.ID))
	}
	// One with no key has none to lose, and moves as it is.
	away.BaseURL, away.KeyTyped = "http://127.0.0.1:1237/v1", false
	if p, err := h.SaveProvider(away); err != nil || p.BaseURL != away.BaseURL {
		t.Errorf("no key: %+v, %v", p, err)
	}
}

// A key the keychain would not give is no provider without one: a run says
// so rather than ask its server with none, and saving the provider leaves
// the key as it is, for when it can be read again.
func TestKeyThatCouldNotBeRead(t *testing.T) {
	keys := useMemKeys(t)
	h := setupHub(t, nil)
	p, err := h.SaveProvider(Form{Preset: "local", BaseURL: "http://127.0.0.1:8080/v1", Model: "m", APIKey: "sk-1", KeyTyped: true})
	if err != nil {
		t.Fatal(err)
	}
	keychain.Use(keys) // forgets what the save kept in memory
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
	// Moved, it may be the one the provider has: it is not left behind,
	// or lost, unsaid.
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
	// Emptied by the user it goes: that is no key unread.
	if _, err := h.SaveProvider(Form{ID: p.ID, Preset: "local", BaseURL: p.BaseURL, Model: "m2", KeyTyped: true}); err != nil || LocalKey(p.ID) != "" {
		t.Errorf("emptied: %v, key %q", err, LocalKey(p.ID))
	}
}

// Leaving with the account deleted takes every provider's key and sign-in
// off the keychain, which no row names after Store.Wipe, and ends the
// sign-ins at OpenAI.
func TestForgetProviders(t *testing.T) {
	keys := useMemKeys(t)
	h := setupHub(t, nil)
	revoked := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		revoked <- r.FormValue("token")
	}))
	defer srv.Close()
	fakeDiscovery(t, &chatgptDiscovery{RevocationEndpoint: srv.URL + "/revoke"})
	for _, p := range []store.Provider{{ID: "key", Preset: "openai", IsLocal: true, AuthKind: "api_key"}, {ID: "plan", Preset: "chatgpt", IsLocal: true, AuthKind: "account"}, {ID: "plain", Preset: "ollama", IsLocal: true, AuthKind: "none"}} {
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
