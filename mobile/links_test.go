package mobile

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/doc"
)

func TestLinkCardAskedAgainAfterAFailure(t *testing.T) {
	was := linkRetry
	linkRetry = 10 * time.Millisecond
	t.Cleanup(func() { linkRetry = was })
	c, _ := open(t)
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

func TestVideosGetTheirTitles(t *testing.T) {
	var asked atomic.Int32
	was := videoTitle
	videoTitle = func(v doc.Video) (string, error) {
		asked.Add(1)
		return "Talk " + v.ID, nil
	}
	t.Cleanup(func() { videoTitle = was })
	c, _ := open(t)
	raw, _ := c.CreatePage(`{"title":"Talks"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	e := openEditor(t, c, row.ID)
	video := func(id, link, title string) *doc.Node {
		n := &doc.Node{ID: id, Type: "video", Data: map[string]any{"url": link}}
		if title != "" {
			n.Data["title"] = title
		}
		return n
	}
	content := doc.FromBlocks([]*doc.Node{
		video("a", "https://youtu.be/dQw4w9WgXcQ", ""),
		video("b", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=5", ""),
		video("c", "https://youtu.be/dQw4w9WgXcQ", "Mine"),
	})
	if _, err := c.EditPage(row.ID, e.Editor, content.Encode(), e.Rev); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		var got []string
		c.q.do(func() {
			for _, n := range c.content(row.ID).All() {
				got = append(got, n.Str("title"))
			}
		})
		if strings.Join(got, "|") == "Talk dQw4w9WgXcQ|Talk dQw4w9WgXcQ|Mine" {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("titles %q", got)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("the site was asked %d times", n)
	}
}
