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
	md := PageMarkdown("", FromBlocks(blocks), nil)
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
		"| a | b |":       {"a", "b"},
		`| a\|b | c |`:    {"a|b", "c"},
		`| a\\| b |`:      {`a\\`, "b"},
		`| \*x\* | y`:     {`\*x\*`, "y"},
		"| --- | :---: |": nil,
	} {
		if got := tableRow(line); !reflect.DeepEqual(got, want) {
			t.Errorf("%q split as %q, want %q", line, got, want)
		}
	}
}
