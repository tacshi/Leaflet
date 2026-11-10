package mobile

import (
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/live"
)

func (c *Core) pageCore() core.Pages {
	return core.Pages{Core: c.core, Open: c.openLive, Notify: c.ev.notice, Step: func(pageID, blockID string) {
		if p := c.pages[pageID]; p != nil {
			p.stepReason = "notes:" + blockID
		}
	}}
}

func (c *Core) openLive(id string) *live.Page {
	if p := c.pages[id]; p != nil {
		return p.live
	}
	return nil
}

func (c *Core) linkSubpageAfter(parent, id, after string) error {
	return c.pageCore().LinkSubpage(parent, id, after)
}

func (c *Core) editContent(id string, edit func(*doc.Content) bool) error {
	return c.pageCore().EditPage(id, edit)
}
