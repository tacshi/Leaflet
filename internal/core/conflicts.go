package core

import (
	"errors"
	"fmt"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

type ConflictSides struct {
	Kept, Other, Deleted string
	KeptHere             bool
}

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

type Resolution string

const (
	KeepMerged     Resolution = "keep"
	UseThisDevice  Resolution = "this_device"
	UseOtherDevice Resolution = "other_device"
	DeleteBlock    Resolution = "delete"
)

func (p Pages) ResolveConflict(id string, how Resolution) error {
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

func (p Pages) putBack(cf store.ConflictRow, node *doc.Node) error {
	if !p.save(cf.PageID) {
		return errNotSaved()
	}
	if c := p.Store.Content(cf.PageID); c != nil {
		if err := p.Store.AddHistory(cf.PageID, c, "conflict", "", locales.SettingsHistoryBeforeResolving()); err != nil {
			return err
		}
	}
	return p.EditPage(cf.PageID, func(c *doc.Content) bool {
		switch cur := c.Find(cf.BlockID); {
		case node == nil:
			c.Remove(cf.BlockID)
			if len(c.Children) == 0 {
				c.Children = doc.Empty().Children
			}
		case cur != nil:
			n := node.Clone()
			if n.Type != "table" && cur.Type != "table" {
				n.Children = cur.Children
			}
			_ = c.Replace(cf.BlockID, n)
			c.Children = c.Lifted().Children
		default:
			_ = c.Apply(doc.InsertOp(doc.Position{At: "end"}, node.Clone()))
		}
		return true
	})
}
