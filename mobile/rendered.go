package mobile

import (
	"leaflet/internal/doc"
	"leaflet/internal/rendered"
)

// MathPage returns the page that draws a math block's TeX, as the
// desktop's: KaTeX's MathML, which a web view shows.
func MathPage(formula string) string { return rendered.MathPage(formula) }

// DiagramPage returns the page that draws a diagram block's Mermaid source
// on a background of bg, a hex color, as the desktop's: Pele's SVG, which
// zooms with a pinch.
func DiagramPage(source, bg string) string { return rendered.DiagramPage(source, bg, false) }

// videoPlayer is a video block's player, as VideoPlayer returns it.
type videoPlayer struct {
	// Page plays the video, loaded at Origin, which YouTube's player
	// wants as the page that frames it.
	Page   string `json:"page"`
	Origin string `json:"origin"`
	// Thumbnail is the video's picture, where its site has one at a
	// known address, or "".
	Thumbnail string `json:"thumbnail"`
	WatchURL  string `json:"watch_url"`
}

// VideoPlayer returns the player of a video block's link, as JSON, or ""
// for a link that is no video Leaflet plays.
func VideoPlayer(link string) string {
	v, ok := doc.ParseVideoURL(link)
	if !ok {
		return ""
	}
	out, _ := encode(videoPlayer{
		Page: rendered.VideoPage(v), Origin: rendered.VideoOrigin,
		Thumbnail: v.Thumbnail(), WatchURL: v.WatchURL(),
	})
	return out
}

// EmbedPage returns the page that runs an interactive embed's HTML, as the
// desktop's: in a sandboxed frame off the network, on a background of bg,
// a hex color, dark or not.
func EmbedPage(src, bg string, dark bool) string { return rendered.EmbedPage("", src, bg, dark, true) }
