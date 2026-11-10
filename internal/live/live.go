package live

import (
	"errors"
	"log"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

type Editor interface {
	Doc() *doc.Content
	Replace(c *doc.Content)
	SetReadOnly(readOnly bool)
	ApplyOp(op doc.Op, newStep bool) error
	ApplyOps(ops []doc.Op) error
}

type Options struct {
	Post    func(fn func())
	CanEdit func() bool
	Notify  func(message string)
	Saved   func()
	Dirty   func(dirty bool)
}

const SaveDelay = 700 * time.Millisecond

type Page struct {
	ID string

	st  *store.Store
	opt Options
	ed  Editor

	loaded     *doc.Content
	stored     string
	row        string
	written    int64
	dirty      bool
	timer      *time.Timer
	unreadable bool
	removed    bool
	readOnly   bool
	closed     bool
}

func Open(st *store.Store, id string, opt Options) (*Page, *doc.Content) {
	row, written, _ := st.StoredContent(id)
	content, err := doc.Decode(row)
	unreadable := err != nil
	if unreadable {
		log.Printf("page %s: %v", id, err)
		content = doc.Empty()
	}
	stored := content.Encode()
	content.RefreshPageMentions(func(id string) (string, bool) {
		p, err := st.Page(id)
		return p.DisplayTitle(), err == nil
	})
	p := &Page{ID: id, st: st, opt: opt, loaded: content.Copy(), stored: stored, row: row, written: written, unreadable: unreadable}
	return p, content
}

func (p *Page) loadedContent() *doc.Content {
	if p.loaded == nil {
		p.loaded = doc.MustDecode(p.stored)
	}
	return p.loaded
}

func (p *Page) Attach(ed Editor) { p.ed = ed }

func (p *Page) Close() { p.closed = true }

func (p *Page) Unreadable() bool { return p.unreadable }

func (p *Page) Stored() bool { return !p.dirty && !p.unreadable && !p.removed }

func (p *Page) Dirty() bool { return p.dirty }

func (p *Page) ReadOnly() bool { return p.readOnly }

func (p *Page) SetReadOnly(readOnly bool) {
	p.readOnly = readOnly
	if p.ed != nil {
		p.ed.SetReadOnly(readOnly)
	}
}

func (p *Page) Changed() { p.setDirty(true) }

func (p *Page) setDirty(dirty bool) {
	if dirty == p.dirty {
		return
	}
	p.dirty = dirty
	if p.opt.Dirty != nil {
		p.opt.Dirty(dirty)
	}
}

func (p *Page) Change(c *doc.Content) bool {
	if p.readOnly {
		return false
	}
	p.ed.Replace(c)
	p.setDirty(true)
	return p.Save()
}

func (p *Page) Edited() {
	if p.readOnly {
		return
	}
	p.setDirty(true)
	p.schedule()
}

func (p *Page) schedule() {
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(SaveDelay, func() {
		p.post(func() {
			if !p.closed {
				p.Save()
			}
		})
	})
}

func (p *Page) post(fn func()) {
	if p.opt.Post != nil {
		p.opt.Post(fn)
		return
	}
	fn()
}

func (p *Page) notify(message string) {
	if p.opt.Notify != nil {
		p.opt.Notify(message)
	}
}

func (p *Page) Save() bool {
	if !p.dirty || p.unreadable || p.removed {
		p.setDirty(false)
		return true
	}
	for range 5 {
		p.External()
		if p.unreadable || p.removed {
			return true
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		content := p.ed.Doc()
		saved, err := p.st.SaveOver(p.ID, p.row, content)
		if errors.Is(err, store.ErrContentChanged) {
			continue
		}
		if err != nil {
			p.notify(locales.AppErrorSave(err.Error()))
			return false
		}
		content.Blocks, content.Deleted = saved.Content.Blocks, saved.Content.Deleted
		p.loaded, p.stored = nil, saved.Encoded
		p.row, p.written = saved.Encoded, saved.WriteID
		p.setDirty(false)
		if saved.Changed {
			_ = p.st.MaybeAutosaveEncoded(p.ID, func() string { return saved.Encoded })
			if p.opt.Saved != nil {
				p.opt.Saved()
			}
		}
		return true
	}
	p.schedule()
	return false
}

func (p *Page) External() {
	if written, err := p.st.WriteID(p.ID); err == nil && written != 0 && written == p.written && p.row != "" && !p.removed {
		return
	}
	row, written, err := p.st.StoredContent(p.ID)
	if store.IsNotFound(err) && p.row != "" {
		p.remove()
		return
	}
	if err != nil {
		return
	}
	if row == p.row && !p.removed {
		p.written = written
		return
	}
	if p.unreadable || p.removed {
		c, err := doc.Decode(row)
		if err != nil {
			p.removed = false
			p.unreadable = true
			return
		}
		p.unreadable, p.removed = false, false
		p.SetReadOnly(p.opt.CanEdit != nil && !p.opt.CanEdit())
		p.ed.Replace(c.Copy())
		p.loaded, p.stored, p.row, p.written = c, c.Encode(), row, written
		return
	}
	if row == p.stored {
		p.row, p.written = row, written
		return
	}
	stored, err := doc.Decode(row)
	if err != nil {
		log.Printf("page %s: %v", p.ID, err)
		if p.dirty {
			if err := p.st.AddHistory(p.ID, p.ed.Doc(), "autosave", "", "Autosave"); err != nil {
				p.notify(locales.AppErrorSave(err.Error()))
			}
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		p.unreadable, p.row, p.written = true, row, written
		p.setDirty(false)
		p.SetReadOnly(true)
		return
	}
	encoded := stored.Encode()
	if encoded == p.stored {
		p.row, p.written = row, written
		return
	}
	if p.dirty {
		local := p.ed.Doc().Stamped(p.loadedContent(), doc.NowMs())
		merged, conflicts := doc.Merge(doc.MustDecode(p.stored), local, stored)
		for _, c := range conflicts {
			if err := p.st.AddConflict(p.ID, c); err != nil {
				p.notify(locales.AppErrorSave(err.Error()))
			}
		}
		p.ed.Replace(merged)
		p.loaded, p.stored, p.row, p.written = nil, encoded, row, written
		p.Edited()
		return
	}
	p.ed.Replace(stored.Copy())
	p.loaded, p.stored, p.row, p.written = stored, encoded, row, written
}

func (p *Page) remove() {
	if p.timer != nil {
		p.timer.Stop()
	}
	if p.dirty {
		p.notify(locales.AppPageRemoved())
	}
	p.removed = true
	p.setDirty(false)
	p.SetReadOnly(true)
}

func (p *Page) Doc() *doc.Content { return p.ed.Doc() }

type peeker interface{ Peek() *doc.Content }

func (p *Page) Peek() *doc.Content {
	if e, ok := p.ed.(peeker); ok {
		return e.Peek()
	}
	return p.ed.Doc()
}

type composer interface {
	Composition() (id uint64, out bool)
	ApplyOpFrom(op doc.Op, newStep bool, read uint64) error
}

func (p *Page) Composition() (id uint64, out bool) {
	if e, ok := p.ed.(composer); ok {
		return e.Composition()
	}
	return 0, false
}

func (p *Page) ApplyOp(op doc.Op, newStep bool) error { return p.ed.ApplyOp(op, newStep) }

func (p *Page) ApplyOpFrom(op doc.Op, newStep bool, read uint64) error {
	if e, ok := p.ed.(composer); ok {
		return e.ApplyOpFrom(op, newStep, read)
	}
	return p.ed.ApplyOp(op, newStep)
}

func (p *Page) ApplyOps(ops []doc.Op) error { return p.ed.ApplyOps(ops) }
