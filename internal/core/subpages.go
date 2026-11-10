package core

import (
	"slices"

	"leaflet/internal/doc"
	"leaflet/locales"
)

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

func (p Pages) LinkSubpageAt(parent, id string, at doc.Position) error {
	return p.linkSubpage(parent, id, func(*doc.Content) doc.Position { return at })
}

func (p Pages) linkSubpage(parent, id string, at func(c *doc.Content) doc.Position) error {
	return p.EditKeptPage(parent, func(c *doc.Content) bool {
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

func (p Pages) UnlinkSubpage(parent, id string) error {
	return p.EditKeptPage(parent, func(c *doc.Content) bool {
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

func (p Pages) MovePage(id, parent string, move func() error) error {
	_, err := p.MovePageAt(id, parent, nil, move)
	return err
}

func (p Pages) MovePageAt(id, parent string, at *doc.Position, move func() error) (left *doc.Position, err error) {
	old := ""
	if pg, err := p.Store.Page(id); err == nil {
		old = pg.ParentID
	}
	if old != "" && old != parent {
		if c := p.content(old); c != nil {
			if was, ok := c.SubpagePlace(id); ok {
				left = &was
			}
		}
	}
	if err := move(); err != nil || old == parent {
		return nil, err
	}
	if old != "" {
		if err := p.UnlinkSubpage(old, id); err != nil {
			p.notify(locales.AppErrorUnlinkSubpage(err.Error()))
		}
	}
	if parent != "" {
		var link error
		if at != nil {
			link = p.LinkSubpageAt(parent, id, *at)
		} else {
			link = p.LinkSubpage(parent, id, "")
		}
		if link != nil {
			p.notify(locales.AppErrorLinkSubpage(link.Error()))
		}
	}
	return left, nil
}

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
