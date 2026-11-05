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

// A page's sidebar chat: its conversations, kept between launches with
// the runs their replies show, and the turns of them the model reads.

// Message is a turn of the sidebar chat. The device keeps it, as a run,
// under its fields' names.
type Message struct {
	Mine      bool   `json:"Mine"`
	Text      string `json:"Text"`
	RunID     string `json:"RunID"`
	Pending   bool   `json:"Pending"`
	Error     bool   `json:"Error"`
	AgentName string `json:"AgentName"`
	// Streamed is a pending reply whose Text is the answer as far as it
	// has come, not what the run is doing.
	Streamed bool `json:"-"`
}

// Session is the agent state of one page: the sidebar conversation and
// the runs made from any surface.
type Session struct {
	PageID         string
	Messages       []*Message
	Runs           []*Run
	ConversationID string
	Running        bool
	Selected       *store.ModelChoice
	// Past are the page's earlier conversations, the latest first.
	Past []*Conversation
}

// Run returns a run by ID.
func (s *Session) Run(id string) *Run {
	for _, r := range s.Runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// Latest is the newest run of this launch that changed the page.
func (s *Session) Latest() *Run {
	for i := len(s.Runs) - 1; i >= 0; i-- {
		if r := s.Runs[i]; len(r.Ops) > 0 && !r.Restored {
			return r
		}
	}
	return nil
}

// Conversation is a page's sidebar chat as the device keeps it between
// launches, with the runs its replies show, so they can still be undone.
type Conversation struct {
	ID       string     `json:"conversation_id"`
	Messages []*Message `json:"messages"`
	Runs     []*Run     `json:"runs,omitempty"`
	// At is when an earlier conversation was left for a new one.
	At time.Time `json:"at,omitzero"`
}

// Title is the conversation's first request, on one line.
func (c *Conversation) Title() string {
	for _, m := range c.Messages {
		if m.Mine {
			line, _, _ := strings.Cut(m.Text, "\n")
			return short(strings.TrimSpace(line))
		}
	}
	return ""
}

// restore marks what an earlier launch left unfinished: a reply still
// pending never got its answer.
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

// maxSavedMessages bounds the chat kept for a page.
const maxSavedMessages = 200

// maxPastConversations bounds the earlier conversations kept for a page.
const maxPastConversations = 50

func chatKey(pageID string) string { return "chat." + pageID }

func pastChatsKey(pageID string) string { return "chat.past." + pageID }

func runningKey(pageID string) string { return "chat.running." + pageID }

// loadChat reads a page's chat and its earlier ones from an earlier launch
// into s, with the ops its run had made when that launch ended, should it
// have ended during one (saveRunning).
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
		r.Ops, r.Edits = run.Ops, run.Edits
		h.saveChat(s)
	}
	if err := h.st.DeleteValue(runningKey(s.PageID)); err != nil {
		log.Printf("agent chat: %v", err)
	}
}

// current is the page's chat as it is kept, its runs with it.
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

// saveChat keeps a page's chat for the next launch.
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

// saveRunning keeps the run working in a page's chat, with the ops it
// made so far, for the next launch to undo should the app quit before it
// ends (loadChat): the run alone, as the rest of the chat, kept as the run
// began, stays as it was while it works. Its end keeps the chat whole.
func (h *Hub) saveRunning(s *Session, run *Run) {
	raw, err := json.Marshal(run)
	if err == nil {
		err = h.st.SetValue(runningKey(s.PageID), string(raw))
	}
	if err != nil {
		log.Printf("agent chat: %v", err)
	}
}

// savePast keeps a page's earlier conversations for the next launch.
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

// leave puts the page's chat first among its earlier ones, when it has
// messages.
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

// NewConversation starts a page's sidebar chat afresh, keeping the one it
// had among the earlier ones.
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

// OpenConversation goes back to an earlier conversation of a page,
// keeping the one it had among the earlier ones.
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

// Chat sends a sidebar chat message.
func (h *Hub) Chat(pageID, text string, focus []string) {
	s := h.Session(pageID)
	t := strings.TrimSpace(text)
	if t == "" || s.Running {
		return
	}
	// The history is the turns before this one, the request itself aside.
	history := chatHistory(s)
	s.Messages = append(s.Messages, &Message{Mine: true, Text: t})
	reply := &Message{Text: locales.AgentReadingPage(), Pending: true}
	s.Messages = append(s.Messages, reply)
	h.Start(pageID, RunOptions{Surface: "sidebar", Instruction: t, Focus: focus, History: history, KeptToPage: keptToPage(s), ReadOthers: readOthers(s), Reply: reply})
	h.saveChat(s)
}

// chatHistory is the sidebar chat as the model reads it: each request
// with its reply, as the agent function builds it from the runs that are
// done. A request whose reply failed, or whose run failed or was undone,
// goes with it, neither said: the model would take the request for done,
// and the page for having what its reply says. Each turn says whether its
// run kept to the page, as keptToPage counts it, and whether its URL guard
// was on, as a reply whose run is not known is taken to have been.
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

// keptToPage reports whether the replies chatHistory gives the model came
// from runs that read nothing beyond the page: the next run may then take
// what the conversation holds for the user's own, and use the web.
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

// readOthers reports whether a run whose reply the chat holds read a page
// someone else writes or reads, judged over the whole chat, as keptToPage
// judges it: what that run read may be in the turns the next run reads,
// and in what it would make a page of the user's own of. A reply whose run
// is not known is taken for one; one whose run failed or was undone,
// whose turn the model no longer reads, is not.
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

// saveRun keeps a run's change with the conversation that shows it: the
// page's chat or an earlier one, whose run the page's bar can still undo.
func (h *Hub) saveRun(s *Session, runID string) {
	for _, c := range s.Past {
		if slices.ContainsFunc(c.Runs, func(r *Run) bool { return r.ID == runID }) {
			h.savePast(s)
			return
		}
	}
	h.saveChat(s)
}
