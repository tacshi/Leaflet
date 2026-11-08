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
	// attempt numbers the sign-ins begun. The app gives one up, or
	// forgets it, by its number: a call that comes late, for one before,
	// leaves the one after alone.
	attempt int64
	// cancel stops the sign-in being made; done closes as it ends, with
	// err or the sign-in in g.
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	g      *agents.ChatGPT
}

// chatgptBegun is a sign-in begun, as BeginChatGPTSignIn returns it: the
// page for the app to open, and the attempt's number.
type chatgptBegun struct {
	URL     string `json:"url"`
	Attempt int64  `json:"attempt"`
}

// chatgptAccount is a ChatGPT sign-in, as FinishChatGPTSignIn returns it:
// who signed in, and the models their plan serves, chat models first.
type chatgptAccount struct {
	Email  string   `json:"email"`
	Models []string `json:"models"`
}

// BeginChatGPTSignIn begins signing in with ChatGPT, for the provider
// providerID ("" for a new one), and returns a chatgptBegun as JSON: the
// page for the app to open, and the number CancelChatGPTSignIn and
// ForgetChatGPTSignIn take. A sign-in begun or made before is given up.
func (c *Core) BeginChatGPTSignIn(providerID string) (_ string, err error) {
	defer catch(&err)
	c.forgetChatGPTSignIn()
	var had *agents.ChatGPT
	if providerID != "" {
		c.q.do(func() { had, _ = c.core.Hub.ChatGPT(providerID) })
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &c.chatgpt
	s.mu.Lock()
	done := make(chan struct{})
	s.attempt++
	attempt := s.attempt
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
		g, err = c.core.Hub.SignInWithChatGPT(ctx, had, func(u string) { opened <- u })
	}()
	select {
	case u := <-opened:
		return encode(chatgptBegun{URL: u, Attempt: attempt})
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

// CancelChatGPTSignIn gives up the sign-in being made, attempt as
// BeginChatGPTSignIn numbered it, as its page is closed before OpenAI
// answered. One already made stays, for the form to save: the page may
// be closed after it said so. Another sign-in begun since stays too.
func (c *Core) CancelChatGPTSignIn(attempt int64) {
	defer catch(nil)
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	if attempt != s.attempt || s.g != nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel, s.done, s.err = nil, nil, nil
}

// ForgetChatGPTSignIn gives up the sign-in being made, and forgets one
// made and not saved, as the form that began attempt closes. Another
// sign-in begun since, in another form, stays.
func (c *Core) ForgetChatGPTSignIn(attempt int64) {
	defer catch(nil)
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	if attempt == s.attempt {
		s.forget()
	}
}

// forgetChatGPTSignIn gives up the sign-in being made, and forgets one
// made, whichever attempt it is.
func (c *Core) forgetChatGPTSignIn() {
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forget()
}

// forget gives up the sign-in being made and forgets one made; s.mu is
// held.
func (s *chatgptSignIn) forget() {
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
