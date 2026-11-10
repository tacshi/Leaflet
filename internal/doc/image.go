package doc

import (
	"net/url"
	"path"
	"strings"
)

var ImageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".svg": true}

const MaxPixels = 50_000_000

func ImageLink(s string) bool {
	s = strings.TrimSpace(s)
	if !WebURL(s) {
		return false
	}
	u, _ := url.Parse(s)
	return ImageExts[strings.ToLower(path.Ext(u.Path))]
}

func ImageNode(link string) (*Node, error) {
	link = strings.TrimSpace(link)
	if !WebURL(link) {
		return nil, toolErr("An image block needs image.url (http or https).")
	}
	return &Node{ID: NewBlockID(), Type: "image", Data: map[string]any{"url": link, "align": "center", "alt": ""}}, nil
}
