package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// Host is what the hub needs from the app. Its methods run on the main
// thread, except Update, which may be called from anywhere.
type Host interface {
	// Update runs fn on the main thread.
	Update(fn func())
	// Editor returns a page's open editor, or nil.
	Editor(pageID string) *editor.Editor
	// SaveNow stores the open editor's pending edits.
	SaveNow(pageID string)
	Online() bool
	Settings() store.Settings
	FlushPage(ctx context.Context, pageID string) error
	SyncNow()
	// Tint marks the blocks the latest run of a page changed.
	Tint(pageID string, ids []string)
}

// Option is an agent people can pick: a provider and a model.
type Option struct {
	Provider store.Provider
	Model    string
}

// Name is the agent's name, its provider's.
func (o Option) Name() string { return o.Provider.Name }

// Local reports whether the model runs on this device or the network.
func (o Option) Local() bool { return o.Provider.IsLocal }

// Offline reports whether the agent works without a connection: a model
// server on this machine or the network, not a signed-in plan.
func (o Option) Offline() bool { return o.Provider.IsLocal && o.Provider.AuthKind != "account" }

// Choice is the option as a setting.
func (o Option) Choice() store.ModelChoice {
	return store.ModelChoice{ProviderID: o.Provider.ID, Model: o.Model}
}

// Label names the agent and its model.
func (o Option) Label() string { return o.Provider.Name + " · " + o.Model }

// Message is a turn of the sidebar chat.
type Message struct {
	Mine      bool
	Text      string
	RunID     string
	Pending   bool
	Error     bool
	AgentName string
}

// Run is one agent run: one undo step, kept with its ops so it can be
// undone as a whole later.
type Run struct {
	ID          string
	Surface     string
	AgentName   string
	Instruction string
	Ops         []doc.Op
	Edits       []Edit
	Reply       string
	Status      string // running | done | failed
	Undone      bool
	Error       string
	At          time.Time
	// Restored is a run of an earlier launch, kept with its chat reply.
	Restored bool `json:"-"`
}

// BlockIDs are the blocks the run touched.
func (r *Run) BlockIDs() []string {
	var out []string
	for _, e := range r.Edits {
		out = append(out, e.BlockIDs...)
	}
	return out
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
			m.Pending, m.Error, m.Text = false, true, "The app closed before the agent finished."
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

// loadChat reads a page's chat and its earlier ones from an earlier launch
// into s.
func (h *Hub) loadChat(s *Session) {
	if raw, ok := h.st.Value(pastChatsKey(s.PageID)); ok {
		if err := json.Unmarshal([]byte(raw), &s.Past); err != nil {
			log.Printf("agent chat: %v", err)
		}
		for _, c := range s.Past {
			c.restore()
		}
	}
	raw, ok := h.st.Value(chatKey(s.PageID))
	if !ok {
		return
	}
	var saved Conversation
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		log.Printf("agent chat: %v", err)
		return
	}
	saved.restore()
	if saved.ID != "" {
		s.ConversationID = saved.ID
	}
	s.Messages, s.Runs = saved.Messages, saved.Runs
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

// Hub runs agents for every page.
type Hub struct {
	st       *store.Store
	be       *remote.Client
	host     Host
	sessions map[string]*Session
}

// NewHub returns the hub.
func NewHub(st *store.Store, be *remote.Client, host Host) *Hub {
	return &Hub{st: st, be: be, host: host, sessions: map[string]*Session{}}
}

// Session returns a page's agent state.
func (h *Hub) Session(pageID string) *Session {
	s := h.sessions[pageID]
	if s == nil {
		s = &Session{PageID: pageID, ConversationID: doc.NewRowID()}
		h.loadChat(s)
		h.sessions[pageID] = s
	}
	return s
}

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
	ps, _ := h.st.Providers()
	var out []Option
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

// ErrNoAgent is returned when no model can run.
type ErrNoAgent struct{ Message string }

func (e *ErrNoAgent) Error() string { return e.Message }

// Resolve picks the agent to use: the one asked for, else the default,
// switching to a local model while offline when the user allows it.
func (h *Hub) Resolve(wanted *store.ModelChoice) (Option, error) {
	all := h.Options()
	if len(all) == 0 {
		return Option{}, &ErrNoAgent{"Add an agent provider in Settings › Agent providers to use agents."}
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
		case offline != nil && (settings.LocalWhenOffline || (needsServer && !signedIn)):
			option = *offline
		case needsServer && !signedIn:
			return Option{}, &ErrNoAgent{"Sign in to use " + option.Name() + ", or add a provider on this machine."}
		default:
			return Option{}, &ErrNoAgent{"Agents need a connection. You can keep editing; agents return when you are back online."}
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
		if errors.Is(err, mygo.ErrSecretNotFound) {
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

// ChatGPT returns the ChatGPT sign-in of a provider, kept in the keychain.
func (h *Hub) ChatGPT(providerID string) (*ChatGPT, error) {
	chatgptMu.Lock()
	defer chatgptMu.Unlock()
	if g := chatgptClients[providerID]; g != nil {
		return g, nil
	}
	raw, err := keychain.Get("chatgpt." + providerID)
	if err != nil {
		return nil, errors.New("Sign in with ChatGPT again in Settings › Agent providers.") //nolint:staticcheck // shown to people as a sentence.
	}
	var acct ChatGPTAccount
	if err := json.Unmarshal(raw, &acct); err != nil {
		return nil, err
	}
	g := NewChatGPT(acct)
	g.OnRefresh = func(a ChatGPTAccount) {
		if err := SaveChatGPT(providerID, a); err != nil {
			log.Printf("keep ChatGPT sign-in: %v", err)
		}
	}
	chatgptClients[providerID] = g
	return g, nil
}

// UseChatGPT keeps a new ChatGPT sign-in for a provider.
func UseChatGPT(providerID string, g *ChatGPT) error {
	g.OnRefresh = func(a ChatGPTAccount) {
		if err := SaveChatGPT(providerID, a); err != nil {
			log.Printf("keep ChatGPT sign-in: %v", err)
		}
	}
	chatgptMu.Lock()
	chatgptClients[providerID] = g
	chatgptMu.Unlock()
	return SaveChatGPT(providerID, g.Account())
}

// SaveChatGPT keeps a ChatGPT sign-in in the keychain.
func SaveChatGPT(providerID string, a ChatGPTAccount) error {
	raw, _ := json.Marshal(a) // #nosec G117 -- the sign-in goes to the keychain.
	return keychain.Set("chatgpt."+providerID, raw)
}

// ForgetChatGPT signs a provider out of ChatGPT on this device.
func ForgetChatGPT(ctx context.Context, providerID string) {
	chatgptMu.Lock()
	g := chatgptClients[providerID]
	delete(chatgptClients, providerID)
	chatgptMu.Unlock()
	if g != nil {
		g.SignOut(ctx)
	}
	_ = keychain.Delete("chatgpt." + providerID)
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

// Chat sends a sidebar chat message.
func (h *Hub) Chat(pageID, text string, focus []string) {
	s := h.Session(pageID)
	t := strings.TrimSpace(text)
	if t == "" || s.Running {
		return
	}
	s.Messages = append(s.Messages, &Message{Mine: true, Text: t})
	reply := &Message{Text: "Reading the page…", Pending: true}
	s.Messages = append(s.Messages, reply)
	history := chatHistory(s.Messages)
	h.Start(pageID, RunOptions{Surface: "sidebar", Instruction: t, Focus: focus, History: history, Reply: reply})
	h.saveChat(s)
}

func chatHistory(msgs []*Message) []map[string]string {
	var turns []map[string]string
	for _, m := range msgs {
		if m.Pending || m.Error {
			continue
		}
		role := "assistant"
		if m.Mine {
			role = "user"
		}
		turns = append(turns, map[string]string{"role": role, "content": m.Text})
	}
	// The newest user message is the request itself.
	if n := len(turns); n > 0 && turns[n-1]["role"] == "user" {
		turns = turns[:n-1]
	}
	if len(turns) > 20 {
		turns = turns[len(turns)-20:]
	}
	return turns
}

// RunOptions describe a run.
type RunOptions struct {
	Surface     string
	Instruction string
	Focus       []string
	Selection   string
	CommentID   string
	Agent       *store.ModelChoice
	History     []map[string]string
	// Reply is the chat message that shows the run.
	Reply *Message
	// Done runs on the main thread when the run ends.
	Done func(*Run)
}

// ErrRunning is a run asked for on a page an agent is already working on.
var ErrRunning = errors.New("An agent is already working on this page. Try again when it is done.") //nolint:staticcheck // shown to people as a sentence.

// Start runs an agent from any surface, in the background. Problems end
// up in Run.Error; a page runs one agent at a time, so a run asked for
// while another works on it fails with ErrRunning.
func (h *Hub) Start(pageID string, o RunOptions) *Run {
	s := h.Session(pageID)
	run := &Run{ID: doc.NewRowID(), Surface: o.Surface, Instruction: o.Instruction, Status: "running", At: time.Now()}
	fail := func(err error) *Run {
		run.AgentName, run.Status, run.Error = "Agent", "failed", err.Error()
		if o.Reply != nil {
			o.Reply.Pending, o.Reply.Error, o.Reply.Text = false, true, run.Error
		}
		if o.Done != nil {
			o.Done(run)
		}
		return run
	}
	if s.Running {
		return fail(ErrRunning)
	}
	option, err := h.Resolve(firstNonNil(o.Agent, s.Selected))
	if err != nil {
		return fail(err)
	}
	h.host.SaveNow(pageID)
	ed := h.host.Editor(pageID)
	var before *doc.Content
	if ed != nil {
		before = ed.Content.Copy()
	} else if c, err := stored(h.st, pageID); err != nil {
		return fail(err)
	} else if c != nil {
		before = c
	} else {
		before = doc.Empty()
	}
	run.AgentName = option.Name()
	s.Runs = append(s.Runs, run)
	s.Running = true
	if o.Reply != nil {
		o.Reply.RunID, o.Reply.AgentName = run.ID, option.Name()
	}
	page, _ := h.st.Page(pageID)
	settings := h.host.Settings()
	req := Request{
		JobID: run.ID, PageID: pageID, PageTitle: page.DisplayTitle(), Surface: o.Surface, Instruction: o.Instruction,
		ProviderID: option.Provider.ID, Model: option.Model, AgentName: option.Name(),
		// ChatGPT's models all call tools, whatever an earlier test found.
		SupportsTools: option.Provider.SupportsTools == nil || *option.Provider.SupportsTools || option.Provider.AuthKind == "account",
		FocusBlockIDs: o.Focus, SelectionText: o.Selection, CommentID: o.CommentID,
		History: o.History, AllowCrossPage: h.crossPage(page, settings),
	}
	if o.Surface == "sidebar" {
		req.ConversationID = s.ConversationID
	}
	d := &delivery{}
	events := Events{
		Status: func(st string) { h.host.Update(func() { run.Status = st }) },
		Op: func(op doc.Op, summary string) {
			h.host.Update(func() {
				run.Ops = append(run.Ops, op)
				run.Edits = append(run.Edits, Edit{Summary: summary, BlockIDs: OpBlockIDs(op)})
				if o.Reply != nil {
					o.Reply.Text = summary + "…"
				}
				switch live := h.host.Editor(pageID); {
				case live != nil:
					d.to(live, run.Ops)
				case !option.Local():
					// The server keeps a cloud run's ops: a page opened
					// later gets them by syncing.
					d.sent = len(run.Ops)
				}
			})
		},
	}
	work := before.Copy()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var res Result
		var err error
		if option.Local() {
			var client Model
			if client, err = h.ModelFor(option.Provider); err == nil {
				res, err = RunLocal(ctx, client, req, h.toolContext(pageID, page.DisplayTitle(), option.Name(), work, settings), events)
			}
		} else {
			if err = h.host.FlushPage(ctx, pageID); err == nil {
				if err = h.insertJob(ctx, req); err == nil {
					res, err = RunCloud(ctx, h.be, run.ID, events)
				}
			}
		}
		h.host.Update(func() { h.finish(s, run, req, option, before, d, res, err, o) })
	}()
	return run
}

func firstNonNil(a, b *store.ModelChoice) *store.ModelChoice {
	if a != nil {
		return a
	}
	return b
}

func (h *Hub) insertJob(ctx context.Context, req Request) error {
	focus := req.FocusBlockIDs
	if focus == nil {
		focus = []string{}
	}
	row := map[string]any{
		"id": req.JobID, "page_id": req.PageID, "user_id": h.be.UserID(), "surface": req.Surface,
		"focus_block_ids": focus, "provider_id": req.ProviderID, "model": req.Model, "status": "queued",
		"request": req.JobRequest(),
	}
	if req.CommentID != "" {
		row["comment_id"] = req.CommentID
	}
	if req.ConversationID != "" {
		row["conversation_id"] = req.ConversationID
	}
	_, err := h.be.Insert(ctx, "ai_jobs", row)
	return err
}

// delivery is how far a run's ops have reached the page: those before
// sent are in an open editor, or were saved with it when it closed.
type delivery struct {
	sent int
	ed   *editor.Editor
}

// to applies the ops ed does not have yet, in order, so an op never comes
// before the blocks it needs. The first op an editor gets starts an undo
// step that the rest join.
func (d *delivery) to(ed *editor.Editor, ops []doc.Op) {
	for _, op := range ops[d.sent:] {
		if op.Op == "create_page" || op.Op == "delete_page" {
			continue
		}
		if err := ed.ApplyOp(op, ed != d.ed); err != nil {
			log.Printf("agent op on the open page: %v", err)
		}
		d.ed = ed
	}
	d.sent = len(ops)
}

// save applies the ops the page does not have yet to its stored content,
// which keeps whatever else changed there during the run.
func (d *delivery) save(st *store.Store, pageID string, ops []doc.Op) error {
	if d.sent == len(ops) {
		return nil
	}
	c, err := stored(st, pageID)
	if err != nil {
		return err
	}
	if c == nil {
		c = doc.Empty()
	}
	for _, op := range ops[d.sent:] {
		if op.Op == "create_page" || op.Op == "delete_page" {
			continue
		}
		if err := c.Apply(op); err != nil {
			log.Printf("agent op on a closed page: %v", err)
		}
	}
	d.sent = len(ops)
	_, _, err = st.SaveContent(pageID, c)
	return err
}

// errUnreadable is a page whose stored content cannot be read.
var errUnreadable = errors.New("The page's content could not be read, so agents leave it as it is.") //nolint:staticcheck // shown to people as a sentence.

// stored is a page's stored content, nil when it is not kept here. What
// cannot be read is an error rather than an empty page, which saving
// would put in its place.
func stored(st *store.Store, pageID string) (*doc.Content, error) {
	row, err := st.ContentRow(pageID)
	if err != nil {
		return nil, nil //nolint:nilerr // not kept on this device.
	}
	c, err := doc.Decode(row.Content)
	if err != nil {
		return nil, errUnreadable
	}
	return c, nil
}

func (h *Hub) finish(s *Session, run *Run, req Request, option Option, before *doc.Content, d *delivery, res Result, err error, o RunOptions) {
	s.Running = false
	run.Reply = res.Reply
	if len(run.Ops) == 0 && len(res.Ops) > 0 {
		run.Ops, run.Edits = res.Ops, res.Edits
	}
	if err != nil {
		run.Status, run.Error = "failed", err.Error()
	} else {
		run.Status = "done"
	}
	pageID := s.PageID
	if len(run.Ops) > 0 {
		if option.Local() {
			// The page may have opened or closed during the run: it gets
			// the ops it missed, open or stored.
			if live := h.host.Editor(pageID); live != nil {
				d.to(live, run.Ops)
			} else if err := d.save(h.st, pageID, run.Ops); err != nil {
				log.Printf("agent run on a closed page: %v", err)
			}
		}
		label := "Before " + option.Name() + ": " + short(req.Instruction)
		jobID := ""
		if option.Local() {
			jobID = run.ID
		}
		if err := h.st.AddHistory(pageID, before, "agent_run", jobID, label); err != nil {
			log.Printf("agent run history: %v", err)
		}
		h.host.Tint(pageID, run.BlockIDs())
	}
	if option.Local() {
		h.logLocalJob(run, req)
		if req.Surface == "comment" && run.Error == "" && run.Reply != "" && len(req.FocusBlockIDs) > 0 {
			if _, err := h.st.AddComment(pageID, req.FocusBlockIDs[0], run.Reply, req.CommentID, "", "agent", option.Name()); err != nil {
				run.Status, run.Error = "failed", err.Error()
			}
		}
	} else {
		h.host.SyncNow()
	}
	if o.Reply != nil {
		o.Reply.Pending = false
		o.Reply.Error = run.Error != ""
		o.Reply.Text = Describe(run)
		h.saveChat(s)
	}
	if o.Done != nil {
		o.Done(run)
	}
}

func short(s string) string {
	rs := []rune(s)
	if len(rs) > 60 {
		return string(rs[:57]) + "…"
	}
	return s
}

func (h *Hub) toolContext(pageID, title, agent string, content *doc.Content, settings store.Settings) ToolContext {
	p, err := h.st.Page(pageID)
	tc := PageTools(h.st, pageID, title, agent, content, err == nil && h.crossPage(p, settings))
	// An agent edits only what the user may.
	tc.ReadOnly = err != nil || !h.st.CanEdit(p)
	return tc
}

// crossPage reports whether an agent on p may read the user's other
// pages. Not on a page other people write in too: what they wrote could
// tell the agent to copy the user's other pages into it.
func (h *Hub) crossPage(p store.Page, settings store.Settings) bool {
	return settings.AgentsReadOtherPages && p.ID != "" && !sharedWithOthers(h.st, p)
}

// sharedWithOthers reports whether anyone but the user has a page: it is
// in someone else's space, or it or a page it is inside has members other
// than the user.
func sharedWithOthers(st *store.Store, p store.Page) bool {
	sess := st.Session()
	seen := map[string]bool{}
	for p.ID != "" && !seen[p.ID] {
		seen[p.ID] = true
		if p.SpaceID != sess.SpaceID {
			return true
		}
		for _, m := range st.MembersOrNil(p.ID) {
			if m.UserID != sess.UserID {
				return true
			}
		}
		if p.ParentID == "" {
			break
		}
		parent, err := st.Page(p.ParentID)
		if err != nil {
			break
		}
		p = parent
	}
	return false
}

func (h *Hub) logLocalJob(run *Run, req Request) {
	var edits []any
	for _, e := range run.Edits {
		edits = append(edits, map[string]any{"summary": e.Summary, "block_ids": e.BlockIDs})
	}
	status := "done"
	if run.Error != "" {
		status = "failed"
	}
	// Local providers have no server row, so the job stays on this device.
	if err := h.st.PutJob(store.Job{
		ID: run.ID, PageID: req.PageID, Surface: run.Surface, FocusBlockIDs: req.FocusBlockIDs, CommentID: req.CommentID,
		ConversationID: req.ConversationID, ProviderID: req.ProviderID, Model: req.Model, Status: status,
		Request: req.JobRequest(), Result: map[string]any{"reply": run.Reply, "ops": run.Ops, "edits": edits}, Error: run.Error,
	}); err != nil {
		log.Printf("agent job: %v", err)
	}
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

// UndoRun reverts a whole run, even after later edits elsewhere on the page.
func (h *Hub) UndoRun(pageID, runID string) {
	s := h.Session(pageID)
	run := s.Run(runID)
	if run == nil || run.Undone {
		return
	}
	h.apply(pageID, doc.InvertRun(run.Ops))
	run.Undone = true
	h.saveRun(s, runID)
	h.host.Tint(pageID, nil)
	h.markJob(runID, "reverted")
}

// RedoRun applies an undone run again.
func (h *Hub) RedoRun(pageID, runID string) {
	s := h.Session(pageID)
	run := s.Run(runID)
	if run == nil || !run.Undone {
		return
	}
	h.apply(pageID, run.Ops)
	run.Undone = false
	h.saveRun(s, runID)
	h.host.Tint(pageID, run.BlockIDs())
	h.markJob(runID, "done")
}

func (h *Hub) apply(pageID string, ops []doc.Op) {
	var blockOps []doc.Op
	for _, o := range ops {
		switch o.Op {
		case "delete_page":
			if _, err := h.st.DeletePage(o.PageID); err != nil {
				log.Printf("undo run: %v", err)
			}
		case "create_page":
			if o.Page != nil {
				if err := h.st.RestorePages([]string{o.Page.ID}); err != nil {
					log.Printf("undo run: %v", err)
				}
			}
		default:
			blockOps = append(blockOps, o)
		}
	}
	if len(blockOps) == 0 {
		return
	}
	if ed := h.host.Editor(pageID); ed != nil {
		if err := ed.ApplyOps(blockOps); err != nil {
			log.Printf("undo run: %v", err)
		}
		return
	}
	c, err := stored(h.st, pageID)
	if err != nil {
		log.Printf("undo run: %v", err)
		return
	}
	if c == nil {
		return
	}
	for _, o := range blockOps {
		if err := c.Apply(o); err != nil {
			log.Printf("undo run: %v", err)
		}
	}
	if _, _, err := h.st.SaveContent(pageID, c); err != nil {
		log.Printf("undo run: %v", err)
	}
}

func (h *Hub) markJob(id, status string) {
	if err := h.st.SetJobStatus(id, status); err != nil {
		log.Printf("job status: %v", err)
	}
	if h.be.SignedIn() && h.host.Online() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := h.be.Upsert(ctx, "ai_jobs", map[string]any{"id": id, "status": status}, ""); err != nil {
				log.Printf("job status: %v", err)
			}
		}()
	}
}

// Describe is how a run ended, for its reply.
// maxTranscript bounds the transcript an agent reads of one recording.
const maxTranscript = 40000

// TranscriptText is a transcript as lines of "[m:ss] text", with a line of
// only punctuation joined to the one before.
func TranscriptText(segs []store.Segment) string {
	var lines []string
	for _, s := range segs {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if strings.TrimFunc(text, func(r rune) bool { return unicode.IsPunct(r) }) == "" && len(lines) > 0 {
			lines[len(lines)-1] += text
			continue
		}
		at := s.StartMs / 1000
		line := fmt.Sprintf("[%d:%02d] ", at/60, at%60)
		if s.Speaker != "" {
			line += "Speaker " + s.Speaker + ": "
		}
		lines = append(lines, line+text)
	}
	out := strings.Join(lines, "\n")
	if r := []rune(out); len(r) > maxTranscript {
		out = string(r[:maxTranscript]) + "\n[The rest of the transcript is cut off.]"
	}
	return out
}

func Describe(r *Run) string {
	switch {
	case r.Error != "":
		return r.Error
	case r.Reply != "":
		return r.Reply
	case len(r.Edits) > 0:
		return fmt.Sprintf("%s.", r.Edits[len(r.Edits)-1].Summary)
	}
	return "The agent made no changes."
}
