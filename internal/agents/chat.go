package agents

import (
	"encoding/json"
	"log"
	"slices"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

type Message struct {
	Mine      bool   `json:"Mine"`
	Text      string `json:"Text"`
	RunID     string `json:"RunID"`
	Pending   bool   `json:"Pending"`
	Error     bool   `json:"Error"`
	AgentName string `json:"AgentName"`
	Streamed  bool   `json:"-"`
}

type Session struct {
	PageID         string
	Messages       []*Message
	Runs           []*Run
	ConversationID string
	Running        bool
	Selected       *store.ModelChoice
	Past           []*Conversation
}

func (s *Session) Run(id string) *Run {
	for _, r := range s.Runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (s *Session) Latest() *Run {
	for i := len(s.Runs) - 1; i >= 0; i-- {
		if r := s.Runs[i]; len(r.Ops) > 0 && !r.Restored {
			return r
		}
	}
	return nil
}

type Conversation struct {
	ID       string     `json:"conversation_id"`
	Messages []*Message `json:"messages"`
	Runs     []*Run     `json:"runs,omitempty"`
	At       time.Time  `json:"at,omitzero"`
}

func (c *Conversation) Title() string {
	for _, m := range c.Messages {
		if m.Mine {
			line, _, _ := strings.Cut(m.Text, "\n")
			return short(strings.TrimSpace(line))
		}
	}
	return ""
}

func (c *Conversation) restore() {
	for _, m := range c.Messages {
		if m.Pending {
			m.Pending, m.Error, m.Text = false, true, locales.AgentAppClosed()
		}
	}
	for _, r := range c.Runs {
		r.Restored = true
		if r.Status != "done" && r.Status != "failed" {
			r.Status = "failed"
		}
	}
}

const maxSavedMessages = 200

const maxPastConversations = 50

func chatKey(pageID string) string { return "chat." + pageID }

func pastChatsKey(pageID string) string { return "chat.past." + pageID }

func runningKey(pageID string) string { return "chat.running." + pageID }

func (h *Hub) loadChat(s *Session) {
	if raw, ok := h.st.Value(pastChatsKey(s.PageID)); ok {
		if err := json.Unmarshal([]byte(raw), &s.Past); err != nil {
			log.Printf("agent chat: %v", err)
		}
		for _, c := range s.Past {
			c.restore()
		}
	}
	if raw, ok := h.st.Value(chatKey(s.PageID)); ok {
		var saved Conversation
		if err := json.Unmarshal([]byte(raw), &saved); err != nil {
			log.Printf("agent chat: %v", err)
		} else {
			saved.restore()
			if saved.ID != "" {
				s.ConversationID = saved.ID
			}
			s.Messages, s.Runs = saved.Messages, saved.Runs
		}
	}
	raw, ok := h.st.Value(runningKey(s.PageID))
	if !ok {
		return
	}
	var run Run
	if err := json.Unmarshal([]byte(raw), &run); err != nil {
		log.Printf("agent chat: %v", err)
	} else if r := s.Run(run.ID); r != nil && len(run.Ops) > len(r.Ops) {
		r.Ops, r.Edits, r.Skipped = run.Ops, run.Edits, run.Skipped
		h.saveChat(s)
	}
	if err := h.st.DeleteValue(runningKey(s.PageID)); err != nil {
		log.Printf("agent chat: %v", err)
	}
}

func (s *Session) current() *Conversation {
	msgs := s.Messages
	if len(msgs) > maxSavedMessages {
		msgs = msgs[len(msgs)-maxSavedMessages:]
	}
	c := &Conversation{ID: s.ConversationID, Messages: msgs}
	for _, m := range msgs {
		if r := s.Run(m.RunID); r != nil {
			c.Runs = append(c.Runs, r)
		}
	}
	return c
}

func (h *Hub) saveChat(s *Session) {
	if len(s.Messages) == 0 {
		if err := h.st.DeleteValue(chatKey(s.PageID)); err != nil {
			log.Printf("agent chat: %v", err)
		}
		return
	}
	raw, err := json.Marshal(s.current())
	if err == nil {
		err = h.st.SetValue(chatKey(s.PageID), string(raw))
	}
	if err != nil {
		log.Printf("agent chat: %v", err)
	}
}

func (h *Hub) saveRunning(s *Session, run *Run) {
	raw, err := json.Marshal(run)
	if err == nil {
		err = h.st.SetValue(runningKey(s.PageID), string(raw))
	}
	if err != nil {
		log.Printf("agent chat: %v", err)
	}
}

func (h *Hub) savePast(s *Session) {
	var err error
	if len(s.Past) == 0 {
		err = h.st.DeleteValue(pastChatsKey(s.PageID))
	} else {
		var raw []byte
		if raw, err = json.Marshal(s.Past); err == nil {
			err = h.st.SetValue(pastChatsKey(s.PageID), string(raw))
		}
	}
	if err != nil {
		log.Printf("agent chat: %v", err)
	}
}

func (h *Hub) leave(s *Session) {
	if len(s.Messages) == 0 {
		return
	}
	c := s.current()
	c.At = time.Now()
	s.Past = append([]*Conversation{c}, s.Past...)
	if len(s.Past) > maxPastConversations {
		s.Past = s.Past[:maxPastConversations]
	}
	h.savePast(s)
}

func (h *Hub) NewConversation(pageID string) {
	s := h.Session(pageID)
	if s.Running {
		return
	}
	h.leave(s)
	s.Messages = nil
	s.ConversationID = doc.NewRowID()
	h.saveChat(s)
}

func (h *Hub) OpenConversation(pageID, id string) {
	s := h.Session(pageID)
	if s.Running {
		return
	}
	i := slices.IndexFunc(s.Past, func(c *Conversation) bool { return c.ID == id })
	if i < 0 {
		return
	}
	c := s.Past[i]
	s.Past = slices.Delete(s.Past, i, i+1)
	h.leave(s)
	s.Messages, s.ConversationID = c.Messages, c.ID
	for _, r := range c.Runs {
		if s.Run(r.ID) == nil {
			s.Runs = append(s.Runs, r)
		}
	}
	h.savePast(s)
	h.saveChat(s)
}

func (h *Hub) Chat(pageID, text string, focus []string) {
	s := h.Session(pageID)
	t := strings.TrimSpace(text)
	if t == "" || s.Running {
		return
	}
	history := chatHistory(s)
	s.Messages = append(s.Messages, &Message{Mine: true, Text: t})
	reply := &Message{Text: locales.AgentReadingPage(), Pending: true}
	s.Messages = append(s.Messages, reply)
	h.Start(pageID, RunOptions{Surface: "sidebar", Instruction: t, Focus: focus, History: history, KeptToPage: keptToPage(s), ReadOthers: readOthers(s), Reply: reply})
	h.saveChat(s)
}

func chatHistory(s *Session) []Turn {
	msgs := s.Messages
	kept := func(m *Message) bool {
		if m.Pending || m.Error {
			return false
		}
		r := s.Run(m.RunID)
		return r == nil || (!r.Undone && r.Status != "failed")
	}
	onPage := func(m *Message) bool {
		r := s.Run(m.RunID)
		return r != nil && r.KeptToPage
	}
	guarded := func(m *Message) bool {
		r := s.Run(m.RunID)
		return r == nil || r.Guarded
	}
	var turns []Turn
	for i, m := range msgs {
		if m.Mine {
			if i+1 < len(msgs) && !msgs[i+1].Mine && kept(msgs[i+1]) {
				turns = append(turns, Turn{Role: "user", Content: m.Text, KeptToPage: onPage(msgs[i+1]), Guarded: guarded(msgs[i+1])})
			}
			continue
		}
		if kept(m) {
			turns = append(turns, Turn{Role: "assistant", Content: m.Text, KeptToPage: onPage(m), Guarded: guarded(m)})
		}
	}
	if len(turns) > 20 {
		turns = turns[len(turns)-20:]
	}
	return turns
}

func keptToPage(s *Session) bool {
	for _, m := range s.Messages {
		if m.Mine || m.Pending || m.Error {
			continue
		}
		r := s.Run(m.RunID)
		if r == nil || !r.KeptToPage && !r.Undone && r.Status != "failed" {
			return false
		}
	}
	return true
}

func readOthers(s *Session) bool {
	for _, m := range s.Messages {
		if m.Mine || m.Pending || m.Error {
			continue
		}
		if r := s.Run(m.RunID); r == nil || r.ReadOthers && !r.Undone && r.Status != "failed" {
			return true
		}
	}
	return false
}

func (h *Hub) saveRun(s *Session, runID string) {
	for _, c := range s.Past {
		if slices.ContainsFunc(c.Runs, func(r *Run) bool { return r.ID == runID }) {
			h.savePast(s)
			return
		}
	}
	h.saveChat(s)
}
