package doc

import "strings"

// PageLinkPrefix starts a link to one of the user's pages: a page mention
// is text linked to it, kept as the page's title.
const PageLinkPrefix = "leaflet://page/"

// PageLink is the link to page id.
func PageLink(id string) string { return PageLinkPrefix + id }

// PageLinkID returns the page a link points to, if it is a page link, or
// a link to a block of one.
func PageLinkID(href string) (string, bool) {
	id, ok := strings.CutPrefix(href, PageLinkPrefix)
	id, _, _ = strings.Cut(id, "#")
	return id, ok && id != ""
}

// BlockLink is the link to block id of page pageID: it opens the page there.
func BlockLink(pageID, id string) string { return PageLink(pageID) + "#" + id }

// LinkBlock returns the block a page link points to, or "" for the page.
func LinkBlock(href string) string {
	if _, ok := PageLinkID(href); !ok {
		return ""
	}
	_, block, _ := strings.Cut(href, "#")
	return block
}

// PageMention is the text and formats of a mention of page id titled
// title: a link to it, which opens it, and a mention, which shows as one
// and keeps its title.
func PageMention(id, title string) Attrs {
	return Attrs{Href: PageLink(id), Mention: &Mention{Kind: "page", ID: id, Name: title}}
}

// RefreshPageMentions writes each page mention in c with its page's title
// now, as title gives it, and reports whether one changed. A page title
// does not know is left as it was written.
func (c *Content) RefreshPageMentions(title func(id string) (string, bool)) bool {
	changed := false
	c.Walk(func(n, _ *Node) {
		if !n.IsText() {
			return
		}
		d := n.Delta()
		touched := false
		for i, r := range d {
			m := r.Attrs.Mention
			if m == nil || m.Kind != "page" {
				continue
			}
			if t, ok := title(m.ID); ok && t != "" && (t != r.Text || t != m.Name) {
				d[i].Text = t
				d[i].Attrs = PageMention(m.ID, t)
				touched = true
			}
		}
		if touched {
			n.SetDelta(d)
			changed = true
		}
	})
	return changed
}

// CopyFor returns a copy of c for a new page: new block IDs, stamped now,
// and links to the pages in pages, of subpage blocks and inline links
// alike, pointing to the page each maps to instead, so a page copied with
// its subpages links to the copies.
func (c *Content) CopyFor(pages map[string]string) *Content {
	children := make([]*Node, len(c.Children))
	for i, n := range c.Children {
		children[i] = n.Clone()
	}
	out := &Content{Children: children}
	out.Walk(func(n, _ *Node) {
		n.ID = ""
		if to, ok := pages[n.Str("page_id")]; ok && n.Type == "subpage" {
			n.Set("page_id", to)
		}
		if !n.IsText() {
			return
		}
		d := n.Delta()
		touched := false
		for i, r := range d {
			id, ok := PageLinkID(r.Attrs.Href)
			to, mapped := pages[id]
			if !ok || !mapped {
				continue
			}
			d[i].Attrs.Href = PageLink(to)
			if m := r.Attrs.Mention; m != nil && m.Kind == "page" {
				d[i].Attrs.Mention = &Mention{Kind: "page", ID: to, Name: m.Name}
			}
			touched = true
		}
		if touched {
			n.SetDelta(d)
		}
	})
	return FromBlocks(out.Children)
}
