package mobile

import (
	"testing"
	"time"

	"leaflet/internal/doc"
)

// A link card whose page could not be read, as offline, is asked again
// once the wait is over, not once a run.
func TestLinkCardAskedAgainAfterAFailure(t *testing.T) {
	was := linkRetry
	linkRetry = 10 * time.Millisecond
	t.Cleanup(func() { linkRetry = was })
	c, _ := open(t)
	// Not a public address: the look-up fails at once.
	const link = "http://127.0.0.1:1/notes"
	card := &doc.Node{ID: "card", Type: "link_preview", Data: map[string]any{"url": link}}
	var first bool
	c.q.do(func() {
		c.fillLinks("p1", doc.FromBlocks([]*doc.Node{card}))
		first = c.linkAsked[link]
	})
	if !first {
		t.Fatal("the link was not asked")
	}
	asked := func() bool {
		var ok bool
		c.q.do(func() { ok = c.linkAsked[link] })
		return ok
	}
	for end := time.Now().Add(5 * time.Second); asked(); {
		if time.Now().After(end) {
			t.Fatal("the link is never asked again")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
