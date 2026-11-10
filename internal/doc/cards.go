package doc

import (
	"net/url"
	"strings"
)

func WebURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.ContainsAny(s, " \t\n")
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
