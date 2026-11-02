package mobile

import (
	"context"
	"errors"
	"sync"
	"time"

	"leaflet/internal/agents"
	"leaflet/locales"
)

// Signing in with ChatGPT, on the phone, as on the desktop: the core
// waits for OpenAI's answer on its loopback while the app shows OpenAI's
// page in a web session, from BeginChatGPTSignIn to FinishChatGPTSignIn.
// The app stays in front meanwhile, so the loopback keeps listening. The
// sign-in waits in the core until SaveProvider keeps it with a provider.

// chatgptSignIn is the provider form's ChatGPT sign-in: one being made,
// and the one made, until it is saved.
type chatgptSignIn struct {
	mu sync.Mutex
	// cancel stops the sign-in being made; done closes as it ends, with
	// err or the sign-in in g.
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	g      *agents.ChatGPT
}

// chatgptAccount is a ChatGPT sign-in, as FinishChatGPTSignIn returns it:
// who signed in, and the models their plan serves, chat models first.
type chatgptAccount struct {
	Email  string   `json:"email"`
	Models []string `json:"models"`
}

// BeginChatGPTSignIn begins signing in with ChatGPT, for the provider
// providerID ("" for a new one), and returns the page for the app to
// open. A sign-in begun or made before is given up.
func (c *Core) BeginChatGPTSignIn(providerID string) (_ string, err error) {
	defer catch(&err)
	c.ForgetChatGPTSignIn()
	var host, clientID string
	c.q.do(func() {
		host = c.core.Hub.HostID()
		if providerID != "" {
			if g, err := c.core.Hub.ChatGPT(providerID); err == nil {
				clientID = g.Account().ClientID
			}
		}
		if id, ok := c.core.Store.Value("chatgpt.client"); ok && clientID == "" {
			clientID = id
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	s := &c.chatgpt
	s.mu.Lock()
	done := make(chan struct{})
	s.cancel, s.done, s.err, s.g = cancel, done, nil, nil
	s.mu.Unlock()
	opened := make(chan string, 1)
	go func() {
		defer cancel()
		var g *agents.ChatGPT
		var err error
		// The sign-in ends however it ends, a panic too, for those
		// waiting on done.
		defer func() {
			s.mu.Lock()
			if s.done == done {
				s.g, s.err = g, err
			}
			s.mu.Unlock()
			close(done)
		}()
		defer catch(&err)
		g, err = agents.SignInChatGPT(ctx, host, clientID, func(u string) { opened <- u })
		if err == nil {
			c.q.do(func() { _ = c.core.Store.SetValue("chatgpt.client", g.Account().ClientID) })
		}
	}()
	select {
	case u := <-opened:
		return u, nil
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err == nil {
			return "", errors.New(locales.SettingsChatGPTSignInFailed())
		}
		return "", s.err
	}
}

// FinishChatGPTSignIn waits for the sign-in BeginChatGPTSignIn began, and
// returns a chatgptAccount as JSON, without its models: the app closes
// OpenAI's page as soon as it answers, then asks ChatGPTModels.
func (c *Core) FinishChatGPTSignIn() (_ string, err error) {
	defer catch(&err)
	s := &c.chatgpt
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return "", errors.New(locales.SettingsChatGPTSignInAgain())
	}
	<-done
	s.mu.Lock()
	g, err := s.g, s.err
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if g == nil {
		// Given up meanwhile.
		return "", errors.New(locales.SettingsChatGPTSignInCancelled())
	}
	return encode(chatgptAccount{Email: g.Account().Email, Models: []string{}})
}

// ChatGPTModels returns the models the plan signed in to in the form
// serves, chat models first, as a JSON array.
func (c *Core) ChatGPTModels() (_ string, err error) {
	defer catch(&err)
	g := c.madeChatGPT()
	if g == nil {
		return "", errors.New(locales.SettingsChatGPTSignInFirst())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := g.ListModels(ctx)
	if err != nil {
		return "", err
	}
	return encode(agents.ChatModels(models, agents.PresetByID("chatgpt").Suggested))
}

// CancelChatGPTSignIn gives up the sign-in being made, as its page is
// closed before OpenAI answered. One already made stays, for the form to
// save: the page may be closed after it said so.
func (c *Core) CancelChatGPTSignIn() {
	defer catch(nil)
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.g != nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel, s.done, s.err = nil, nil, nil
}

// ForgetChatGPTSignIn gives up the sign-in being made, and forgets one
// made and not saved, as the form closes.
func (c *Core) ForgetChatGPTSignIn() {
	defer catch(nil)
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel, s.done, s.err, s.g = nil, nil, nil, nil
}

// madeChatGPT returns the sign-in made in the provider form, if one was.
func (c *Core) madeChatGPT() *agents.ChatGPT {
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.g
}

// chatgptFor returns the sign-in a provider form runs on: the one just
// made, or the one kept for providerID.
func (c *Core) chatgptFor(providerID string) (*agents.ChatGPT, error) {
	if g := c.madeChatGPT(); g != nil {
		return g, nil
	}
	if providerID == "" {
		return nil, errors.New(locales.SettingsChatGPTSignInFirst())
	}
	var g *agents.ChatGPT
	var err error
	c.q.do(func() { g, err = c.core.Hub.ChatGPT(providerID) })
	return g, err
}
