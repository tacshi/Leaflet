package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"sync"

	"leaflet/internal/agents/endpoint"
	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/store"
	"leaflet/locales"
)

// The agents people can pick, and which runs: the providers and their
// models, the keys kept for them, and the ChatGPT sign-ins.

// Option is an agent people can pick: a provider and a model.
type Option struct {
	Provider store.Provider
	Model    string
}

// Name is the agent's name, its provider's.
func (o Option) Name() string { return o.Provider.Name }

// Local reports whether the agent runs from this device: against a model
// server on it or the network, or a cloud provider whose key is kept here.
func (o Option) Local() bool { return o.Provider.IsLocal }

// Offline reports whether the agent works without a connection: a model
// server on this machine or the network, a preset's or a custom one, not
// a signed-in plan or a cloud provider kept on this device.
func (o Option) Offline() bool {
	p := o.Provider
	return p.IsLocal && p.AuthKind != "account" && (PresetByID(p.Preset).Local || endpoint.IsLocal(p.BaseURL))
}

// Choice is the option as a setting.
func (o Option) Choice() store.ModelChoice {
	return store.ModelChoice{ProviderID: o.Provider.ID, Model: o.Model}
}

// Label names the agent and its model.
func (o Option) Label() string { return o.Provider.Name + " · " + o.Model }

// Models returns a provider's models, or its preset's suggestions.
func Models(p store.Provider) []string {
	if len(p.Models) > 0 {
		return p.Models
	}
	var out []string
	if p.DefaultModel != "" {
		out = append(out, p.DefaultModel)
	}
	for _, m := range PresetByID(p.Preset).Suggested {
		if m != p.DefaultModel {
			out = append(out, m)
		}
	}
	return out
}

// Options lists every connected agent with its default model.
func (h *Hub) Options() []Option {
	h.optionsMu.Lock()
	defer h.optionsMu.Unlock()
	if h.options == nil {
		ps, err := h.st.Providers()
		if err != nil {
			return nil // read again next time
		}
		h.options = optionsOf(ps)
	}
	return slices.Clone(h.options)
}

// optionsOf is Options of providers ps, never nil.
func optionsOf(ps []store.Provider) []Option {
	out := []Option{}
	for _, p := range ps {
		m := p.DefaultModel
		if m == "" {
			if ms := Models(p); len(ms) > 0 {
				m = ms[0]
			}
		}
		if m != "" {
			out = append(out, Option{Provider: p, Model: m})
		}
	}
	return out
}

// ErrNoAgent is returned when no model can run, saying why in the app's
// language.
type ErrNoAgent struct{ Message string }

func (e *ErrNoAgent) Error() string { return e.Message }

// Resolve picks the agent to use: the one asked for, else the default,
// switching to a model on this machine while offline.
func (h *Hub) Resolve(wanted *store.ModelChoice) (Option, error) {
	all := h.Options()
	if len(all) == 0 {
		return Option{}, &ErrNoAgent{locales.AgentNoProvider()}
	}
	settings := h.host.Settings()
	pick := func(c *store.ModelChoice) (Option, bool) {
		if c == nil {
			return Option{}, false
		}
		for _, o := range all {
			if o.Provider.ID == c.ProviderID {
				if c.Model != "" {
					o.Model = c.Model
				}
				return o, true
			}
		}
		return Option{}, false
	}
	option, ok := pick(wanted)
	if !ok {
		if def, has := settings.Default(); has {
			// The default names a provider, which runs its own model; an
			// older default may name a model too.
			def.Model = ""
			option, ok = pick(&def)
		}
	}
	if !ok {
		option = all[0]
	}
	signedIn := h.be.SignedIn()
	online := h.host.Online() && !settings.WorkOffline
	// Cloud providers need the server; a ChatGPT plan needs the network.
	needsServer := !option.Local()
	usable := option.Offline() || (online && (!needsServer || signedIn))
	if !usable {
		var offline *Option
		for i := range all {
			if all[i].Offline() {
				offline = &all[i]
				break
			}
		}
		switch {
		case offline != nil:
			option = *offline
		case needsServer && !signedIn:
			return Option{}, &ErrNoAgent{locales.AgentSignInToUse(option.Name())}
		default:
			return Option{}, &ErrNoAgent{locales.AgentNeedsConnection()}
		}
	}
	return option, nil
}

// Available reports whether any agent can run now.
func (h *Hub) Available() bool {
	_, err := h.Resolve(nil)
	return err == nil
}

// LocalKey is the API key of a local provider, kept in the keychain.
func LocalKey(providerID string) string {
	raw, err := keychain.Get("provider." + providerID + ".key")
	if err != nil {
		return ""
	}
	return string(raw)
}

// SetLocalKey keeps a local provider's API key in the keychain.
func SetLocalKey(providerID, key string) error {
	if key == "" {
		err := keychain.Delete("provider." + providerID + ".key")
		if errors.Is(err, keychain.ErrNotFound) {
			return nil
		}
		return err
	}
	return keychain.Set("provider."+providerID+".key", []byte(key))
}

// ModelFor is the model a provider on this device runs on: its server, or
// the ChatGPT plan it signed in to.
func (h *Hub) ModelFor(p store.Provider) (Model, error) {
	if p.AuthKind == "account" {
		return h.ChatGPT(p.ID)
	}
	return NewClient(p.BaseURL, LocalKey(p.ID)), nil
}

var (
	chatgptMu      sync.Mutex
	chatgptClients = map[string]*ChatGPT{}
)

// KnownChatGPT is the provider's ChatGPT sign-in if it was read from the
// keychain already, else nil, without reading it: for what is drawn,
// which must not wait on the keychain.
func (h *Hub) KnownChatGPT(providerID string) *ChatGPT {
	chatgptMu.Lock()
	defer chatgptMu.Unlock()
	return chatgptClients[providerID]
}

// ChatGPT returns the ChatGPT sign-in of a provider, kept in the keychain.
func (h *Hub) ChatGPT(providerID string) (*ChatGPT, error) {
	chatgptMu.Lock()
	defer chatgptMu.Unlock()
	if g := chatgptClients[providerID]; g != nil {
		return g, nil
	}
	raw, err := keychain.Get("chatgpt." + providerID)
	if err != nil {
		return nil, errors.New(locales.AgentChatgptSignInAgain())
	}
	var acct ChatGPTAccount
	if err := json.Unmarshal(raw, &acct); err != nil {
		return nil, err
	}
	g := NewChatGPT(acct)
	keepRefreshed(providerID, g)
	chatgptClients[providerID] = g
	return g, nil
}

// keepRefreshed keeps g's account as its refresh token rotates, while g
// is still the provider's sign-in: a run may hold one that another
// sign-in replaced, or that was signed out, and its renewal must not
// put it back. Under chatgptMu, the saves go in the order the sign-ins
// change, and g renews one at a time, so its saves go in order too.
func keepRefreshed(providerID string, g *ChatGPT) {
	g.OnRefresh = func(a ChatGPTAccount) {
		chatgptMu.Lock()
		defer chatgptMu.Unlock()
		if chatgptClients[providerID] != g {
			return
		}
		if err := SaveChatGPT(providerID, a); err != nil {
			log.Printf("keep ChatGPT sign-in: %v", err)
		}
	}
}

// UseChatGPT keeps a new ChatGPT sign-in for a provider.
func UseChatGPT(providerID string, g *ChatGPT) error {
	keepRefreshed(providerID, g)
	chatgptMu.Lock()
	defer chatgptMu.Unlock()
	chatgptClients[providerID] = g
	return SaveChatGPT(providerID, g.Account())
}

// keepSecret and forgetSecret write the keychain; tests keep the
// sign-ins elsewhere.
var (
	keepSecret   = keychain.Set
	forgetSecret = keychain.Delete
)

// SaveChatGPT keeps a ChatGPT sign-in in the keychain.
func SaveChatGPT(providerID string, a ChatGPTAccount) error {
	raw, _ := json.Marshal(a) // #nosec G117 -- the sign-in goes to the keychain.
	return keepSecret("chatgpt."+providerID, raw)
}

// ForgetChatGPT signs a provider out of ChatGPT on this device.
func ForgetChatGPT(ctx context.Context, providerID string) {
	chatgptMu.Lock()
	g := chatgptClients[providerID]
	delete(chatgptClients, providerID)
	_ = forgetSecret("chatgpt." + providerID)
	chatgptMu.Unlock()
	if g != nil {
		g.SignOut(ctx)
	}
}

// HostID names this device to OpenAI's sign-in, the same every time.
func (h *Hub) HostID() string {
	if id, ok := h.st.Value("chatgpt.host"); ok {
		return id
	}
	id := "urn:uuid:" + doc.NewRowID()
	_ = h.st.SetValue("chatgpt.host", id)
	return id
}
