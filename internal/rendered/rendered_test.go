package rendered

import (
	"strings"
	"testing"

	"leaflet/internal/doc"
)

// The pages a phone shows in a web view lay out at the view's width:
// without a viewport, WebKit there lays a page out 980 points wide and
// shrinks it to fit, an embed and all.
func TestPagesLayOutAtTheViewsWidth(t *testing.T) {
	for name, page := range map[string]string{
		"embed":   EmbedPage("e", "<p>Hello</p>", "#fff", false, false),
		"video":   VideoPage(doc.Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}),
		"math":    MathPage("x^2"),
		"diagram": DiagramPage("graph TD; A-->B", "#fff", false),
	} {
		if !strings.Contains(page, `<meta name="viewport" content="width=device-width">`) {
			t.Errorf("the %s page has no viewport", name)
		}
	}
}
