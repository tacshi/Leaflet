package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
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
//
// The content at each rev is never changed once it is the page's: an
// agent's op changes a copy of only the blocks it touches and those they
// are in (thawed), sharing the rest, so the revs an editor keeps share
// what they have alike rather than each being a copy of the whole page.
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
	// shown is whether the app has the page as cur is: an agent's edits
	// go to it agentEvery at the most, and newStep whether those not
	// shown yet start an undo step. shownAt is when it last got them,
	// and showing the timer that sends those waiting.
	shown, newStep bool
	shownAt        time.Time
	showing        *time.Timer
	// stepReason is why the change to show next is a step of its own, for
	// the app, which keeps its undo steps, to take it as one.
	stepReason string
	// retry is how long a page no editor shows waits to try its failed
	// save again, longer each time; 0 while its saves go through.
	retry time.Duration
	// fresh is roughly how many bytes the agent's ops made since the last
	// rev, which the next rev holds of its own: the rest it shares.
	fresh int
	// coded are the JSON of the top-level blocks of the content the app
	// was last given, by block, for the next to encode again only those
	// that changed.
	coded map[*doc.Node][]byte
}

// pageEditor is one of the app's editors of an open page.
type pageEditor struct {
	// seen is the content the editor last had at each rev it may still
	// edit over: the core's version, or its latest edit of it.
	seen map[int64]*doc.Content
	// sizes are roughly the bytes each of seen holds that the content
	// before it does not share, by rev.
	sizes map[int64]int
	// last is the rev of its latest edit, or of the page as it opened.
	last int64
}

// keepRevs is how many of the newest revs an editor keeps the content of,
// besides its latest edit's, and keepBytes about how many bytes of their
// own they hold at most. An editor edits over the newest rev it heard
// of, which reaches it moments after the core makes it, or over its
// latest edit's while it holds back what it heard for typing it has yet
// to send. An agent's revs share most of what they hold; a merge or a
// change from outside is a page of its own each, and a long page's run
// of them is kept to fewer.
const (
	keepRevs  = 32
	keepBytes = 32 << 20
)

// snapshot is c as an editor keeps it: its blocks shared, which no one
// changes once they are the page's, and its meta apart, which a save
// gives the page's content anew.
func snapshot(c *doc.Content) *doc.Content {
	return &doc.Content{Children: c.Children, Blocks: c.Blocks, Deleted: c.Deleted}
}

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

// saw keeps c as the content the editor had at rev, holding about size
// bytes of its own, forgetting the oldest revs it kept, but its latest
// edit's and this one, while it keeps too many or too many bytes.
func (e *pageEditor) saw(rev int64, c *doc.Content, size int) {
	if e.sizes == nil {
		e.sizes = map[int64]int{}
	}
	e.seen[rev], e.sizes[rev] = c, size
	for {
		total := 0
		for r := range e.seen {
			total += e.sizes[r]
		}
		if len(e.seen) <= keepRevs+1 && total <= keepBytes {
			return
		}
		oldest := int64(-1)
		for r := range e.seen {
			if r != e.last && r != rev && (oldest < 0 || r < oldest) {
				oldest = r
			}
		}
		if oldest < 0 {
			return
		}
		e.forget(oldest)
	}
}

// forget drops what the editor kept of rev.
func (e *pageEditor) forget(rev int64) {
	delete(e.seen, rev)
	delete(e.sizes, rev)
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
func (c *Core) OpenPage(id string) (_ string, err error) {
	defer catch(&err)
	var raw string
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
						p.retryIfUnused()
						p.closeIfUnused()
					})
				},
				CanEdit: func() bool { return c.canEdit(id) },
				Notify:  c.ev.notice,
				// A save goes up soon; what changed elsewhere comes with
				// the passes, rather than with each pause in the typing.
				Saved: c.core.Sync.KickPush,
				// Every save says whether edits still wait, the timer's or
				// another's (a version named, an export, a copy), for
				// the byline to say "Saving…" meanwhile.
				Dirty: func(saving bool) { c.ev.pageSaving(id, saving) },
			})
			p.live.Attach(p)
			p.live.SetReadOnly(!c.canEdit(id) || p.live.Unreadable())
			c.pages[id] = p
			c.core.Sync.SetOpen(id, true)
			c.loadMembers(id)
		}
		c.editors++
		c.fillLinks(id, p.cur)
		// Encoded here, on the queue: a save changes the content in
		// place, stamping its blocks. The app's editor nests blocks only
		// in the blocks that hold blocks: the children of others come
		// after them, as a merge leaves them, and as the edits it sends
		// have them.
		raw, err = encode(openedPage{ID: id, Editor: c.editors, Content: p.cur.Lifted(), Rev: p.rev, ReadOnly: p.live.ReadOnly(), Unreadable: p.live.Unreadable()})
		p.editors[c.editors] = &pageEditor{seen: map[int64]*doc.Content{p.rev: snapshot(p.cur)}, sizes: map[int64]int{p.rev: len(raw)}, last: p.rev}
	})
	return raw, err
}

// loadMembers reads who page id is shared with from the server, for its
// byline, as the desktop reads it as a page opens. Sync reads them itself
// for the user's own pages as they open (sync.Service.SetOpen), on the
// server: here they are read for the pages others share, and for all of
// them while sync goes through iCloud Drive instead. Run on the core's
// queue.
func (c *Core) loadMembers(id string) {
	if !c.core.Remote.SignedIn() {
		return
	}
	if pg, err := c.core.Store.Page(id); err == nil && pg.SpaceID == c.core.Store.Session().SpaceID && !c.core.Sync.ICloudOn() {
		return
	}
	guard.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
		defer cancel()
		_ = c.core.Sync.LoadMembers(ctx, id)
	})
}

// EditPage gives the core the content of an open page as one of the
// app's editors has it after an edit, made over rev: it is saved shortly
// after the typing stops. Made over an older rev, or with the page open in
// another window too, it is merged with what changed since, and the merge
// comes back as PageUpdated, for every editor of the page to show.
//
// It returns the rev it left the page at, which has the edit: a change at
// an older one, which PageUpdated may bring after EditPage returns, is
// from before it, and the editor shows the merge at this rev, or after,
// instead.
func (c *Core) EditPage(id string, editor int64, content string, rev int64) (left int64, err error) {
	defer catch(&err)
	edit, err := doc.DecodeEdit(content)
	if err != nil {
		return 0, err
	}
	c.q.do(func() {
		p := c.pages[id]
		if p == nil || p.editors[editor] == nil {
			err = errNotOpen
			return
		}
		if err = p.edit(p.editors[editor], edit, rev, len(content)); err == nil {
			c.fillLinks(id, p.cur)
			left = p.rev
		}
	})
	return left, err
}

// edit takes an editor's edit, made over rev, of about size bytes.
func (p *openPage) edit(e *pageEditor, edit *doc.Content, rev int64, size int) error {
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
		raw := p.encode(merged)
		p.bump(merged, len(raw))
		p.c.ev.pageUpdated(p.id, raw, p.rev, "merge", false)
	}
	// The editor goes on editing over rev until it hears of a newer one;
	// what came before rev it won't edit over again.
	e.last = rev
	e.saw(rev, snapshot(edit), size)
	for r := range e.seen {
		if r < rev {
			e.forget(r)
		}
	}
	p.live.Edited()
	return nil
}

// bump makes c the page's content at a new rev, which each of its editors
// may edit over once it hears of it: of about size bytes its own, which
// the content before it does not share.
func (p *openPage) bump(c *doc.Content, size int) {
	p.cur = c
	p.rev++
	seen := snapshot(c)
	for _, e := range p.editors {
		e.saw(p.rev, seen, size)
	}
}

// encode is c in JSON as the app's editors show it (doc.Content.Lifted),
// as Encode writes it: its top-level blocks the content last encoded had
// too are written as they were then, which they still are, as no one
// changes the page's blocks; the others are encoded.
func (p *openPage) encode(c *doc.Content) string {
	if l := c.Lifted(); l != c {
		p.coded = nil
		return l.Encode()
	}
	coded := make(map[*doc.Node][]byte, len(c.Children))
	size := 0
	for _, n := range c.Children {
		raw, ok := p.coded[n]
		if !ok {
			var err error
			if raw, err = json.Marshal(n); err != nil {
				panic(err)
			}
		}
		coded[n] = raw
		size += len(raw) + 1
	}
	blocks, deleted := times(c.Blocks), times(c.Deleted)
	var b strings.Builder
	b.Grow(size + len(blocks) + len(deleted) + 128)
	b.WriteString(`{"document":{"type":"page","id":"root","children":[`)
	for i, n := range c.Children {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(coded[n])
	}
	b.WriteString(`]},"meta":{"v":1,"blocks":`)
	b.Write(blocks)
	b.WriteString(`,"deleted":`)
	b.Write(deleted)
	b.WriteString(`}}`)
	p.coded = coded
	return b.String()
}

// times is a content's block times in JSON, as Encode writes them, none
// as {}.
func times(t map[string]int64) []byte {
	if t == nil {
		t = map[string]int64{}
	}
	raw, err := json.Marshal(t)
	if err != nil {
		panic(err)
	}
	return raw
}

// ClosePage closes an editor of an open page: the last to close it saves
// its edits and closes it. Edits that could not be saved are not dropped:
// the page stays open, with no editor, to try again shortly and as the
// app goes to the background, and closes once they are saved. Opened
// again before then, it shows them.
func (c *Core) ClosePage(id string, editor int64) {
	defer catch(nil)
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
	defer catch(nil)
	all := true
	c.q.do(func() {
		for _, p := range c.pages {
			if !p.live.Save() {
				all = false
			}
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
	raw := p.encode(c)
	p.bump(c, len(raw))
	p.c.ev.pageUpdated(p.id, raw, p.rev, "external", false)
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

// apply applies an agent's ops to copies of what they change, so ops that
// fail halfway change nothing (thawed). The app gets them agentEvery at
// the most.
func (p *openPage) apply(ops []doc.Op, newStep bool) error {
	c, made := p.cur, 0
	for _, op := range ops {
		var size int
		c, size = thawed(c, op)
		if err := c.Apply(op); err != nil {
			return err
		}
		made += size
	}
	// An undo step of its own begins after those waiting.
	if newStep {
		p.show()
	}
	p.cur, p.shown, p.newStep = c, false, p.newStep || newStep
	p.fresh += made
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
	raw := p.encode(p.cur)
	p.bump(p.cur, p.fresh)
	p.fresh = 0
	reason := "agent"
	if p.stepReason != "" {
		reason, p.stepReason = p.stepReason, ""
	}
	p.c.ev.pageUpdated(p.id, raw, p.rev, reason, newStep)
}

// thawed returns c for op to apply to in place without changing c: a
// content of its own that shares c's blocks, but those op changes the
// children of, which are copies, and the root's. Op changes only the
// children of the blocks it names and of the blocks those are in, or the
// top level's (doc.Content.Apply): the block it replaces, moves or
// deletes, the ones it deletes, and its position's anchor. It returns
// about how many bytes the copies and what op brings take.
func thawed(c *doc.Content, op doc.Op) (*doc.Content, int) {
	named := map[string]bool{}
	for _, id := range append([]string{op.ID}, op.IDs...) {
		if id != "" {
			named[id] = true
		}
	}
	if op.Position != nil && op.Position.Anchor() != "" {
		named[op.Position.Anchor()] = true
	}
	size := 0
	children, copied := thaw(c.Children, named, &size)
	if !copied {
		children = slices.Clone(c.Children)
		size += 8 * len(children)
	}
	for _, n := range op.Nodes {
		size += encodedSize(n)
	}
	if op.Node != nil {
		size += encodedSize(op.Node)
	}
	return &doc.Content{Children: children, Blocks: c.Blocks, Deleted: c.Deleted}, size
}

// thaw returns nodes with each of the named blocks, and each that holds
// one, copied with its children's list, adding about the bytes it copies
// to size; nodes itself, and false, when none of them is there.
func thaw(nodes []*doc.Node, named map[string]bool, size *int) ([]*doc.Node, bool) {
	var out []*doc.Node
	for i, n := range nodes {
		children, below := thaw(n.Children, named, size)
		if !below && !named[n.ID] {
			continue
		}
		if out == nil {
			out = slices.Clone(nodes)
			*size += 8 * len(out)
		}
		if !below {
			children = slices.Clone(n.Children)
			*size += 8 * len(children)
		}
		k := *n
		k.Children = children
		out[i] = &k
		*size += 64
	}
	return out, out != nil
}

// encodedSize is about how many bytes n takes, as JSON.
func encodedSize(n *doc.Node) int {
	raw, _ := json.Marshal(n)
	return len(raw)
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
func (c *Core) Members(pageID string) (_ string, err error) {
	defer catch(&err)
	var list []store.Member
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
