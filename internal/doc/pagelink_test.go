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
