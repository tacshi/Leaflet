package core

import (
	"errors"
	"log"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/live"
	"leaflet/locales"
)

// Agents change pages as the app has them, through Pages.
var _ agents.Pages = Pages{}

// Pages changes pages as the app has them, the same on the desktop and the
// phone: a page the app has open through its live page, so the editor
// shows the change as one from outside it, and the others over their
// stored content. It runs where the app touches its open pages: the
// desktop's main thread, the iOS app's core queue.
type Pages struct {
	*Core
	// Open returns page id as the app has it open, or nil while it isn't.
	Open func(id string) *live.Page
	// Notify tells the user, in words for them, what could not be done
	// besides what was asked, as a card that could not be put in a page
	// or taken out of it as its subpage moved: the page moved all the
	// same.
	Notify func(message string)
	// Step, on an app that keeps its own undo steps, is told a block of an
	// open page was written apart from its editor, as a step of its own; it
	// may be nil.
	Step func(pageID, blockID string)
}

// open returns page id as the app has it open, when its content could be
// read: one whose content couldn't has none to change, and what changes it
// reads the stored content, and leaves it.
func (p Pages) open(id string) *live.Page {
	if p.Open == nil {
		return nil
	}
	if lp := p.Open(id); lp != nil && !lp.Unreadable() {
		return lp
	}
	return nil
}

// content returns page id's content as the app has it: as its open page
// shows it, or as stored, or nil when this device keeps none or can't read
// it.
func (p Pages) content(id string) *doc.Content {
	if lp := p.open(id); lp != nil {
		return lp.Doc()
	}
	return p.Store.Content(id)
}

// save saves the edits of page id, while the app has it open. It reports
// whether none are left to save; when some are, the page told the user
// why, and what reads the stored content would read it without them.
func (p Pages) save(id string) bool {
	if p.Open == nil {
		return true
	}
	if lp := p.Open(id); lp != nil {
		return lp.Save()
	}
	return true
}

// errNotSaved stops what would go on from the stored content of a page
// open in the app whose edits could not be saved; the page said why. It
// is made as it is returned, to be in the language the app shows then.
func errNotSaved() error { return errors.New(locales.AppErrorNotSaved()) }

// canEdit reports whether the user may change page id.
func (p Pages) canEdit(id string) bool {
	pg, err := p.Store.Page(id)
	return err == nil && p.Store.CanEdit(pg)
}

// notify tells the user message, and logs it.
func (p Pages) notify(message string) {
	log.Print(message)
	if p.Notify != nil {
		p.Notify(message)
	}
}

// EditPage changes page id's content with edit, which reports whether it
// changed it: through the page while the app has it open, or else over its
// stored content, an empty page when this device keeps none, saved only
// over what edit was given (store.UpdateContent). A page the user can't
// change is left as it is. An open page whose change could not be saved
// shows it still to be saved, and that is an error: what goes on from the
// stored content would go on without it.
func (p Pages) EditPage(id string, edit func(c *doc.Content) bool) error {
	if !p.canEdit(id) {
		return nil
	}
	if lp := p.open(id); lp != nil {
		c := lp.Doc().Copy()
		if edit(c) && !lp.Change(c) {
			return errNotSaved()
		}
		return nil
	}
	changed := false
	err := p.Store.UpdateContent(id, func(c *doc.Content, _ bool) (bool, error) {
		changed = edit(c)
		return changed, nil
	})
	// A save goes up soon; what changed elsewhere comes with the passes.
	if err == nil && changed && p.Sync != nil {
		p.Sync.KickPush()
	}
	return err
}
