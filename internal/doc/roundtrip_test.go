package doc

import (
	"slices"
	"strings"
	"testing"

	"leaflet/locales"
)

// readBack is blocks written as a page's Markdown and read back.
func readBack(blocks ...*Node) []*Node {
	return MarkdownBlocks(PageMarkdown("", FromBlocks(blocks), MarkdownOptions{}))
}

// sameShape fails t when blocks read back from their Markdown are not as
// they were.
func sameShape(t *testing.T, blocks ...*Node) {
	t.Helper()
	md := PageMarkdown("", FromBlocks(blocks), MarkdownOptions{})
	if got, want := shape(MarkdownBlocks(md)), shape(blocks); !slices.Equal(got, want) {
		t.Errorf("read back from\n%s\nas\n%q\nwant\n%q", md, got, want)
	}
}

func block(typ string, d Delta, kids ...*Node) *Node {
	n := textNode(typ, d)
	n.Children = kids
	return n
}

// A row of a table that is all dashes is a row, as a cell of "-" is the
// cell's text; only the line under the header is not.
func TestTableRowOfDashesReadsBack(t *testing.T) {
	table, _ := TableNode([][]string{{"a", "b"}, {"-", "--"}, {":-:", "x"}}, "")
	got := readBack(table)
	if len(got) != 1 || got[0].Type != "table" {
		t.Fatalf("read back as %q", shape(got))
	}
	if rows := TableRows(got[0]); !slices.EqualFunc(rows, [][]string{{"a", "b"}, {"-", "--"}, {":-:", "x"}}, slices.Equal) {
		t.Errorf("rows %q", rows)
	}
}

// Spaces at the start of a paragraph's text don't indent its line, which
// would put it in the list item before it.
func TestLeadingSpacesDontNest(t *testing.T) {
	item := block("bulleted_list", Delta{{Text: "item"}}, block("bulleted_list", Delta{{Text: "child"}}))
	para := block("paragraph", Delta{{Text: "    after\n  more"}})
	got := readBack(item, para)
	if len(got) != 2 || got[1].Type != "paragraph" || got[1].Delta().Plain() != "after\nmore" {
		t.Errorf("read back as %q", shape(got))
	}
}

// A line of a block's text that starts with a space of another kind, as a
// no-break or an ideographic space, then what would start a block, is
// text of the block: a $$ there doesn't make the blocks after it math,
// nor a </details> end the block.
func TestSpacesOfAnyKindBeforeABlockStart(t *testing.T) {
	for _, space := range []string{" ", "　", "\v", "\f"} {
		for _, start := range []string{"$$", "</details>", "| a |"} {
			for _, typ := range []string{"paragraph", "bulleted_list", "quote"} {
				text := "a\n" + space + start
				got := readBack(block("paragraph", Delta{{Text: "before"}}), block(typ, Delta{{Text: text}}), block("paragraph", Delta{{Text: "after"}}))
				if len(got) != 3 || got[1].Type != typ || got[1].Delta().Plain() != text || got[2].Delta().Plain() != "after" {
					t.Errorf("%s %q read back as %q", typ, text, shape(got))
				}
			}
		}
	}
}

// A ! before a link is text, not the start of a picture, at the start of
// a block or in it.
func TestBangBeforeALinkStaysText(t *testing.T) {
	link := Attrs{Href: "https://x.dev/cat.png"}
	for _, d := range []Delta{
		{{Text: "!"}, {Text: "cat", Attrs: link}},
		{{Text: "see!"}, {Text: "cat", Attrs: link}},
		{{Text: "hi!"}, {Text: "@Ann", Attrs: Attrs{Mention: &Mention{Kind: "user", ID: "u1", Name: "Ann"}}}},
	} {
		if md := d.ToMarkdown(); strings.Contains(strings.ReplaceAll(md, `\!`, ""), "![") {
			t.Errorf("%q has a picture's ![", md)
		}
		if got := FromMarkdown(d.ToMarkdown()); !sameShown(got, d.Compact()) {
			t.Errorf("%v read back as %v", d, got)
		}
		if d[1].Attrs.Mention == nil {
			sameShape(t, block("paragraph", d))
		}
	}
	if got := EscapeBlockStart("![a](https://x.dev/a.png)"); got != `\![a](https://x.dev/a.png)` {
		t.Errorf("escaped as %q", got)
	}
}

// Code over a line break, where the next line would start a block, and a
// </summary> in a toggle's code, read back as they were, the code
// keeping its text.
func TestCodeOverLinesReadsBack(t *testing.T) {
	code := Attrs{Code: true}
	for _, text := range []string{"<details>", "- item", "</details>", "# x", "  > quote"} {
		d := Delta{{Text: "a "}, {Text: "x\n" + text, Attrs: code}}
		got := readBack(block("paragraph", d))
		if len(got) != 1 || got[0].Type != "paragraph" || got[0].Delta().Plain() != "a x\n"+text {
			t.Errorf("%q read back as %q", text, shape(got))
		}
	}
	toggle := block("toggle_list", Delta{{Text: "see "}, {Text: "</summary>", Attrs: code}, {Text: " and </summary>"}})
	toggle.Set("collapsed", false)
	sameShape(t, toggle, block("paragraph", Delta{{Text: "after"}}))
}

// A | in a table cell's code reads back in the cell, a backslash before
// it too.
func TestPipesInTableCodeReadBack(t *testing.T) {
	table, _ := TableNode([][]string{{"`a|b`", "`a\\|b`"}, {`x\|y`, "`\\`"}}, "")
	got := readBack(table)
	if len(got) != 1 || got[0].Type != "table" {
		t.Fatalf("read back as %q", shape(got))
	}
	if got, want := TableRows(got[0]), TableRows(table); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("rows %q, want %q", got, want)
	}
}

// A page mention keeps the formats around its link and those of all its
// text.
func TestPageMentionKeepsItsFormats(t *testing.T) {
	for md, want := range map[string]Attrs{
		"**[Plan](leaflet://page/p1)**":     {Bold: true},
		"[**Plan**](leaflet://page/p1)":     {Bold: true},
		"*[~~Plan~~](leaflet://page/p1)*":   {Italic: true, Strike: true},
		"[**Pl**an](leaflet://page/p1)":     {},
		"[`Plan`](leaflet://page/p1) after": {Code: true},
	} {
		d := FromMarkdown(md)
		m := PageMention("p1", "Plan")
		want.Href, want.Mention = m.Href, m.Mention
		if len(d) == 0 || d[0].Text != "Plan" || !d[0].Attrs.Equal(want) {
			t.Errorf("%q read as %+v", md, d)
		}
	}
	bold := PageMention("p1", "Plan")
	bold.Bold = true
	d := Delta{{Text: "see "}, {Text: "Plan", Attrs: bold}}
	if got := FromMarkdown(d.ToMarkdown()); !sameShown(got, d) {
		t.Errorf("%q read back as %+v", d.ToMarkdown(), got)
	}
}

// A page mention shows on a web page as the page's title, a person as @
// and their name.
func TestMentionsInHTML(t *testing.T) {
	d := Delta{
		{Text: "Plan", Attrs: PageMention("p1", "Plan")},
		{Text: " "},
		{Text: "@Ann", Attrs: Attrs{Mention: &Mention{Kind: "user", ID: "u1", Name: "Ann"}}},
	}
	if got := d.HTML(); got != "Plan @Ann" {
		t.Errorf("HTML %q", got)
	}
}

// A chart's series with no name is nameless wherever the chart shows,
// and a null label empty, not "<nil>"; a number label is written as the
// server writes it, not as 1.25e+06.
func TestChartWithoutSeriesName(t *testing.T) {
	n := &Node{ID: "c", Type: "chart", Data: map[string]any{
		"labels": []any{"a", nil, 1250000.0, []any{"x"}}, "series": []any{map[string]any{"values": []any{1.0}}},
	}}
	md := PageMarkdown("", FromBlocks([]*Node{n}), MarkdownOptions{})
	page := PageHTML("", FromBlocks([]*Node{n}), HTMLOptions{})
	for _, out := range []string{md, page, ChartCSV(n, "Label")} {
		if strings.Contains(out, "nil") || !strings.Contains(out, "1250000") {
			t.Errorf("%q names the series <nil>, or a label not as written", out)
		}
	}
	if ch := ChartOf(n); ch.Kind != "bar" || !slices.Equal(ch.Labels, []string{"a", "", "1250000", ""}) || len(ch.Series) != 1 || ch.Series[0].Name != "" || ch.Series[0].Values[0] != 1 {
		t.Errorf("read as %+v", ch)
	}
}

// A numbered list of a thousand items and more reads back as one, while
// a line starting with a year, as "2024.", is still a paragraph.
func TestLongNumberedListReadsBack(t *testing.T) {
	var items []*Node
	for range 1002 {
		items = append(items, block("numbered_list", Delta{{Text: "x"}}))
	}
	got := readBack(items...)
	if len(got) != len(items) || got[len(got)-1].Type != "numbered_list" {
		t.Fatalf("read back %d blocks, the last %s", len(got), got[len(got)-1].Type)
	}
	if got := ParseLines("1. one\n2024. year"); len(got) != 2 || got[1].Type != "paragraph" {
		t.Errorf("read as %q", shape(got))
	}
}

// A language with spaces in its name reads back from its fence.
func TestCodeLanguageWithSpacesReadsBack(t *testing.T) {
	n := CodeNode("x", "Objective C")
	if l := n.Str("language"); l != "objective-c" {
		t.Errorf("language %q", l)
	}
	n.Set("language", "visual basic")
	got := readBack(n)
	if len(got) != 1 || got[0].Str("language") != "visual-basic" {
		t.Errorf("read back as %q", shape(got))
	}
}

// What an embed, a video and a recording stand for in an export is in the
// language it is written for, whatever the app shows; an embed with the
// title a new one takes, in any language, is named only for what it is.
func TestExportSaysBlocksInItsLanguage(t *testing.T) {
	embed := &Node{ID: "e", Type: "embed", Data: map[string]any{"title": ""}}
	video := &Node{ID: "v", Type: "video", Data: map[string]any{"url": "https://x.dev/a.mp4"}}
	rec := &Node{ID: "r", Type: "recording", Data: map[string]any{"title": "Sync"}}
	made := &Node{ID: "m", Type: "embed", Data: map[string]any{"title": locales.In("zh-Hans").DialogEmbedTitle()}}
	ja := locales.In("ja")
	md := PageMarkdown("", FromBlocks([]*Node{embed, video, rec, made}), MarkdownOptions{Lang: "ja"})
	for _, want := range []string{ja.ExportEmbed(), ja.ExportVideo(), ja.ExportRecordingTitled("Sync")} {
		if want == "" || !strings.Contains(md, want) {
			t.Errorf("%q does not say %q", md, want)
		}
	}
	if strings.Contains(md, "Recording") || strings.Contains(md, "Video") || strings.Contains(md, made.Str("title")) {
		t.Errorf("%q is not all in Japanese", md)
	}
	if strings.Count(md, "*"+ja.ExportEmbed()+"*") != 2 {
		t.Errorf("%q names an embed by a title a new one takes", md)
	}
	if got := PageMarkdown("", FromBlocks([]*Node{video}), MarkdownOptions{}); !strings.Contains(got, "[Video]") {
		t.Errorf("%q is not in the catalogs' own language", got)
	}
}

// A rule is three or more of one mark, with only spaces and tabs among
// and around them, as CommonMark and the server read one: a list item's
// "--" or a line starting with other spaces is text, and stays text,
// written and read again.
func TestRulesAsCommonMarkReadsThem(t *testing.T) {
	for _, s := range []string{"---", " * * *", "-\t-\t-", "___\t", "  ----  "} {
		if !IsRule(s) {
			t.Errorf("%q is a rule", s)
		}
	}
	for _, s := range []string{"\v---", "\u0085---", "-*-", "--", "- - x", " ---"} {
		if IsRule(s) {
			t.Errorf("%q is not a rule", s)
		}
	}
	todo := block("todo_list", Delta{{Text: "---"}})
	todo.Set("checked", false)
	for _, b := range []*Node{
		block("bulleted_list", Delta{{Text: "--"}}),
		block("bulleted_list", Delta{{Text: "- -"}}),
		todo,
		block("paragraph", Delta{{Text: "\v---"}}),
		block("paragraph", Delta{{Text: "\u0085---"}}),
		block("paragraph", Delta{{Text: "-\t-\t-"}}),
	} {
		sameShape(t, b)
		md := PageMarkdown("", FromBlocks([]*Node{b}), MarkdownOptions{})
		if again := PageMarkdown("", FromBlocks(MarkdownBlocks(md)), MarkdownOptions{}); again != md {
			t.Errorf("%q wrote %q, then %q", b.Delta().Plain(), md, again)
		}
	}
	if got := MarkdownBlocks(" ---"); len(got) != 1 || got[0].Type != "divider" {
		t.Errorf("an indented rule read as %q", shape(got))
	}
}

// Formats Markdown can't say all of are written as they read back, the
// same Markdown each time: a mention whose first space can't be in
// italics, and an italic over a space between two bold words.
func TestFormatsWriteTheSameEachTime(t *testing.T) {
	for _, md := range []string{"*[ 0](leaflet://page/0)*", "**0**_**0** **0**_", "<details>\n<summary>a\n  `x\n 000`</summary>\n</details>"} {
		written := PageMarkdown("", FromBlocks(MarkdownBlocks(md)), MarkdownOptions{})
		if again := PageMarkdown("", FromBlocks(MarkdownBlocks(written)), MarkdownOptions{}); again != written {
			t.Errorf("%q wrote %q, then %q", md, written, again)
		}
	}
	m := FromMarkdown("*[ 0](leaflet://page/0)*")
	if len(m) != 1 || !m[0].Attrs.Italic || m[0].Attrs.Mention == nil {
		t.Errorf("read as %+v", m)
	}
	toggle := block("toggle_list", Delta{{Text: "a\n  b "}, {Text: "x\n 000", Attrs: Attrs{Code: true}}})
	toggle.Set("collapsed", true)
	if got := readBack(toggle); len(got) != 1 || got[0].Delta().Plain() != "a\nb x\n 000" {
		t.Errorf("read back as %q", shape(got))
	}
}
