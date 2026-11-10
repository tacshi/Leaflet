package mobile

import (
	"errors"

	"leaflet/internal/core"
	"leaflet/internal/store"
	"leaflet/locales"
)

type conflictRow struct {
	ID        string `json:"id"`
	PageID    string `json:"page_id"`
	PageTitle string `json:"page_title"`
	Kept      string `json:"kept"`
	Other     string `json:"other"`
	Deleted   string `json:"deleted,omitempty"`
	KeptHere  bool   `json:"kept_here"`
}

func (c *Core) Conflicts() (_ string, err error) {
	defer catch(&err)
	var out []conflictRow
	c.q.do(func() {
		var rows []store.ConflictRow
		if rows, err = c.core.Store.Conflicts(); err != nil {
			return
		}
		out = make([]conflictRow, 0, len(rows))
		for _, cf := range rows {
			s := core.SidesOf(cf)
			out = append(out, conflictRow{ID: cf.ID, PageID: cf.PageID, PageTitle: c.titleOf(cf.PageID),
				Kept: s.Kept, Other: s.Other, Deleted: s.Deleted, KeptHere: s.KeptHere})
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

func (c *Core) ResolveConflict(id, how string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.pageCore().ResolveConflict(id, core.Resolution(how)) })
	if err != nil {
		return errors.New(locales.SettingsResolveFailed(err.Error()))
	}
	return nil
}
