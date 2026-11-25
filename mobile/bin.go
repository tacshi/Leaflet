package mobile

import (
	"time"

	"leaflet/internal/core"
	"leaflet/internal/store"
)

// BinDays is how many days the bin keeps what is in it.
const BinDays = store.BinDays

type binRow struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Icon     string `json:"icon,omitempty"`
	When     string `json:"when"`
	Template bool   `json:"template"`
	Pages    int    `json:"pages"`
	Size     int64  `json:"size"`
}

// BinItems lists what was deleted and can still be restored, the latest deletion first.
func (c *Core) BinItems() (_ string, err error) {
	defer catch(&err)
	var items []store.BinItem
	c.q.do(func() {
		if items, err = c.core.Store.Bin(); err != nil {
			return
		}
		c.binListed = make(map[string]store.BinRef, len(items))
		for _, it := range items {
			c.binListed[binKey(string(it.Kind), it.ID)] = it.BinRef
		}
		c.forgetCards()
	})
	if err != nil {
		return "", err
	}
	now := time.Now()
	out := make([]binRow, 0, len(items))
	for _, it := range items {
		out = append(out, binRow{Kind: string(it.Kind), ID: it.ID, Title: it.Title, Icon: it.Icon, When: core.BinWhen(it, now),
			Template: it.Template, Pages: it.Pages, Size: it.File.SizeBytes})
	}
	return encode(out)
}

// BinRestore takes a page, with the pages deleted with it, or a file out of the bin. A page deleted here since the
// core opened goes back where its card was in the page above it.
func (c *Core) BinRestore(kind, id string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		ref := store.BinRef{Kind: store.BinKind(kind), ID: id}
		card, deletedHere := c.cards[id]
		if ref.Kind != store.BinPage || !deletedHere {
			err = c.pageCore().RestoreFromBin(ref)
			return
		}
		var ids []string
		if ids, err = c.core.Store.InBin(id); err != nil {
			return
		}
		if err = c.pageCore().RestorePages(ids, card); err != nil {
			return
		}
		delete(c.cards, id)
		if c.core.Sync != nil {
			c.core.Sync.KickPush()
		}
	})
	return err
}

// BinPurge deletes a page or a file of the bin for good, as BinItems last listed it: one restored since, or deleted
// again, is left as it is.
func (c *Core) BinPurge(kind, id string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		ref, ok := c.binListed[binKey(kind, id)]
		if !ok {
			if ref, err = c.inBin(kind, id); err != nil || ref.ID == "" {
				return
			}
		}
		err = c.core.PurgeBin(ref)
		c.forgetCards()
	})
	return err
}

// BinEmpty deletes everything in the bin for good.
func (c *Core) BinEmpty() (err error) {
	defer catch(&err)
	c.q.do(func() {
		err = c.core.EmptyBin()
		c.forgetCards()
	})
	return err
}

func binKey(kind, id string) string { return kind + ":" + id }

// inBin is the item of the bin as it is now, or none.
func (c *Core) inBin(kind, id string) (store.BinRef, error) {
	items, err := c.core.Store.Bin()
	for _, it := range items {
		if string(it.Kind) == kind && it.ID == id {
			return it.BinRef, err
		}
	}
	return store.BinRef{}, err
}

// forgetCards forgets where the cards were of the pages deleted here that are in the bin no more: restored elsewhere,
// deleted for good, or gone.
func (c *Core) forgetCards() {
	for id := range c.cards {
		if p, err := c.core.Store.Page(id); err != nil || p.DeletedAt.IsZero() || !p.PurgedAt.IsZero() {
			delete(c.cards, id)
		}
	}
}

// TrashFile puts a file in the bin.
func (c *Core) TrashFile(id string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.pageCore().TrashFile(id) })
	return err
}
