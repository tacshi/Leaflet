package rendered

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
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

// The math and diagram pages run only their own scripts, which their
// policy names by their hashes: what a formula or a diagram's source
// holds, even a script's end, stays data, and nothing reaches the
// network, on the phone as in the desktop's frames.
func TestRenderedPagesRunOnlyTheirScripts(t *testing.T) {
	script := regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)
	policy := regexp.MustCompile(`<meta http-equiv="Content-Security-Policy" content="([^"]*)">`)
	evil := `</script><script>fetch("https://evil.test")</script><img src=x onerror=alert(1)>`
	for name, page := range map[string]string{
		"math":    MathPage(`x^2 ` + evil),
		"diagram": DiagramPage("graph TD; A[\""+evil+"\"]-->B", "#fff", true),
	} {
		m := policy.FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("the %s page has no policy", name)
		}
		csp := m[1]
		_, scripts, _ := strings.Cut(csp, "script-src")
		if scripts, _, _ = strings.Cut(scripts, ";"); !strings.HasPrefix(csp, "default-src 'none'; ") || strings.Contains(scripts, "unsafe") {
			t.Errorf("the %s page's policy is %q", name, csp)
		}
		runs := 0
		for _, s := range script.FindAllStringSubmatch(page, -1) {
			if strings.Contains(s[1], "application/json") {
				if strings.Contains(s[2], "</") {
					t.Errorf("the %s page's data ends its element: %q", name, s[2])
				}
				continue
			}
			runs++
			sum := sha256.Sum256([]byte(s[2]))
			if !strings.Contains(csp, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
				t.Errorf("the %s page's policy does not let its script %.40q run", name, s[2])
			}
		}
		if runs == 0 || strings.Count(csp, "'sha256-") != runs {
			t.Errorf("the %s page runs %d scripts, its policy names %d", name, runs, strings.Count(csp, "'sha256-"))
		}
	}
}
