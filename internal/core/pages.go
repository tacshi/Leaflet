package core

import (
	"errors"
	"log"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/live"
	"leaflet/locales"
)

var _ agents.Pages = Pages{}

type Pages struct {
	*Core
	Open   func(id string) *live.Page
	Notify func(message string)
	Step   func(pageID, blockID string)
}

func (p Pages) open(id string) *live.Page {
	if p.Open == nil {
		return nil
	}
	if lp := p.Open(id); lp != nil && !lp.Unreadable() {
		return lp
	}
	return nil
}

func (p Pages) content(id string) *doc.Content {
	if lp := p.open(id); lp != nil {
		return lp.Doc()
	}
	return p.Store.Content(id)
}

func (p Pages) save(id string) bool {
	if p.Open == nil {
		return true
	}
	if lp := p.Open(id); lp != nil {
		return lp.Save()
	}
	return true
}

func errNotSaved() error { return errors.New(locales.AppErrorNotSaved()) }

func (p Pages) canEdit(id string) bool {
	pg, err := p.Store.Page(id)
	return err == nil && p.Store.CanEdit(pg)
}

func (p Pages) notify(message string) {
	log.Print(message)
	if p.Notify != nil {
		p.Notify(message)
	}
}

func (p Pages) EditPage(id string, edit func(c *doc.Content) bool) error {
	return p.editPage(id, false, edit)
}

// EditKeptPage edits a page only if its content is on this device: an edit
// of the empty document standing in for content that is not would be saved
// as the page.
func (p Pages) EditKeptPage(id string, edit func(c *doc.Content) bool) error {
	return p.editPage(id, true, edit)
}

func (p Pages) editPage(id string, onlyKept bool, edit func(c *doc.Content) bool) error {
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
	err := p.Store.UpdateContent(id, func(c *doc.Content, kept bool) (bool, error) {
		changed = (kept || !onlyKept) && edit(c)
		return changed, nil
	})
	if err == nil && changed && p.Sync != nil {
		p.Sync.KickPush()
	}
	return err
}
