package mobile

import (
	"errors"

	"leaflet/internal/core"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Settings › Sync and storage › Conflicts: the blocks two devices changed
// at once, as sync merged them, kept one way, for the user to settle
// another, as on the desktop (core.ResolveConflict).

// conflictRow is a conflict as Conflicts lists it.
type conflictRow struct {
	ID        string `json:"id"`
	PageID    string `json:"page_id"`
	PageTitle string `json:"page_title"`
	// Kept is the start of the block's text as merged, Other the side's
	// that was not kept: "" when Deleted names that side, "here" or
	// "there", as the block one device deleted and the other edited.
	Kept    string `json:"kept"`
	Other   string `json:"other"`
	Deleted string `json:"deleted,omitempty"`
	// KeptHere is whether the version kept is this device's, so the other
	// device's is the one to put back, or the other way.
	KeptHere bool `json:"kept_here"`
}

// Conflicts returns the conflicts not resolved yet, newest first, as a
// JSON array of conflictRow.
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

// ResolveConflict settles a conflict, as the desktop's Resolve menu does,
// how says: a core.Resolution, "keep", "this_device", "other_device" or
// "delete".
func (c *Core) ResolveConflict(id, how string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.pageCore().ResolveConflict(id, core.Resolution(how)) })
	if err != nil {
		return errors.New(locales.SettingsResolveFailed(err.Error()))
	}
	return nil
}
