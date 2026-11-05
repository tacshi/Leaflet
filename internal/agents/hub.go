package agents

import (
	"cmp"
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Host is what the hub needs from the app. Its methods run on the main
// thread, except Update, which may be called from anywhere, and
// FlushPage, which a cloud run calls from its own goroutine.
type Host interface {
	// Update runs fn on the main thread, which changes the agent state of
	// page pageID, or of every page for "".
	Update(pageID string, fn func())
	// Live returns the page open with its edits, or nil while it is not
	// open.
	Live(pageID string) LiveDoc
	// SaveNow stores the open editor's pending edits.
	SaveNow(pageID string)
	Online() bool
	Settings() store.Settings
	// FlushPage pushes a page's pending changes to the server, or all of
	// them for "".
	FlushPage(ctx context.Context, pageID string) error
	SyncNow()
	// Tint marks the blocks the latest run of a page changed.
	Tint(pageID string, ids []string)
}

// LiveDoc is a page open in the app, as an agent run changes it: the
// desktop's editor, or the iOS app's, through package live.
type LiveDoc interface {
	// Doc is the content as it is open, with what is being typed.
	Doc() *doc.Content
	// Peek is the content as Doc gives it, without the read counting as
	// what the user is composing going out with the page, to be saved:
	// content from outside after Doc's, with the same text where the
	// composition is, is taken to hold it, and the text is taken out. A
	// run reads the page with it, and gives back the composition it read
	// with its ops (ApplyOpFrom); the page an op is checked against, and
	// kept in history before it, are read with it too.
	Peek() *doc.Content
	// Composition is what the user is composing in the page, by its
	// number, 0 for none, and whether it has gone out with the page, to be
	// saved or read.
	Composition() (id uint64, out bool)
	// ApplyOp applies an op, starting a new undo step with newStep or
	// joining the one before.
	ApplyOp(op doc.Op, newStep bool) error
	// ApplyOpFrom is ApplyOp of an op written from the page as read while
	// the composition numbered read was in it: only then is the op's text
	// where the composition is taken for the composition's, which goes as
	// the change gives it up. Any other op keeps its text.
	ApplyOpFrom(op doc.Op, newStep bool, read uint64) error
	// ApplyOps applies ops as one undo step.
	ApplyOps(ops []doc.Op) error
}

// Run is one agent run: one undo step, kept with its ops so it can be
// undone as a whole later.
type Run struct {
	ID          string    `json:"ID"`
	Surface     string    `json:"Surface"`
	AgentName   string    `json:"AgentName"`
	Instruction string    `json:"Instruction"`
	Ops         []doc.Op  `json:"Ops"`
	Edits       []Edit    `json:"Edits"`
	Reply       string    `json:"Reply"`
	Status      string    `json:"Status"` // running | done | failed
	Undone      bool      `json:"Undone"`
	Error       string    `json:"Error"`
	At          time.Time `json:"At"`
	// Stopped is a run the user stopped: it keeps the edits it made
	// before, which undo as one step as a finished run's do.
	Stopped bool `json:"Stopped"`
	// Restored is a run of an earlier launch, kept with its chat reply.
	Restored bool `json:"-"`
	// Answer is the model's answer as far as it has come, while the run
	// works and has not edited the page.
	Answer string `json:"-"`
	// Activity is what the run is doing while a call takes a while, as
	// searching the web, until it answers or edits again.
	Activity string `json:"-"`
	// KeptToPage is a run that read nothing beyond its page: a turn of
	// its conversation that the next run may take for the user's.
	KeptToPage bool `json:"KeptToPage,omitempty"`
	// Guarded is a run whose URL guard was on as it ended: its reply may
	// carry what it read beyond its page or on the web (ReplyBlocks).
	Guarded bool `json:"Guarded,omitempty"`
	// ReadOthers is a run that read a page someone else writes or reads,
	// or followed turns that did, as the server's jobs say it of theirs:
	// the turns of its conversation after it make no page of the user's
	// own (readOthers).
	ReadOthers bool `json:"read_others,omitempty"`
	// Removed are what Undo took away with each page the run made: the
	// subpages that went with it and where its card was, for Redo to put
	// them all back.
	Removed map[string]RemovedPages `json:"Removed,omitempty"`
	// Left are the pages and folders the run made that the user deleted
	// since, which the latest Undo left as they were: Redo leaves them so
	// too, rather than bring back what the user deleted.
	Left []string `json:"Left,omitempty"`
	// Local is a run of this device's, all of whose changes were made
	// here: none of them is still to come with a sync (awaited).
	Local bool `json:"Local,omitempty"`
	// kept is a run whose "Before" snapshot is in the page's history.
	kept bool
	// stop cancels a run on this device while it works; a cloud run goes
	// on on the server whatever the app does, so it has none.
	stop context.CancelFunc
	// cancel ends the run's work here, a cloud run's wait for the server
	// too, as its account leaves (Forget); epoch is the hub's then.
	cancel context.CancelFunc
	epoch  int64
}

// RemovedPages are the pages deleting one took away, its subpages with it,
// the page first, and where its card was in its parent.
type RemovedPages struct {
	IDs  []string     `json:"IDs"`
	Card doc.Position `json:"Card"`
}

// Stoppable reports whether the run is working and Stop can end it.
func (r *Run) Stoppable() bool { return r.stop != nil && !r.Stopped }

// BlockIDs are the blocks the run touched.
func (r *Run) BlockIDs() []string {
	var out []string
	for _, e := range r.Edits {
		out = append(out, e.BlockIDs...)
	}
	return out
}

// Hub runs agents for every page.
type Hub struct {
	st       *store.Store
	be       *remote.Client
	host     Host
	sessions map[string]*Session
	// PublicLink reports whether a page has a public link open now, which
	// the app serves; nil is none. PublicPages are those pages, for a cloud
	// workspace run, whose leases may not have reached its server yet
	// (ServePublicLink).
	PublicLink  func(pageID string) bool
	PublicPages func() []string
	// ICloudLease and ICloudLinks say the public links devices serve in the
	// Leaflet folder of iCloud Drive, which the pages go to the other
	// devices through, signed out, while iCloud sync is on: ICloudLease
	// says so of this device's (ServePublicLink) until stopped, and
	// ICloudLinks are the pages the other devices say so of, with when
	// each lease ends, by that device's clock (publicElsewhere). Nil is
	// none.
	ICloudLease func(pageID string) (stop func())
	ICloudLinks func() map[string]time.Time
	// links are when each page's public link, which another device
	// serves, stops counting, as the server last said (publicElsewhere),
	// and leases how many leases this device took of each page's link, the
	// latest of which stands (ServePublicLink). Guarded by linksMu.
	linksMu sync.Mutex
	links   map[string]time.Time
	leases  map[string]int
	// epoch counts the accounts the hub has worked for: Forget moves it
	// on, and a run started for an earlier one writes nothing more.
	epoch atomic.Int64
	// runs is what the runs of this account work under, and endRuns ends
	// it: Forget does, there and then, as the pages go right after it,
	// before the main thread could end them one by one. Guarded by runsMu.
	runsMu  sync.Mutex
	runs    context.Context
	endRuns context.CancelFunc
	// options is Options as the providers last read made it, until the
	// store says they changed: the app asks for it as it draws, for every
	// comment and every agent menu. Guarded by optionsMu.
	optionsMu sync.Mutex
	options   []Option
}

// Forget stops every run and forgets every page's agent state, as the
// account whose pages they are leaves this device: a run that ends once
// its pages are removed would store them again, with its snapshot, its
// job and its chat, for the next account to upload. Call it before the
// pages go; any goroutine may. A run ending meanwhile writes nothing.
func (h *Hub) Forget() {
	h.epoch.Add(1)
	h.runsMu.Lock()
	if h.endRuns != nil {
		h.endRuns()
	}
	h.runs, h.endRuns = nil, nil
	h.runsMu.Unlock()
	h.host.Update("", func() {
		for _, s := range h.sessions {
			for _, run := range s.Runs {
				if run.cancel != nil {
					run.cancel()
				}
			}
		}
		h.sessions = map[string]*Session{}
	})
}

// gone reports whether a run's account has left this device since it
// started (Forget).
func (h *Hub) gone(run *Run) bool { return run.epoch != h.epoch.Load() }

// runContext is what a run of this account works under (Hub.runs).
func (h *Hub) runContext() context.Context {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	if h.runs == nil {
		h.runs, h.endRuns = context.WithCancel(context.Background())
	}
	return h.runs
}

// NewHub returns the hub.
func NewHub(st *store.Store, be *remote.Client, host Host) *Hub {
	h := &Hub{st: st, be: be, host: host, sessions: map[string]*Session{}}
	st.Subscribe(func(c store.Change) {
		if c.Table == "model_providers" || c.Table == "*" {
			h.optionsMu.Lock()
			h.options = nil
			h.optionsMu.Unlock()
		}
	})
	return h
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

// RunOptions describe a run.
type RunOptions struct {
	Surface     string
	Instruction string
	Focus       []string
	Selection   string
	CommentID   string
	Agent       *store.ModelChoice
	History     []Turn
	// KeptToPage is a History whose runs read nothing beyond the page,
	// and ReadOthers one a run of which read a page someone else writes or
	// reads.
	KeptToPage bool
	ReadOthers bool
	// Reply is the chat message that shows the run.
	Reply *Message
	// OpenPage is the page open as a workspace run is asked for, which
	// its agent is told of: "" for none.
	OpenPage string
	// Done runs on the main thread when the run ends.
	Done func(*Run)
}

// ErrRunning is a run asked for on a page an agent is already working on,
// said in the app's language.
var ErrRunning error = errRunning{}

type errRunning struct{}

func (errRunning) Error() string { return locales.AgentRunning() }

// ErrAccountLeft ends a run whose account signed out of this device before
// it finished (Forget), said in the app's language.
var ErrAccountLeft error = errAccountLeft{}

type errAccountLeft struct{}

func (errAccountLeft) Error() string { return locales.AgentAccountLeft() }

// Start runs an agent from any surface, in the background. Problems end
// up in Run.Error; a page runs one agent at a time, so a run asked for
// while another works on it fails with ErrRunning.
func (h *Hub) Start(pageID string, o RunOptions) *Run {
	s := h.Session(pageID)
	run := &Run{ID: doc.NewRowID(), Surface: o.Surface, Instruction: o.Instruction, Status: "running", At: time.Now(), epoch: h.epoch.Load()}
	fail := func(err error) *Run {
		// An agent that never resolved is named for what it is.
		run.AgentName, run.Status, run.Error = locales.CommonAgent(), "failed", err.Error()
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
	// A workspace run is of no page: it changes none's blocks, and reads
	// them as they are stored, the open page's as it is open.
	workspace := pageID == WorkspaceID
	h.host.SaveNow(pageID)
	if o.OpenPage != "" {
		h.host.SaveNow(o.OpenPage)
	}
	ed := h.host.Live(pageID)
	var before *doc.Content
	// read is what the user was composing in the page as the run read it,
	// which its ops can hold (ApplyOpFrom): a local run reads the page as
	// it is open, a cloud run as it is stored, which holds the composition
	// only once it went out with the page, as the save just before takes
	// it.
	var read uint64
	switch {
	case workspace:
		before = doc.Empty()
	case ed != nil:
		before = ed.Peek().Copy()
		if id, out := ed.Composition(); out || option.Local() {
			read = id
		}
	default:
		if before, err = stored(h.st, pageID); err != nil {
			return fail(err)
		}
	}
	run.AgentName, run.Local = option.Name(), option.Local()
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
		FocusBlockIDs: o.Focus, SelectionText: o.Selection, CommentID: o.CommentID, Thread: h.thread(pageID, o.CommentID),
		History: o.History, KeptToPage: o.KeptToPage, ReadOthers: o.ReadOthers, AllowCrossPage: h.crossPage(page, settings),
		Web: h.web(settings),
	}
	if o.Surface == "sidebar" || o.Surface == "workspace" {
		req.ConversationID = s.ConversationID
	}
	if workspace {
		// Its job is of no page, and it reads the user's pages as the user
		// lets agents, the open one among them.
		req.PageID, req.PageTitle, req.AllowCrossPage = "", "", settings.AgentsReadOtherPages
		req.OpenPage = h.openPage(o.OpenPage)
		if h.PublicPages != nil {
			req.PublicPages = h.PublicPages()
		}
	}
	d := h.newDelivery(pageID, run, option)
	d.read = read
	stream := &replyStream{h: h, pageID: pageID, run: run, reply: o.Reply}
	events := Events{
		Status: func(st string) { h.host.Update(pageID, func() { run.Status = st }) },
		Op: func(op doc.Op, summary string) {
			stream.clear()
			h.host.Update(pageID, func() { h.arrived(s, run, option, before, d, o, op, summary) })
		},
		Text: stream.set,
		Revise: func(at int, op *doc.Op, summary string, delta []doc.Op) {
			h.host.Update(pageID, func() { h.revised(s, run, d, o, at, op, summary, delta) })
		},
		Activity: func(text string) {
			stream.clear()
			h.host.Update(pageID, func() {
				run.Activity, run.Answer = text, ""
				if o.Reply != nil && o.Reply.Pending {
					o.Reply.Text, o.Reply.Streamed = text, false
				}
			})
		},
	}
	work := before.Copy()
	// Asked here, on the main thread, as the host's methods are.
	online := h.host.Online()
	ctx, cancel := context.WithTimeout(h.runContext(), 10*time.Minute)
	run.cancel = cancel
	if option.Local() {
		run.stop = cancel
	}
	go func() {
		defer guard.Recover()
		defer cancel()
		// A panic in the run fails it, as an error does: the run still
		// ends, and the page is no longer taken as working.
		res, err := func() (res Result, err error) {
			defer guard.RecoverAs(&err)
			if option.Local() {
				var client Model
				if client, err = h.ModelFor(option.Provider); err == nil {
					var tc ToolContext
					if workspace {
						tc = h.workspaceTools(ctx, option.Name(), settings, online)
						// The open page is told of as the tools see it, a public
						// link another device serves of it among what they know.
						if req.OpenPage != nil {
							if p, ok := tc.Workspace.Page(req.OpenPage.ID); ok {
								open := *req.OpenPage
								open.Shared = p.Shared
								req.OpenPage = &open
							}
						}
					} else {
						tc = h.toolContext(pageID, page.DisplayTitle(), option.Name(), work, settings)
						if (tc.ListPages != nil || tc.CreatePage != nil) && h.publicElsewhere(ctx, pageID, online) {
							tc.ListPages, tc.ReadOtherPage, tc.ListFolders, tc.CreatePage = nil, nil, nil, nil
						}
						// The pages it reads are counted as this one is, and a
						// public link may start on this one while it works, here
						// or on another device.
						if tc.ReadOtherPage != nil {
							tc.Alone = func(id string) bool { return h.alone(ctx, id, online) }
							tc.LinkedNow = func() bool { return h.linkedNow(ctx, pageID, online) }
						}
					}
					res, err = RunLocal(ctx, client, req, tc, events)
				}
			} else {
				// What the server reads of the user's pages it has first: the
				// page's, or for a workspace run all of them.
				flush := pageID
				if workspace {
					flush = ""
				}
				if err = h.host.FlushPage(ctx, flush); err == nil {
					if err = h.insertJob(ctx, req); err == nil {
						res, err = RunCloud(ctx, h.be, run.ID, events)
					}
				}
			}
			return res, err
		}()
		h.host.Update(pageID, func() { h.finish(s, run, req, option, before, d, res, err, o) })
	}()
	return run
}

// Stop ends a page's run on this device that is still working. It ends as
// a finished run does, with the edits it made, as they came, on the page.
func (h *Hub) Stop(pageID, runID string) {
	run := h.Session(pageID).Run(runID)
	if run == nil || !run.Stoppable() {
		return
	}
	run.Stopped = true
	run.stop()
}

// chatSaveEvery spaces the saves of a chat's run as its ops come.
const chatSaveEvery = time.Second

// arrived takes a run's op as it comes, on the main thread. An open page
// gets it at once, and so its editor saves it before the run ends: the
// page's "Before" snapshot goes first, and the chat keeps the run's ops
// as they come (a save a second at most, after the first of the run
// alone: saveRunning), so a run the app quit during can still be undone,
// and the page still goes back to how it was.
func (h *Hub) arrived(s *Session, run *Run, option Option, before *doc.Content, d *delivery, o RunOptions, op doc.Op, summary string) {
	if h.gone(run) {
		return
	}
	run.Ops = append(run.Ops, op)
	run.Edits = append(run.Edits, editOf(op, summary))
	run.Answer, run.Activity = "", ""
	if o.Reply != nil {
		o.Reply.Text, o.Reply.Streamed = summary+"…", false
	}
	switch live := h.host.Live(s.PageID); {
	case live != nil:
		if err := h.keepBefore(s.PageID, run, option, before); err != nil {
			log.Printf("agent run history: %v", err)
		}
		d.to(live, run.Ops)
		h.keepRunning(s, run, d, o, chatSaveEvery)
	case !option.Local():
		// The server keeps a cloud run's ops: a page opened
		// later gets them by syncing.
		d.sent = len(run.Ops)
	case s.PageID == WorkspaceID:
		// Its tools have changed the user's pages already: each op is kept
		// as it comes, for Undo after a launch the app quit during.
		h.keepRunning(s, run, d, o, 0)
	}
}

// keepRunning keeps a run's ops with its chat as they come, a save each
// every at most: the chat whole the first time, which has the run then,
// and after that the run alone (saveRunning).
func (h *Hub) keepRunning(s *Session, run *Run, d *delivery, o RunOptions, every time.Duration) {
	if o.Reply == nil || !d.chatSaved.IsZero() && time.Since(d.chatSaved) < every {
		return
	}
	if d.chatSaved.IsZero() {
		h.saveChat(s)
	} else {
		h.saveRunning(s, run)
	}
	d.chatSaved = time.Now()
}

// revised takes a change of a run's op at at as the blocks of the call
// it is go in while the call streams, on the main thread. A page that has
// the op gets the change, open or stored; one that does not yet gets the
// op as it is when it does.
func (h *Hub) revised(s *Session, run *Run, d *delivery, o RunOptions, at int, op *doc.Op, summary string, delta []doc.Op) {
	if at < 0 || at >= len(run.Ops) || h.gone(run) {
		return
	}
	had := d.sent > at
	if op == nil {
		run.Ops, run.Edits = run.Ops[:at], run.Edits[:at]
		d.sent = min(d.sent, len(run.Ops))
	} else {
		run.Ops[at] = *op
		run.Edits[at] = editOf(*op, summary)
		if o.Reply != nil {
			o.Reply.Text, o.Reply.Streamed = summary+"…", false
		}
	}
	if !had {
		return
	}
	if live := h.host.Live(s.PageID); live != nil {
		for _, c := range delta {
			d.apply(live, c)
		}
	} else if err := storeOps(h.st, s.PageID, delta, true, false, run.ID, "agent op on a closed page"); err != nil {
		log.Printf("agent op on a closed page: %v", err)
	}
}

// runJobID is the job a run's history names: a local run's own, kept on
// this device; a cloud run's server job keeps its own history.
func runJobID(run *Run, option Option) string {
	if option.Local() {
		return run.ID
	}
	return ""
}

// keepBefore keeps the page as it was before a run in its history, once
// a run: when its first op reaches the page, or at its end.
func (h *Hub) keepBefore(pageID string, run *Run, option Option, before *doc.Content) error {
	// A workspace run changed no page's blocks: Undo takes back its
	// changes to the pages around them.
	if run.kept || pageID == WorkspaceID {
		return nil
	}
	label := "Before " + option.Name() + ": " + short(run.Instruction)
	if err := h.st.AddHistory(pageID, before, "agent_run", runJobID(run, option), label); err != nil {
		return err
	}
	run.kept = true
	return nil
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
		"id": req.JobID, "user_id": h.be.UserID(), "surface": req.Surface,
		"focus_block_ids": focus, "provider_id": req.ProviderID, "model": req.Model, "status": "queued",
		"request": req.JobRequest(),
	}
	// A workspace run's job is of no page.
	if req.PageID != "" {
		row["page_id"] = req.PageID
	}
	if req.CommentID != "" {
		row["comment_id"] = req.CommentID
	}
	if req.ConversationID != "" {
		row["conversation_id"] = req.ConversationID
	}
	// What a page gave the request can hold what Postgres refuses.
	store.CleanRow(row)
	_, err := h.be.Insert(ctx, "ai_jobs", row)
	return err
}

// thread is the comment thread comment id is in, for a run answering it,
// as the server's runs read it: its first comment, then the replies not
// deleted; nil for no comment.
func (h *Hub) thread(pageID, id string) []ThreadComment {
	if id == "" {
		return nil
	}
	ts, err := h.st.Threads(pageID)
	if err != nil {
		return nil
	}
	comment := func(c store.Comment) ThreadComment {
		who := c.AuthorName
		switch {
		case c.AuthorKind == "agent":
			who = cmp.Or(who, "Agent") + " (agent)"
		case c.AuthorKind == "guest":
			who = cmp.Or(who, "Guest") + " (guest, through the page's public link)"
		case who == "":
			who = "Someone"
		}
		body := c.Body
		if !c.DeletedAt.IsZero() {
			body = "(deleted)"
		}
		return ThreadComment{Author: who, Body: body}
	}
	for _, t := range ts {
		if t.Root.ID != id && !slices.ContainsFunc(t.Replies, func(c store.Comment) bool { return c.ID == id }) {
			continue
		}
		out := []ThreadComment{comment(t.Root)}
		for _, r := range t.Replies {
			if r.DeletedAt.IsZero() {
				out = append(out, comment(r))
			}
		}
		return out
	}
	return nil
}

func (h *Hub) finish(s *Session, run *Run, req Request, option Option, before *doc.Content, d *delivery, res Result, err error, o RunOptions) {
	s.Running = false
	run.stop, run.cancel = nil, nil
	if h.gone(run) {
		// Its account left this device, its pages with it: it stores
		// nothing, and ends for whoever waits on it.
		run.Status, run.Error = "failed", ErrAccountLeft.Error()
		if o.Reply != nil {
			o.Reply.Pending, o.Reply.Streamed, o.Reply.Error, o.Reply.Text = false, false, true, run.Error
		}
		if o.Done != nil {
			o.Done(run)
		}
		return
	}
	run.Reply, run.Answer, run.Activity = res.Reply, "", ""
	run.KeptToPage, run.Guarded, run.ReadOthers = res.KeptToPage, res.Guarded, res.ReadOthers
	// A public link started on its page as it went on, after it read beyond
	// the page: it ended as a run the user stopped does.
	if errors.Is(err, errLinkedStop) {
		run.Stopped = true
	}
	// A cloud run whose stream was cut off has its ops from the server's
	// job, those after the cut too: all of them are the run's, to undo.
	if len(res.Ops) > len(run.Ops) {
		run.Ops, run.Edits = res.Ops, res.Edits
	}
	switch {
	case run.Stopped && err == nil:
		// Stopped after it had finished, before it ended here: it ends as
		// it finished, with its answer.
		run.Stopped = false
	case run.Stopped:
		// What it said before it stopped is no answer, and what it edited
		// stays, for Undo.
		run.Reply, err = "", nil
	}
	if err != nil {
		run.Status, run.Error = "failed", err.Error()
	} else {
		run.Status = "done"
	}
	pageID := s.PageID
	saved := true
	if len(run.Ops) > 0 && option.Local() {
		// The page may have opened or closed during the run: it gets the
		// ops it missed, open or stored.
		if live := h.host.Live(pageID); live != nil {
			if err := h.keepBefore(pageID, run, option, before); err != nil {
				log.Printf("agent run history: %v", err)
			}
			d.to(live, run.Ops)
		} else if len(d.missed(run.Ops)) > 0 {
			// The snapshot goes first, as an MCP edit's: edits that could
			// not keep one are not stored, so none is stored that the page
			// cannot go back from.
			err := h.keepBefore(pageID, run, option, before)
			if err == nil {
				err = d.save(h.st, pageID, run.Ops, run.ID)
			}
			if err != nil {
				// The edits are not on the page, whatever the agent said.
				log.Printf("agent run on a closed page: %v", err)
				saved = false
				run.Status = "failed"
				if run.Error == "" {
					run.Error = err.Error()
				}
			}
		}
	}
	if len(run.Ops) > 0 && saved {
		// A run whose snapshot is not kept yet, a cloud run's ops the page
		// gets by syncing, gets it now; once a run, whenever it is kept.
		if err := h.keepBefore(pageID, run, option, before); err != nil {
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
		o.Reply.Pending, o.Reply.Streamed = false, false
		o.Reply.Error = run.Error != ""
		o.Reply.Text = Describe(run)
		h.saveChat(s)
		// The chat has the run whole now.
		if !d.chatSaved.IsZero() {
			if err := h.st.DeleteValue(runningKey(pageID)); err != nil {
				log.Printf("agent chat: %v", err)
			}
		}
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

func (h *Hub) logLocalJob(run *Run, req Request) {
	var edits []any
	for _, e := range run.Edits {
		edit := map[string]any{"summary": e.Summary, "block_ids": e.BlockIDs}
		if e.PageID != "" {
			edit["page_id"] = e.PageID
		}
		edits = append(edits, edit)
	}
	status := "done"
	if run.Error != "" {
		status = "failed"
	}
	// Local providers have no server row, so the job stays on this device.
	if err := h.st.PutJob(store.Job{
		ID: run.ID, PageID: req.PageID, Surface: run.Surface, FocusBlockIDs: req.FocusBlockIDs, CommentID: req.CommentID,
		ConversationID: req.ConversationID, ProviderID: req.ProviderID, Model: req.Model, Status: status,
		Request: req.JobRequest(), Result: map[string]any{"reply": run.Reply, "ops": run.Ops, "edits": edits, "read_others": run.ReadOthers}, Error: run.Error,
	}); err != nil {
		log.Printf("agent job: %v", err)
	}
}

// ReplyBlocks are a run's answer in words, reply, as blocks to put in its
// page, as Insert puts them: its Markdown read as blocks. The reply keeps
// to no URL guard (checkURLs): of a run whose guard was on, or that read
// beyond its page, each picture is a link to it instead, as the app loads
// a picture by itself, and its URL, which the run picked, could carry
// what it read. A run not known, nil, is taken for one of those.
func ReplyBlocks(reply string, run *Run) []*doc.Node {
	nodes := doc.MarkdownBlocks(reply)
	if run != nil && run.KeptToPage && !run.Guarded {
		return nodes
	}
	var unload func([]*doc.Node)
	unload = func(nodes []*doc.Node) {
		for i, n := range nodes {
			if n.Type == "image" {
				link := n.Str("url")
				p := &doc.Node{ID: n.ID, Type: "paragraph", Children: n.Children}
				p.SetDelta(doc.Delta{{Text: cmp.Or(strings.TrimSpace(n.Str("alt")), link), Attrs: doc.Attrs{Href: link}}})
				nodes[i] = p
			}
			unload(nodes[i].Children)
		}
	}
	unload(nodes)
	return nodes
}

// Describe is how a run ended, for its reply.
func Describe(r *Run) string {
	switch {
	case r.Error != "":
		return r.Error
	case r.Stopped && len(r.Edits) > 0:
		return locales.AgentStoppedAfter(r.Edits[len(r.Edits)-1].Summary)
	case r.Stopped:
		return locales.AgentStoppedBefore()
	case r.Reply != "":
		return r.Reply
	case len(r.Edits) > 0:
		return locales.AgentEditMade(r.Edits[len(r.Edits)-1].Summary)
	}
	return locales.AgentNoChanges()
}
