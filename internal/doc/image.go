package doc

import (
	"net/url"
	"path"
	"strings"
)

var ImageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".svg": true}

const MaxPixels = 50_000_000

// NeedsImageURL is the error of an image block with no web link.
const NeedsImageURL = "An image block needs image.url (http or https)."

func ImageLink(s string) bool {
	s = strings.TrimSpace(s)
	if !WebURL(s) {
		return false
	}
	u, _ := url.Parse(s)
	return ImageExts[strings.ToLower(path.Ext(u.Path))]
}

// KeptImage is prev, a picture of a file the app keeps, with the alt text a
// spec gives it, when the spec keeps its url, which is no web link: false
// when prev is no such picture or the spec gives another url.
func KeptImage(prev *Node, spec map[string]any) (*Node, bool) {
	if prev.Type != "image" || prev.Str("file_id") == "" {
		return nil, false
	}
	img := asMap(spec["image"])
	if img == nil {
		img = spec
	}
	if url, _ := img["url"].(string); strings.TrimSpace(url) != prev.Str("url") {
		return nil, false
	}
	n := prev.Clone()
	if alt, ok := img["alt"].(string); ok {
		n.Set("alt", alt)
	}
	return n, true
}

func ImageNode(link string) (*Node, error) {
	link = strings.TrimSpace(link)
	if !WebURL(link) {
		return nil, toolErr(NeedsImageURL)
	}
	return &Node{ID: NewBlockID(), Type: "image", Data: map[string]any{"url": link, "align": "center", "alt": ""}}, nil
}
