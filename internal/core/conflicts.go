package core

import (
	"errors"
	"fmt"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Settings › Conflicts: the blocks two devices changed at once, as sync
// merged them, kept one way, for the user to settle another.

// ConflictSides is a conflict as Conflicts lists it.
type ConflictSides struct {
	// Kept is the start of the block's text as merged, Other the side's
	// that was not kept: "" when Deleted names that side, "here" or
	// "there", as the block one device deleted and the other edited.
	Kept, Other, Deleted string
	// KeptHere is whether the version kept is this device's, so the other
	// device's is the one to put back (UseOtherDevice), or the other way
	// (UseThisDevice).
	KeptHere bool
}

// SidesOf is conflict cf as Conflicts lists it.
func SidesOf(cf store.ConflictRow) ConflictSides {
	kept, other := cf.LocalNode, cf.RemoteNode
	if cf.Kept == "remote" {
		kept, other = cf.RemoteNode, cf.LocalNode
	}
	s := ConflictSides{Kept: textOf(kept), Other: textOf(other), KeptHere: cf.Kept != "remote"}
	switch {
	case cf.LocalNode == nil:
		s.Deleted = "here"
	case cf.RemoteNode == nil:
		s.Deleted = "there"
	}
	return s
}

// textOf is the start of a block's text, on one line.
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

// Resolution is how ResolveConflict settles a conflict.
type Resolution string

// The ways to settle a conflict: as merged (the newer version, or the
// block one device deleted kept), with this device's version put back, or
// the other device's, or with the block deleted again, for one the other
// device deleted.
const (
	KeepMerged     Resolution = "keep"
	UseThisDevice  Resolution = "this_device"
	UseOtherDevice Resolution = "other_device"
	DeleteBlock    Resolution = "delete"
)

// ResolveConflict settles conflict id as how says. The page keeps a version
// from before in its history first, its edits saved, when its block
// changes, open or not. One the user can no longer edit is put back
// nothing, and stays to resolve. A page whose content this device doesn't
// keep takes the block as an empty page, as a subpage's card goes into
// one, rather than the version chosen being lost.
func (p Pages) ResolveConflict(id string, how Resolution) error {
	// Any other how would delete the block.
	switch how {
	case KeepMerged, UseThisDevice, UseOtherDevice, DeleteBlock:
	default:
		return fmt.Errorf("no way to resolve a conflict named %q", how)
	}
	rows, err := p.Store.Conflicts()
	if err != nil {
		return err
	}
	i := -1
	for k := range rows {
		if rows[k].ID == id {
			i = k
		}
	}
	if i < 0 {
		return errors.New(locales.SettingsConflictGone())
	}
	cf := rows[i]
	if how != KeepMerged {
		if !p.canEdit(cf.PageID) {
			return errors.New(locales.AppErrorReadOnly())
		}
		node := map[Resolution]*doc.Node{UseThisDevice: cf.LocalNode, UseOtherDevice: cf.RemoteNode}[how]
		if err := p.putBack(cf, node); err != nil {
			return err
		}
	}
	return p.Store.ResolveConflict(cf.ID)
}

// putBack puts node, a version of a conflict's block, back, or with node
// nil deletes the block.
func (p Pages) putBack(cf store.ConflictRow, node *doc.Node) error {
	p.save(cf.PageID)
	if c := p.Store.Content(cf.PageID); c != nil {
		if err := p.Store.AddHistory(cf.PageID, c, "conflict", "", locales.SettingsHistoryBeforeResolving()); err != nil {
			return err
		}
	}
	return p.EditPage(cf.PageID, func(c *doc.Content) bool {
		switch {
		case node == nil:
			c.Remove(cf.BlockID)
			if len(c.Children) == 0 {
				c.Children = doc.Empty().Children
			}
		case c.Find(cf.BlockID) != nil:
			_ = c.Replace(cf.BlockID, node.Clone())
		default:
			_ = c.Insert(doc.Position{At: "end"}, node.Clone())
		}
		return true
	})
}
