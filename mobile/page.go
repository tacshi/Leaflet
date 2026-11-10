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

type openPage struct {
	c              *Core
	id             string
	live           *live.Page
	cur            *doc.Content
	rev            int64
	editors        map[int64]*pageEditor
	shown, newStep bool
	shownAt        time.Time
	showing        *time.Timer
	stepReason     string
	retry          time.Duration
	fresh          int
	coded          map[*doc.Node][]byte
}

type pageEditor struct {
	seen  map[int64]*doc.Content
	sizes map[int64]int
	last  int64
}

const (
	keepRevs  = 32
	keepBytes = 32 << 20
)

func snapshot(c *doc.Content) *doc.Content {
	return &doc.Content{Children: c.Children, Blocks: c.Blocks, Deleted: c.Deleted}
}

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

func (e *pageEditor) forget(rev int64) {
	delete(e.seen, rev)
	delete(e.sizes, rev)
}

type openedPage struct {
	ID         string       `json:"id"`
	Editor     int64        `json:"editor"`
	Content    *doc.Content `json:"content"`
	Rev        int64        `json:"rev"`
	ReadOnly   bool         `json:"read_only"`
	Unreadable bool         `json:"unreadable"`
}

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
				Post: func(fn func()) {
					c.q.post(func() {
						fn()
						p.retryIfUnused()
						p.closeIfUnused()
					})
				},
				CanEdit: func() bool { return c.canEdit(id) },
				Notify:  c.ev.notice,
				Saved:   c.core.Sync.KickPush,
				Dirty:   func(saving bool) { c.ev.pageSaving(id, saving) },
			})
			p.live.Attach(p)
			p.live.SetReadOnly(!c.canEdit(id) || p.live.Unreadable())
			c.pages[id] = p
			c.core.Sync.SetOpen(id, true)
			c.loadMembers(id)
		}
		c.editors++
		c.fillLinks(id, p.cur)
		raw, err = encode(openedPage{ID: id, Editor: c.editors, Content: p.cur.Lifted(), Rev: p.rev, ReadOnly: p.live.ReadOnly(), Unreadable: p.live.Unreadable()})
		p.editors[c.editors] = &pageEditor{seen: map[int64]*doc.Content{p.rev: snapshot(p.cur)}, sizes: map[int64]int{p.rev: len(raw)}, last: p.rev}
	})
	return raw, err
}

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

func (p *openPage) edit(e *pageEditor, edit *doc.Content, rev int64, size int) error {
	if rev > p.rev {
		return fmt.Errorf("the edit is over rev %d, after the page's %d", rev, p.rev)
	}
	if p.live.ReadOnly() {
		return nil
	}
	p.show()
	if rev == p.rev && len(p.editors) == 1 {
		p.cur = edit
	} else {
		base := e.base(rev)
		local := edit.Stamped(base, time.Now().UnixMilli())
		merged, conflicts := doc.Merge(base, local, p.cur)
		for _, cf := range conflicts {
			if err := p.c.core.Store.AddConflict(p.id, cf); err != nil {
				p.c.ev.notice(locales.AppErrorSave(err.Error()))
			}
		}
		raw := p.encode(merged)
		p.bump(merged, len(raw))
		p.c.ev.pageUpdated(p.id, raw, p.rev, "merge", false)
	}
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

func (p *openPage) bump(c *doc.Content, size int) {
	p.cur = c
	p.rev++
	seen := snapshot(c)
	for _, e := range p.editors {
		e.saw(p.rev, seen, size)
	}
}

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

const (
	retryFirst = 5 * time.Second
	retryMost  = 2 * time.Minute
)

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

func (p *openPage) closeIfUnused() {
	if len(p.editors) > 0 || p.live.Dirty() || p.c.pages[p.id] != p {
		return
	}
	p.live.Close()
	delete(p.c.pages, p.id)
	p.c.core.Sync.SetOpen(p.id, false)
}

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

func (c *Core) external(id string) {
	if p := c.pages[id]; p != nil {
		p.live.External()
	}
}

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

const agentEvery = 150 * time.Millisecond

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
	p.live.Edited()
	return nil
}

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

func encodedSize(n *doc.Node) int {
	raw, _ := json.Marshal(n)
	return len(raw)
}

type member struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	Accepted    bool   `json:"accepted"`
}

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
