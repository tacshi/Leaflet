package doc

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMarkdownBlocksReadsAReply(t *testing.T) {
	var types []string
	for _, n := range MarkdownBlocks("It says:\n\n> a quote\n\n- one\n") {
		types = append(types, n.Type)
	}
	if got := strings.Join(types, " "); got != "paragraph quote bulleted_list" {
		t.Errorf("blocks %q", got)
	}
}

// A page exported as Markdown reads back as the same blocks: text that
// looks like a block's marker stays text, and a | stays in its cell.
func TestMarkdownExportReadsBack(t *testing.T) {
	var blocks []*Node
	for _, s := range []string{"```js", "# not a heading", "2*3*4", "- not an item", "1. not numbered", "> not a quote", "| not a table |", "$$", "---", "[!NOTE]"} {
		blocks = append(blocks, textNode("paragraph", Delta{{Text: s}}))
	}
	table, _ := TableNode([][]string{{"a|b", `c\|d`}, {"`x|y`", "e"}}, "")
	blocks = append(blocks, table, textNode("paragraph", Delta{{Text: "last"}}))
	md := PageMarkdown("", FromBlocks(blocks), MarkdownOptions{})
	got := MarkdownBlocks(md)
	if len(got) != len(blocks) {
		t.Fatalf("%q read back as %d blocks", md, len(got))
	}
	for i, n := range got {
		if n.Type != blocks[i].Type || !reflect.DeepEqual(n.Delta(), blocks[i].Delta()) {
			t.Errorf("block %d: %s %+v, want %s %+v", i, n.Type, n.Delta(), blocks[i].Type, blocks[i].Delta())
		}
	}
	if rows := TableRows(got[len(got)-2]); !reflect.DeepEqual(rows, TableRows(table)) {
		t.Errorf("table rows %q, want %q", rows, TableRows(table))
	}
}

func TestPasteMarkdownBlankLinesSeparate(t *testing.T) {
	nodes := ParseLines("## Start\n\n> [!NOTE]\n> Needs Go\n\n1. one\n\n2. two\n\n```go\na := 1\n\nb := 2\n```\n")
	var types []string
	for _, n := range nodes {
		types = append(types, n.Type)
		if n.Type == "paragraph" && n.Delta().Len() == 0 {
			t.Errorf("an empty paragraph in %v", types)
		}
	}
	if got := strings.Join(types, " "); got != "heading callout numbered_list numbered_list code" {
		t.Fatalf("blocks %s", got)
	}
	if code := nodes[4].Delta().Plain(); code != "a := 1\n\nb := 2" {
		t.Errorf("code %q lost its blank line", code)
	}
}

// The markers Markdown has for headings and lists all read as blocks, as
// copying escapes them all, and a line that is a picture is one.
func TestPasteMarkdownMarkers(t *testing.T) {
	var got []string
	for _, n := range ParseLines("#### Step\n###### Six\n####### seven\n#tag\n+ plus\n+ [x] done\n- [X] also\n1) one\n2024. year\n![A cat](https://x.dev/cat.png)\n![local](file:///cat.png)") {
		got = append(got, fmt.Sprintf("%s%d:%s%s", n.Type, n.Int("level", 0), n.Delta().Plain(), n.Str("alt")))
	}
	want := []string{"heading4:Step", "heading6:Six", "paragraph0:####### seven", "paragraph0:#tag", "bulleted_list0:plus",
		"todo_list0:done", "todo_list0:also", "numbered_list0:one", "paragraph0:2024. year", "image0:A cat", "paragraph0:!local"}
	if !slices.Equal(got, want) {
		t.Errorf("blocks\n%q\nwant\n%q", got, want)
	}
}

func TestTableRowSplitsOnUnescapedPipes(t *testing.T) {
	for line, want := range map[string][]string{
		"| a | b |":         {"a", "b"},
		`| a\|b | c |`:      {"a|b", "c"},
		`| a\\| b |`:        {`a\\`, "b"},
		`| \*x\* | y`:       {`\*x\*`, "y"},
		"| `a\\|b` | c |":   {"`a|b`", "c"},
		"| `a\\\\|b` | c |": {"`a\\|b`", "c"},
	} {
		if got, rule := tableRow(line); !reflect.DeepEqual(got, want) || rule {
			t.Errorf("%q split as %q, %v, want %q", line, got, rule, want)
		}
	}
	if _, rule := tableRow("| --- | :---: |"); !rule {
		t.Error("the line under the header is not read as one")
	}
}

// shape writes blocks as lines of their type, the data that is theirs,
// and their text, indented under what holds them, to compare trees.
func shape(nodes []*Node) []string {
	var out []string
	var visit func([]*Node, string)
	visit = func(nodes []*Node, indent string) {
		for _, n := range nodes {
			s := indent + n.Type
			for _, k := range []string{"level", "checked", "language", "tone", "collapsed", "url", "alt", "formula", "source"} {
				if v, ok := n.Data[k]; ok {
					s += fmt.Sprintf(" %s=%v", k, v)
				}
			}
			if n.IsText() {
				s += fmt.Sprintf(" %q", n.Delta().ToMarkdown())
			}
			if n.Type == "table" {
				s += fmt.Sprintf(" %q", TableRows(n))
				out = append(out, s)
				continue
			}
			out = append(out, s)
			visit(n.Children, indent+"  ")
		}
	}
	visit(nodes, "")
	return out
}

// Every block Markdown has a way to write reads back from a page's
// Markdown as it was, nested and empty ones too.
func TestPageMarkdownRoundTrips(t *testing.T) {
	n := func(typ, md string, data map[string]any, kids ...*Node) *Node {
		x := &Node{ID: NewBlockID(), Type: typ, Data: map[string]any{}, Children: kids}
		for k, v := range data {
			x.Data[k] = v
		}
		x.SetDelta(FromMarkdown(md))
		return x
	}
	code := CodeNode("if a {\n\treturn\n}", "go")
	empty := CodeNode("", "")
	table, _ := TableNode([][]string{{"a|b", "two\nlines"}, {"`x|y`", `\<br> stays`}}, "")
	img, _ := ImageNode("https://x.dev/cat.png")
	img.Set("alt", "A cat")
	blocks := []*Node{
		n("heading", "Title", map[string]any{"level": 1}),
		n("heading", "", map[string]any{"level": 3}),
		n("paragraph", "one\ntwo **bold\nacross**\n\nafter a blank line\n- not a list\nends in \\\\", nil),
		n("bulleted_list", "item\nsecond line", nil,
			n("bulleted_list", "nested", nil, n("todo_list", "deep", map[string]any{"checked": true})),
			n("paragraph", "a paragraph in it\nof two lines", nil),
			n("quote", "quoted", nil),
			code,
			n("callout", "careful", map[string]any{"tone": "warning"}),
		),
		n("bulleted_list", "", nil),
		n("numbered_list", "first", nil, n("numbered_list", "sub", nil)),
		n("numbered_list", "", nil),
		n("todo_list", "", map[string]any{"checked": false}),
		n("todo_list", "done", map[string]any{"checked": true}),
		n("quote", "", nil),
		n("quote", "a quote\nof lines\n\nand a gap", nil),
		n("callout", "", map[string]any{"tone": "info"}),
		n("callout", "tip\non lines", map[string]any{"tone": "tip"}),
		n("toggle_list", "open *toggle*\non lines", map[string]any{"collapsed": false},
			n("paragraph", "inside", nil),
			n("toggle_list", "folded", map[string]any{"collapsed": true}, n("bulleted_list", "deeper", nil)),
		),
		n("toggle_list", "", map[string]any{"collapsed": true}),
		{ID: NewBlockID(), Type: "divider"},
		empty,
		img,
		table,
		MathNode("E = mc^2"),
		DiagramNode("graph TD\n  A-->B"),
		n("paragraph", "<details> is text, and <summary>", nil),
		n("paragraph", "last", nil),
	}
	md := PageMarkdown("", FromBlocks(blocks), MarkdownOptions{})
	got, want := shape(MarkdownBlocks(md)), shape(blocks)
	if !slices.Equal(got, want) {
		t.Errorf("%s\nread back as\n%s\nwant\n%s", md, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A list item nests under the one it is indented under, by the indent
// that item had, not by how deep it is.
func TestParseLinesNestsByIndent(t *testing.T) {
	for text, want := range map[string][]string{
		"- a\n    - b\n        - c\n    - d": {`bulleted_list "a"`, `  bulleted_list "b"`, `    bulleted_list "c"`, `  bulleted_list "d"`},
		"1. Step\n   1. Sub\n      - detail\n        - more\n      - detail2": {
			`numbered_list "Step"`, `  numbered_list "Sub"`, `    bulleted_list "detail"`, `      bulleted_list "more"`, `    bulleted_list "detail2"`},
		"- a\n - b":                              {`bulleted_list "a"`, `bulleted_list "b"`},
		"1. a\n  - b\n\t- c":                     {`numbered_list "a"`, `  bulleted_list "b"`, `  bulleted_list "c"`},
		"- a\n        - b\n- c":                  {`bulleted_list "a"`, `  bulleted_list "b"`, `bulleted_list "c"`},
		"- a\nnot in it\n  - b":                  {`bulleted_list "a"`, `paragraph "not in it"`, `bulleted_list "b"`},
		"- a\n  more of a\n\n  under a\n\nafter": {`bulleted_list "a\nmore of a"`, `  paragraph "under a"`, `paragraph "after"`},
	} {
		if got := shape(ParseLines(text)); !slices.Equal(got, want) {
			t.Errorf("%q read as\n%s\nwant\n%s", text, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

// Lines are blocks of their own but where a hard break, a quote's > or an
// item's indent carries one on, and a marker ending its line is an empty
// block of its kind.
func TestParseLinesContinues(t *testing.T) {
	for text, want := range map[string][]string{
		"one\ntwo":                      {`paragraph "one"`, `paragraph "two"`},
		"one\\\ntwo\\\n\\\nthree":       {`paragraph "one\ntwo\n\nthree"`},
		"one\\\n- two":                  {`paragraph "one\\\\"`, `bulleted_list "two"`},
		"ends in two  \nspaces":         {`paragraph "ends in two  "`, `paragraph "spaces"`},
		"> a\n> b\n>\n> c\n\n> d":       {`quote "a\nb\n\nc"`, `quote "d"`},
		"> a\\\nlazy\nnot":              {`quote "a\nlazy"`, `paragraph "not"`},
		"> [!NOTE]\n> a\n> [!TIP]\n> b": {`callout tone=info "a"`, `callout tone=tip "b"`},
		"-\n- [ ]\n- [x]\n1.\n>\n#":     {`bulleted_list ""`, `todo_list checked=false ""`, `todo_list checked=true ""`, `numbered_list ""`, `quote ""`, `heading level=1 ""`},
		"- - -\n--\n-x":                 {`divider`, `paragraph "--"`, `paragraph "-x"`},
	} {
		if got := shape(ParseLines(text)); !slices.Equal(got, want) {
			t.Errorf("%q read as\n%s\nwant\n%s", text, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

// A <details> is a toggle holding what is up to its </details>, its
// <summary> its text, on the line or lines after or on its own line.
func TestParseLinesToggles(t *testing.T) {
	for text, want := range map[string][]string{
		"<details><summary>Hi</summary>\n\nin\n</details>\nout":              {`toggle_list collapsed=true "Hi"`, `  paragraph "in"`, `paragraph "out"`},
		"<DETAILS open>\n\n<summary>two\nlines</summary>\n- a\n\n</details>": {`toggle_list collapsed=false "two\nlines"`, `  bulleted_list "a"`},
		"- item\n\n  <details>\n  <summary>t</summary>\n\n  - x\n\n  </details>\n\n  after": {
			`bulleted_list "item"`, `  toggle_list collapsed=true "t"`, `    bulleted_list "x"`, `  paragraph "after"`},
		"<details>\nno summary": {`toggle_list collapsed=true ""`, `  paragraph "no summary"`},
		"</details>":            {`paragraph "</details>"`},
	} {
		if got := shape(ParseLines(text)); !slices.Equal(got, want) {
			t.Errorf("%q read as\n%s\nwant\n%s", text, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

// A fence names its language in any case, and an empty diagram or
// formula, with nothing to draw, makes no block, as a spec of one makes
// none.
func TestParseLinesFences(t *testing.T) {
	for text, want := range map[string][]string{
		"```Mermaid\ngraph TD\n```":              {`diagram source=graph TD`},
		"```JS\nx\n```":                          {`code language=javascript "x"`},
		"```mermaid\n\n```\n$$\n $$\nx":          {`paragraph "x"`},
		"```\n```":                               {`code language= ""`},
		"- a\n\n  ```go\n  x := 1\n    y\n  ```": {`bulleted_list "a"`, `  code language=go "x := 1\n  y"`},
	} {
		if got := shape(ParseLines(text)); !slices.Equal(got, want) {
			t.Errorf("%q read as\n%s\nwant\n%s", text, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

// A picture line may have a title and a link with parentheses.
func TestParseLinesImages(t *testing.T) {
	for text, want := range map[string]string{
		`![cat](https://x.dev/a.png "A cat")`:   "https://x.dev/a.png cat",
		"![x](https://x.dev/a_(1).png)":         "https://x.dev/a_(1).png x",
		"![*a* b](<https://x.dev/a(.png>)":      "https://x.dev/a(.png a b",
		`![a\]b](https://x.dev/a.png 'single')`: "https://x.dev/a.png a]b",
	} {
		got := ParseLines(text)
		if len(got) != 1 || got[0].Type != "image" || got[0].Str("url")+" "+got[0].Str("alt") != want {
			t.Errorf("%q read as %v", text, shape(got))
		}
	}
	// A link an image block refuses, without a host's name, is text.
	for _, text := range []string{"![a](https://:80/a.png)", "![a](http:///a.png)"} {
		if got := ParseLines(text); len(got) != 1 || got[0].Type != "paragraph" {
			t.Errorf("%q read as %v", text, shape(got))
		}
	}
}

// A line break in a table's cell is a <br>, and reads back as one; a <br>
// of the text, or in code, stays text.
func TestTableCellLineBreaks(t *testing.T) {
	table, _ := TableNode([][]string{{"h"}, {"a\nb"}}, "")
	table.Children[1].Children[0].SetDelta(Delta{{Text: "x\ny <br> "}, {Text: "a<br>b", Attrs: Attrs{Code: true}}})
	md := PageMarkdown("", FromBlocks([]*Node{table}), MarkdownOptions{})
	if !strings.Contains(md, "| x<br>y \\<br> `a<br>b` |") {
		t.Errorf("markdown %q", md)
	}
	got := MarkdownBlocks(md)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Children[1].Children[0].Delta(), table.Children[1].Children[0].Delta()) {
		t.Errorf("read back as %v", shape(got))
	}
	if rows := TableRows(MarkdownBlocks("| a<BR/>b | c<br />d |")[0]); !reflect.DeepEqual(rows, [][]string{{"a\nb", "c\nd"}}) {
		t.Errorf("rows %q", rows)
	}
}
