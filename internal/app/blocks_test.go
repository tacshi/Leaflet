package app

import (
	"strings"
	"testing"
)

func TestReadLinkInfo(t *testing.T) {
	src := `<html><head><title> Fallback  title </title>
<meta content="The Go &amp; you" property="og:title">
<meta name="description" content="Plain description">
<meta property='og:description' content='Build simple, secure, scalable systems'>
<meta property="og:image" content="/images/go-logo.png">
<meta property="og:site_name" content="Go">
</head><body><meta property="og:title" content="not this"></body></html>`
	got := readLinkInfo("https://go.dev/doc/", src)
	want := linkInfo{"The Go & you", "Build simple, secure, scalable systems", "https://go.dev/images/go-logo.png", "Go"}
	if got != want {
		t.Errorf("got %+v", got)
	}
	if got := readLinkInfo("https://x.dev", "<title>Only a title</title>"); got.title != "Only a title" || got.image != "" {
		t.Errorf("got %+v", got)
	}
	// A picture that is not on the web is left out.
	if got := readLinkInfo("https://x.dev", `<meta property="og:image" content="javascript:alert(1)">`); got.image != "" {
		t.Errorf("image %q", got.image)
	}
}

func TestRenderedPages(t *testing.T) {
	if !strings.Contains(peleModule, "\nconst peleRender = ") || strings.Contains(peleModule[len(peleModule)-400:], "export{") {
		t.Fatal("Pele's render function was not found")
	}
	// Source goes in as a string the page cannot break out of.
	page := diagramPage("graph TD\n  A[</script><b>x</b>]-->B")
	if strings.Contains(page, "</script><b>") || strings.Contains(page, "<b>x</b>") {
		t.Error("diagram source not escaped")
	}
	if page := mathPage(`\frac{a}{b}`); !strings.Contains(page, `"\\frac{a}{b}"`) || !strings.Contains(page, "katex.render(") {
		t.Error("math page")
	}
}
