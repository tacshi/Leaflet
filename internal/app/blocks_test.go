package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/internal/web"
)

func TestRenderedPages(t *testing.T) {
	if !strings.Contains(rendered.PeleModule, "\nconst peleRender = ") || strings.Contains(rendered.PeleModule[len(rendered.PeleModule)-400:], "export{") {
		t.Fatal("Pele's render function was not found")
	}
	page := rendered.DiagramPage("graph TD\n  A[</script><b>x</b>]-->B", "#ffffff", false)
	if strings.Contains(page, "</script><b>") || strings.Contains(page, "<b>x</b>") {
		t.Error("diagram source not escaped")
	}
	if strings.Contains(page, `"full":true`) || strings.Contains(page, "#v{height:100%}") {
		t.Error("inline diagram page fills its frame")
	}
	if !strings.Contains(page, "--pele-bg:#ffffff") {
		t.Error("diagram labels have no background")
	}
	if full := rendered.DiagramPage("graph TD\n  A-->B", "#ffffff", true); !strings.Contains(full, `"full":true`) || !strings.Contains(full, "#v{height:100%}") {
		t.Error("full screen diagram page does not fill its frame")
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

func TestSetMediaLink(t *testing.T) {
	img := &doc.Node{ID: "img", Type: "image", Data: map[string]any{"url": "https://example.com/a.png", "align": "center"}}
	video := &doc.Node{ID: "vid", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "title": "Old"}}
	ed := editor.New(doc.Empty(), nil)
	ed.Edit(func(c *doc.Content) { c.Children = []*doc.Node{img, video} })
	a := &App{page: &pageState{editor: ed}, videos: map[string]*mygo.WebView{}}

	a.dialogs.editLink(img)
	if a.dialogs.linkURL != "https://example.com/a.png" || a.dialogs.linkKind != "image" {
		t.Errorf("an image's dialog starts at %q, %q", a.dialogs.linkURL, a.dialogs.linkKind)
	}
	a.setMediaLink("img", "https://example.com/b.png")
	if n := ed.Content.Find("img"); n.Str("url") != "https://example.com/b.png" || n.Str("align") != "center" {
		t.Errorf("image %v", n.Data)
	}
	a.dialogs.editLink(ed.Content.Find("vid"))
	if a.dialogs.linkURL != "https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("a video's dialog starts at %q", a.dialogs.linkURL)
	}
	a.setMediaLink("vid", "https://vimeo.com/76979871")
	if n := ed.Content.Find("vid"); n.Str("url") != "https://vimeo.com/76979871" || n.Str("title") != "" {
		t.Errorf("video %v", n.Data)
	}
	ed.Undo()
	if n := ed.Content.Find("vid"); n.Str("url") != "https://youtu.be/dQw4w9WgXcQ" || n.Str("title") != "Old" {
		t.Errorf("after undo, video %v", n.Data)
	}
}

func TestLinkInfoKeptInAClosedPage(t *testing.T) {
	a := templatesApp(t)
	card := &doc.Node{ID: "l1", Type: "link_preview", Data: map[string]any{"url": "https://go.dev/"}}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Links", Content: doc.FromBlocks([]*doc.Node{card})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.keepLinkInfo(p.ID, "https://go.dev/", web.LinkInfo{Title: "Go", Site: "go.dev"})
	n := a.st.Content(p.ID).Find("l1")
	if n == nil || n.Str("title") != "Go" || n.Str("site") != "go.dev" {
		t.Errorf("stored card %+v", n)
	}
}

func TestWebAnswersAskAgainAfterAFailure(t *testing.T) {
	a := &App{}
	var w webAnswers[string]
	asked := 0
	ask := func() (string, error) {
		if asked++; asked == 1 {
			return "", errors.New("offline")
		}
		return "Go", nil
	}
	var got []string
	keep := func(s string) { got = append(got, s) }
	if v := w.get(a, "go.dev", ask, keep); v != "" || asked != 1 || !w.settled("go.dev") {
		t.Fatalf("first: %q, asked %d, settled %v", v, asked, w.settled("go.dev"))
	}
	if v := w.get(a, "go.dev", ask, keep); v != "" || asked != 1 {
		t.Fatalf("right after it failed: %q, asked %d", v, asked)
	}
	w.failed["go.dev"] = time.Now().Add(-imageRetry)
	w.get(a, "go.dev", ask, keep)
	if v := w.get(a, "go.dev", ask, keep); v != "Go" || asked != 2 || fmt.Sprint(got) != "[Go]" {
		t.Errorf("once the wait was over: %q, asked %d, got %v", v, asked, got)
	}
}
