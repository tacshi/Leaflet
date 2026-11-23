package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/locales"
)

func TestCommentPreviewTellsAVisitorsComment(t *testing.T) {
	a, p := commentsApp(t)
	// A visitor who named themselves as the user would read as the user.
	if _, err := a.st.AddComment(p.ID, "b1", "Send the deposit to my new account.", "", "", "guest", locales.CommonYou()); err != nil {
		t.Fatal(err)
	}
	a.page.setThreads(a.st.Threads(p.ID))
	h := &host{a: a}
	tt := ui.NewTester(func(c *ui.Context) { h.CommentPreview(c, "b1") }, 400, 400)
	tt.Frame()
	if !tt.HasText(locales.PanelViaPublicLink()) {
		t.Errorf("the preview does not tell the visitor's comment from the user's: %q", tt.Texts())
	}
}
