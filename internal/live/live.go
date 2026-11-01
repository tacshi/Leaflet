// Package live is a page open in the app: what is typed into it, saved
// shortly after the typing stops and only over the content it started
// from, and what is stored meanwhile from outside (sync, agent runs on the
// server, leaflet mcp, restores), merged into what is being typed. The
// desktop's editor and the iOS app's both open pages through it, and an
// agent run changes an open page through it (agents.LiveDoc).
package live

import (
	"errors"
	"log"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Editor shows the open page and takes the edits: the desktop's editor, or
// the iOS app's. Its methods run on the main thread.
type Editor interface {
	// Doc is the content as shown, with what is being typed.
	Doc() *doc.Content
	// Replace shows c, which comes from outside the editor, keeping the
	// caret where it can.
	Replace(c *doc.Content)
	// SetReadOnly keeps the user from changing the page, or lets them.
	SetReadOnly(readOnly bool)
	// ApplyOp applies an agent's op, starting a new undo step with
	// newStep or joining the one before.
	ApplyOp(op doc.Op, newStep bool) error
	// ApplyOps applies an agent run's ops as one undo step.
	ApplyOps(ops []doc.Op) error
}

// Options are what a page needs from the app.
type Options struct {
	// Post runs fn on the main thread, where the save shortly after the
	// typing stops runs.
	Post func(fn func())
	// CanEdit reports whether the user may change the page, as content
	// that can be read again opens.
	CanEdit func() bool
	// Notify tells the user, in words for them, that edits could not be
	// saved.
	Notify func(message string)
	// Saved runs after edits were stored, for sync to send them.
	Saved func()
}

// SaveDelay is how long after the typing stops the edits are saved.
const SaveDelay = 700 * time.Millisecond

// Page is a page open in the app. Its methods run on the main thread.
type Page struct {
	// ID is the page's.
	ID string

	st  *store.Store
	opt Options
	ed  Editor

	// loaded is the stored content the editor started from or last saved,
	// and stored that content as the store has it, encoded, before any
	// change that is no edit (page mentions' new titles). row is the
	// stored content exactly as the store has it ("" when it has none),
	// for a save to go only over it.
	loaded *doc.Content
	stored string
	row    string
	dirty  bool
	timer  *time.Timer
	// unreadable is set while the stored content could not be read: the
	// page opens empty, or keeps what it showed when such content comes
	// in, read-only, and is never saved over.
	unreadable bool
	readOnly   bool
	closed     bool
}

// Open opens a page, returning it and the content for the editor to start
// from, which Attach then gives it. Content that can't be read opens as an
// empty page, kept from being saved in its place.
func Open(st *store.Store, id string, opt Options) (*Page, *doc.Content) {
	row, _ := st.ContentRow(id)
	content, err := doc.Decode(row.Content)
	unreadable := err != nil
	if unreadable {
		log.Printf("page %s: %v", id, err)
		content = doc.Empty()
	}
	stored := content.Encode()
	// Mentions of pages show their titles now, as renamed since; this
	// is no edit, so it saves with the page's next one. The store has
	// the title the page just closed saved.
	content.RefreshPageMentions(func(id string) (string, bool) {
		p, err := st.Page(id)
		return p.DisplayTitle(), err == nil
	})
	p := &Page{ID: id, st: st, opt: opt, loaded: content.Copy(), stored: stored, row: row.Content, unreadable: unreadable}
	return p, content
}

// Attach gives the page the editor that shows it, made from the content
// Open returned.
func (p *Page) Attach(ed Editor) { p.ed = ed }

// Close closes the page, saved or not: a save still waiting for the typing
// to stop does not run.
func (p *Page) Close() { p.closed = true }

// Unreadable reports whether the stored content could not be read.
func (p *Page) Unreadable() bool { return p.unreadable }

// Dirty reports whether edits wait to be saved.
func (p *Page) Dirty() bool { return p.dirty }

// ReadOnly reports whether the user is kept from changing the page.
func (p *Page) ReadOnly() bool { return p.readOnly }

// SetReadOnly keeps the user from changing the page, or lets them.
func (p *Page) SetReadOnly(readOnly bool) {
	p.readOnly = readOnly
	if p.ed != nil {
		p.ed.SetReadOnly(readOnly)
	}
}

// Changed marks the content as edited, to be saved by the next Save.
func (p *Page) Changed() { p.dirty = true }

// Edited marks the content as edited, and saves it shortly after the
// typing stops. A page the user can't change saves nothing.
func (p *Page) Edited() {
	if p.readOnly {
		return
	}
	p.dirty = true
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

// Save stores the edits. It reports whether nothing is left to save.
func (p *Page) Save() bool {
	if !p.dirty || p.unreadable {
		return true
	}
	// What was stored since, by another process, merges in first, so the
	// save keeps it. The save goes only over what merged: what is stored
	// in between merges on the next try instead of being written over.
	for range 5 {
		p.External()
		if p.unreadable {
			// What was stored since can't be read: it is not saved over.
			return true
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		// An empty row saves only while the page still has none.
		content := p.ed.Doc()
		stamped, changed, err := p.st.SaveContentOver(p.ID, p.row, content)
		if errors.Is(err, store.ErrContentChanged) {
			continue
		}
		if err != nil {
			// The edits wait, still to be saved, for the next save or the
			// page closing.
			p.notify(locales.AppErrorSave(err.Error()))
			return false
		}
		p.dirty = false
		content.Blocks, content.Deleted = stamped.Blocks, stamped.Deleted
		p.loaded, p.stored = stamped.Copy(), stamped.Encode()
		if changed {
			p.row = p.stored
			_ = p.st.MaybeAutosave(p.ID, stamped)
			if p.opt.Saved != nil {
				p.opt.Saved()
			}
		}
		return true
	}
	// Stored over each time, the edits wait for the save that merging
	// them scheduled.
	return false
}

// External brings content stored from outside the editor (sync, agent
// runs on the server, restores) into the page, merging what is being
// typed.
func (p *Page) External() {
	row, err := p.st.ContentRow(p.ID)
	if err != nil || row.Content == p.row {
		return
	}
	if p.unreadable {
		// Content that can be read again, as a sync brought it, opens.
		c, err := doc.Decode(row.Content)
		if err != nil {
			return
		}
		p.unreadable = false
		p.SetReadOnly(p.opt.CanEdit != nil && !p.opt.CanEdit())
		p.ed.Replace(c.Copy())
		p.loaded, p.stored, p.row = c.Copy(), c.Encode(), row.Content
		return
	}
	stored, err := doc.Decode(row.Content)
	if err != nil {
		// Content this version can't read (damaged, or from a newer
		// Leaflet) is not shown as an empty page to be saved over: the
		// editor keeps what it has, read-only with the notice a page
		// opened so has, and is never saved over it. Edits not yet saved
		// are kept in the page's history.
		log.Printf("page %s: %v", p.ID, err)
		if p.dirty {
			if err := p.st.AddHistory(p.ID, p.ed.Doc(), "autosave", "", "Autosave"); err != nil {
				p.notify(locales.AppErrorSave(err.Error()))
			}
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		p.unreadable, p.dirty, p.row = true, false, row.Content
		p.SetReadOnly(true)
		return
	}
	if stored.Encode() == p.stored {
		p.row = row.Content
		return
	}
	if p.dirty {
		local := p.ed.Doc().Stamped(p.loaded, doc.NowMs())
		merged, conflicts := doc.Merge(doc.MustDecode(p.stored), local, stored)
		// The side a conflict loses is kept for recovery before the
		// editor shows the merge.
		for _, c := range conflicts {
			if err := p.st.AddConflict(p.ID, c); err != nil {
				p.notify(locales.AppErrorSave(err.Error()))
			}
		}
		p.ed.Replace(merged)
		p.loaded, p.stored, p.row = stored.Copy(), stored.Encode(), row.Content
		p.Edited()
		return
	}
	p.ed.Replace(stored.Copy())
	p.loaded, p.stored, p.row = stored.Copy(), stored.Encode(), row.Content
}

// Doc is the content as it is open, with what is being typed.
func (p *Page) Doc() *doc.Content { return p.ed.Doc() }

// ApplyOp applies an agent's op to the open page, starting a new undo
// step with newStep or joining the one before.
func (p *Page) ApplyOp(op doc.Op, newStep bool) error { return p.ed.ApplyOp(op, newStep) }

// ApplyOps applies an agent run's ops to the open page as one undo step.
func (p *Page) ApplyOps(ops []doc.Op) error { return p.ed.ApplyOps(ops) }
