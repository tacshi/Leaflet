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

func TestParseLinesInvalidUTF8(t *testing.T) {
	got := ParseLines("<details>\n<summary>caf\xe9 \xe9</summary>after\n</details>")
	if len(got) != 1 || got[0].Type != "toggle_list" || got[0].Delta().Plain() != "caf\uFFFD \uFFFD" {
		t.Fatalf("read as %q", shape(got))
	}
	if kids := got[0].Children; len(kids) != 1 || kids[0].Delta().Plain() != "after" {
		t.Errorf("children %q", shape(kids))
	}
}

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
	for _, text := range []string{"![a](https://:80/a.png)", "![a](http:///a.png)"} {
		if got := ParseLines(text); len(got) != 1 || got[0].Type != "paragraph" {
			t.Errorf("%q read as %v", text, shape(got))
		}
	}
}

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

func TestNestingStopsAtADepth(t *testing.T) {
	depth := func(nodes []*Node) int {
		var of func([]*Node) int
		of = func(nodes []*Node) int {
			most := 0
			for _, n := range nodes {
				most = max(most, of(n.Children))
			}
			return most + 1
		}
		return of(nodes) - 1
	}
	const n = maxDepth + 20
	var items, toggles, mixed strings.Builder
	var want, twice []string
	for i := range n {
		fmt.Fprintf(&items, "%s- item %d\n", strings.Repeat("\t", i), i)
		fmt.Fprintf(&toggles, "<details><summary>item %d</summary>\n", i)
		fmt.Fprintf(&mixed, "%s- item %d\n%[1]s  <details>\n%[1]s  <summary>item %[2]d</summary>\n", strings.Repeat("    ", i), i)
		want = append(want, fmt.Sprintf("item %d", i))
		twice = append(twice, want[i], want[i])
	}
	toggles.WriteString("inside\n" + strings.Repeat("</details>\n", n) + "\nafter")
	oneLine := "x\n" + strings.Repeat("<details>", n) + "inside" + strings.Repeat("\n</details>", n) + "\nafter"
	for name, tc := range map[string]struct {
		md   string
		want []string
	}{
		"items":    {items.String(), want},
		"toggles":  {toggles.String(), append(slices.Clone(want), "inside", "after")},
		"one line": {oneLine, append(append([]string{"x"}, make([]string, n)...), "inside", "after")},
		"mixed":    {mixed.String(), twice},
	} {
		c := FromBlocks(ParseLines(tc.md))
		if got := depth(c.Children); got != maxDepth {
			t.Errorf("%s: read %d deep, want %d", name, got, maxDepth)
		}
		if got := texts(c); !slices.Equal(got, tc.want) {
			t.Errorf("%s: read as %q, want %q", name, got, tc.want)
		}
	}
	after := ParseLines(toggles.String())
	if last := after[len(after)-1]; len(after) != 2 || last.Delta().Plain() != "after" {
		t.Errorf("the toggles close on %d blocks", len(after))
	}
	c := FromBlocks(ParseLines("x\n" + strings.Repeat("<details>", 20000)))
	if _, err := Decode(c.Encode()); err != nil {
		t.Error(err)
	}
}

func TestTableCellLinksReadInLinearTime(t *testing.T) {
	if testing.Short() {
		t.Skip("times reads")
	}
	read := func(n int) func() {
		md := "| " + strings.Repeat("[", n) + "<br>" + strings.Repeat("](u)", n) + " |\n| a |"
		return func() { ParseLines(md) }
	}
	small, large := bestInTurns(read(1000), read(16000))
	if large > 64*small {
		t.Errorf("1000 links read in %v, 16000 in %v", small, large)
	}
	rows := TableRows(ParseLines("| [a<br>[b<br>c](v)](u<br>) | `<br>` |\n| x | y |")[0])
	if want := "[a\n[b\nc](v)](u<br>)"; cellBreaks("[a<br>[b<br>c](v)](u<br>)") != want || len(rows) != 2 || rows[0][1] != "`<br>`" {
		t.Errorf("breaks in links read as %q, %q", cellBreaks("[a<br>[b<br>c](v)](u<br>)"), rows)
	}
}

func TestTogglesOnALineReadInLinearTime(t *testing.T) {
	if testing.Short() {
		t.Skip("times reads")
	}
	for _, unit := range []string{"<details>", "<details><summary>a</summary>", "<details> <summary>`a` ``</summary>  ", "<details>`a` "} {
		read := func(n int) func() {
			md := strings.Repeat(unit, n)
			return func() { ParseLines(md) }
		}
		small, large := bestInTurns(read(500), read(8000))
		if large > 64*small {
			t.Errorf("%q: 500 read in %v, 8000 in %v", unit, small, large)
		}
	}
	got := ParseLines("<details> <summary>a `</summary>` b</summary>  <details open><summary>c ``</summary>d `e` \n</details>\nf")
	want := []string{"toggle_list collapsed=true \"a `</summary>` b\"", "  toggle_list collapsed=false \"c \\\\`\\\\`\"", "    paragraph \"d `e`\"", "  paragraph \"f\""}
	if !slices.Equal(shape(got), want) {
		t.Errorf("toggles on a line read as %q, want %q", shape(got), want)
	}
}
