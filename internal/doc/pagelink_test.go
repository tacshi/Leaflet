package doc

import (
	"reflect"
	"strings"
	"testing"
)

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

func TestPersonMentionMarkdown(t *testing.T) {
	bob := &Mention{Kind: "user", ID: "u1", Name: "Bob [B]"}
	d := Delta{{Text: "Call "}, {Text: "@Bob [B]", Attrs: Attrs{Mention: bob}}, {Text: " and "},
		{Text: "@Helper", Attrs: Attrs{Bold: true, Mention: &Mention{Kind: "agent", ID: "a1", Name: "Helper"}}}}
	md := d.ToMarkdown()
	if want := `Call [@Bob \[B\]](mention:user:u1) and **[@Helper](mention:agent:a1)**`; md != want {
		t.Fatalf("markdown %q, want %q", md, want)
	}
	if back := FromMarkdown(md); !reflect.DeepEqual(back, d) {
		t.Errorf("read back %+v", back)
	}
	todo := &Node{ID: "t", Type: "todo_list"}
	todo.SetDelta(FromMarkdown(Delta{{Text: "Call "}, {Text: "@Bob", Attrs: Attrs{Mention: &Mention{Kind: "user", ID: "u1", Name: "Bob"}}}}.ToMarkdown()))
	if got := FromBlocks([]*Node{todo}).Todos(); len(got) != 1 || got[0].Owner != "Bob" {
		t.Errorf("todos %+v", got)
	}
	if back := FromMarkdown("[x](mention:page:p1)"); len(back) != 1 || back[0].Attrs.Mention != nil || back[0].Attrs.Href != "mention:page:p1" {
		t.Errorf("mention:page read as %+v", back)
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

func TestRefreshPageMentionsKeepsFormats(t *testing.T) {
	a := PageMention("p1", "Old")
	a.Bold, a.Color = true, "#ef4444"
	n := textNode("paragraph", Delta{{Text: "Old", Attrs: a}})
	c := &Content{Children: []*Node{n}}
	if !c.RefreshPageMentions(func(string) (string, bool) { return "New", true }) {
		t.Fatal("no change")
	}
	r := n.Delta()[0]
	if m := r.Attrs.Mention; r.Text != "New" || m == nil || m.Name != "New" || r.Attrs.Href != PageLink("p1") {
		t.Errorf("mention %+v", r)
	}
	if !r.Attrs.Bold || r.Attrs.Color != "#ef4444" {
		t.Errorf("formats %+v", r.Attrs)
	}
}

func TestCopyPagesKeepsBlockLinks(t *testing.T) {
	head := &Node{ID: "h", Type: "heading"}
	head.SetDelta(Delta{{Text: "Part"}})
	video := &Node{ID: "v", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"}}
	links := &Node{ID: "l", Type: "paragraph"}
	links.SetDelta(Delta{{Text: "part", Attrs: Attrs{Href: BlockLink("old", "h")}}, {Text: " "},
		{Text: "1:23", Attrs: Attrs{Href: MomentLink("old", "v", 83)}}, {Text: " "},
		{Text: "elsewhere", Attrs: Attrs{Href: BlockLink("sub", "x")}}, {Text: " "},
		{Text: "kept", Attrs: Attrs{Href: BlockLink("other", "h")}}, {Text: " "},
		{Text: "gone", Attrs: Attrs{Href: BlockLink("sub", "h")}}})
	c := FromBlocks([]*Node{links, head, video})
	sub := FromBlocks([]*Node{para("x", "there")})
	copies := CopyPages(map[string]*Content{"old": c, "sub": sub}, map[string]string{"old": "new", "sub": "sub2"})
	out := copies["old"]
	d := out.Children[0].Delta()
	if want := BlockLink("new", out.Children[1].ID); d[0].Attrs.Href != want {
		t.Errorf("link to a block %q, want %q", d[0].Attrs.Href, want)
	}
	if want := MomentLink("new", out.Children[2].ID, 83); d[2].Attrs.Href != want {
		t.Errorf("link to a moment %q, want %q", d[2].Attrs.Href, want)
	}
	if want := BlockLink("sub2", copies["sub"].Children[0].ID); d[4].Attrs.Href != want {
		t.Errorf("link to another copied page's block %q, want %q", d[4].Attrs.Href, want)
	}
	if want := BlockLink("other", "h"); d[6].Attrs.Href != want {
		t.Errorf("link to a page not copied %q, want %q", d[6].Attrs.Href, want)
	}
	if want := PageLink("sub2"); d[8].Attrs.Href != want {
		t.Errorf("link to a block its page lacks %q, want %q", d[8].Attrs.Href, want)
	}
}

func TestCopyPages(t *testing.T) {
	p := &Node{ID: "p", Type: "paragraph"}
	p.SetDelta(Delta{{Text: "Notes", Attrs: PageMention("notes", "Notes")}, {Text: " and "}, {Text: "RFC", Attrs: PageMention("rfc", "RFC")}})
	kid := &Node{ID: "k", Type: "paragraph"}
	kid.SetDelta(Delta{{Text: "inside"}})
	list := &Node{ID: "l", Type: "bulleted_list", Children: []*Node{kid}}
	list.SetDelta(Delta{{Text: "item"}})
	sub := &Node{ID: "s", Type: "subpage", Data: map[string]any{"page_id": "notes"}}
	c := FromBlocks([]*Node{p, list, sub})
	out := CopyPages(map[string]*Content{"me": c}, map[string]string{"notes": "notes2"})["me"]

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

func TestReadableMarkdownNamesMentions(t *testing.T) {
	d := Delta{{Text: "Call "}, {Text: "@Bob", Attrs: Attrs{Mention: &Mention{Kind: "user", ID: "u1", Name: "Bob"}}}, {Text: " and "},
		{Text: "Plan", Attrs: PageMention("p1", "Plan")}}
	if got := d.ReadableMarkdown(); !strings.Contains(got, "Call @Bob and ") || strings.Contains(got, "mention:") || !strings.Contains(got, "leaflet://page/p1") {
		t.Errorf("readable %q", got)
	}
	n := &Node{ID: "b1", Type: "paragraph"}
	n.SetDelta(d)
	if got := PageMarkdown("", &Content{Children: []*Node{n}}, MarkdownOptions{PageTitle: func(string) string { return "Plan" }}); strings.Contains(got, "mention:") {
		t.Errorf("export %q", got)
	}
	cell := &Node{ID: "c1", Type: "table/cell", Data: map[string]any{"rowPosition": 0, "colPosition": 0}, Children: []*Node{n.Clone()}}
	table := &Node{ID: "t1", Type: "table", Data: map[string]any{"rowsLen": 1, "colsLen": 1}, Children: []*Node{cell}}
	if got := PageMarkdown("", &Content{Children: []*Node{table}}, MarkdownOptions{}); strings.Contains(got, "mention:") || !strings.Contains(got, "@Bob") {
		t.Errorf("exported table %q", got)
	}
	if got := d.ToMarkdown(); !strings.Contains(got, "[@Bob](mention:user:u1)") {
		t.Errorf("agents' markdown %q", got)
	}
}
