package doc

import (
	"net/url"
	"strconv"
	"strings"
)

const PageLinkPrefix = "leaflet://page/"

func PageLink(id string) string { return PageLinkPrefix + id }

func PageLinkID(href string) (string, bool) {
	id, ok := strings.CutPrefix(href, PageLinkPrefix)
	id, _, _ = strings.Cut(id, "#")
	return id, ok && id != ""
}

func BlockLink(pageID, id string) string { return PageLink(pageID) + "#" + id }

func LinkBlock(href string) string {
	if _, ok := PageLinkID(href); !ok {
		return ""
	}
	_, block, _ := strings.Cut(href, "#")
	block, _, _ = strings.Cut(block, "?")
	return block
}

func MomentLink(pageID, id string, at int) string {
	return BlockLink(pageID, id) + "?t=" + strconv.Itoa(max(at, 0))
}

func LinkMoment(href string) (int, bool) {
	if LinkBlock(href) == "" {
		return 0, false
	}
	_, frag, _ := strings.Cut(href, "#")
	_, query, ok := strings.Cut(frag, "?")
	if !ok {
		return 0, false
	}
	q, _ := url.ParseQuery(query)
	t := strings.TrimSpace(q.Get("t"))
	if at, ok := ParseClock(t); ok {
		return at, true
	}
	if t == "" {
		return 0, false
	}
	return linkSeconds(t)
}

func PageMention(id, title string) Attrs {
	return Attrs{Href: PageLink(id), Mention: &Mention{Kind: "page", ID: id, Name: title}}
}

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
				now := PageMention(m.ID, t)
				d[i].Text = t
				d[i].Attrs.Href, d[i].Attrs.Mention = now.Href, now.Mention
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

func CopyPages(contents map[string]*Content, pages map[string]string) map[string]*Content {
	copies := make(map[string]*Content, len(contents))
	blocks := make(map[string]map[string]string, len(contents))
	for id, c := range contents {
		children := make([]*Node, len(c.Children))
		for i, n := range c.Children {
			children[i] = n.Clone()
		}
		copies[id] = &Content{Children: children}
		blocks[id] = map[string]string{}
		copies[id].Walk(func(n, _ *Node) {
			blocks[id][n.ID] = NewBlockID()
			n.ID = blocks[id][n.ID]
		})
	}
	for id, c := range copies {
		c.relink(pages, blocks)
		copies[id] = FromBlocks(c.Children)
	}
	return copies
}

func (c *Content) relink(pages map[string]string, blocks map[string]map[string]string) {
	c.Walk(func(n, _ *Node) {
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
			if block, copied := blocks[id][LinkBlock(r.Attrs.Href)]; copied {
				d[i].Attrs.Href = BlockLink(to, block)
				if _, moment, ok := strings.Cut(r.Attrs.Href, "?"); ok {
					d[i].Attrs.Href += "?" + moment
				}
			}
			if m := r.Attrs.Mention; m != nil && m.Kind == "page" {
				d[i].Attrs.Mention = &Mention{Kind: "page", ID: to, Name: m.Name}
			}
			touched = true
		}
		if touched {
			n.SetDelta(d)
		}
	})
}
