package web

import (
	"slices"
	"testing"

	"leaflet/internal/doc"
)

func TestReadLinkInfo(t *testing.T) {
	src := `<html><head><title> Fallback  title </title>
<meta content="The Go &amp; you" property="og:title">
<meta name="description" content="Plain description">
<meta property='og:description' content='Build simple, secure, scalable systems'>
<meta property="og:image" content="/images/go-logo.png">
<meta property="og:site_name" content="Go">
</head><body><meta property="og:title" content="not this"></body></html>`
	got := ReadLinkInfo("https://go.dev/doc/", src)
	want := LinkInfo{"The Go & you", "Build simple, secure, scalable systems", "https://go.dev/images/go-logo.png", "Go"}
	if got != want {
		t.Errorf("got %+v", got)
	}
	if got := ReadLinkInfo("https://x.dev", "<title>Only a title</title>"); got.Title != "Only a title" || got.Image != "" {
		t.Errorf("got %+v", got)
	}
	// A picture that is not on the web is left out.
	if got := ReadLinkInfo("https://x.dev", `<meta property="og:image" content="javascript:alert(1)">`); got.Image != "" {
		t.Errorf("image %q", got.Image)
	}
}

func TestFillLinkCards(t *testing.T) {
	c := doc.FromBlocks([]*doc.Node{
		{ID: "a", Type: "link_preview", Data: map[string]any{"url": "https://go.dev/"}},
		{ID: "b", Type: "link_preview", Data: map[string]any{"url": "https://go.dev/"}},
		{ID: "c", Type: "link_preview", Data: map[string]any{"url": "https://x.dev/", "title": "X"}},
	})
	if got := Unfilled(c); !slices.Equal(got, []string{"https://go.dev/"}) {
		t.Errorf("unfilled %v", got)
	}
	if !(LinkInfo{Title: "Go", Site: "go.dev"}).Fill(c, "https://go.dev/") || c.Find("b").Str("title") != "Go" {
		t.Errorf("filled %+v", c.Find("b").Data)
	}
	if len(Unfilled(c)) != 0 || (LinkInfo{Title: "Go"}).Fill(c, "https://go.dev/") {
		t.Error("filled cards were filled again")
	}
}
