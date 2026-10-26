package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// Latest is the newest run that changed the page and is not undone.
func (s *Session) Latest() *Run {
	for i := len(s.Runs) - 1; i >= 0; i-- {
		if r := s.Runs[i]; len(r.Ops) > 0 {
			return r
		}
	}
	return nil
}

// NewConversation clears the sidebar chat.
func (s *Session) NewConversation() {
	s.Messages = nil
	s.ConversationID = doc.NewRowID()
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

// Start runs an agent from any surface, in the background. Problems end
// up in Run.Error.
func (h *Hub) Start(pageID string, o RunOptions) *Run {
	s := h.Session(pageID)
	option, err := h.Resolve(firstNonNil(o.Agent, s.Selected))
	run := &Run{ID: doc.NewRowID(), Surface: o.Surface, Instruction: o.Instruction, Status: "running", At: time.Now()}
	if err != nil {
		run.AgentName, run.Status, run.Error = "Agent", "failed", err.Error()
		if o.Reply != nil {
			o.Reply.Pending, o.Reply.Error, o.Reply.Text = false, true, run.Error
		}
		if o.Done != nil {
			o.Done(run)
		}
		return run
	}
	run.AgentName = option.Name()
	s.Runs = append(s.Runs, run)
	s.Running = true
	if o.Reply != nil {
		o.Reply.RunID, o.Reply.AgentName = run.ID, option.Name()
	}
	h.host.SaveNow(pageID)
	ed := h.host.Editor(pageID)
	var before *doc.Content
	if ed != nil {
		before = ed.Content.Copy()
	} else if c := h.st.Content(pageID); c != nil {
		before = c
	} else {
		before = doc.Empty()
	}
	page, _ := h.st.Page(pageID)
	settings := h.host.Settings()
	req := Request{
		JobID: run.ID, PageID: pageID, PageTitle: page.DisplayTitle(), Surface: o.Surface, Instruction: o.Instruction,
		ProviderID: option.Provider.ID, Model: option.Model, AgentName: option.Name(),
		// ChatGPT's models all call tools, whatever an earlier test found.
		SupportsTools: option.Provider.SupportsTools == nil || *option.Provider.SupportsTools || option.Provider.AuthKind == "account",
		FocusBlockIDs: o.Focus, SelectionText: o.Selection, CommentID: o.CommentID,
		History: o.History, AllowCrossPage: settings.AgentsReadOtherPages,
	}
	if o.Surface == "sidebar" {
		req.ConversationID = s.ConversationID
	}
	first := true
	events := Events{
		Status: func(st string) { h.host.Update(func() { run.Status = st }) },
		Op: func(op doc.Op, summary string) {
			h.host.Update(func() {
				run.Ops = append(run.Ops, op)
				run.Edits = append(run.Edits, Edit{Summary: summary, BlockIDs: OpBlockIDs(op)})
				if o.Reply != nil {
					o.Reply.Text = summary + "…"
				}
				if op.Op == "create_page" || op.Op == "delete_page" {
					return
				}
				if live := h.host.Editor(pageID); live != nil {
					if err := live.ApplyOp(op, first); err != nil {
						log.Printf("agent op on the open page: %v", err)
					}
					first = false
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
		h.host.Update(func() { h.finish(s, run, req, option, before, work, ed == nil, res, err, o) })
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

func (h *Hub) finish(s *Session, run *Run, req Request, option Option, before, work *doc.Content, closed bool, res Result, err error, o RunOptions) {
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
			if live := h.host.Editor(pageID); live != nil && closed {
				// The page was opened during the run: it gets the run's
				// changes as one step.
				if err := live.ApplyOps(run.Ops); err != nil {
					log.Printf("agent run on the open page: %v", err)
				}
			} else if closed {
				if _, _, err := h.st.SaveContent(pageID, work); err != nil {
					log.Printf("agent run on a closed page: %v", err)
				}
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
	return PageTools(h.st, pageID, title, agent, content, settings.AgentsReadOtherPages)
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

// UndoRun reverts a whole run, even after later edits elsewhere on the page.
func (h *Hub) UndoRun(pageID, runID string) {
	s := h.Session(pageID)
	run := s.Run(runID)
	if run == nil || run.Undone {
		return
	}
	h.apply(pageID, doc.InvertRun(run.Ops))
	run.Undone = true
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
	c := h.st.Content(pageID)
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
