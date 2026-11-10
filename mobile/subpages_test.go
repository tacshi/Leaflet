package mobile

import (
	"encoding/json"
	"testing"

	"leaflet/internal/doc"
)

func links(t *testing.T, c *Core, id string) []string {
	t.Helper()
	raw, err := c.OpenPage(id)
	if err != nil {
		t.Fatal(err)
	}
	var pg openedPage
	_ = json.Unmarshal([]byte(raw), &pg)
	var out []string
	pg.Content.Walk(func(n, _ *doc.Node) {
		if n.Type == "subpage" {
			out = append(out, n.Str("page_id"))
		}
	})
	return out
}

func create(t *testing.T, c *Core, page string) pageRow {
	t.Helper()
	raw, err := c.CreatePage(page)
	if err != nil {
		t.Fatal(err)
	}
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	return row
}

func TestSubpagesKeepTheirCards(t *testing.T) {
	c, _ := open(t)
	parent := create(t, c, `{"title":"Trip"}`)
	other := create(t, c, `{"title":"Work"}`)

	child := create(t, c, `{"title":"Packing","link":true,"parent_id":"`+parent.ID+`"}`)
	create(t, c, `{"title":"Inline","parent_id":"`+parent.ID+`"}`)
	if got := links(t, c, parent.ID); len(got) != 1 || got[0] != child.ID {
		t.Fatalf("parent links %v", got)
	}

	if err := c.MovePage(child.ID, other.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := links(t, c, parent.ID); len(got) != 0 {
		t.Fatalf("old parent still links %v", got)
	}
	if got := links(t, c, other.ID); len(got) != 1 || got[0] != child.ID {
		t.Fatalf("new parent links %v", got)
	}

	sibling := create(t, c, `{"title":"Tickets","link":true,"parent_id":"`+other.ID+`"}`)
	ids, err := c.DeletePage(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := links(t, c, other.ID); len(got) != 1 || got[0] != sibling.ID {
		t.Fatalf("links after delete %v", got)
	}
	if err := c.RestorePages(ids); err != nil {
		t.Fatal(err)
	}
	if got := links(t, c, other.ID); len(got) != 2 || got[0] != child.ID {
		t.Fatalf("links after undo %v", got)
	}
}
