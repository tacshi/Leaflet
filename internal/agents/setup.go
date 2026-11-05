package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"leaflet/internal/agents/endpoint"
	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Setting providers up from Settings, as the desktop and the iOS app do:
// where one is kept, testing it, saving and removing it, and signing in
// with ChatGPT. Each waits on the network or the keychain: off the app's
// main thread. What goes wrong they say in words for the user.

// Form is a provider as Settings' dialog has it, to test or save.
type Form struct {
	// ID is the provider's; "" for a new one.
	ID      string
	Preset  string
	Name    string
	BaseURL string
	// APIKey is the key typed, or the one kept here of a provider edited;
	// "" is none.
	APIKey string
	// KeyTyped says APIKey is what the user has before them, typed just now
	// or shown gone, "" among it to take a key away. Not typed, it is the
	// key kept here, or none that could be read: it goes only to the
	// address it was saved for, and saving leaves the one kept as it is.
	KeyTyped bool
	Model    string
	// Models are those the provider lists, as the dialog has them.
	Models []string
	// SupportsTools is what a test in the dialog found, or the provider
	// had; nil while it is not known.
	SupportsTools *bool
	// ChatGPT is the sign-in a ChatGPT provider runs on: the one made in
	// the dialog, else its own; nil before signing in.
	ChatGPT *ChatGPT
}

// movedKey reports whether the form moves its provider to another origin
// without a key typed for it: the key kept here does not go along, as the
// server's providers' do not (keyStaysWith), or it would go to whatever
// server the form now names. Saving refuses that while a key is kept, so
// it goes neither there nor away unsaid; an app that shows it gone says so
// by KeyTyped.
func (h *Hub) movedKey(f Form) bool {
	if f.KeyTyped || f.ID == "" || PresetByID(f.Preset).Auth == "account" {
		return false
	}
	p, err := h.st.Provider(f.ID)
	return err == nil && !endpoint.SameOrigin(p.BaseURL, f.BaseURL)
}

// KeptHere reports whether the form's cloud provider is kept on this
// device, its key in the keychain, and runs from here: one signed out, or
// one already kept here. Signed in, a new one is kept on the server, for
// every device. A provider on this device's network, or ChatGPT's, is
// none: they are this device's anyway.
func (h *Hub) KeptHere(f Form) bool {
	preset := PresetByID(f.Preset)
	if preset.Local || preset.Auth == "account" {
		return false
	}
	if f.ID != "" {
		p, err := h.st.Provider(f.ID)
		return err == nil && p.IsLocal
	}
	return !h.st.Session().SignedIn
}

// TestProvider tries the form's provider: whether its model answers and
// calls tools, and the models it serves, chat models first. A key typed
// in is tried from this device, before it goes to the server; a saved
// cloud provider is tried by the server, which keeps its key; a ChatGPT
// one through its sign-in.
func (h *Hub) TestProvider(f Form) TestResult {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	preset := PresetByID(f.Preset)
	key := strings.TrimSpace(f.APIKey)
	if h.movedKey(f) {
		key = ""
	}
	var res TestResult
	switch {
	case preset.Auth == "account":
		switch {
		case f.ChatGPT == nil:
			res.Message = locales.SettingsChatGPTSignInFirst()
		case f.Model == "":
			res.Message = locales.SettingsChooseModelToTest()
		default:
			models, _ := f.ChatGPT.ListModels(ctx)
			res = TestModel(ctx, f.ChatGPT, f.Model, models)
		}
	case preset.Local || key != "":
		res = NewClient(f.BaseURL, key).Test(ctx, f.Model)
	case f.ID != "" && h.st.Session().SignedIn && !h.KeptHere(f):
		res = h.testOnServer(ctx, f.ID, f.Model)
	default:
		res.Message = locales.SettingsPasteKeyToTest()
	}
	res.Models = ChatModels(res.Models, preset.Suggested)
	return res
}

// testOnServer has the server try a provider it keeps, with its key.
func (h *Hub) testOnServer(ctx context.Context, id, model string) TestResult {
	r, err := h.be.Invoke(ctx, "providers", map[string]any{"action": "test", "provider_id": id, "model": model})
	if err != nil {
		return TestResult{Message: err.Error()}
	}
	var res TestResult
	res.OK, _ = r["ok"].(bool)
	res.SupportsTools, _ = r["supports_tools"].(bool)
	res.Message, _ = r["message"].(string)
	if l, ok := r["models"].([]any); ok {
		for _, m := range l {
			if id, ok := m.(string); ok {
				res.Models = append(res.Models, id)
			}
		}
	}
	return res
}

// SaveProvider adds or changes the form's provider, and returns it as it
// is kept. One on this device's network is kept here, and so is a cloud
// one KeptHere says is, its key in the keychain, and ChatGPT's, with the
// form's sign-in; another cloud one goes to the server, which keeps its
// key. What a test in the form found of its tools, and the models it
// listed, are kept with it. Making it the default agent is the app's.
func (h *Hub) SaveProvider(f Form) (store.Provider, error) {
	preset := PresetByID(f.Preset)
	account := preset.Auth == "account"
	// An account's provider goes to its preset's address, whatever the
	// form says.
	if !account && strings.TrimSpace(f.BaseURL) == "" || strings.TrimSpace(f.Model) == "" {
		return store.Provider{}, errors.New(locales.SettingsNeedURLAndModel())
	}
	name := strings.TrimSpace(f.Name)
	if name == "" {
		name = preset.Name
	}
	key := strings.TrimSpace(f.APIKey)
	moved := h.movedKey(f)
	if moved {
		key = ""
	}
	old, oldErr := h.st.Provider(f.ID)
	// None typed and none read, as the keychain's prompt was turned down:
	// the one kept stays as it is, for the provider's next read of it.
	unread := oldErr == nil && !f.KeyTyped && key == "" && !moved && !account
	here := h.KeptHere(f)
	switch {
	case moved && (preset.Local || here) && keyKept(f.ID):
		return store.Provider{}, errors.New(locales.SettingsKeyStaysBehind())
	case here && key == "" && !unread:
		return store.Provider{}, errors.New(locales.SettingsNeedKey())
	case account && f.ChatGPT == nil && f.ID == "":
		return store.Provider{}, errors.New(locales.SettingsChatGPTSignInFirst())
	case !preset.Local && !here && !account:
		return h.saveOnServer(f, name, key)
	}
	p := store.Provider{
		ID: f.ID, Preset: f.Preset, Name: name, BaseURL: endpoint.NormalizeBaseURL(f.BaseURL),
		AuthKind: "none", DefaultModel: f.Model, IsLocal: true, SupportsTools: f.SupportsTools, Models: f.Models,
	}
	if p.ID == "" {
		p.ID = doc.NewRowID()
	}
	if key != "" {
		p.AuthKind = "api_key"
	}
	if unread {
		p.AuthKind = old.AuthKind
	}
	if account {
		p.AuthKind, p.BaseURL = "account", preset.BaseURL
		// Without a sign-in of the form's, it keeps the one it has.
		if f.ChatGPT != nil {
			if err := UseChatGPT(p.ID, f.ChatGPT); err != nil {
				return store.Provider{}, errors.New(locales.SettingsKeepSignInFailed(err.Error()))
			}
		}
	}
	if oldErr == nil {
		p.CreatedAt = old.CreatedAt
	}
	if err := h.st.PutProvider(p); err != nil {
		return store.Provider{}, err
	}
	if !account && !unread {
		if err := SetLocalKey(p.ID, key); err != nil {
			return store.Provider{}, errors.New(locales.SettingsSaveKeyFailed(err.Error()))
		}
	}
	return p, nil
}

// saveOnServer saves the form's cloud provider on the server, with key,
// the one typed, if any: it keeps the key, for every device. It is kept
// here as the server has it.
func (h *Hub) saveOnServer(f Form, name, key string) (store.Provider, error) {
	body := map[string]any{
		"preset": f.Preset, "name": name, "base_url": endpoint.NormalizeBaseURL(f.BaseURL),
		"auth_kind": "api_key", "default_model": f.Model,
	}
	if f.ID != "" {
		body["id"] = f.ID
	}
	if key != "" {
		body["api_key"] = key
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := h.be.Invoke(ctx, "providers", map[string]any{"action": "save", "provider": body})
	if err != nil {
		return store.Provider{}, err
	}
	raw, _ := json.Marshal(res["provider"])
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return store.Provider{}, err
	}
	p := store.ProviderFromRemote(row)
	if f.SupportsTools != nil {
		// What the form knows, from the provider or a test run in it,
		// over what the server last heard.
		p.SupportsTools = f.SupportsTools
	}
	if len(p.Models) == 0 {
		p.Models = f.Models
	}
	if err := h.st.PutProvider(p); err != nil {
		return store.Provider{}, err
	}
	return p, nil
}

// RemoveProvider removes a provider, with its key or sign-in. One kept on
// the server goes there first: removed only here, sync would bring it
// back, so until the server has deleted it, it stays; and so it does
// while its key can't be removed from the keychain, to try again. A
// default agent it was is none from then on, as Store.Settings reads one
// whose provider is gone.
func (h *Hub) RemoveProvider(id string) error {
	p, err := h.st.Provider(id)
	if err != nil {
		return err
	}
	if !p.IsLocal {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := h.be.Invoke(ctx, "providers", map[string]any{"action": "delete", "id": id}); err != nil {
			return errors.New(locales.SettingsDeleteFailed(err.Error()))
		}
	}
	if err := SetLocalKey(id, ""); err != nil {
		return errors.New(locales.SettingsDeleteKeyFailed(err.Error()))
	}
	if p.AuthKind == "account" {
		g, err := dropChatGPT(id)
		if err != nil {
			return errors.New(locales.SettingsDeleteKeyFailed(err.Error()))
		}
		if g != nil {
			guard.Go(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				g.SignOut(ctx)
			})
		}
	}
	if err := h.st.DeleteProvider(id); err != nil {
		return errors.New(locales.SettingsDeleteFailed(err.Error()))
	}
	return nil
}

// chatgptClientKey keeps the client this device last signed in to ChatGPT
// as.
const chatgptClientKey = "chatgpt.client"

// SignInWithChatGPT signs in with ChatGPT for a provider's dialog, as the
// client of had, the sign-in it has, else as the one this device last
// signed in as, which OpenAI's page then knows; open hears the page to
// open. It gives up after five minutes, or once ctx ends, as the dialog
// closes.
func (h *Hub) SignInWithChatGPT(ctx context.Context, had *ChatGPT, open func(url string)) (*ChatGPT, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	clientID := ""
	if had != nil {
		clientID = had.Account().ClientID
	}
	if clientID == "" {
		clientID, _ = h.st.Value(chatgptClientKey)
	}
	g, err := SignInChatGPT(ctx, h.HostID(), clientID, open)
	if err != nil {
		return nil, err
	}
	if err := h.st.SetValue(chatgptClientKey, g.Account().ClientID); err != nil {
		log.Printf("ChatGPT client: %v", err)
	}
	return g, nil
}
