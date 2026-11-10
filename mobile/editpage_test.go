package mobile

import (
	"encoding/json"
	"testing"

	"leaflet/internal/doc"
)

func TestEditPageReturnsTheRevItLeftThePageAt(t *testing.T) {
	c, ev := open(t)
	raw, _ := c.CreatePage(`{"title":"Plan"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	a := openEditor(t, c, row.ID)

	start := doc.FromBlocks([]*doc.Node{paragraph("a", "One"), paragraph("b", "Two")})
	left, err := c.EditPage(row.ID, a.Editor, start.Encode(), a.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if left != a.Rev {
		t.Fatalf("an edit over the newest rev %d left the page at %d", a.Rev, left)
	}

	b := openEditor(t, c, row.ID)
	edit := start.Copy()
	edit.Find("a").SetDelta(doc.Delta{{Text: "One!"}})
	left, err = c.EditPage(row.ID, a.Editor, edit.Encode(), a.Rev)
	if err != nil {
		t.Fatal(err)
	}
	u := wait(t, ev, "merge")
	if left <= a.Rev || u.rev != left || texts(doc.MustDecode(u.content)) != "One!|Two" {
		t.Fatalf("left the page at %d; the merge came at %d with %q", left, u.rev, texts(doc.MustDecode(u.content)))
	}

	edit = b.Content.Copy()
	edit.Find("b").SetDelta(doc.Delta{{Text: "Two!"}})
	before := left
	left, err = c.EditPage(row.ID, b.Editor, edit.Encode(), b.Rev)
	if err != nil {
		t.Fatal(err)
	}
	u = wait(t, ev, "merge")
	if left <= before || u.rev != left || texts(doc.MustDecode(u.content)) != "One!|Two!" {
		t.Fatalf("left the page at %d after %d; the merge came at %d with %q", left, before, u.rev, texts(doc.MustDecode(u.content)))
	}

	if left, err = c.EditPage(row.ID, 999, edit.Encode(), b.Rev); err == nil || left != 0 {
		t.Fatalf("an unknown editor left the page at %d, err %v", left, err)
	}
}

func TestAnEditOverTheRevShownKeepsAChangeNotShown(t *testing.T) {
	c, ev := open(t)
	row := create(t, c, `{"title":"Plan"}`)
	a := openEditor(t, c, row.ID)
	start := doc.FromBlocks([]*doc.Node{paragraph("a", ""), paragraph("b", "Other")})
	if _, err := c.EditPage(row.ID, a.Editor, start.Encode(), a.Rev); err != nil {
		t.Fatal(err)
	}

	b := openEditor(t, c, row.ID)
	edit := b.Content.Copy()
	edit.Find("b").SetDelta(doc.Delta{{Text: "Changed"}})
	if _, err := c.EditPage(row.ID, b.Editor, edit.Encode(), b.Rev); err != nil {
		t.Fatal(err)
	}
	wait(t, ev, "merge")
	c.ClosePage(row.ID, b.Editor)

	composed := start.Copy()
	composed.Find("a").SetDelta(doc.Delta{{Text: "か"}})
	if _, err := c.EditPage(row.ID, a.Editor, composed.Encode(), a.Rev); err != nil {
		t.Fatal(err)
	}
	if u := wait(t, ev, "merge"); texts(doc.MustDecode(u.content)) != "か|Changed" {
		t.Fatalf("merged %q", texts(doc.MustDecode(u.content)))
	}
}
