package doc

import (
	"fmt"
	"strings"
	"testing"

	"leaflet/locales"
)

func TestDeltaHTMLEscapes(t *testing.T) {
	d := Delta{
		{Text: "<script>alert(1)</script>"},
		{Text: "bad", Attrs: Attrs{Href: "javascript:alert(1)"}},
		{Text: "ok", Attrs: Attrs{Href: "https://x.dev/?a=1&b=2", Bold: true}},
		{Text: "red", Attrs: Attrs{Color: "#ff0000"}},
		{Text: "evil", Attrs: Attrs{Color: "red;background:url(x)"}},
		{Text: "a\nb"},
		{Text: "x", Attrs: Attrs{Mention: &Mention{Kind: "user", Name: "<Ann>"}}},
	}
	got := d.HTML()
	for _, want := range []string{
		"&lt;script&gt;alert(1)&lt;/script&gt;",
		"bad",
		`<a href="https://x.dev/?a=1&amp;b=2" rel="noopener noreferrer"><strong>ok</strong></a>`,
		`<span style="color:#ff0000">red</span>`,
		"evil",
		"a<br>b",
		"@&lt;Ann&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, bad := range []string{"<script>", "javascript:", "url(x)"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q in %s", bad, got)
		}
	}
}

func TestPageHTMLBlocks(t *testing.T) {
	item := func(id, typ, text string, kids ...*Node) *Node {
		n := &Node{ID: id, Type: typ, Children: kids}
		n.SetDelta(Delta{{Text: text}})
		return n
	}
	todo := item("t1", "todo_list", "done")
	todo.Set("checked", true)
	heading := item("h1", "heading", "Plan")
	heading.Set("level", 2)
	code := CodeNode("x < 1", "go")
	code.ID = "c1"
	table, err := TableNode([][]string{{"A", "B"}, {"1", "**2**"}}, "tb")
	if err != nil {
		t.Fatal(err)
	}
	local := &Node{ID: "i1", Type: "image", Data: map[string]any{"url": "file:///Users/me/a.png", "alt": "a"}}
	gone := &Node{ID: "i2", Type: "image", Data: map[string]any{"url": "file:///Users/me/b.png"}}
	web := &Node{ID: "i3", Type: "image", Data: map[string]any{"url": "https://x.dev/c.png"}}
	embed := &Node{ID: "e1", Type: "embed", Data: map[string]any{"title": "Demo", "html": "<script>steal()</script>"}}
	math := MathNode("x^2")
	math.ID = "m1"
	c := &Content{Children: []*Node{
		heading,
		item("b1", "bulleted_list", "one", item("b2", "bulleted_list", "nested")),
		item("b3", "bulleted_list", "two"),
		item("n1", "numbered_list", "first"),
		todo,
		code, table, local, gone, web, embed, math,
	}}
	out := PageHTML("A <title>", c, HTMLOptions{
		Image: func(n *Node) string {
			if n.ID == "i1" {
				return "/tok/f/1"
			}
			return ""
		},
		Script: func(name string) string { return "/tok/" + name + ".js" },
		Tail:   "<aside>threads</aside>",
	})
	for _, want := range []string{
		"<title>A &lt;title&gt;</title>",
		`<h2 id="b-h1" data-block="h1">Plan</h2>`,
		"<ul>\n<li id=\"b-b1\" data-block=\"b1\">one\n<ul>\n<li id=\"b-b2\" data-block=\"b2\">nested</li>\n</ul>\n</li>\n<li id=\"b-b3\" data-block=\"b3\">two</li>\n</ul>",
		"<ol>\n<li id=\"b-n1\"",
		`<input type="checkbox" disabled checked> done`,
		`<code class="language-go">x &lt; 1</code>`,
		`<th class="nowrap">A</th><th class="nowrap">B</th>`,
		`<td class="nowrap"><strong>2</strong></td>`,
		`<img src="/tok/f/1" alt="a"`,
		`<img src="https://x.dev/c.png"`,
		"<em>Demo</em>",
		`data-tex="x^2"`,
		`<script src="/tok/math.js" defer></script>`,
		`<script src="/tok/comments.js" defer></script>`,
		"</main><aside>threads</aside></body>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, bad := range []string{"steal()", "b.png", "diagram.js", "file://"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q in\n%s", bad, out)
		}
	}
}

func TestPageHTMLVideoCodeCallout(t *testing.T) {
	video := &Node{ID: "v1", Type: "video", Data: map[string]any{"url": "https://www.youtube.com/watch?v=aqz-KE-bpKQ", "title": "Big Buck Bunny"}}
	file := &Node{ID: "v2", Type: "video", Data: map[string]any{"url": "https://x.dev/clip.mp4"}}
	code := CodeNode("x := 1", "go")
	code.ID = "c1"
	callout := &Node{ID: "k1", Type: "callout", Data: map[string]any{"tone": "tip"}}
	callout.SetDelta(Delta{{Text: "Note"}})
	embed := &Node{ID: "e1", Type: "embed", Data: map[string]any{"title": "Piano", "html": "<script>x()</script>", "height": 2000}}
	out := PageHTML("T", &Content{Children: []*Node{video, file, code, callout, embed}}, HTMLOptions{
		Embed:       func(n *Node) string { return "/tok/e/" + n.ID },
		Script:      func(name string) string { return "/tok/" + name + ".js" },
		Code:        func(code, lang string) string { return "<b>" + lang + "</b>" },
		CalloutIcon: func(tone string) string { return "<svg>" + tone + "</svg>" },
	})
	for _, want := range []string{
		`<iframe src="https://www.youtube-nocookie.com/embed/aqz-KE-bpKQ?playsinline=1&amp;rel=0&amp;enablejsapi=1"`,
		`<script src="/tok/video.js" defer></script>`,
		`>Big Buck Bunny</a></figcaption>`,
		`<video controls preload="metadata" src="https://x.dev/clip.mp4">`,
		`<code class="language-go"><b>go</b></code>`,
		`<div id="b-k1" data-block="k1" class="callout tip"><span class="icon"><svg>tip</svg></span><div>Note</div></div>`,
		`<figcaption>Piano</figcaption><iframe sandbox="allow-scripts" src="/tok/e/e1" title="Piano" data-height="1200" style="height:1200px"`,
		`<script src="/tok/embed.js" defer></script>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "x()") {
		t.Errorf("an embed's HTML is in the page:\n%s", out)
	}
	if strings.Contains(out, "autoplay=1") || !strings.Contains(out, `allow="autoplay;`) {
		t.Errorf("a video plays as the page loads, or can't when asked:\n%s", out)
	}
}

func TestPageHTMLWithoutScripts(t *testing.T) {
	d := DiagramNode("graph TD; A-->B")
	d.ID = "d1"
	out := PageHTML("T", &Content{Children: []*Node{d}}, HTMLOptions{})
	if !strings.Contains(out, "<pre id=\"b-d1\" data-block=\"d1\"><code>graph TD; A--&gt;B</code></pre>") || strings.Contains(out, "<script") {
		t.Errorf("diagram without its script:\n%s", out)
	}
}

func TestPageHTMLTableShortColumns(t *testing.T) {
	table, err := TableNode([][]string{
		{"Step", "What to do"},
		{"1. Listen & restate", "Don't touch the marker yet. Repeat the problem in my own words"},
	}, "tb")
	if err != nil {
		t.Fatal(err)
	}
	out := PageHTML("T", &Content{Children: []*Node{table}}, HTMLOptions{})
	for _, want := range []string{
		`<th class="nowrap">Step</th><th>What to do</th>`,
		`<td class="nowrap">1. Listen &amp; restate</td><td>Don&#39;t touch`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestTableWithBadSize(t *testing.T) {
	for _, size := range [][2]any{{-1, 2}, {2, -5}, {1e9, 1e9}, {"x", 2}, {3, 3}} {
		table, err := TableNode([][]string{{"A", "B"}, {"1", "2"}}, "tb")
		if err != nil {
			t.Fatal(err)
		}
		table.Data["rowsLen"], table.Data["colsLen"] = size[0], size[1]
		c := &Content{Children: []*Node{table}}
		_ = PageHTML("T", c, HTMLOptions{})
		_ = PageMarkdown("T", c, MarkdownOptions{PageTitle: func(string) string { return "" }})
		_ = BlockViews(c)
		rows, cols := TableSize(table)
		if rows < 0 || rows > 2 || cols < 0 || cols > 2 {
			t.Errorf("size %v is %d×%d", size, rows, cols)
		}
	}
	table := &Node{ID: "tb", Type: "table", Data: map[string]any{"rowsLen": 1 << 30, "colsLen": 1 << 30}, Children: []*Node{
		{ID: "a", Type: "table/cell", Data: map[string]any{"rowPosition": 1 << 29, "colPosition": 0}},
		{ID: "b", Type: "table/cell", Data: map[string]any{"rowPosition": 0, "colPosition": 1 << 29}},
	}}
	if rows, cols := TableSize(table); rows*cols > maxTableCells {
		t.Errorf("%d×%d cells", rows, cols)
	}
	_ = PageHTML("T", &Content{Children: []*Node{table}}, HTMLOptions{})
}

func TestPageHTMLFoldsLongTranscript(t *testing.T) {
	n := &Node{ID: "r1", Type: "recording", Data: map[string]any{"recording_id": "rec", "title": "Call"}}
	lines := func(count int) []TranscriptLine {
		var out []TranscriptLine
		for i := range count {
			out = append(out, TranscriptLine{Start: float64(i), Time: fmt.Sprintf("0:%02d", i), Text: fmt.Sprintf("Line %c.", 'A'+i)})
		}
		return out
	}
	page := func(count int) string {
		return PageHTML("Notes", FromBlocks([]*Node{n}), HTMLOptions{Lang: "en", Recording: func(*Node) RecordingView {
			return RecordingView{Title: "Call", Lines: lines(count)}
		}})
	}
	long := page(FoldedLines + 4)
	before, after, ok := strings.Cut(long, "<details><summary>"+locales.In("en").RecShowAll(FoldedLines+4))
	if !ok || strings.Count(before, "<p>") != FoldedLines || strings.Count(after, "<p>") != 4 || !strings.Contains(after, "Line J.</span></p></details></div>") {
		t.Errorf("a long transcript:\n%s", long)
	}
	if short := page(FoldedLines + 2); strings.Contains(short, "<details>") || strings.Count(short, "<p>") != FoldedLines+2 {
		t.Errorf("a short transcript:\n%s", short)
	}
}

func TestPageHTMLPrint(t *testing.T) {
	text := func(id, typ, s string) *Node {
		n := &Node{ID: id, Type: typ}
		n.SetDelta(Delta{{Text: s}})
		return n
	}
	folded := text("h1", "heading", "Later")
	folded.Set("level", 2)
	folded.Set("collapsed", true)
	toggle := text("tg", "toggle_list", "More")
	toggle.Set("collapsed", true)
	toggle.Children = []*Node{text("in", "paragraph", "Inside")}
	code := text("hc", "heading", "Code")
	code.Set("level", 2)
	c := FromBlocks([]*Node{
		folded, text("p", "paragraph", "Hidden"), code, text("c", "code", "x"), toggle,
		{ID: "v", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "title": "Song"}},
		{ID: "i", Type: "image", Data: map[string]any{"url": "https://x.dev/c.png"}},
		{ID: "r", Type: "recording", Data: map[string]any{"recording_id": "r1"}},
		{ID: "m", Type: "math_equation", Data: map[string]any{"formula": "x^2"}},
		{ID: "d", Type: "diagram", Data: map[string]any{"source": "graph TD; A-->B"}},
	})
	var lines []TranscriptLine
	for i := range 12 {
		lines = append(lines, TranscriptLine{Time: fmt.Sprint(i), Text: fmt.Sprint("line ", i)})
	}
	opts := HTMLOptions{
		Recording: func(*Node) RecordingView { return RecordingView{Title: "Talk", Lines: lines} },
		ScriptSource: func(name string) string {
			if name != "math" && name != "diagram" {
				return ""
			}
			return "/*" + name + "*/"
		},
	}
	screen := PageHTML("Trip", c, opts)
	opts.Print = true
	paper := PageHTML("Trip", c, opts)
	for _, want := range []string{"prefers-color-scheme:dark", `<details class="fold">`, `<iframe src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ`, `loading="lazy"`, "<details><summary>"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen's page lacks %q", want)
		}
		if strings.Contains(paper, want) {
			t.Errorf("the printed page has %q", want)
		}
	}
	for _, want := range []string{
		":root{color-scheme:light}",
		"<h2 id=\"b-h1\" data-block=\"h1\">Later</h2>\n<p id=\"b-p\" data-block=\"p\">Hidden</p>",
		"<div class=\"keep\"><h2 id=\"b-hc\" data-block=\"hc\">Code</h2>\n<pre id=\"b-c\" data-block=\"c\"><code>x</code></pre>\n</div>",
		`<details id="b-tg" data-block="tg" open>`,
		`<a id="b-v" data-block="v" class="card link" href="https://www.youtube.com/watch?v=dQw4w9WgXcQ"`,
		`<img src="https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg" alt="">`,
		`<img src="https://x.dev/c.png" alt="">`,
		"line 11",
		"</main><script>/*math*/</script>\n<script type=\"module\">/*diagram*/</script>\n</body>",
	} {
		if !strings.Contains(paper, want) {
			t.Errorf("the printed page lacks %q:\n%s", want, paper)
		}
	}
	if strings.Contains(paper, "<script src=") {
		t.Error("the printed page links a script")
	}
	for lang, font := range map[string]string{"": "PingFangSC-Regular", "zh-Hans": "PingFangSC-Regular", "ja": "HiraginoSans-W3"} {
		opts.Lang = lang
		if page := PageHTML("Trip", c, opts); !strings.Contains(page, `@font-face{font-family:"Leaflet CJK";src:local("`+font+`")`) {
			t.Errorf("a page in %q is not in %s", lang, font)
		}
	}
	opts.Lang = "en"
	for _, s := range []struct{ region, text, font string }{
		{"US", "旅の計画", "HiraginoSans-W3"},
		{"TW", "旅の計画", "HiraginoSans-W3"},
		{"TW", "旅行計畫", "PingFangTC-Regular"},
		{"HK", "旅行計劃", "PingFangHK-Regular"},
		{"US", "旅行计划", "PingFangSC-Regular"},
	} {
		opts.Region = s.region
		trip := FromBlocks([]*Node{para("p", s.text)})
		if page := PageHTML("Trip", trip, opts); !strings.Contains(page, `@font-face{font-family:"Leaflet CJK";src:local("`+s.font+`")`) {
			t.Errorf("%q printed in %s is not in %s", s.text, s.region, s.font)
		}
	}
	if strings.Contains(screen, "Leaflet CJK") {
		t.Error("the screen's page names the printed page's fonts")
	}
}
