package doc

import (
	"net/url"
	"strings"
)

// Link cards and files are blocks shown as cards. A link card,
// "link_preview" as in appflowy_editor, is a web page by its link, with
// the title, description, picture and site name its page gives, as the
// app learns them. A file is one kept with the page: data.file_id is the
// file, and url, name, size and mime are as the file is on this device.

// WebURL reports whether s is a web link, http or https to a host.
func WebURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.ContainsAny(s, " \t\n")
}

// LinkNode is a link card for a web link.
func LinkNode(link string) (*Node, error) {
	link = strings.TrimSpace(link)
	if !WebURL(link) {
		return nil, toolErr("A link_preview block needs link.url, an http or https link.")
	}
	return &Node{ID: NewBlockID(), Type: "link_preview", Data: map[string]any{"url": link}}, nil
}

// cardTitle is how a link card or file reads in text: its title or name,
// else its link.
func cardTitle(n *Node) string {
	for _, k := range []string{"title", "name", "url"} {
		if s := strings.TrimSpace(n.Str(k)); s != "" {
			return s
		}
	}
	return ""
}
