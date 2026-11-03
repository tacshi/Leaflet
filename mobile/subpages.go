package mobile

import (
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/live"
)

// A page's subpages show in it as cards, subpage blocks, which the core
// keeps in step as the desktop app does (core.Pages): a subpage made from
// the library gets a card at the end of its parent, and a page moving out
// of a parent leaves it, its card going with it. A page deleted takes its
// card with it, which Undo puts back.

// pageCore is the core as it changes pages, those open in the app through
// their live pages, so the editors show the change. Run on the core's
// queue.
func (c *Core) pageCore() core.Pages {
	return core.Pages{Core: c.core, Open: c.openLive, Notify: c.ev.notice}
}

// openLive returns page id's live page while the app has it open.
func (c *Core) openLive(id string) *live.Page {
	if p := c.pages[id]; p != nil {
		return p.live
	}
	return nil
}

// linkSubpageAfter puts a card for subpage id in page parent after the
// card for subpage after, as a copy's goes after its original's, or at the
// end without one. Run on the core's queue.
func (c *Core) linkSubpageAfter(parent, id, after string) error {
	return c.pageCore().LinkSubpage(parent, id, after)
}

// editContent changes page id's content with edit, which reports whether
// it changed it, as core.Pages.EditPage does. Run on the core's queue.
func (c *Core) editContent(id string, edit func(*doc.Content) bool) error {
	return c.pageCore().EditPage(id, edit)
}
