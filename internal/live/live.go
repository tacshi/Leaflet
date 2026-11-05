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
	// Dirty runs as edits come to wait to be saved, and as they stop
	// waiting, however that comes: typing, a save the timer or anyone
	// else runs, or content that can't be read coming in. It runs on the
	// main thread.
	Dirty func(dirty bool)
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
	// change that is no edit (page mentions' new titles); loaded is nil
	// while it is stored decoded, as after a save (loadedContent). row is
	// the stored content exactly as the store has it ("" when it has none),
	// for a save to go only over it, and written the write it was
	// (store.WriteID), which tells whether it changed without reading it.
	loaded  *doc.Content
	stored  string
	row     string
	written int64
	dirty   bool
	timer   *time.Timer
	// unreadable is set while the stored content could not be read: the
	// page opens empty, or keeps what it showed when such content comes
	// in, read-only, and is never saved over.
	unreadable bool
	// removed is set once the page's content is no longer on this device,
	// as when its share was withdrawn: it has nothing to save over.
	removed  bool
	readOnly bool
	closed   bool
}

// Open opens a page, returning it and the content for the editor to start
// from, which Attach then gives it. Content that can't be read opens as an
// empty page, kept from being saved in its place.
func Open(st *store.Store, id string, opt Options) (*Page, *doc.Content) {
	row, written, _ := st.StoredContent(id)
	content, err := doc.Decode(row)
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
	p := &Page{ID: id, st: st, opt: opt, loaded: content.Copy(), stored: stored, row: row, written: written, unreadable: unreadable}
	return p, content
}

// loadedContent is the content the editor started from or last saved
// (loaded), decoded from stored the first time it is needed after a save.
func (p *Page) loadedContent() *doc.Content {
	if p.loaded == nil {
		p.loaded = doc.MustDecode(p.stored)
	}
	return p.loaded
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
func (p *Page) Changed() { p.setDirty(true) }

// setDirty says whether edits wait to be saved, telling Options.Dirty as
// that changes.
func (p *Page) setDirty(dirty bool) {
	if dirty == p.dirty {
		return
	}
	p.dirty = dirty
	if p.opt.Dirty != nil {
		p.opt.Dirty(dirty)
	}
}

// Change shows c, the content changed from outside the editor, as when a
// subpage's card comes or goes, a version is restored or a conflict
// resolved, and saves it at once. It is no undo step: undoing in the page
// would take back what was not done there. A page the user can't change
// takes nothing.
func (p *Page) Change(c *doc.Content) {
	if p.readOnly {
		return
	}
	p.ed.Replace(c)
	p.setDirty(true)
	p.Save()
}

// Edited marks the content as edited, and saves it shortly after the
// typing stops. A page the user can't change saves nothing.
func (p *Page) Edited() {
	if p.readOnly {
		return
	}
	p.setDirty(true)
	p.schedule()
}

// schedule saves the edits shortly after the typing stops.
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

// Save stores the edits. It reports whether nothing is left to save.
func (p *Page) Save() bool {
	if !p.dirty || p.unreadable || p.removed {
		p.setDirty(false)
		return true
	}
	// What was stored since, by another process, merges in first, so the
	// save keeps it. The save goes only over what merged: what is stored
	// in between merges on the next try instead of being written over.
	for range 5 {
		p.External()
		if p.unreadable || p.removed {
			// What was stored since can't be read, or is gone: it is not
			// saved over.
			return true
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		// An empty row saves only while the page still has none.
		content := p.ed.Doc()
		saved, err := p.st.SaveOver(p.ID, p.row, content)
		if errors.Is(err, store.ErrContentChanged) {
			continue
		}
		if err != nil {
			// The edits wait, still to be saved, for the next save or the
			// page closing.
			p.notify(locales.AppErrorSave(err.Error()))
			return false
		}
		// The store encoded what it stored, which is all this keeps: the
		// content saved is decoded again only if it is needed, which
		// typing on does not need.
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
	// Stored over each time, the edits wait for a save shortly after, as
	// the merge before each try stopped the one it scheduled.
	p.schedule()
	return false
}

// External brings content stored from outside the editor (sync, agent
// runs on the server, restores) into the page, merging what is being
// typed.
func (p *Page) External() {
	// The write stored tells, without reading the content, that it is
	// still the one the page has, as each save finds before it saves.
	if written, err := p.st.WriteID(p.ID); err == nil && written != 0 && written == p.written && p.row != "" {
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
	if row == p.row {
		p.written = written
		return
	}
	if p.unreadable || p.removed {
		// Content that can be read again, as a sync brought it, opens, as
		// does content back on this device, as a page shared again.
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
		// The side a conflict loses is kept for recovery before the
		// editor shows the merge.
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

// remove leaves the page once its content is no longer on this device, as
// when its share was withdrawn, the device left the account, or starter
// pages went as it joined a library: read-only, with nothing to save, as
// nothing is there to save over. Edits not saved are lost with the page,
// which the user is told.
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

// Doc is the content as it is open, with what is being typed.
func (p *Page) Doc() *doc.Content { return p.ed.Doc() }

// peeker is an editor whose content can be read without the read counting
// as what is being composed going out with it (editor.Editor.Peek).
type peeker interface{ Peek() *doc.Content }

// Peek is the content as it is open, with what is being typed, as Doc
// gives it, without its read counting as an editor's composition going out
// with the page (editor.Editor.Peek). An editor that keeps no such count
// gives its Doc.
func (p *Page) Peek() *doc.Content {
	if e, ok := p.ed.(peeker); ok {
		return e.Peek()
	}
	return p.ed.Doc()
}

// composer is an editor that numbers its input method's compositions, for
// an agent run's ops to give back the one it read the page with
// (editor.Editor.Composition).
type composer interface {
	Composition() (id uint64, out bool)
	ApplyOpFrom(op doc.Op, newStep bool, read uint64) error
}

// Composition is what is being composed in the open page, by its number,
// and whether it has gone out with the page, as editor.Editor.Composition
// tells them; none in an editor that numbers no compositions.
func (p *Page) Composition() (id uint64, out bool) {
	if e, ok := p.ed.(composer); ok {
		return e.Composition()
	}
	return 0, false
}

// ApplyOp applies an agent's op to the open page, starting a new undo
// step with newStep or joining the one before.
func (p *Page) ApplyOp(op doc.Op, newStep bool) error { return p.ed.ApplyOp(op, newStep) }

// ApplyOpFrom is ApplyOp of an op written from the page as read while the
// composition numbered read was in it, as editor.Editor.ApplyOpFrom takes
// it; ApplyOp in an editor that numbers no compositions.
func (p *Page) ApplyOpFrom(op doc.Op, newStep bool, read uint64) error {
	if e, ok := p.ed.(composer); ok {
		return e.ApplyOpFrom(op, newStep, read)
	}
	return p.ed.ApplyOp(op, newStep)
}

// ApplyOps applies an agent run's ops to the open page as one undo step.
func (p *Page) ApplyOps(ops []doc.Op) error { return p.ed.ApplyOps(ops) }
