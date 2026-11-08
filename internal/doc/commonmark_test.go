package doc

import (
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// commonMarkText is what a CommonMark reader, GitHub's as MyGo has it,
// makes of a page's Markdown: each block of text, as its kind and the
// text it shows, a line break a space, and the address of each link in
// it. Raw HTML, which a reader shows as markup or not at all, is "html".
func commonMarkText(md string) (blocks []string, links []string) {
	d := parser.New(parser.GFM | parser.ExtAlerts)
	d.Set(md)
	for i := 1; i < d.Len(); i++ {
		switch b := d.Block(i); b.Kind {
		case parser.Paragraph, parser.Heading:
			// An alert's marker leaves a paragraph of no lines.
			if b.Kind == parser.Paragraph && d.LineCount(i) == 0 {
				continue
			}
			in := d.Inlines(i)
			text := parser.PlainText(in)
			for _, x := range in {
				switch x.Kind {
				case parser.HTML:
					text = "html"
				case parser.Link, parser.Image:
					links = append(links, x.Dest)
				}
			}
			blocks = append(blocks, b.Kind.String()+": "+text)
		case parser.HTMLBlock, parser.CodeBlock, parser.ThematicBreak, parser.Table, parser.MathBlock:
			blocks = append(blocks, b.Kind.String())
		}
	}
	return blocks, links
}

// Text that only a CommonMark reader would take for markup, as a line of
// ='s under another, which makes it a heading, an HTML block or tag, an
// entity, an autolink, or a heading's closing #s, is escaped in a page's
// Markdown: GitHub shows each block as the page does, and Leaflet reads
// it back as it was.
func TestPageMarkdownReadsAsWrittenInCommonMark(t *testing.T) {
	texts := []string{
		"Total\n===",
		"Total\n--",
		"Total\n====   ",
		"<!-- note -->",
		"a\n<!-- note -->",
		"<div>x</div>",
		"a\n<div>",
		"<?php echo 1; ?>",
		"<!DOCTYPE html>",
		"<![CDATA[x]]>",
		"<b>bold</b> and </i>",
		"<foo>",
		"AT&amp;T, &copy; 2024, &#65; and &#x42;",
		"<https://a.b> and <a@b.co> and <1x@y.io>",
		"1 < 2, a <3 and a & b",
	}
	for _, typ := range []string{"paragraph", "bulleted_list", "quote", "callout"} {
		for _, text := range texts {
			n := textNode(typ, Delta{{Text: text}})
			md := PageMarkdown("", FromBlocks([]*Node{n}), MarkdownOptions{})
			got, _ := commonMarkText(md)
			want := []string{"paragraph: " + strings.Join(strings.Fields(text), " ")}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s %q wrote %q, which reads as %q", typ, text, md, got)
			}
			if back := MarkdownBlocks(md); len(back) != 1 || back[0].Type != typ || back[0].Delta().Plain() != strings.TrimRight(text, " ") {
				t.Errorf("%s %q wrote %q, which Leaflet reads back as %v", typ, text, md, shape(back))
			}
		}
	}
	for _, text := range []string{"Use #", "Use ##", "##", "#", "C#", "a \\#", "<div>", "&amp;"} {
		h := textNode("heading", Delta{{Text: text}})
		h.Set("level", 2)
		md := PageMarkdown(text, FromBlocks([]*Node{h}), MarkdownOptions{})
		got, _ := commonMarkText(md)
		if want := []string{"heading: " + text, "heading: " + text}; !reflect.DeepEqual(got, want) {
			t.Errorf("heading %q wrote %q, which reads as %q", text, md, got)
		}
		if back := MarkdownBlocks(md); len(back) != 2 || back[1].Type != "heading" || back[1].Delta().Plain() != text {
			t.Errorf("heading %q wrote %q, which Leaflet reads back as %v", text, md, shape(back))
		}
	}
}

// A link's address goes where the page's link does in a CommonMark
// reader, an entity in it as written; an & that starts none stays as it
// is.
func TestPageMarkdownLinksReadAsWrittenInCommonMark(t *testing.T) {
	for _, href := range []string{"https://x.dev/a&amp;b", "https://x.dev/?a=1&b=2", "https://x.dev/a(b)&#65;"} {
		n := textNode("paragraph", Delta{{Text: "x", Attrs: Attrs{Href: href}}})
		img, err := ImageNode(href)
		if err != nil {
			t.Fatal(err)
		}
		md := PageMarkdown("", FromBlocks([]*Node{n, img}), MarkdownOptions{})
		if _, links := commonMarkText(md); !reflect.DeepEqual(links, []string{href, href}) {
			t.Errorf("%q wrote %q, which links to %q", href, md, links)
		}
		if back := MarkdownBlocks(md); len(back) != 2 || back[0].Delta()[0].Attrs.Href != href || back[1].Str("url") != href {
			t.Errorf("%q wrote %q, which Leaflet reads back as %v", href, md, shape(back))
		}
	}
	md := PageMarkdown("", FromBlocks([]*Node{textNode("paragraph", Delta{{Text: "x", Attrs: Attrs{Href: "https://x.dev/?a=1&b=2"}}})}), MarkdownOptions{})
	if !strings.Contains(md, "(https://x.dev/?a=1&b=2)") {
		t.Errorf("a query's & is escaped: %q", md)
	}
}
