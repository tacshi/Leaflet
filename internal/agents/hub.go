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

type Host interface {
	Update(pageID string, fn func())
	Live(pageID string) LiveDoc
	SaveNow(pageID string)
	Online() bool
	Settings() store.Settings
	Tint(pageID string, ids []string)
}

type LiveDoc interface {
	Doc() *doc.Content
	Peek() *doc.Content
	Composition() (id uint64, out bool)
	ApplyOp(op doc.Op, newStep bool) error
	ApplyOpFrom(op doc.Op, newStep bool, read uint64) error
	ApplyOps(ops []doc.Op) error
}

type Run struct {
	ID          string                  `json:"ID"`
	Surface     string                  `json:"Surface"`
	AgentName   string                  `json:"AgentName"`
	Instruction string                  `json:"Instruction"`
	Ops         []doc.Op                `json:"Ops"`
	Edits       []Edit                  `json:"Edits"`
	Reply       string                  `json:"Reply"`
	Status      string                  `json:"Status"`
	Undone      bool                    `json:"Undone"`
	Error       string                  `json:"Error"`
	At          time.Time               `json:"At"`
	Stopped     bool                    `json:"Stopped"`
	Restored    bool                    `json:"-"`
	Answer      string                  `json:"-"`
	Activity    string                  `json:"-"`
	KeptToPage  bool                    `json:"KeptToPage,omitempty"`
	Guarded     bool                    `json:"Guarded,omitempty"`
	ReadOthers  bool                    `json:"read_others,omitempty"`
	Removed     map[string]RemovedPages `json:"Removed,omitempty"`
	Left        []string                `json:"Left,omitempty"`
	Local       bool                    `json:"Local,omitempty"`
	Skipped     []int                   `json:"Skipped,omitempty"`
	kept        bool
	stop        context.CancelFunc
	cancel      context.CancelFunc
	epoch       int64
}

func (r *Run) skip(at int) {
	if !slices.Contains(r.Skipped, at) {
		r.Skipped = append(r.Skipped, at)
	}
}

func (r *Run) landed() []doc.Op {
	if len(r.Skipped) == 0 {
		return r.Ops
	}
	out := make([]doc.Op, 0, len(r.Ops))
	for i, o := range r.Ops {
		if !slices.Contains(r.Skipped, i) {
			out = append(out, o)
		}
	}
	return out
}

type RemovedPages struct {
	IDs  []string     `json:"IDs"`
	Card doc.Position `json:"Card"`
}

func (r *Run) Stoppable() bool { return r.stop != nil && !r.Stopped }

func (r *Run) BlockIDs() []string {
	var out []string
	for _, e := range r.Edits {
		out = append(out, e.BlockIDs...)
	}
	return out
}

type Hub struct {
	st          *store.Store
	be          *remote.Client
	host        Host
	sessions    map[string]*Session
	PublicLink  func(pageID string) bool
	ICloudLease func(pageID string) (stop func())
	ICloudLinks func() (map[string]time.Time, error)
	linksMu     sync.Mutex
	links       map[string]time.Time
	listed      map[string]bool
	leases      map[string]int
	epoch       atomic.Int64
	runsMu      sync.Mutex
	runs        context.Context
	endRuns     context.CancelFunc
	optionsMu   sync.Mutex
	options     []Option
}

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

func (h *Hub) gone(run *Run) bool { return run.epoch != h.epoch.Load() }

func (h *Hub) runContext() context.Context {
	h.runsMu.Lock()
	defer h.runsMu.Unlock()
	if h.runs == nil {
		h.runs, h.endRuns = context.WithCancel(context.Background())
	}
	return h.runs
}

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

func (h *Hub) Session(pageID string) *Session {
	s := h.sessions[pageID]
	if s == nil {
		s = &Session{PageID: pageID, ConversationID: doc.NewRowID()}
		h.loadChat(s)
		h.sessions[pageID] = s
	}
	return s
}

type RunOptions struct {
	Surface     string
	Instruction string
	Focus       []string
	Selection   string
	CommentID   string
	Agent       *store.ModelChoice
	History     []Turn
	KeptToPage  bool
	ReadOthers  bool
	Reply       *Message
	OpenPage    string
	Done        func(*Run)
}

var ErrRunning error = errRunning{}

type errRunning struct{}

func (errRunning) Error() string { return locales.AgentRunning() }

var ErrAccountLeft error = errAccountLeft{}

type errAccountLeft struct{}

func (errAccountLeft) Error() string { return locales.AgentAccountLeft() }

func (h *Hub) Start(pageID string, o RunOptions) *Run {
	s := h.Session(pageID)
	run := &Run{ID: doc.NewRowID(), Surface: o.Surface, Instruction: o.Instruction, Status: "running", At: time.Now(), epoch: h.epoch.Load()}
	fail := func(err error) *Run {
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
	workspace := pageID == WorkspaceID
	h.host.SaveNow(pageID)
	if o.OpenPage != "" {
		h.host.SaveNow(o.OpenPage)
	}
	ed := h.host.Live(pageID)
	var before *doc.Content
	var read uint64
	switch {
	case workspace:
		before = doc.Empty()
	case ed != nil:
		before = ed.Peek().Copy()
		read, _ = ed.Composition()
	default:
		if before, err = stored(h.st, pageID); err != nil {
			return fail(err)
		}
	}
	run.AgentName, run.Local = option.Name(), true
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
		SupportsTools: option.Provider.SupportsTools == nil || *option.Provider.SupportsTools || option.Provider.AuthKind == "account",
		FocusBlockIDs: o.Focus, SelectionText: o.Selection, CommentID: o.CommentID, Thread: h.thread(pageID, o.CommentID),
		History: o.History, KeptToPage: o.KeptToPage, ReadOthers: o.ReadOthers, AllowCrossPage: h.crossPage(page, settings),
		Web: h.web(settings),
	}
	if o.Surface == "sidebar" || o.Surface == "workspace" {
		req.ConversationID = s.ConversationID
	}
	if workspace {
		req.PageID, req.PageTitle, req.AllowCrossPage = "", "", settings.AgentsReadOtherPages
		req.OpenPage = h.openPage(o.OpenPage)
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
	online := h.host.Online()
	ctx, cancel := context.WithTimeout(h.runContext(), 10*time.Minute)
	run.cancel, run.stop = cancel, cancel
	go func() {
		defer guard.Recover()
		defer cancel()
		res, err := func() (res Result, err error) {
			defer guard.RecoverAs(&err)
			var client Model
			if client, err = h.ModelFor(option.Provider); err == nil {
				var tc ToolContext
				if workspace {
					tc = h.workspaceTools(ctx, option.Name(), settings, online)
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
					if tc.ReadOtherPage != nil {
						tc.Alone = func(id string) bool { return h.alone(ctx, id, online) }
						tc.LinkedNow = func() bool { return h.linkedNow(ctx, pageID, online) }
					}
				}
				res, err = RunLocal(ctx, client, req, tc, events)
			}
			return res, err
		}()
		h.host.Update(pageID, func() { h.finish(s, run, req, option, before, d, res, err, o) })
	}()
	return run
}

func (h *Hub) Stop(pageID, runID string) {
	run := h.Session(pageID).Run(runID)
	if run == nil || !run.Stoppable() {
		return
	}
	run.Stopped = true
	run.stop()
}

const chatSaveEvery = time.Second

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
		} else if err := d.to(live, run.Ops); err != nil {
			log.Printf("agent op on the open page: %v", err)
		}
		h.keepRunning(s, run, d, o, chatSaveEvery)
	case s.PageID == WorkspaceID:
		h.keepRunning(s, run, d, o, 0)
	}
}

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

func (h *Hub) revised(s *Session, run *Run, d *delivery, o RunOptions, at int, op *doc.Op, summary string, delta []doc.Op) {
	if at < 0 || at >= len(run.Ops) || h.gone(run) {
		return
	}
	had := d.sent > at
	if op == nil {
		run.Ops, run.Edits = run.Ops[:at], run.Edits[:at]
		run.Skipped = slices.DeleteFunc(run.Skipped, func(i int) bool { return i >= at })
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
			if err := d.apply(live, c, at); err != nil {
				log.Printf("agent op on the open page: %v", err)
				d.failed = errors.Join(d.failed, err)
				break
			}
		}
	} else if skipped, err := storeOps(h.st, s.PageID, delta, true, false, run.ID, "agent op on a closed page"); err != nil {
		log.Printf("agent op on a closed page: %v", err)
		d.failed = errors.Join(d.failed, err)
	} else if len(skipped) > 0 {
		run.skip(at)
	}
}

func (h *Hub) keepBefore(pageID string, run *Run, option Option, before *doc.Content) error {
	if run.kept || pageID == WorkspaceID {
		return nil
	}
	label := "Before " + option.Name() + ": " + short(run.Instruction)
	if err := h.st.AddHistory(pageID, before, "agent_run", run.ID, label); err != nil {
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
	if errors.Is(err, errLinkedStop) {
		run.Stopped = true
	}
	switch {
	case run.Stopped && err == nil:
		run.Stopped = false
	case run.Stopped:
		run.Reply, err = "", nil
	}
	if err != nil {
		run.Status, run.Error = "failed", err.Error()
	} else {
		run.Status = "done"
	}
	pageID := s.PageID
	saved := true
	if len(run.Ops) > 0 {
		var err error
		if live := h.host.Live(pageID); live != nil {
			err = h.keepBefore(pageID, run, option, before)
			if err == nil {
				err = d.to(live, run.Ops)
			}
		} else if missed, _ := d.missed(run.Ops); len(missed) > 0 {
			err = h.keepBefore(pageID, run, option, before)
			if err == nil {
				err = d.save(h.st, pageID, run.Ops, run.ID)
			}
		}
		err = errors.Join(d.failed, err)
		if err != nil {
			log.Printf("agent run on the page: %v", err)
			saved = false
			run.Status = "failed"
			if run.Error == "" {
				run.Error = err.Error()
			}
		}
	}
	if len(run.Ops) > 0 && saved {
		if err := h.keepBefore(pageID, run, option, before); err != nil {
			log.Printf("agent run history: %v", err)
		}
		h.host.Tint(pageID, run.BlockIDs())
	}
	h.logJob(run, req)
	if req.Surface == "comment" && run.Error == "" && run.Reply != "" && len(req.FocusBlockIDs) > 0 {
		if _, err := h.st.AddComment(pageID, req.FocusBlockIDs[0], run.Reply, req.CommentID, "", "agent", option.Name()); err != nil {
			run.Status, run.Error = "failed", err.Error()
		}
	}
	if o.Reply != nil {
		o.Reply.Pending, o.Reply.Streamed = false, false
		o.Reply.Error = run.Error != ""
		o.Reply.Text = Describe(run)
		h.saveChat(s)
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

func (h *Hub) logJob(run *Run, req Request) {
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
	if err := h.st.PutJob(store.Job{
		ID: run.ID, PageID: req.PageID, Surface: run.Surface, FocusBlockIDs: req.FocusBlockIDs, CommentID: req.CommentID,
		ConversationID: req.ConversationID, ProviderID: req.ProviderID, Model: req.Model, Status: status,
		Request: req.JobRequest(), Result: map[string]any{"reply": run.Reply, "ops": run.Ops, "edits": edits, "read_others": run.ReadOthers}, Error: run.Error,
	}); err != nil {
		log.Printf("agent job: %v", err)
	}
}

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
