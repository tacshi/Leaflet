package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

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

func TestRenderedBlockShowsChromeOnHover(t *testing.T) {
	a := &App{}
	n, err := doc.SpecToNode(map[string]any{"type": "math_equation", "formula": "x^2"}, "", "Leaflet")
	if err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { a.renderedBlock(c, n, 400) })
	}, 500, 200)
	tt.Frame()
	label, ok := tt.Find("Math")
	if !ok {
		t.Fatal("no Math label")
	}
	// Inked: pixels of the border's left edge and of the label that differ
	// from the page around them.
	inked := func() (border, text int) {
		img := tt.Image()
		scale := float32(img.Bounds().Dx()) / 500
		ground := img.RGBAAt(int(5*scale), int(5*scale))
		for y := label.Y; y < label.Y+label.H; y++ {
			if img.RGBAAt(int(20*scale), int(y*scale)) != ground {
				border++
			}
			for x := label.X; x < label.X+label.W; x++ {
				if img.RGBAAt(int(x*scale), int(y*scale)) != ground {
					text++
				}
			}
		}
		return border, text
	}
	if border, text := inked(); border != 0 || text != 0 {
		t.Errorf("without the pointer: border %d, label %d pixels", border, text)
	}
	tt.Move(label.X+10, label.Y+label.H/2)
	if border, text := inked(); border == 0 || text == 0 {
		t.Errorf("with the pointer over it: border %d, label %d pixels", border, text)
	}
}
