package doc

import (
	"net/url"
	"strings"
	"unicode"
)

// WebURL reports whether s is a web address: http or https, to a host,
// with no space of any kind in it, as a link a page loads or opens takes.
func WebURL(s string) bool {
	if strings.ContainsFunc(s, unicode.IsSpace) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

func LinkNode(link string) (*Node, error) {
	link = strings.TrimSpace(link)
	if !WebURL(link) {
		return nil, toolErr("A link_preview block needs link.url, an http or https link.")
	}
	return &Node{ID: NewBlockID(), Type: "link_preview", Data: map[string]any{"url": link}}, nil
}

func cardTitle(n *Node) string {
	for _, k := range []string{"title", "name", "url"} {
		if s := strings.TrimSpace(n.Str(k)); s != "" {
			return s
		}
	}
	return ""
}

// SiteName is the name of the site a web address is on: its host without
// "www.", or the address as it is when it has none.
func SiteName(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Hostname() == "" {
		return link
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// LoadedURL is the web address a block has the app load by itself, as a
// picture's, a video's, a link card's or a card's picture, or "" when it
// has none.
func LoadedURL(n *Node) string {
	switch n.Type {
	case "image", "link_preview", "video":
		return n.Str("url")
	case "card":
		return n.Str("image")
	}
	return ""
}
