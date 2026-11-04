package doc

import (
	"reflect"
	"strings"
	"testing"
)

func TestClock(t *testing.T) {
	for sec, want := range map[int]string{0: "0:00", 5: "0:05", 83: "1:23", 3599: "59:59", 3600: "1:00:00", 3723: "1:02:03", -4: "0:00"} {
		if got := Clock(sec); got != want {
			t.Errorf("Clock(%d) = %q, want %q", sec, got, want)
		}
	}
	for s, want := range map[string]int{"1:23": 83, "01:23": 83, "0:00": 0, "75:00": 4500, "123:45": 7425, "1:02:03": 3723, "10:00:00": 36000} {
		if got, ok := ParseClock(s); !ok || got != want {
			t.Errorf("ParseClock(%q) = %d, %v, want %d", s, got, ok, want)
		}
	}
	for _, s := range []string{"", "1", "1:2", "1:234", "1:60", "1:60:00", "1:2:03", "123:4:56", "1234:56", ":12", "1:23 ", "a1:23", "1:23:45:01"} {
		if got, ok := ParseClock(s); ok {
			t.Errorf("ParseClock(%q) = %d, but it is no time", s, got)
		}
	}
}

func TestNoteLines(t *testing.T) {
	text := func(s string) NotePart { return NotePart{Text: s, At: -1} }
	at := func(s string, sec int) NotePart { return NotePart{Text: s, At: sec} }
	got := NoteLines("A talk on caching.\r\n\n0:00 Intro\n[1:23] - Why caches miss\n1:02:03: Wrap-up, as 4:56 showed\n  12:30\n" +
		"10:30am stand-up\nat 1:23:45:01 or 1.2:30, not 3:2 or v2:30, but (5:00)\n12:00 | Lunch")
	want := []NoteLine{
		{At: -1, Parts: []NotePart{text("A talk on caching.")}},
		{At: 0, Parts: []NotePart{text("Intro")}},
		{At: 83, Parts: []NotePart{text("Why caches miss")}},
		{At: 3723, Parts: []NotePart{text("Wrap-up, as "), at("4:56", 296), text(" showed")}},
		{At: 750},
		{At: -1, Parts: []NotePart{text("10:30am stand-up")}},
		{At: -1, Parts: []NotePart{text("at 1:23:45:01 or 1.2:30, not 3:2 or v2:30, but ("), at("5:00", 300), text(")")}},
		{At: 720, Parts: []NotePart{text("Lunch")}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%+v\nwant\n%+v", got, want)
	}
	if got[3].Text() != "Wrap-up, as 4:56 showed" {
		t.Errorf("text %q", got[3].Text())
	}
	if NoteLines(" \n\n ") != nil {
		t.Error("blank notes have lines")
	}
}

func TestMomentLink(t *testing.T) {
	link := MomentLink("p1", "b1", 83)
	if link != "leaflet://page/p1#b1?t=83" {
		t.Fatalf("link %q", link)
	}
	if id, ok := PageLinkID(link); !ok || id != "p1" || LinkBlock(link) != "b1" {
		t.Errorf("page %q %v, block %q", id, ok, LinkBlock(link))
	}
	for href, want := range map[string]int{link: 83, "leaflet://page/p1#b1?t=1m23s": 83, "leaflet://page/p1#b1?t=1:02:03": 3723, "leaflet://page/p1#b1?x=1&t=0": 0} {
		if got, ok := LinkMoment(href); !ok || got != want {
			t.Errorf("LinkMoment(%q) = %d, %v, want %d", href, got, ok, want)
		}
	}
	for _, href := range []string{BlockLink("p1", "b1"), PageLink("p1"), "leaflet://page/p1#b1?t=", "leaflet://page/p1#b1?t=soon", "https://x.dev/#b1?t=3", "leaflet://page/p1?t=3"} {
		if got, ok := LinkMoment(href); ok {
			t.Errorf("LinkMoment(%q) = %d, but it names no moment", href, got)
		}
	}
	// It stays a link in an agent's Markdown, not a mention of the page.
	d := FromMarkdown("See [1:23](" + link + ")")
	if d[1].Attrs.Href != link || d[1].Attrs.Mention != nil || !strings.Contains(d.ToMarkdown(), "("+link+")") {
		t.Errorf("read as %+v", d)
	}
}

func TestVideoNotes(t *testing.T) {
	spec := map[string]any{"type": "video", "video": map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "title": "Talk", "notes": "  0:00 Intro\r\n1:23 Caches  \n"}}
	n, err := SpecToNode(spec, "v1", "Agent")
	if err != nil || n.Str("notes") != "0:00 Intro\n1:23 Caches" {
		t.Fatalf("got %+v, %v", n, err)
	}
	if v := BlockViews(FromBlocks([]*Node{n}))[0]["video"].(map[string]any); v["notes"] != "0:00 Intro\n1:23 Caches" {
		t.Errorf("view %v", v)
	}
	if n, _ := SpecToNode(map[string]any{"type": "video", "url": "https://youtu.be/dQw4w9WgXcQ", "notes": " "}, "", "Agent"); n.Data["notes"] != nil {
		t.Errorf("blank notes kept: %v", n.Data)
	}
	long := map[string]any{"type": "video", "video": map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "notes": strings.Repeat("字", MaxNotes+1)}}
	if _, err := SpecToNode(long, "", "Agent"); err == nil {
		t.Error("notes past the cap were taken")
	}
	c := FromBlocks([]*Node{n})
	if c.PlainText() != "Talk\n0:00 Intro\n1:23 Caches" {
		t.Errorf("plain %q", c.PlainText())
	}
	md := PageMarkdown("P", c, MarkdownOptions{})
	if !strings.Contains(md, "[Talk](https://youtu.be/dQw4w9WgXcQ)\n\n- [0:00](https://www.youtube.com/watch?v=dQw4w9WgXcQ) Intro\n"+
		"- [1:23](https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=83s) Caches\n") {
		t.Errorf("markdown %q", md)
	}
}

func TestVideoNotesHTML(t *testing.T) {
	var notes []string
	for i := range FoldedLines + 4 {
		notes = append(notes, Clock(i*60)+" Part "+string(rune('A'+i)))
	}
	video := &Node{ID: "v1", Type: "video", Data: map[string]any{"url": "https://vimeo.com/76979871", "title": "Film", "notes": strings.Join(notes, "\n") + "\nsee 2:05 again"}}
	other := &Node{ID: "v2", Type: "video", Data: map[string]any{"url": "https://www.bilibili.com/video/BV1KofNYjEuK"}}
	para := &Node{ID: "p1", Type: "paragraph"}
	para.SetDelta(Delta{{Text: "As "}, {Text: "1:00", Attrs: Attrs{Href: MomentLink("pg", "v2", 60)}}, {Text: " and "},
		{Text: "gone", Attrs: Attrs{Href: MomentLink("pg", "nope", 5)}}, {Text: " show."}})
	c := &Content{Children: []*Node{video, other, para}}
	out := PageHTML("T", c, HTMLOptions{Script: func(name string) string { return "/" + name + ".js" }})
	for _, want := range []string{
		`<figure id="b-v1" data-block="v1" class="video" data-provider="vimeo">`,
		`<div class="transcript notes" style="--ts:4ch"><p><a class="ts" href="https://vimeo.com/76979871" rel="noopener noreferrer" data-t="0" data-src="https://player.vimeo.com/video/76979871?autoplay=1&amp;dnt=1">0:00</a><span class="line">Part A</span></p>`,
		`<details><summary>` + "Show all 11 lines",
		`<span class="line">see <a class="at" href="https://vimeo.com/76979871#t=125s" rel="noopener noreferrer" data-t="125" data-src="https://player.vimeo.com/video/76979871?autoplay=1&amp;dnt=1#t=125s">2:05</a> again</span></p></details></div></figure>`,
		`As <a href="https://www.bilibili.com/video/BV1KofNYjEuK?t=60" rel="noopener noreferrer" data-video="v2" data-t="60" data-src="https://player.bilibili.com/player.html?autoplay=1&amp;bvid=BV1KofNYjEuK&amp;high_quality=1&amp;t=60">1:00</a> and gone show.`,
		`<script src="/video.js" defer></script>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// On paper every line shows, each time a link to the site there.
	paper := PageHTML("T", c, HTMLOptions{Print: true})
	if strings.Contains(paper, "<details>") || strings.Contains(paper, "data-t=") || strings.Contains(paper, "video.js") ||
		!strings.Contains(paper, `<a class="ts" href="https://vimeo.com/76979871#t=540s" rel="noopener noreferrer">9:00</a>`) ||
		!strings.Contains(paper, `As <a href="https://www.bilibili.com/video/BV1KofNYjEuK?t=60" rel="noopener noreferrer">1:00</a>`) {
		t.Errorf("printed:\n%s", paper)
	}
}
