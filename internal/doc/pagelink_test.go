package doc

import "testing"

func TestPageMentionMarkdown(t *testing.T) {
	d := Delta{{Text: "See "}, {Text: "RFC: Sync", Attrs: PageMention("rfc", "RFC: Sync")}}
	md := d.ToMarkdown()
	if md != "See [RFC: Sync](leaflet://page/rfc)" {
		t.Fatalf("markdown %q", md)
	}
	back := FromMarkdown(md)
	if len(back) != 2 || back[1].Text != "RFC: Sync" {
		t.Fatalf("back %+v", back)
	}
	if m := back[1].Attrs.Mention; m == nil || m.Kind != "page" || m.ID != "rfc" || back[1].Attrs.Href != PageLink("rfc") {
		t.Errorf("attrs %+v", back[1].Attrs)
	}
	if id, ok := PageLinkID("https://x.dev"); ok {
		t.Errorf("web link is page %q", id)
	}
}

func TestRefreshPageMentions(t *testing.T) {
	n := &Node{ID: "p", Type: "paragraph"}
	n.SetDelta(Delta{{Text: "Old", Attrs: PageMention("rfc", "Old")}, {Text: " and "}, {Text: "Gone", Attrs: PageMention("gone", "Gone")}})
	c := FromBlocks([]*Node{n})
	titles := map[string]string{"rfc": "RFC v2"}
	look := func(id string) (string, bool) { s, ok := titles[id]; return s, ok }
	if !c.RefreshPageMentions(look) {
		t.Fatal("nothing changed")
	}
	d := c.Children[0].Delta()
	if d.Plain() != "RFC v2 and Gone" || d[0].Attrs.Mention.Name != "RFC v2" {
		t.Errorf("delta %+v", d)
	}
	if c.RefreshPageMentions(look) {
		t.Error("changed again")
	}
}

func TestCopyFor(t *testing.T) {
	p := &Node{ID: "p", Type: "paragraph"}
	p.SetDelta(Delta{{Text: "Notes", Attrs: PageMention("notes", "Notes")}, {Text: " and "}, {Text: "RFC", Attrs: PageMention("rfc", "RFC")}})
	kid := &Node{ID: "k", Type: "paragraph"}
	kid.SetDelta(Delta{{Text: "inside"}})
	list := &Node{ID: "l", Type: "bulleted_list", Children: []*Node{kid}}
	list.SetDelta(Delta{{Text: "item"}})
	sub := &Node{ID: "s", Type: "subpage", Data: map[string]any{"page_id": "notes"}}
	c := FromBlocks([]*Node{p, list, sub})
	out := c.CopyFor(map[string]string{"notes": "notes2"})

	seen := map[string]bool{}
	out.Walk(func(n, _ *Node) {
		if n.ID == "" || n.ID == "p" || n.ID == "l" || n.ID == "k" || n.ID == "s" || seen[n.ID] {
			t.Errorf("block %s kept an old or blank ID", n.ID)
		}
		seen[n.ID] = true
		if out.Blocks[n.ID] == 0 {
			t.Errorf("block %s is not stamped", n.ID)
		}
	})
	if got := out.Children[2].Str("page_id"); got != "notes2" {
		t.Errorf("subpage links %q", got)
	}
	d := out.Children[0].Delta()
	if d[0].Attrs.Href != PageLink("notes2") || d[0].Attrs.Mention.ID != "notes2" || d[0].Attrs.Mention.Name != "Notes" {
		t.Errorf("mention of a copied page %+v", d[0].Attrs)
	}
	if d[2].Attrs.Href != PageLink("rfc") || d[2].Attrs.Mention.ID != "rfc" {
		t.Errorf("mention of another page %+v", d[2].Attrs)
	}
	if out.Children[1].Children[0].Delta().Plain() != "inside" {
		t.Error("nested block lost")
	}
	if c.Children[0].ID != "p" || c.Children[2].Str("page_id") != "notes" {
		t.Error("the original changed")
	}
}

func TestBlockLink(t *testing.T) {
	link := BlockLink("prd", "b1")
	if link != "leaflet://page/prd#b1" {
		t.Fatalf("link %q", link)
	}
	if id, ok := PageLinkID(link); !ok || id != "prd" || LinkBlock(link) != "b1" {
		t.Errorf("page %q %v block %q", id, ok, LinkBlock(link))
	}
	if LinkBlock(PageLink("prd")) != "" || LinkBlock("https://x.dev/#a") != "" {
		t.Error("a block where there is none")
	}
	if _, ok := PageLinkID("leaflet://page/#b1"); ok {
		t.Error("a link with no page")
	}
}
