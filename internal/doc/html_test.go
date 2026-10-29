package doc

import (
	"strings"
	"testing"
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
		"<th>A</th><th>B</th>",
		"<td><strong>2</strong></td>",
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
		`<iframe src="https://www.youtube-nocookie.com/embed/aqz-KE-bpKQ?playsinline=1&amp;rel=0"`,
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
	if strings.Contains(out, "autoplay") {
		t.Errorf("a video plays as the page loads:\n%s", out)
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
