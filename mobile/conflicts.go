package mobile

import (
	"errors"
	"fmt"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Settings › Sync and storage › Conflicts: the blocks two devices changed
// at once, as sync merged them, kept one way, for the user to settle
// another, as on the desktop.

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
	// device's is the one to put back (resolveOtherDevice), or the other
	// way (resolveThisDevice).
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
			kept, other := cf.LocalNode, cf.RemoteNode
			if cf.Kept == "remote" {
				kept, other = cf.RemoteNode, cf.LocalNode
			}
			r := conflictRow{ID: cf.ID, PageID: cf.PageID, PageTitle: c.titleOf(cf.PageID), Kept: textOf(kept), Other: textOf(other),
				KeptHere: cf.Kept != "remote"}
			switch {
			case cf.LocalNode == nil:
				r.Deleted = "here"
			case cf.RemoteNode == nil:
				r.Deleted = "there"
			}
			out = append(out, r)
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

// textOf is the start of a block's text, on one line, as the desktop's
// conflicts show it.
func textOf(n *doc.Node) string {
	if n == nil {
		return ""
	}
	rs := []rune(strings.Join(strings.Fields(n.Delta().Plain()), " "))
	if len(rs) > 40 {
		return string(rs[:39]) + "…"
	}
	return string(rs)
}

// How ResolveConflict settles a conflict: as merged ("keep": the newer
// version, or the block one device deleted kept), with this device's
// version put back, or the other device's, or with the block deleted
// again, for one the other device deleted.
const (
	resolveKeep        = "keep"
	resolveThisDevice  = "this_device"
	resolveOtherDevice = "other_device"
	resolveDelete      = "delete"
)

// ResolveConflict settles a conflict, as the desktop's Resolve menu does,
// how says: resolveKeep, resolveThisDevice, resolveOtherDevice or
// resolveDelete. A page not open keeps a version from before in its
// history first. One the user can no longer edit is put back nothing, and
// stays to resolve.
func (c *Core) ResolveConflict(id, how string) (err error) {
	defer catch(&err)
	// Any other how would delete the block.
	switch how {
	case resolveKeep, resolveThisDevice, resolveOtherDevice, resolveDelete:
	default:
		return fmt.Errorf("no way to resolve a conflict named %q", how)
	}
	c.q.do(func() {
		var rows []store.ConflictRow
		if rows, err = c.core.Store.Conflicts(); err != nil {
			return
		}
		var cf *store.ConflictRow
		for i := range rows {
			if rows[i].ID == id {
				cf = &rows[i]
			}
		}
		if cf == nil {
			err = errors.New(locales.SettingsConflictGone())
			return
		}
		if how != resolveKeep {
			if !c.canEdit(cf.PageID) {
				err = errors.New(locales.AppErrorReadOnly())
				return
			}
			node := map[string]*doc.Node{resolveThisDevice: cf.LocalNode, resolveOtherDevice: cf.RemoteNode}[how]
			err = c.putBack(*cf, node)
		}
		if err == nil {
			err = c.core.Store.ResolveConflict(cf.ID)
		}
	})
	if err != nil {
		return errors.New(locales.SettingsResolveFailed(err.Error()))
	}
	return nil
}

// putBack puts node, a version of a conflict's block, back, or with node
// nil deletes the block, on the queue.
func (c *Core) putBack(cf store.ConflictRow, node *doc.Node) error {
	if c.pages[cf.PageID] == nil {
		if content := c.core.Store.Content(cf.PageID); content != nil {
			if err := c.core.Store.AddHistory(cf.PageID, content, "conflict", "", locales.SettingsHistoryBeforeResolving()); err != nil {
				return err
			}
		}
	}
	return c.editContent(cf.PageID, func(content *doc.Content) bool {
		switch {
		case node == nil:
			content.Remove(cf.BlockID)
			if len(content.Children) == 0 {
				content.Children = doc.Empty().Children
			}
		case content.Find(cf.BlockID) != nil:
			_ = content.Replace(cf.BlockID, node.Clone())
		default:
			_ = content.Insert(doc.Position{At: "end"}, node.Clone())
		}
		return true
	})
}
