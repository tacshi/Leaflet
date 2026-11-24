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
	c.q.do(func() { items, err = c.core.Store.Bin() })
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

// BinRestore takes a page, with the pages deleted with it, or a file out of the bin.
func (c *Core) BinRestore(kind, id string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.pageCore().RestoreFromBin(store.BinRef{Kind: store.BinKind(kind), ID: id}) })
	return err
}

// BinPurge deletes a page or a file of the bin for good.
func (c *Core) BinPurge(kind, id string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.PurgeBin(store.BinRef{Kind: store.BinKind(kind), ID: id}) })
	return err
}

// BinEmpty deletes everything in the bin for good.
func (c *Core) BinEmpty() (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.EmptyBin() })
	return err
}

// TrashFile puts a file in the bin.
func (c *Core) TrashFile(id string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.pageCore().TrashFile(id) })
	return err
}
