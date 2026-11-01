package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/live"
	"leaflet/internal/store"
	"leaflet/locales"
)

// openPage is a page open in the app, as the core sees it: the live page
// that saves and merges it, and the content the app's editors show, which
// the app sends after each edit. The two run apart, on different threads,
// so each version of the content the core gives the app has a rev, and
// the app's edits say which rev they were made over. An edit made over an
// older rev than the core's is merged with what changed since, per block,
// and the merge goes back to the app. Touched only on the core's queue.
type openPage struct {
	c    *Core
	id   string
	live *live.Page
	cur  *doc.Content
	rev  int64
	// editors are the app's editors that show the page, a window each, by
	// the number OpenPage gave each. The page stays open until the last
	// of them closes it, and its edits are saved.
	editors map[int64]*pageEditor
	// saving is whether edits wait to be saved, as PageSaving last told
	// the app, for its byline to say "Saving…".
	saving bool
	// shown is whether the app has the page as cur is: an agent's edits
	// go to it agentEvery at the most, and newStep whether those not
	// shown yet start an undo step. shownAt is when it last got them,
	// and showing the timer that sends those waiting.
	shown, newStep bool
	shownAt        time.Time
	showing        *time.Timer
	// retry is how long a page no editor shows waits to try its failed
	// save again, longer each time; 0 while its saves go through.
	retry time.Duration
}

// pageEditor is one of the app's editors of an open page.
type pageEditor struct {
	// seen is the content the editor last had at each rev it may still
	// edit over: the core's version, or its latest edit of it.
	seen map[int64]*doc.Content
	// last is the rev of its latest edit, or of the page as it opened.
	last int64
}

// keepRevs is how many of the newest revs an editor keeps the content of,
// besides its latest edit's. An editor edits over the newest rev it heard
// of, which reaches it moments after the core makes it, or over its
// latest edit's while it holds back what it heard for typing it has yet
// to send.
const keepRevs = 32

// base is the content the editor had at rev, for an edit over it to merge
// from. A rev it no longer keeps has the newest kept before it instead,
// its latest edit's at the oldest, rather than none, which would set the
// edit against the whole page.
func (e *pageEditor) base(rev int64) *doc.Content {
	if c := e.seen[rev]; c != nil {
		return c
	}
	at := int64(-1)
	for r := range e.seen {
		if r < rev && r > at {
			at = r
		}
	}
	return e.seen[at]
}

// saw keeps c as the content the editor had at rev, forgetting the oldest
// rev it kept but its latest edit's when it keeps too many.
func (e *pageEditor) saw(rev int64, c *doc.Content) {
	e.seen[rev] = c
	if len(e.seen) <= keepRevs+1 {
		return
	}
	oldest := int64(-1)
	for r := range e.seen {
		if r != e.last && (oldest < 0 || r < oldest) {
			oldest = r
		}
	}
	delete(e.seen, oldest)
}

// openedPage is an open page as OpenPage returns it.
type openedPage struct {
	ID string `json:"id"`
	// Editor is the number of the editor that opened the page, for its
	// edits and its closing to name it.
	Editor   int64        `json:"editor"`
	Content  *doc.Content `json:"content"`
	Rev      int64        `json:"rev"`
	ReadOnly bool         `json:"read_only"`
	// Unreadable is a page whose content this version of Leaflet could
	// not read: it opens empty and read-only, and is never saved over.
	Unreadable bool `json:"unreadable"`
}

// OpenPage opens a page for an editor of the app's, returning it as
// openedPage. A page already open, in another window or kept open to save
// its edits, is returned as it is now.
func (c *Core) OpenPage(id string) (string, error) {
	var out openedPage
	var err error
	c.q.do(func() {
		p := c.pages[id]
		if p == nil {
			if _, err = c.core.Store.Page(id); err != nil {
				return
			}
			p = &openPage{c: c, id: id, editors: map[int64]*pageEditor{}, shown: true}
			p.live, p.cur = live.Open(c.core.Store, id, live.Options{
				// The save shortly after the typing stops runs so.
				Post: func(fn func()) {
					c.q.post(func() {
						fn()
						p.tellSaving()
						p.retryIfUnused()
						p.closeIfUnused()
					})
				},
				CanEdit: func() bool { return c.canEdit(id) },
				Notify:  c.ev.notice,
				Saved:   c.core.Sync.Kick,
			})
			p.live.Attach(p)
			p.live.SetReadOnly(!c.canEdit(id) || p.live.Unreadable())
			c.pages[id] = p
			c.core.Sync.SetOpen(id, true)
			// Who the page is shared with comes from the server, for its
			// byline, as the desktop reads it as a page opens.
			if c.core.Remote.SignedIn() {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
					defer cancel()
					_ = c.core.Sync.LoadMembers(ctx, id)
				}()
			}
		}
		c.editors++
		p.editors[c.editors] = &pageEditor{seen: map[int64]*doc.Content{p.rev: p.cur.Copy()}, last: p.rev}
		out = openedPage{ID: id, Editor: c.editors, Content: p.cur, Rev: p.rev, ReadOnly: p.live.ReadOnly(), Unreadable: p.live.Unreadable()}
		c.fillLinks(id, p.cur)
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

// EditPage gives the core the content of an open page as one of the
// app's editors has it after an edit, made over rev: it is saved shortly
// after the typing stops. Made over an older rev, or with the page open in
// another window too, it is merged with what changed since, and the merge
// comes back as PageUpdated, for every editor of the page to show.
func (c *Core) EditPage(id string, editor int64, content string, rev int64) error {
	edit, err := doc.Decode(content)
	if err != nil {
		return err
	}
	c.q.do(func() {
		p := c.pages[id]
		if p == nil || p.editors[editor] == nil {
			err = errNotOpen
			return
		}
		if err = p.edit(p.editors[editor], edit, rev); err == nil {
			c.fillLinks(id, p.cur)
		}
	})
	return err
}

func (p *openPage) edit(e *pageEditor, edit *doc.Content, rev int64) error {
	if rev > p.rev {
		return fmt.Errorf("the edit is over rev %d, after the page's %d", rev, p.rev)
	}
	if p.live.ReadOnly() {
		return nil
	}
	// An agent's edits the app does not have yet are at a rev of their
	// own, for this edit to merge with.
	p.show()
	if rev == p.rev && len(p.editors) == 1 {
		p.cur = edit
	} else {
		// An editor in another window has the page at this rev too, and
		// would edit over it without this edit: it gets a new rev.
		base := e.base(rev)
		local := edit.Stamped(base, time.Now().UnixMilli())
		merged, conflicts := doc.Merge(base, local, p.cur)
		// The side a conflict loses is kept for recovery, as the live
		// page keeps it merging a sync.
		for _, cf := range conflicts {
			if err := p.c.core.Store.AddConflict(p.id, cf); err != nil {
				p.c.ev.notice(locales.AppErrorSave(err.Error()))
			}
		}
		p.bump(merged)
		p.c.ev.pageUpdated(p.id, merged.Encode(), p.rev, "merge", false)
	}
	// The editor goes on editing over rev until it hears of a newer one;
	// what came before rev it won't edit over again.
	e.seen[rev] = edit.Copy()
	e.last = rev
	for r := range e.seen {
		if r < rev {
			delete(e.seen, r)
		}
	}
	p.live.Edited()
	p.tellSaving()
	return nil
}

// tellSaving tells the app whether the page's edits wait to be saved, as
// that changes.
func (p *openPage) tellSaving() {
	if saving := p.live.Dirty(); saving != p.saving {
		p.saving = saving
		p.c.ev.pageSaving(p.id, saving)
	}
}

// bump makes c the page's content at a new rev, which each of its editors
// may edit over once it hears of it.
func (p *openPage) bump(c *doc.Content) {
	p.cur = c
	p.rev++
	seen := c.Copy()
	for _, e := range p.editors {
		e.saw(p.rev, seen)
	}
}

// ClosePage closes an editor of an open page: the last to close it saves
// its edits and closes it. Edits that could not be saved are not dropped:
// the page stays open, with no editor, to try again shortly and as the
// app goes to the background, and closes once they are saved. Opened
// again before then, it shows them.
func (c *Core) ClosePage(id string, editor int64) {
	c.q.do(func() {
		p := c.pages[id]
		if p == nil {
			return
		}
		delete(p.editors, editor)
		if len(p.editors) > 0 {
			return
		}
		if !p.live.Save() {
			p.live.Edited()
		}
		p.tellSaving()
		p.closeIfUnused()
	})
}

// retryFirst and retryMost bound how long a page no editor shows waits to
// try a failed save again.
const (
	retryFirst = 5 * time.Second
	retryMost  = 2 * time.Minute
)

// retryIfUnused tries a failed save of a page that no editor shows again
// later, as no edit will: sooner the first time, then less often.
func (p *openPage) retryIfUnused() {
	if len(p.editors) > 0 || !p.live.Dirty() || p.c.pages[p.id] != p {
		p.retry = 0
		return
	}
	p.retry = min(max(2*p.retry, retryFirst), retryMost)
	time.AfterFunc(p.retry, func() {
		p.c.q.post(func() {
			if len(p.editors) == 0 && p.live.Dirty() && p.c.pages[p.id] == p {
				p.live.Edited()
			}
		})
	})
}

// closeIfUnused closes a page that no editor shows once its edits are
// saved.
func (p *openPage) closeIfUnused() {
	if len(p.editors) > 0 || p.live.Dirty() || p.c.pages[p.id] != p {
		return
	}
	p.live.Close()
	delete(p.c.pages, p.id)
	p.c.core.Sync.SetOpen(p.id, false)
}

// SaveOpenPages saves the open pages' edits now, as the app goes to the
// background. It reports whether all of them were saved.
func (c *Core) SaveOpenPages() bool {
	all := true
	c.q.do(func() {
		for _, p := range c.pages {
			if !p.live.Save() {
				all = false
			}
			p.tellSaving()
			p.closeIfUnused()
		}
	})
	return all
}

// external brings content stored from outside into a page, if it is open.
func (c *Core) external(id string) {
	if p := c.pages[id]; p != nil {
		p.live.External()
	}
}

// The page as live.Editor: the app's editor, through events.

func (p *openPage) Doc() *doc.Content { return p.cur }

func (p *openPage) Replace(c *doc.Content) {
	p.show()
	p.bump(c)
	p.c.ev.pageUpdated(p.id, c.Encode(), p.rev, "external", false)
}

func (p *openPage) SetReadOnly(readOnly bool) { p.c.ev.pageReadOnly(p.id, readOnly) }

func (p *openPage) ApplyOp(op doc.Op, newStep bool) error {
	return p.apply([]doc.Op{op}, newStep)
}

func (p *openPage) ApplyOps(ops []doc.Op) error { return p.apply(ops, true) }

// agentEvery spaces the agent's edits an open page shows: each is the
// whole page for the app, while a call's blocks stream in every few
// hundredths of a second.
const agentEvery = 150 * time.Millisecond

// apply applies an agent's ops to a copy of the content, so ops that fail
// halfway change nothing. The app gets them agentEvery at the most.
func (p *openPage) apply(ops []doc.Op, newStep bool) error {
	c := p.cur.Copy()
	for _, op := range ops {
		if err := c.Apply(op); err != nil {
			return err
		}
	}
	// An undo step of its own begins after those waiting.
	if newStep {
		p.show()
	}
	p.cur, p.shown, p.newStep = c, false, p.newStep || newStep
	if p.showing == nil {
		p.showing = time.AfterFunc(max(0, agentEvery-time.Since(p.shownAt)), func() {
			p.c.q.post(func() {
				p.showing = nil
				p.show()
			})
		})
	}
	// Saved as the desktop's editor saves an agent's edit, shortly after.
	p.live.Edited()
	p.tellSaving()
	return nil
}

// show gives the app the agent's edits it does not have yet, at a new rev.
func (p *openPage) show() {
	if p.shown {
		return
	}
	newStep := p.newStep
	p.shown, p.newStep, p.shownAt = true, false, time.Now()
	if p.c.pages[p.id] != p {
		return
	}
	p.bump(p.cur)
	p.c.ev.pageUpdated(p.id, p.cur.Encode(), p.rev, "agent", newStep)
}

// member is someone a page is shared with, as Members lists them.
type member struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	// Role is "owner", "editor" or "viewer".
	Role string `json:"role"`
	// Accepted is false while they have not accepted the page's invite.
	Accepted bool `json:"accepted"`
}

// Members returns who a page is shared with, as this device knows, as a
// JSON array of member: the byline's "Shared with" counts them. Opening
// the page reads them from the server, and Changed says "page_members"
// once they come.
func (c *Core) Members(pageID string) (string, error) {
	var list []store.Member
	var err error
	c.q.do(func() { list, err = c.core.Store.Members(pageID) })
	if err != nil {
		return "", err
	}
	out := make([]member, 0, len(list))
	for _, m := range list {
		out = append(out, member{UserID: m.UserID, DisplayName: m.DisplayName, Email: m.Email, Role: m.Role, Accepted: m.Accepted})
	}
	return encode(out)
}

func (c *Core) canEdit(id string) bool {
	pg, err := c.core.Store.Page(id)
	return err == nil && c.core.Store.CanEdit(pg)
}

func encode(v any) (string, error) {
	raw, err := json.Marshal(v)
	return string(raw), err
}
