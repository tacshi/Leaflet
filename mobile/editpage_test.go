package mobile

import (
	"encoding/json"
	"testing"

	"leaflet/internal/doc"
)

// EditPage returns the rev it left the page at: the rev the edit was made
// over, when it is the page's newest, or the merge's, which PageUpdated
// brings at that rev with the edit in it. A change at an older rev is
// from before the edit.
func TestEditPageReturnsTheRevItLeftThePageAt(t *testing.T) {
	c, ev := open(t)
	raw, _ := c.CreatePage(`{"title":"Plan"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	a := openEditor(t, c, row.ID)

	// Alone and over the newest rev, the edit is the page at that rev.
	start := doc.FromBlocks([]*doc.Node{paragraph("a", "One"), paragraph("b", "Two")})
	left, err := c.EditPage(row.ID, a.Editor, start.Encode(), a.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if left != a.Rev {
		t.Fatalf("an edit over the newest rev %d left the page at %d", a.Rev, left)
	}

	// With another window open, the edit gets a new rev, which the merge
	// comes at.
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

	// An edit over an older rev is merged at a newer one, which has both.
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

	// An editor the page doesn't have leaves it as it was.
	if left, err = c.EditPage(row.ID, 999, edit.Encode(), b.Rev); err == nil || left != 0 {
		t.Fatalf("an unknown editor left the page at %d, err %v", left, err)
	}
}
