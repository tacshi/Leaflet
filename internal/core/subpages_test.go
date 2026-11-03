package core

import (
	"slices"
	"testing"

	"leaflet/internal/doc"
)

// A card goes where it is put, or at the end of the page when that place
// has gone meanwhile, open or not; a page with a card for the subpage
// gets no second one.
func TestLinkSubpage(t *testing.T) {
	f := newPagesFixture(t)
	trip := f.page(t, "Trip", "", block("p1", "Plans"))
	packing := f.page(t, "Packing", trip.ID)
	tickets := f.page(t, "Tickets", trip.ID)
	gone := doc.Position{After: "gone"}
	if err := f.LinkSubpageAt(trip.ID, packing.ID, gone); err != nil {
		t.Fatal(err)
	}
	if got := f.cards(trip.ID); !slices.Equal(got, []string{packing.ID}) {
		t.Errorf("stored: cards %v", got)
	}
	f.openPage(trip.ID)
	if err := f.LinkSubpageAt(trip.ID, tickets.ID, gone); err != nil {
		t.Fatal(err)
	}
	if got := f.cards(trip.ID); !slices.Equal(got, []string{packing.ID, tickets.ID}) {
		t.Errorf("open: cards %v", got)
	}
	// A copy's goes after its original's.
	cp := f.page(t, "Packing copy", trip.ID)
	if err := f.LinkSubpage(trip.ID, cp.ID, packing.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.LinkSubpage(trip.ID, packing.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.cards(trip.ID); !slices.Equal(got, []string{packing.ID, cp.ID, tickets.ID}) {
		t.Errorf("cards %v", got)
	}
}

// Unlinking takes every card for the subpage, leaving a page whose only
// block was one a block to type in; a page with no card for it is left as
// it is, its editor given nothing.
func TestUnlinkSubpage(t *testing.T) {
	f := newPagesFixture(t)
	trip := f.page(t, "Trip", "")
	packing := f.page(t, "Packing", trip.ID)
	if _, _, err := f.st.SaveContent(trip.ID, doc.FromBlocks([]*doc.Node{doc.SubpageNode(packing.ID), doc.SubpageNode(packing.ID)})); err != nil {
		t.Fatal(err)
	}
	if err := f.UnlinkSubpage(trip.ID, packing.ID); err != nil {
		t.Fatal(err)
	}
	c := f.st.Content(trip.ID)
	if len(c.Children) != 1 || c.Children[0].Type != "paragraph" {
		t.Errorf("left %s", c.Encode())
	}

	ed := f.openPage(trip.ID)
	if err := f.UnlinkSubpage(trip.ID, packing.ID); err != nil || ed.replaced != 0 {
		t.Errorf("%v: the editor was given %d changes", err, ed.replaced)
	}
}

// Moving a page takes its card out of the parent it leaves and puts one
// at the end of the parent it goes into.
func TestMovePageKeepsItsCard(t *testing.T) {
	f := newPagesFixture(t)
	trip := f.page(t, "Trip", "")
	work := f.page(t, "Work", "", block("w1", "Tasks"))
	packing := f.page(t, "Packing", trip.ID)
	if err := f.LinkSubpage(trip.ID, packing.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.openPage(trip.ID)
	err := f.MovePage(packing.ID, work.ID, func() error { return f.st.MovePage(packing.ID, work.ID, "", "") })
	if err != nil {
		t.Fatal(err)
	}
	if got := f.cards(trip.ID); len(got) != 0 {
		t.Errorf("the old parent still has %v", got)
	}
	if c := f.st.Content(work.ID); len(c.Children) != 2 || c.Children[1].Str("page_id") != packing.ID {
		t.Errorf("the new parent is %s", c.Encode())
	}
}

// Deleting a page takes its card, which Undo puts back where it was, the
// parent open or not.
func TestDeletePageTakesItsCard(t *testing.T) {
	for _, open := range []bool{false, true} {
		f := newPagesFixture(t)
		trip := f.page(t, "Trip", "")
		packing := f.page(t, "Packing", trip.ID)
		tickets := f.page(t, "Tickets", trip.ID)
		under := f.page(t, "Socks", packing.ID)
		for _, id := range []string{packing.ID, tickets.ID} {
			if err := f.LinkSubpage(trip.ID, id, ""); err != nil {
				t.Fatal(err)
			}
		}
		if open {
			f.openPage(trip.ID)
		}
		ids, card, err := f.DeletePage(packing.ID)
		if err != nil || !slices.Equal(ids, []string{packing.ID, under.ID}) {
			t.Fatalf("open %v: deleted %v, %v", open, ids, err)
		}
		if got := f.cards(trip.ID); !slices.Equal(got, []string{tickets.ID}) {
			t.Errorf("open %v: cards after delete %v", open, got)
		}
		if err := f.RestorePages(ids, card); err != nil {
			t.Fatal(err)
		}
		if got := f.cards(trip.ID); !slices.Equal(got, []string{packing.ID, tickets.ID}) {
			t.Errorf("open %v: cards after undo %v", open, got)
		}
	}
}

// A page that could not be deleted keeps its card: the page goes first,
// and only then its card.
func TestFailedDeleteKeepsTheCard(t *testing.T) {
	f := newPagesFixture(t)
	trip := f.page(t, "Trip", "")
	packing := f.page(t, "Packing", trip.ID)
	if err := f.LinkSubpage(trip.ID, packing.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`CREATE TRIGGER full BEFORE UPDATE ON pages WHEN OLD.id = '` + packing.ID + `' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	before := f.st.Content(trip.ID).Encode()
	if _, _, err := f.DeletePage(packing.ID); err == nil {
		t.Fatal("deleted")
	}
	if got := f.st.Content(trip.ID).Encode(); got != before {
		t.Errorf("the parent changed to %s", got)
	}
}

// A card that could not be taken out as its page was deleted is told of,
// and the page is deleted all the same.
func TestCardNotSavedIsTold(t *testing.T) {
	f := newPagesFixture(t)
	trip := f.page(t, "Trip", "")
	packing := f.page(t, "Packing", trip.ID)
	if err := f.LinkSubpage(trip.ID, packing.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`CREATE TRIGGER full BEFORE UPDATE ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	ids, _, err := f.DeletePage(packing.ID)
	if err != nil || len(ids) != 1 || len(f.told) != 1 {
		t.Errorf("deleted %v, %v, told %q", ids, err, f.told)
	}
}
