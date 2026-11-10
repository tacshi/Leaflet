package doc

import (
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/markdown/parser"
)

func commonMarkText(md string) (blocks []string, links []string) {
	d := parser.New(parser.GFM | parser.ExtAlerts)
	d.Set(md)
	for i := 1; i < d.Len(); i++ {
		switch b := d.Block(i); b.Kind {
		case parser.Paragraph, parser.Heading:
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
		"Head\n--- | ---",
		"Head\n--- |",
		"Head\n:--",
		"Name | Size\n:-- | --:",
		"a|b\n-|-",
		"a\n:-:",
		"--- |",
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

func TestPageMarkdownKeepsAddressesWhole(t *testing.T) {
	for _, tc := range []struct {
		text  string
		links []string
	}{
		{"see https://x.edu/~user/ now", []string{"https://x.edu/~user/"}},
		{"https://youtu.be/abc_-def", []string{"https://youtu.be/abc_-def"}},
		{"(https://en.wikipedia.org/wiki/Foo_(bar))", []string{"https://en.wikipedia.org/wiki/Foo_(bar)"}},
		{"www.x.dev/~a/b_-c and http://x.dev:80/?q=a_-b#~c", []string{"http://www.x.dev/~a/b_-c", "http://x.dev:80/?q=a_-b#~c"}},
		{"go to https://x.dev\nnext", []string{"https://x.dev"}},
		{"https://x.dev/~a \nwww.x.dev  \n\nHTTPS://X.DEV/A_-B\nend", []string{"https://x.dev/~a", "http://www.x.dev", "HTTPS://X.DEV/A_-B"}},
		{"~a~ https://x.dev/a~~b~~ https://x.dev/_a_-b_ x_-y", nil},
	} {
		for _, typ := range []string{"paragraph", "bulleted_list", "quote", "todo_list"} {
			n := textNode(typ, Delta{{Text: tc.text}})
			md := PageMarkdown("", FromBlocks([]*Node{n}), MarkdownOptions{})
			var links []string
			breaks := 0
			d := parser.New(parser.GFM | parser.ExtAlerts)
			d.Set(md)
			for i := 1; i < d.Len(); i++ {
				if d.Block(i).Kind != parser.Paragraph {
					continue
				}
				for _, x := range d.Inlines(i) {
					switch x.Kind {
					case parser.Link:
						links = append(links, x.Dest)
					case parser.LineBreak:
						breaks++
					}
				}
			}
			if tc.links != nil && !reflect.DeepEqual(links, tc.links) {
				t.Errorf("%s %q wrote %q, which links to %q, want %q", typ, tc.text, md, links, tc.links)
			}
			if want := strings.Count(tc.text, "\n"); breaks != want {
				t.Errorf("%s %q wrote %q, which has %d line breaks, want %d", typ, tc.text, md, breaks, want)
			}
			if back := MarkdownBlocks(md); len(back) != 1 || back[0].Type != typ || back[0].Delta().Plain() != tc.text {
				t.Errorf("%s %q wrote %q, which Leaflet reads back as %v", typ, tc.text, md, shape(back))
			}
		}
	}
	n := textNode("paragraph", Delta{{Text: "https://x.dev/~a/b~c", Attrs: Attrs{Href: "https://x.dev"}}})
	if md := PageMarkdown("", FromBlocks([]*Node{n}), MarkdownOptions{}); md != "[https://x.dev/\\~a/b\\~c](https://x.dev)\n" {
		t.Errorf("a link's text wrote %q", md)
	}
}
