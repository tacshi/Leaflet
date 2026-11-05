package doc

import (
	"net/url"
	"path"
	"strings"
)

// ImageExts are the kinds of picture an image block shows, by extension.
var ImageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".svg": true}

// MaxPixels is the most pixels a picture may have to be shown: a small
// file can say it is far larger, and decoding it would take more memory
// than the device has.
const MaxPixels = 50_000_000

// ImageLink reports whether s is a web link to a picture, as the extension
// of its path says.
func ImageLink(s string) bool {
	s = strings.TrimSpace(s)
	if !WebURL(s) {
		return false
	}
	u, _ := url.Parse(s)
	return ImageExts[strings.ToLower(path.Ext(u.Path))]
}

// ImageNode is an image block showing a picture from the web.
func ImageNode(link string) (*Node, error) {
	link = strings.TrimSpace(link)
	if !WebURL(link) {
		return nil, toolErr("An image block needs image.url (http or https).")
	}
	return &Node{ID: NewBlockID(), Type: "image", Data: map[string]any{"url": link, "align": "center", "alt": ""}}, nil
}
