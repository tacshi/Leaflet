package core

import (
	"slices"

	"leaflet/internal/doc"
	"leaflet/locales"
)

// A page's subpages show in it as cards, subpage blocks, which are kept in
// step with them: a subpage made from the sidebar or the library gets a
// card at the end of its parent, and a page moving out of a parent leaves
// it, its card going with it. A page deleted takes its card with it, which
// Undo puts back where it was.

// LinkSubpage puts a card for subpage id in page parent, after the card for
// subpage after, as a copy's goes after its original's, or at the end
// without one; unless parent has a card for it already.
func (p Pages) LinkSubpage(parent, id, after string) error {
	return p.linkSubpage(parent, id, func(c *doc.Content) doc.Position {
		at := doc.Position{At: "end"}
		c.Walk(func(n, _ *doc.Node) {
			if after != "" && n.Type == "subpage" && n.Str("page_id") == after {
				at = doc.Position{After: n.ID}
			}
		})
		return at
	})
}

// LinkSubpageAt puts a card for subpage id in page parent at at, or at the
// end when that place is gone; unless parent has a card for it already.
func (p Pages) LinkSubpageAt(parent, id string, at doc.Position) error {
	return p.linkSubpage(parent, id, func(*doc.Content) doc.Position { return at })
}

func (p Pages) linkSubpage(parent, id string, at func(c *doc.Content) doc.Position) error {
	return p.EditPage(parent, func(c *doc.Content) bool {
		if _, ok := c.SubpagePlace(id); ok {
			return false
		}
		link := doc.SubpageNode(id)
		if c.Insert(at(c), link) == nil {
			return true
		}
		return c.Insert(doc.Position{At: "end"}, link) == nil
	})
}

// UnlinkSubpage takes the cards for subpage id out of page parent, which is
// saved only when it had one. A page whose only block was the card keeps
// one to type in.
func (p Pages) UnlinkSubpage(parent, id string) error {
	return p.EditPage(parent, func(c *doc.Content) bool {
		var links []string
		c.Walk(func(n, _ *doc.Node) {
			if n.Type == "subpage" && n.Str("page_id") == id {
				links = append(links, n.ID)
			}
		})
		for _, l := range links {
			c.Remove(l)
		}
		if len(links) > 0 && len(c.Children) == 0 {
			c.Children = doc.Empty().Children
		}
		return len(links) > 0
	})
}

// MovePage moves page id under page parent, or to the top level, by move,
// and keeps its cards in step: a parent it leaves loses its card, and one
// it goes into gets one at its end. It returns move's error; a card that
// could not be changed the user is told of, as the page moved all the
// same.
func (p Pages) MovePage(id, parent string, move func() error) error {
	old := ""
	if pg, err := p.Store.Page(id); err == nil {
		old = pg.ParentID
	}
	if err := move(); err != nil || old == parent {
		return err
	}
	if old != "" {
		if err := p.UnlinkSubpage(old, id); err != nil {
			p.notify(locales.AppErrorUnlinkSubpage(err.Error()))
		}
	}
	if parent != "" {
		if err := p.LinkSubpage(parent, id, ""); err != nil {
			p.notify(locales.AppErrorLinkSubpage(err.Error()))
		}
	}
	return nil
}

// DeletePage deletes page id and its subpages, returning their IDs, the
// page's first, for RestorePages to bring them back, and takes the page's
// card out of its parent, rather than leave it to say the page is gone.
// card is where the card was, for RestorePages to put it back: the
// parent's end when it had none. The app saves the edits of the pages it
// has open first, for Undo to bring them back with them.
func (p Pages) DeletePage(id string) (ids []string, card doc.Position, err error) {
	card = doc.Position{At: "end"}
	parent := ""
	if pg, err := p.Store.Page(id); err == nil && pg.ParentID != "" {
		parent = pg.ParentID
		if c := p.content(parent); c != nil {
			if at, ok := c.SubpagePlace(id); ok {
				card = at
			}
		}
	}
	// The page goes first: one that could not be deleted keeps its card.
	if ids, err = p.Store.DeletePage(id); err != nil {
		return nil, card, err
	}
	if parent != "" {
		if err := p.UnlinkSubpage(parent, id); err != nil {
			p.notify(locales.AppErrorUnlinkSubpage(err.Error()))
		}
	}
	return ids, card, nil
}

// RestorePages brings back pages DeletePage deleted, given ids as it
// returned them, and puts the page's card back in its parent at card, or
// at the end when that place is gone, unless the parent came back with it.
func (p Pages) RestorePages(ids []string, card doc.Position) error {
	if err := p.Store.RestorePages(ids); err != nil || len(ids) == 0 {
		return err
	}
	pg, err := p.Store.Page(ids[0])
	if err != nil || pg.ParentID == "" || slices.Contains(ids, pg.ParentID) {
		return nil
	}
	if err := p.LinkSubpageAt(pg.ParentID, pg.ID, card); err != nil {
		p.notify(locales.AppErrorLinkSubpage(err.Error()))
	}
	return nil
}
