package agents

import (
	"context"
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

type Form struct {
	ID            string
	Preset        string
	Name          string
	BaseURL       string
	APIKey        string
	KeyTyped      bool
	Model         string
	Models        []string
	SupportsTools *bool
	ChatGPT       *ChatGPT
}

func (h *Hub) movedKey(f Form) bool {
	if f.KeyTyped || f.ID == "" || PresetByID(f.Preset).Auth == "account" {
		return false
	}
	p, err := h.st.Provider(f.ID)
	return err == nil && !endpoint.SameOrigin(p.BaseURL, f.BaseURL)
}

func keyed(f Form) bool {
	preset := PresetByID(f.Preset)
	return !preset.Local && preset.Auth != "account"
}

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
	default:
		res.Message = locales.SettingsPasteKeyToTest()
	}
	res.Models = ChatModels(res.Models, preset.Suggested)
	return res
}

func (h *Hub) SaveProvider(f Form) (store.Provider, error) {
	preset := PresetByID(f.Preset)
	account := preset.Auth == "account"
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
	unread := oldErr == nil && !f.KeyTyped && key == "" && !moved && !account
	switch {
	case moved && !account && keyKept(f.ID):
		return store.Provider{}, errors.New(locales.SettingsKeyStaysBehind())
	case keyed(f) && key == "" && !unread:
		return store.Provider{}, errors.New(locales.SettingsNeedKey())
	case account && f.ChatGPT == nil && f.ID == "":
		return store.Provider{}, errors.New(locales.SettingsChatGPTSignInFirst())
	}
	p := store.Provider{
		ID: f.ID, Preset: f.Preset, Name: name, BaseURL: endpoint.NormalizeBaseURL(f.BaseURL),
		AuthKind: "none", DefaultModel: f.Model, SupportsTools: f.SupportsTools, Models: f.Models,
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
			if oldErr == nil {
				_ = h.st.PutProvider(old)
			} else {
				_ = h.st.DeleteProvider(p.ID)
			}
			return store.Provider{}, errors.New(locales.SettingsSaveKeyFailed(err.Error()))
		}
	}
	return p, nil
}

func (h *Hub) RemoveProvider(id string) error {
	p, err := h.st.Provider(id)
	if err != nil {
		return err
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

const chatgptClientKey = "chatgpt.client"

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
