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

type Option struct {
	Provider store.Provider
	Model    string
}

func (o Option) Name() string { return o.Provider.Name }

func (o Option) Offline() bool {
	p := o.Provider
	return p.AuthKind != "account" && (PresetByID(p.Preset).Local || endpoint.IsLocal(p.BaseURL))
}

func (o Option) Choice() store.ModelChoice {
	return store.ModelChoice{ProviderID: o.Provider.ID, Model: o.Model}
}

func (o Option) Label() string { return o.Provider.Name + " · " + o.Model }

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

func (h *Hub) Options() []Option {
	h.optionsMu.Lock()
	defer h.optionsMu.Unlock()
	if h.options == nil {
		ps, err := h.st.Providers()
		if err != nil {
			return nil
		}
		h.options = optionsOf(ps)
	}
	return slices.Clone(h.options)
}

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

type ErrNoAgent struct{ Message string }

func (e *ErrNoAgent) Error() string { return e.Message }

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
			def.Model = ""
			option, ok = pick(&def)
		}
	}
	if !ok {
		option = all[0]
	}
	online := h.host.Online() && !settings.WorkOffline
	if !option.Offline() && !online {
		var offline *Option
		for i := range all {
			if all[i].Offline() {
				offline = &all[i]
				break
			}
		}
		if offline == nil {
			return Option{}, &ErrNoAgent{locales.AgentNeedsConnection()}
		}
		option = *offline
	}
	return option, nil
}

func (h *Hub) Available() bool {
	_, err := h.Resolve(nil)
	return err == nil
}

func LocalKey(providerID string) string {
	key, _ := keptKey(providerID)
	return key
}

func keptKey(providerID string) (string, error) {
	raw, err := keychain.Get("provider." + providerID + ".key")
	if errors.Is(err, keychain.ErrNotFound) {
		return "", nil
	}
	return string(raw), err
}

func keyKept(providerID string) bool {
	key, err := keptKey(providerID)
	return key != "" || err != nil
}

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

func (h *Hub) ModelFor(p store.Provider) (Model, error) {
	if p.AuthKind == "account" {
		return h.ChatGPT(p.ID)
	}
	key, err := keptKey(p.ID)
	if err != nil && p.AuthKind == "api_key" {
		return nil, errors.New(locales.AgentKeyUnread(p.Name))
	}
	if key == "" && p.AuthKind == "api_key" {
		return nil, errors.New(locales.AgentKeyMissing(p.Name))
	}
	return NewClient(p.BaseURL, key), nil
}

var (
	chatgptMu      sync.Mutex
	chatgptClients = map[string]*ChatGPT{}
	chatgptDrops   = map[string]int{}
	chatgptSaves   sync.Mutex
)

func (h *Hub) KnownChatGPT(providerID string) *ChatGPT {
	chatgptMu.Lock()
	defer chatgptMu.Unlock()
	return chatgptClients[providerID]
}

func (h *Hub) ChatGPT(providerID string) (*ChatGPT, error) {
	chatgptMu.Lock()
	g, drops := chatgptClients[providerID], chatgptDrops[providerID]
	chatgptMu.Unlock()
	if g != nil {
		return g, nil
	}
	raw, err := readSecret("chatgpt." + providerID)
	if err != nil {
		return nil, errors.New(locales.AgentChatgptSignInAgain())
	}
	var acct ChatGPTAccount
	if err := json.Unmarshal(raw, &acct); err != nil {
		return nil, err
	}
	chatgptMu.Lock()
	defer chatgptMu.Unlock()
	switch {
	case chatgptClients[providerID] != nil:
		return chatgptClients[providerID], nil
	case chatgptDrops[providerID] != drops:
		return nil, errors.New(locales.AgentChatgptSignInAgain())
	}
	g = NewChatGPT(acct)
	keepRefreshed(providerID, g)
	chatgptClients[providerID] = g
	return g, nil
}

func keepRefreshed(providerID string, g *ChatGPT) {
	g.OnRefresh = func(a ChatGPTAccount) {
		chatgptSaves.Lock()
		defer chatgptSaves.Unlock()
		chatgptMu.Lock()
		current := chatgptClients[providerID] == g
		chatgptMu.Unlock()
		if !current {
			return
		}
		if err := SaveChatGPT(providerID, a); err != nil {
			log.Printf("keep ChatGPT sign-in: %v", err)
		}
	}
}

func UseChatGPT(providerID string, g *ChatGPT) error {
	keepRefreshed(providerID, g)
	chatgptSaves.Lock()
	defer chatgptSaves.Unlock()
	chatgptMu.Lock()
	chatgptClients[providerID] = g
	chatgptMu.Unlock()
	return SaveChatGPT(providerID, g.Account())
}

var (
	readSecret   = keychain.Get
	keepSecret   = keychain.Set
	forgetSecret = keychain.Delete
)

func SaveChatGPT(providerID string, a ChatGPTAccount) error {
	raw, _ := json.Marshal(a) // #nosec G117
	return keepSecret("chatgpt."+providerID, raw)
}

func ForgetChatGPT(ctx context.Context, providerID string) error {
	g, err := dropChatGPT(providerID)
	if g != nil {
		g.SignOut(ctx)
	}
	return err
}

func dropChatGPT(providerID string) (*ChatGPT, error) {
	chatgptSaves.Lock()
	defer chatgptSaves.Unlock()
	chatgptMu.Lock()
	g := chatgptClients[providerID]
	chatgptMu.Unlock()
	if g == nil {
		if raw, err := readSecret("chatgpt." + providerID); err == nil {
			var acct ChatGPTAccount
			if json.Unmarshal(raw, &acct) == nil {
				g = NewChatGPT(acct)
			}
		}
	}
	err := forgetSecret("chatgpt." + providerID)
	chatgptMu.Lock()
	defer chatgptMu.Unlock()
	if read := chatgptClients[providerID]; read != nil {
		g = read
	}
	delete(chatgptClients, providerID)
	chatgptDrops[providerID]++
	return g, err
}

func (h *Hub) ForgetProviders(ctx context.Context) {
	ps, err := h.st.Providers()
	if err != nil {
		log.Printf("forget providers: %v", err)
		return
	}
	for _, p := range ps {
		if err := SetLocalKey(p.ID, ""); err != nil {
			log.Printf("forget provider key: %v", err)
		}
		if p.AuthKind != "account" {
			continue
		}
		if err := ForgetChatGPT(ctx, p.ID); err != nil {
			log.Printf("forget ChatGPT sign-in: %v", err)
		}
	}
}

func (h *Hub) HostID() string {
	if id, ok := h.st.Value("chatgpt.host"); ok {
		return id
	}
	id := "urn:uuid:" + doc.NewRowID()
	_ = h.st.SetValue("chatgpt.host", id)
	return id
}
