package doc

import (
	"reflect"
	"slices"
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
		"10:30am stand-up\nat 1:23:45:01 or 1.2:30, not 3:2 or v2:30, but (5:00)\n12:00 | Lunch\n12:34:56:78 frames\n1:23.5 in, as 4:56.7 and 8:09.")
	want := []NoteLine{
		{At: -1, Parts: []NotePart{text("A talk on caching.")}},
		{At: 0, Parts: []NotePart{text("Intro")}},
		{At: 83, Parts: []NotePart{text("Why caches miss")}},
		{At: 3723, Parts: []NotePart{text("Wrap-up, as "), at("4:56", 296), text(" showed")}},
		{At: 750},
		{At: -1, Parts: []NotePart{text("10:30am stand-up")}},
		{At: -1, Parts: []NotePart{text("at 1:23:45:01 or 1.2:30, not 3:2 or v2:30, but ("), at("5:00", 300), text(")")}},
		{At: 720, Parts: []NotePart{text("Lunch")}},
		{At: -1, Parts: []NotePart{text("12:34:56:78 frames")}},
		{At: -1, Parts: []NotePart{text("1:23.5 in, as 4:56.7 and "), at("8:09", 489), text(".")}},
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
	for _, href := range []string{BlockLink("p1", "b1"), PageLink("p1"), "leaflet://page/p1#b1?t=", "leaflet://page/p1#b1?t=soon", "https://x.dev/#b1?t=3", "leaflet://page/p1?t=3",
		"leaflet://page/p1#b1?t=99999999999999999999", "leaflet://page/p1#b1?t=999999999h", "leaflet://page/p1#b1?t=596524h"} {
		if got, ok := LinkMoment(href); ok {
			t.Errorf("LinkMoment(%q) = %d, but it names no moment", href, got)
		}
	}
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
	lines := []string{"---", "# Chapter one", "1. numbered", "> quoted", "<details>", "- dashed", "** *"}
	md = notesMarkdown(strings.Join(lines, "\n"), func(int) string { return "" })
	back := ParseLines(md)
	if len(back) != len(lines) {
		t.Fatalf("read back %d blocks of %q", len(back), md)
	}
	for i, n := range back {
		if n.Type != "bulleted_list" || n.Delta().Plain() != lines[i] {
			t.Errorf("line %q read back as %s %q from %q", lines[i], n.Type, n.Delta().Plain(), md)
		}
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
	paper := PageHTML("T", c, HTMLOptions{Print: true})
	if strings.Contains(paper, "<details>") || strings.Contains(paper, "data-t=") || strings.Contains(paper, "video.js") ||
		!strings.Contains(paper, `<a class="ts" href="https://vimeo.com/76979871#t=540s" rel="noopener noreferrer">9:00</a>`) ||
		!strings.Contains(paper, `As <a href="https://www.bilibili.com/video/BV1KofNYjEuK?t=60" rel="noopener noreferrer">1:00</a>`) {
		t.Errorf("printed:\n%s", paper)
	}
}

func TestRecordingMomentHTML(t *testing.T) {
	para := &Node{ID: "p1", Type: "paragraph"}
	para.SetDelta(Delta{{Text: "1:23", Attrs: Attrs{Href: MomentLink("pg", "r1", 83)}}})
	c := &Content{Children: []*Node{{ID: "r1", Type: "recording"}, para}}
	want := `<a href="#b-r1" rel="noopener noreferrer" data-media="r1" data-t="83">1:23</a>`
	if out := PageHTML("T", c, HTMLOptions{}); !strings.Contains(out, want) {
		t.Errorf("missing %q in\n%s", want, out)
	}
	if out := PageHTML("T", c, HTMLOptions{Print: true}); strings.Contains(out, "<a ") || !strings.Contains(out, "1:23") {
		t.Errorf("on paper:\n%s", out)
	}
}

func TestFileNotesHTML(t *testing.T) {
	audio := &Node{ID: "a1", Type: "file", Data: map[string]any{"name": "call.m4a", "notes": "0:05 Hello\nsee 1:02"}}
	pdf := &Node{ID: "d1", Type: "file", Data: map[string]any{"name": "report.pdf", "notes": "0:05 Page one"}}
	plain := &Node{ID: "z1", Type: "file", Data: map[string]any{"name": "a.zip"}}
	asked := 0
	opts := HTMLOptions{File: func(n *Node) (string, string, string) {
		asked++
		return "/f/" + n.ID, "", ""
	}}
	c := &Content{Children: []*Node{audio, pdf, plain}}
	out := PageHTML("T", c, opts)
	for _, want := range []string{
		`<figure id="b-a1" data-block="a1" class="file"><a class="card file" href="/f/a1" download="call.m4a">`,
		`<audio controls preload="metadata" src="/f/a1"></audio>`,
		`<a class="ts" href="#b-a1" data-media="a1" data-t="5">0:05</a><span class="line">Hello</span>`,
		`see <a class="at" href="#b-a1" data-media="a1" data-t="62">1:02</a>`,
		`<span class="ts">0:05</span><span class="line">Page one</span>`,
		`<a id="b-z1" data-block="z1" class="card file" href="/f/z1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if asked != 3 {
		t.Errorf("asked %d times for 3 files", asked)
	}
	opts.Print = true
	if out := PageHTML("T", c, opts); strings.Contains(out, "<audio") || strings.Contains(out, "data-media") {
		t.Errorf("on paper:\n%s", out)
	}
}

func TestFileNotesMarkdown(t *testing.T) {
	c := &Content{Children: []*Node{{ID: "a1", Type: "file", Data: map[string]any{"name": "call.m4a", "url": "file:///x/call.m4a", "notes": "0:05 Hello"}}}}
	if got := PageMarkdown("", c, MarkdownOptions{}); !strings.Contains(got, "- 0:05 Hello") {
		t.Errorf("on this device:\n%s", got)
	}
	got := PageMarkdown("", c, MarkdownOptions{Link: func(string) string { return "files/call.m4a" }})
	if !strings.Contains(got, "- [0:05](files/call.m4a#t=5) Hello") {
		t.Errorf("in a zip:\n%s", got)
	}
}

func TestFileWithNotes(t *testing.T) {
	prev := &Node{ID: "a1", Type: "file", Data: map[string]any{"file_id": "f1", "url": "file:///x", "name": "call.m4a", "size": 3, "notes": "old"}}
	n, err := FileWithNotes(prev, map[string]any{"type": "file", "name": "evil.exe", "file": map[string]any{"notes": " 0:05 New\r\n", "file_id": "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if n.Str("notes") != "0:05 New" || n.Str("file_id") != "f1" || n.Str("name") != "call.m4a" || prev.Str("notes") != "old" {
		t.Errorf("got %+v, prev %+v", n.Data, prev.Data)
	}
	if n, err = FileWithNotes(prev, map[string]any{"type": "file", "file": map[string]any{"notes": ""}}); err != nil || n.Str("notes") != "" {
		t.Errorf("cleared to %q, %v", n.Str("notes"), err)
	}
	if _, err := FileWithNotes(prev, map[string]any{"notes": strings.Repeat("x", MaxNotes+1)}); err == nil {
		t.Error("too long a note went in")
	}
}

func TestWithoutSpeakers(t *testing.T) {
	notes := "0:00 Speaker 1: Hello there.\n0:05 说话人 2：你好\n[0:10] 話者 A：こんにちは\nSpeaker 3: No time.\n0:20 Speakers agree: yes.\nPlain line"
	want := "0:00 Hello there.\n0:05 你好\n[0:10] こんにちは\nNo time.\n0:20 Speakers agree: yes.\nPlain line"
	if got := WithoutSpeakers(notes, nil); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	if !HasSpeakers(notes, nil) || HasSpeakers(want, nil) || HasSpeakers("", nil) {
		t.Error("HasSpeakers is wrong")
	}
	if got := WithoutSpeakers("0:01 Speaker 1: Speaker 2: said so", nil); got != "0:01 Speaker 2: said so" {
		t.Errorf("got %q", got)
	}
}

func TestShownNotes(t *testing.T) {
	n := &Node{ID: "a1", Type: "file", Data: map[string]any{"name": "call.m4a", "notes": "0:05 Speaker 1: Hello"}}
	if got := ShownNotes(n); got != n.Str("notes") {
		t.Errorf("shown %q", got)
	}
	n.Set(SpeakersHidden, true)
	if got := ShownNotes(n); got != "0:05 Hello" {
		t.Errorf("hidden %q", got)
	}
	c := &Content{Children: []*Node{n}}
	if got := PageMarkdown("", c, MarkdownOptions{}); strings.Contains(got, "Speaker") {
		t.Errorf("Markdown:\n%s", got)
	}
	if got := PageHTML("T", c, HTMLOptions{}); strings.Contains(got, "Speaker 1") {
		t.Errorf("HTML:\n%s", got)
	}
	if n.Str("notes") != "0:05 Speaker 1: Hello" {
		t.Errorf("notes %q", n.Str("notes"))
	}
}

func TestRenameSpeaker(t *testing.T) {
	notes := "0:00 说话人 1：你好\n0:05 说话人 2：再见\n0:09 说话人 1：好的"
	got, names := RenameSpeaker(notes, nil, "说话人 1", " 小 明: \n")
	if want := "0:00 小 明：你好\n0:05 说话人 2：再见\n0:09 小 明：好的"; got != want {
		t.Fatalf("got %q", got)
	}
	if len(names) != 1 || names[0] != "小 明" {
		t.Fatalf("names %q", names)
	}
	if !HasSpeakers("0:00 小 明：你好", names) || HasSpeakers("0:00 小 明：你好", nil) {
		t.Error("the name isn't known by the list alone")
	}
	if got := WithoutSpeakers(got, names); got != "0:00 你好\n0:05 再见\n0:09 好的" {
		t.Errorf("hidden %q", got)
	}
	again, names := RenameSpeaker(got, names, "小 明", "Lin")
	if want := "0:00 Lin：你好\n0:05 说话人 2：再见\n0:09 Lin：好的"; again != want || !slices.Equal(names, []string{"Lin"}) {
		t.Errorf("got %q %q", again, names)
	}
	full, names := RenameSpeaker("0:00 Ann: Hi", []string{"Ann"}, "Ann", "Lin")
	if full != "0:00 Lin: Hi" || len(names) != 1 || names[0] != "Lin" {
		t.Errorf("got %q %q", full, names)
	}
	merged, _ := RenameSpeaker("0:00 Speaker 1: A\n0:01 Speaker 2: B", nil, "Speaker 1", "Speaker 2")
	if merged != "0:00 Speaker 2: A\n0:01 Speaker 2: B" {
		t.Errorf("merged %q", merged)
	}
	if same, _ := RenameSpeaker(notes, nil, "说话人 1", " : "); same != notes {
		t.Errorf("empty name: %q", same)
	}
}

func TestSetLineSpeaker(t *testing.T) {
	notes := "0:00 Speaker 1: A\n\n0:05 Speaker 2: B\n0:09 No speaker"
	lines := SpokenLines(notes, nil, false)
	if len(lines) != 3 || lines[1].Line != 2 || lines[1].Speaker != "Speaker 2" || lines[2].Speaker != "" {
		t.Fatalf("lines %+v", lines)
	}
	got, names := SetLineSpeaker(notes, nil, lines[1].Line, "Speaker 1")
	if want := "0:00 Speaker 1: A\n\n0:05 Speaker 1: B\n0:09 No speaker"; got != want {
		t.Fatalf("got %q", got)
	}
	got, names = SetLineSpeaker(got, names, lines[2].Line, "Ann")
	if want := "0:00 Speaker 1: A\n\n0:05 Speaker 1: B\n0:09 Ann: No speaker"; got != want || !HasSpeakers("0:09 Ann: No speaker", names) {
		t.Fatalf("got %q, names %q", got, names)
	}
	if same, _ := SetLineSpeaker(got, names, 99, "Bob"); same != got {
		t.Errorf("a line that isn't there: %q", same)
	}
	if want := []string{"Speaker 1", "Ann"}; !slices.Equal(NoteSpeakers(got, names), want) {
		t.Errorf("speakers %q", NoteSpeakers(got, names))
	}
	if hidden := SpokenLines(got, names, true); hidden[2].Parts[0].Text != "No speaker" || hidden[2].Speaker != "Ann" {
		t.Errorf("hidden %+v", hidden[2])
	}
}

func TestSpeakerNamesOf(t *testing.T) {
	n := &Node{ID: "a", Type: "file", Data: map[string]any{}}
	SetSpeakerNames(n, []string{"Ann", "Lin"})
	if got := SpeakerNamesOf(n); !slices.Equal(got, []string{"Ann", "Lin"}) {
		t.Errorf("names %q", got)
	}
	SetSpeakerNames(n, nil)
	if _, ok := n.Data[SpeakerNames]; ok {
		t.Error("no names left a key")
	}
}

func TestNotesTextAndBlocks(t *testing.T) {
	notes := "0:05 Speaker 1: Hello\nA plain line"
	if got := NotesText(notes, nil, false); got != "Speaker 1: Hello\nA plain line" {
		t.Errorf("text %q", got)
	}
	if got := NotesText(notes, nil, true); got != "Hello\nA plain line" {
		t.Errorf("hidden %q", got)
	}
	blocks := NotesBlocks(notes, nil, true)
	if len(blocks) != 2 {
		t.Fatalf("%d blocks", len(blocks))
	}
	if d := blocks[0].Delta(); len(d) != 2 || d[0].Text != "0:05  " || !d[0].Attrs.Bold || d[1].Text != "Hello" {
		t.Errorf("first %+v", d)
	}
	if d := blocks[1].Delta(); len(d) != 1 || d[0].Text != "A plain line" {
		t.Errorf("second %+v", d)
	}
}
