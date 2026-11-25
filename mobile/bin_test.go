package mobile

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func binList(t *testing.T, c *Core) []binRow {
	t.Helper()
	raw, err := c.BinItems()
	if err != nil {
		t.Fatal(err)
	}
	var rows []binRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func cardsKept(c *Core) int {
	n := 0
	c.q.do(func() { n = len(c.cards) })
	return n
}

// TestBinRestorePutsTheCardBack deletes a linked subpage and restores it from the bin in the same session: its card
// goes back where it was in the page above it, and where it was is then forgotten.
func TestBinRestorePutsTheCardBack(t *testing.T) {
	c, _ := open(t)
	parent := create(t, c, `{"title":"Trip"}`)
	first := create(t, c, `{"title":"Packing","link":true,"parent_id":"`+parent.ID+`"}`)
	second := create(t, c, `{"title":"Tickets","link":true,"parent_id":"`+parent.ID+`"}`)
	if _, err := c.DeletePage(first.ID); err != nil {
		t.Fatal(err)
	}
	if rows := binList(t, c); len(rows) != 1 || rows[0].ID != first.ID {
		t.Fatalf("bin %+v", rows)
	}
	if err := c.BinRestore("page", first.ID); err != nil {
		t.Fatal(err)
	}
	if got := links(t, c, parent.ID); !slices.Equal(got, []string{first.ID, second.ID}) {
		t.Errorf("parent links %v, want the card back first", got)
	}
	if n := cardsKept(c); n != 0 {
		t.Errorf("%d cards kept", n)
	}
}

// TestBinPurgeTakesTheDeletionItListed lists the bin, then has a page restored and deleted again, as another device
// would: deleting for good what was listed leaves the page in the bin, and what the bin lists then goes. Where a
// deleted card was is forgotten once its page is deleted for good.
func TestBinPurgeTakesTheDeletionItListed(t *testing.T) {
	c, _ := open(t)
	p := create(t, c, `{"title":"Plans"}`)
	other := create(t, c, `{"title":"Other"}`)
	for _, id := range []string{p.ID, other.ID} {
		if _, err := c.DeletePage(id); err != nil {
			t.Fatal(err)
		}
	}
	binList(t, c)
	st := c.core.Store
	if err := st.RestorePages([]string{p.ID}); err != nil {
		t.Fatal(err)
	}
	for now := time.Now().UnixMilli(); time.Now().UnixMilli() == now; {
		time.Sleep(time.Millisecond)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.BinPurge("page", p.ID); err != nil {
		t.Fatal(err)
	}
	if st.IsPurged(p.ID) {
		t.Fatal("a page deleted again since the bin was listed was deleted for good")
	}
	binList(t, c)
	if err := c.BinPurge("page", p.ID); err != nil {
		t.Fatal(err)
	}
	if !st.IsPurged(p.ID) {
		t.Error("the page as the bin listed it was not deleted for good")
	}
	if n := cardsKept(c); n != 1 {
		t.Errorf("%d cards kept, want the other page's", n)
	}
	if err := c.BinEmpty(); err != nil {
		t.Fatal(err)
	}
	if n := cardsKept(c); n != 0 || !st.IsPurged(other.ID) {
		t.Errorf("%d cards kept after the bin was emptied", n)
	}
}
