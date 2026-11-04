package mobile

import (
	"encoding/json"
	"errors"
	"strings"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Agents run in the core, as on the desktop: the app asks, and reads the
// page's agent state again on each AgentsChanged.

// agentState is a page's agent state, as AgentState returns it.
type agentState struct {
	Messages []agentMessage `json:"messages"`
	Runs     []agentRun     `json:"runs"`
	Running  bool           `json:"running"`
	// Options are the agents to pick from; Available says one can run.
	Options   []agentOption `json:"options"`
	Available bool          `json:"available"`
	// Current is the agent the chat asks: the one picked for the page,
	// else the default; nil while none can run.
	Current *agentChoice `json:"current,omitempty"`
	// DefaultAgent is the agent asked where none is named, as Settings
	// sets it; nil while none can run.
	DefaultAgent *agentChoice `json:"default_agent,omitempty"`
}

// agentChoice is an agent and the model it runs, as the chat's picker
// has them.
type agentChoice struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

type agentMessage struct {
	Mine      bool   `json:"mine"`
	Text      string `json:"text"`
	RunID     string `json:"run_id,omitempty"`
	Pending   bool   `json:"pending"`
	Error     bool   `json:"error"`
	AgentName string `json:"agent_name,omitempty"`
	// Streamed is a pending reply whose text is the answer as far as it
	// has come, not what the run is doing.
	Streamed bool `json:"streamed"`
}

type agentRun struct {
	ID          string   `json:"id"`
	Surface     string   `json:"surface"`
	AgentName   string   `json:"agent_name"`
	Instruction string   `json:"instruction"`
	Status      string   `json:"status"`
	Undone      bool     `json:"undone"`
	Error       string   `json:"error,omitempty"`
	Edits       []string `json:"edits"`
	BlockIDs    []string `json:"block_ids"`
	// Summary says what the run did, in words for the user.
	Summary   string `json:"summary"`
	Stoppable bool   `json:"stoppable"`
	// Answer is the model's answer as far as it has come, while the run
	// works and has not edited the page.
	Answer string `json:"answer,omitempty"`
	// Activity is what the run is doing while a call takes a while, as
	// searching the web.
	Activity string `json:"activity,omitempty"`
	// Settled is a run whose Undo no longer shows outside the chat, as
	// the desktop's run bar hides it: a version restored since put the
	// page back to before or after it.
	Settled bool `json:"settled"`
}

type agentOption struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
	Name       string `json:"name"`
	Label      string `json:"label"`
	Local      bool   `json:"local"`
}

// AgentState returns a page's agent state: its chat, its runs, and the
// agents to pick from, as JSON.
func (c *Core) AgentState(pageID string) (_ string, err error) {
	defer catch(&err)
	var out agentState
	c.q.do(func() {
		s := c.core.Hub.Session(pageID)
		out.Messages = []agentMessage{}
		out.Runs = []agentRun{}
		out.Running = s.Running
		for _, m := range s.Messages {
			out.Messages = append(out.Messages, agentMessage{
				Mine: m.Mine, Text: m.Text, RunID: m.RunID, Pending: m.Pending, Error: m.Error, AgentName: m.AgentName,
				Streamed: m.Streamed,
			})
		}
		for _, r := range s.Runs {
			edits := make([]string, 0, len(r.Edits))
			for _, e := range r.Edits {
				edits = append(edits, e.Summary)
			}
			out.Runs = append(out.Runs, agentRun{
				ID: r.ID, Surface: r.Surface, AgentName: r.AgentName, Instruction: r.Instruction, Status: r.Status,
				Undone: r.Undone, Error: r.Error, Edits: edits, BlockIDs: r.BlockIDs(), Summary: agents.Describe(r),
				Stoppable: r.Stoppable(), Answer: r.Answer, Activity: r.Activity, Settled: c.settled[pageID] == r.ID,
			})
		}
		out.Options = c.options()
		out.Available = c.core.Hub.Available()
		if o, err := c.core.Hub.Resolve(nil); err == nil {
			out.DefaultAgent = &agentChoice{ProviderID: o.Provider.ID, Model: o.Model}
		}
		// The one picked shows as picked even while it can't run, as
		// offline.
		if o, err := c.core.Hub.Resolve(s.Selected); err == nil {
			out.Current = &agentChoice{ProviderID: o.Provider.ID, Model: o.Model}
		} else if s.Selected != nil {
			out.Current = &agentChoice{ProviderID: s.Selected.ProviderID, Model: s.Selected.Model}
		}
	})
	return encode(out)
}

func (c *Core) options() []agentOption {
	out := []agentOption{}
	for _, o := range c.core.Hub.Options() {
		out = append(out, agentOption{
			ProviderID: o.Provider.ID, Model: o.Model, Name: o.Name(), Label: o.Label(), Local: o.Local(),
		})
	}
	return out
}

// SelectAgent picks the agent and model the page's chat asks, as the
// desktop panel's picker does.
func (c *Core) SelectAgent(pageID, providerID, model string) {
	defer catch(nil)
	c.q.do(func() {
		c.core.Hub.Session(pageID).Selected = &store.ModelChoice{ProviderID: providerID, Model: model}
	})
	c.ev.agentsChanged(pageID)
}

// Chat sends a message to the page's agent chat, about the blocks in
// focus, a JSON array of IDs.
func (c *Core) Chat(pageID, text, focus string) {
	defer catch(nil)
	var ids []string
	_ = json.Unmarshal([]byte(focus), &ids)
	c.q.do(func() { c.core.Hub.Chat(pageID, text, ids) })
}

// Ask starts an agent run from the editor: surface is "inline" or
// "selection", focus a JSON array of block IDs, and agent the provider
// and model to run, or "" for the default. It returns the run's ID, or why
// it could not start, as when another run of the page's is working or no
// agent can run: such a run is never one of the page's runs.
func (c *Core) Ask(pageID, surface, instruction, focus, selection, providerID, model string) (_ string, err error) {
	defer catch(&err)
	var ids []string
	_ = json.Unmarshal([]byte(focus), &ids)
	var id string
	c.q.do(func() {
		var agent *store.ModelChoice
		if providerID != "" {
			agent = &store.ModelChoice{ProviderID: providerID, Model: model}
		}
		run := c.core.Hub.Start(pageID, agents.RunOptions{
			Surface: surface, Instruction: instruction, Focus: ids, Selection: selection, Agent: agent,
		})
		// The run reports from its goroutine through the queue, after this:
		// failed now, it failed to start.
		if run.Status == "failed" {
			err = errors.New(run.Error)
			return
		}
		id = run.ID
	})
	return id, err
}

// StopAgent stops a page's run that is still working.
func (c *Core) StopAgent(pageID, runID string) {
	defer catch(nil)
	c.q.do(func() { c.core.Hub.Stop(pageID, runID) })
}

// UndoRun takes a run's edits back off the page, as one step. Edits it
// can't take back, as later ones changed their blocks, leave the run as it
// was, and it fails saying why.
func (c *Core) UndoRun(pageID, runID string) (err error) {
	defer catch(&err)
	agent := ""
	c.q.do(func() {
		if err = c.core.Hub.UndoRun(pageID, runID); err != nil {
			if run := c.core.Hub.Session(pageID).Run(runID); run != nil {
				agent = run.AgentName
			}
		}
	})
	if err != nil {
		return errors.New(locales.PageUndoRunFailed(agent, err.Error()))
	}
	return nil
}

// ReplyBlocks returns a run's answer in words as the blocks of its
// Markdown, a JSON array of nodes, for the page's editor to put after the
// block it was asked about, as the desktop's Insert does, as one step to
// undo: of a run that read beyond the page or the web, its pictures as
// links to them (agents.ReplyBlocks). A run that edited the page, or has
// no answer, gives none.
func (c *Core) ReplyBlocks(pageID, runID string) (_ string, err error) {
	defer catch(&err)
	nodes := []*doc.Node{}
	c.q.do(func() {
		run := c.core.Hub.Session(pageID).Run(runID)
		if run == nil || run.Error != "" || run.Reply == "" || len(run.Ops) > 0 {
			return
		}
		nodes = agents.ReplyBlocks(run.Reply, run)
	})
	return encode(nodes)
}

// RedoRun puts an undone run's edits back. Edits it can't put back leave
// the run undone, and it fails saying why.
func (c *Core) RedoRun(pageID, runID string) (err error) {
	defer catch(&err)
	agent := ""
	c.q.do(func() {
		if err = c.core.Hub.RedoRun(pageID, runID); err != nil {
			if run := c.core.Hub.Session(pageID).Run(runID); run != nil {
				agent = run.AgentName
			}
		}
	})
	if err != nil {
		return errors.New(locales.PageRedoRunFailed(agent, err.Error()))
	}
	return nil
}

// NewConversation starts the page's agent chat over.
func (c *Core) NewConversation(pageID string) {
	defer catch(nil)
	c.q.do(func() { c.core.Hub.NewConversation(pageID) })
}

// Comments.

// commentJSON is a comment, as Threads gives it.
type commentJSON struct {
	ID         string `json:"id"`
	BlockID    string `json:"block_id"`
	AuthorKind string `json:"author_kind"`
	AuthorName string `json:"author_name"`
	Body       string `json:"body"`
	Quote      string `json:"quote,omitempty"`
	Resolved   bool   `json:"resolved"`
	// CreatedAt is in milliseconds since 1970.
	CreatedAt int64 `json:"created_at"`
	// Deleted is a first comment deleted, kept for the replies under it:
	// its words are gone, as the desktop says "Comment deleted".
	Deleted bool `json:"deleted,omitempty"`
	// MayDelete is whether the user may delete it, as the server lets
	// them: their own, or a guest's on a page of theirs.
	MayDelete bool `json:"may_delete"`
}

type threadJSON struct {
	Root    commentJSON   `json:"root"`
	Replies []commentJSON `json:"replies"`
	// MayResolve is whether the user may resolve or reopen the thread, as
	// the server lets them: its author, or someone who may edit the page.
	// Another's change would never go up.
	MayResolve bool `json:"may_resolve"`
}

// commentOf is m as Threads lists it to uid, on a page of theirs or not.
func commentOf(m store.Comment, uid string, owns bool) commentJSON {
	j := commentJSON{
		ID: m.ID, BlockID: m.BlockID, AuthorKind: m.AuthorKind, AuthorName: m.AuthorName, Body: m.Body,
		Quote: m.Quote, Resolved: m.Resolved, CreatedAt: millis(m.CreatedAt),
	}
	if !m.DeletedAt.IsZero() {
		j.Deleted, j.Body, j.Quote = true, "", ""
		return j
	}
	j.MayDelete = mine(m, uid) || m.AuthorKind == "guest" && owns
	return j
}

// mine reports whether a comment is uid's own.
func mine(m store.Comment, uid string) bool {
	return m.AuthorKind == "user" && (m.AuthorID == "" || m.AuthorID == uid)
}

// Threads returns a page's comment threads, as JSON.
func (c *Core) Threads(pageID string) (_ string, err error) {
	defer catch(&err)
	var ts []store.Thread
	var uid string
	var owns, canEdit bool
	c.q.do(func() {
		if ts, err = c.core.Store.Threads(pageID); err != nil {
			return
		}
		sess := c.core.Store.Session()
		uid = sess.UserID
		p, perr := c.core.Store.Page(pageID)
		owns = perr == nil && p.SpaceID == sess.SpaceID
		canEdit = c.canEdit(pageID)
	})
	if err != nil {
		return "", err
	}
	out := make([]threadJSON, 0, len(ts))
	for _, t := range ts {
		replies := make([]commentJSON, 0, len(t.Replies))
		for _, r := range t.Replies {
			replies = append(replies, commentOf(r, uid, owns))
		}
		out = append(out, threadJSON{Root: commentOf(t.Root, uid, owns), Replies: replies, MayResolve: canEdit || mine(t.Root, uid)})
	}
	return encode(out)
}

// AddComment adds a comment on a block, quoting the text it is on, or a
// reply to the thread of parentID. A comment that mentions @agent, or an
// agent by name, has that agent answer in the thread, as on the desktop.
func (c *Core) AddComment(pageID, blockID, body, parentID, quote string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		var m store.Comment
		m, err = c.core.Store.AddComment(pageID, blockID, body, parentID, quote, "", "")
		if err != nil {
			return
		}
		root := m
		if parentID != "" {
			if r, e := c.core.Store.Comment(parentID); e == nil {
				root = r
			}
		}
		c.agentInComment(pageID, root, body)
		c.core.Sync.KickPush()
	})
	return err
}

// agentInComment runs the agent a comment mentions on its thread's block,
// where the comment shows it marked, as the desktop does: one named, or
// the default for @agent. A run that fails says why in a notice, as the
// desktop's toast.
func (c *Core) agentInComment(pageID string, root store.Comment, body string) {
	agent, mentioned, err := core.CommentAgent(c.core.Hub, body)
	if err != nil {
		c.ev.notice(err.Error())
		return
	}
	if !mentioned {
		return
	}
	c.core.Hub.Start(pageID, agents.RunOptions{
		Surface: "comment", Instruction: body, Focus: []string{root.BlockID}, Selection: root.Quote,
		CommentID: root.ID, Agent: &agent,
		Done: func(r *agents.Run) {
			if r.Error != "" {
				c.ev.notice(r.AgentName + ": " + r.Error)
			}
		},
	})
}

// ResolveThread resolves a thread, or opens it again.
func (c *Core) ResolveThread(rootID string, resolved bool) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.Store.SetResolved(rootID, resolved) })
	return err
}

// DeleteComment deletes a comment.
func (c *Core) DeleteComment(id string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.Store.DeleteComment(id) })
	return err
}

// Providers.

type presetJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	BaseURL     string   `json:"base_url"`
	Auth        string   `json:"auth"`
	Group       string   `json:"group"`
	Local       bool     `json:"local"`
	Suggested   []string `json:"suggested"`
	Placeholder string   `json:"placeholder"`
}

type providerJSON struct {
	ID           string   `json:"id"`
	Preset       string   `json:"preset"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url"`
	AuthKind     string   `json:"auth_kind"`
	DefaultModel string   `json:"default_model"`
	IsLocal      bool     `json:"is_local"`
	Models       []string `json:"models"`
	// HasKey says a local provider's API key is kept on this device.
	HasKey bool `json:"has_key"`
	// APIKey is the key to keep, as SaveProvider takes it.
	APIKey string `json:"api_key,omitempty"`
	// SupportsTools is what a test found, kept with a local provider.
	SupportsTools *bool `json:"supports_tools,omitempty"`
	// Email is who a ChatGPT provider signed in as.
	Email string `json:"email,omitempty"`
}

// providerForm is a provider as SaveProvider takes it, as the core tests
// and saves one: with the key kept here of one edited, which the app's
// form never shows.
func providerForm(in providerJSON) agents.Form {
	key := strings.TrimSpace(in.APIKey)
	if key == "" && in.ID != "" {
		key = agents.LocalKey(in.ID)
	}
	return agents.Form{
		ID: in.ID, Preset: in.Preset, Name: in.Name, BaseURL: in.BaseURL, APIKey: key, Model: in.DefaultModel,
		Models: in.Models, SupportsTools: in.SupportsTools,
	}
}

// providerTest is what TestProvider found.
type providerTest struct {
	OK            bool     `json:"ok"`
	SupportsTools bool     `json:"supports_tools"`
	Message       string   `json:"message"`
	Models        []string `json:"models"`
}

// TestProvider tries a provider, given as SaveProvider takes it, and
// returns what it found, as JSON: whether its model answers and calls
// tools, and the models it serves, chat models first. A key typed in is
// tried from this device, before it goes to the server; a saved cloud
// provider is tried by the server, which keeps its key; as on the
// desktop.
func (c *Core) TestProvider(provider string) (_ string, err error) {
	defer catch(&err)
	var in providerJSON
	if err := json.Unmarshal([]byte(provider), &in); err != nil {
		return "", err
	}
	f := providerForm(in)
	if agents.PresetByID(in.Preset).Auth == "account" {
		if f.ChatGPT, err = c.chatgptFor(in.ID); err != nil {
			return encode(providerTest{Message: err.Error(), Models: []string{}})
		}
	}
	res := c.core.Hub.TestProvider(f)
	return encode(providerTest{OK: res.OK, SupportsTools: res.SupportsTools, Message: res.Message, Models: res.Models})
}

// Presets returns the providers to add, as JSON, as the desktop offers
// them.
func (c *Core) Presets() (_ string, err error) {
	defer catch(&err)
	out := []presetJSON{}
	for _, p := range agents.Presets {
		out = append(out, presetJSON{
			ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, Auth: p.Auth, Group: p.Group, Local: p.Local,
			Suggested: p.Suggested, Placeholder: p.Placeholder,
		})
	}
	return encode(out)
}

// Providers returns the agent providers, as JSON.
func (c *Core) Providers() (_ string, err error) {
	defer catch(&err)
	var ps []store.Provider
	c.q.do(func() { ps, err = c.core.Store.Providers() })
	if err != nil {
		return "", err
	}
	out := make([]providerJSON, 0, len(ps))
	for _, p := range ps {
		row := providerJSON{
			ID: p.ID, Preset: p.Preset, Name: p.Name, BaseURL: p.BaseURL, AuthKind: p.AuthKind,
			DefaultModel: p.DefaultModel, IsLocal: p.IsLocal, Models: p.Models, HasKey: agents.LocalKey(p.ID) != "",
			SupportsTools: p.SupportsTools,
		}
		if p.AuthKind == "account" {
			if g, err := c.core.Hub.ChatGPT(p.ID); err == nil {
				row.Email = g.Account().Email
			}
		}
		out = append(out, row)
	}
	return encode(out)
}

// SaveProvider adds or changes a provider, given as JSON: a local one is
// kept on this device with its key in the keychain, and so is a cloud one
// while signed out; signed in, a cloud one is kept on the server, which
// keeps its key; as on the desktop. The first provider is the default
// agent.
func (c *Core) SaveProvider(provider string) (err error) {
	defer catch(&err)
	var in providerJSON
	if err := json.Unmarshal([]byte(provider), &in); err != nil {
		return err
	}
	f := providerForm(in)
	account := agents.PresetByID(in.Preset).Auth == "account"
	if account {
		// One edited without signing in again keeps the sign-in it has.
		f.ChatGPT = c.madeChatGPT()
	}
	p, err := c.core.Hub.SaveProvider(f)
	if err != nil {
		return err
	}
	c.q.do(func() { c.defaultAgent(p.ID) })
	if account {
		c.ForgetChatGPTSignIn()
	}
	return nil
}

// defaultAgent makes a provider the default agent while there is none.
func (c *Core) defaultAgent(providerID string) {
	s := c.core.Store.Settings()
	if s.DefaultModel == "" {
		s.DefaultModel = store.ModelChoice{ProviderID: providerID}.Encode()
		_ = c.core.Store.SaveSettings(s)
	}
}

// RemoveProvider removes a provider, with its key or sign-in, as the
// desktop does: one kept on the server goes there first, since removed
// only here, sync would bring it back. The default agent it was stops
// being one.
func (c *Core) RemoveProvider(id string) (err error) {
	defer catch(&err)
	return c.core.Hub.RemoveProvider(id)
}
