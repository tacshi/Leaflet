package doc

import "strings"

// PageLinkPrefix starts a link to one of the user's pages: a page mention
// is text linked to it, kept as the page's title.
const PageLinkPrefix = "leaflet://page/"

// PageLink is the link to page id.
func PageLink(id string) string { return PageLinkPrefix + id }

// PageLinkID returns the page a link points to, if it is a page link.
func PageLinkID(href string) (string, bool) {
	id, ok := strings.CutPrefix(href, PageLinkPrefix)
	return id, ok && id != ""
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
