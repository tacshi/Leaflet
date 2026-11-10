package mobile

import (
	"context"
	"errors"
	"sync"
	"time"

	"leaflet/internal/agents"
	"leaflet/locales"
)

type chatgptSignIn struct {
	mu      sync.Mutex
	attempt int64
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	g       *agents.ChatGPT
}

type chatgptBegun struct {
	URL     string `json:"url"`
	Attempt int64  `json:"attempt"`
}

type chatgptAccount struct {
	Email  string   `json:"email"`
	Models []string `json:"models"`
}

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
		return "", errors.New(locales.SettingsChatGPTSignInCancelled())
	}
	return encode(chatgptAccount{Email: g.Account().Email, Models: []string{}})
}

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

func (c *Core) ForgetChatGPTSignIn(attempt int64) {
	defer catch(nil)
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	if attempt == s.attempt {
		s.forget()
	}
}

func (c *Core) forgetChatGPTSignIn() {
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forget()
}

func (s *chatgptSignIn) forget() {
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel, s.done, s.err, s.g = nil, nil, nil, nil
}

func (c *Core) madeChatGPT() *agents.ChatGPT {
	s := &c.chatgpt
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.g
}

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
