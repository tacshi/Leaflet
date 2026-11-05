package doc

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// markdownSeeds are Markdown as people paste it and PageMarkdown writes
// it, for the fuzz tests to start from.
var markdownSeeds = []string{
	"# Title\n\nSome **bold** and *italic* text with `code` and [a link](https://x.dev).",
	"- one\n  - nested\n- two\n\n1. first\n2. second\n\n- [ ] todo\n- [x] done",
	"> a quote\\\n> on lines\n\n> [!TIP]\n> a callout\n> - not a list",
	"<details open>\n<summary>A toggle `</summary>`</summary>\n\n- inside\n\n</details>",
	"| a | b |\n| --- | --- |\n| - | `x\\|y` |\n| -- | a\\\\|b |\n| <br> | x<br>y |",
	"```go\nfunc main() {}\n```\n\n$$\nx^2\n$$\n\n```mermaid\ngraph TD\n```",
	"---\n\n![A cat](https://x.dev/cat.png)\n\n\\![not](https://x.dev/a.png)",
	"a `x\\\nb` c\\\n<details>\\\n  - not an item",
	"- item\n\n    indented after\n\n1000. late\n2024. year",
	"* star\n+ plus\n10) ten\n\n#tag\n####### seven\n\\# escaped",
	"[Page](leaflet://page/p1) and **[Bold](leaflet://page/p2)** and [@Ann](mention:user:u1)",
	// What the fuzz tests found: a line of other spaces, a lone \r, and a
	// line break at a block's end.
	"*\v    \v*", "00\r ", "`\\\n`",
	// and then: a paragraph only of spaces among list items, a cell
	// ending in spaces, emphasis around code that is only a line break,
	// and rules of the kinds CommonMark reads and the server reads alike.
	"*\n*\v*\n*", "|<***~~ ~~", "*`\\\n`*", "- --\n\n \u00a0---\n\v---\n-\t-\t-\n\u0085---",
	"\u00a0```go\nx := 1\n```", "\u3000```\ncode\n```",
	// and a third time: a page mention in italics with a space first, a
	// summary's code over a line break, and bold in an italic that keeps a
	// space between.
	"*[ 0](leaflet://page/0)*", "<details>\n<summary>a `x\n 000`</summary>\n</details>", "**0**_**0** **0**_",
}

// After one reading, a page's Markdown reads back as the blocks it was
// written from: PageMarkdown writes all that MarkdownBlocks reads.
func FuzzMarkdownBlocksRoundTrip(f *testing.F) {
	for _, s := range markdownSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		if !utf8.ValidString(md) || len(md) > 4000 {
			t.Skip()
		}
		once := MarkdownBlocks(md)
		written := PageMarkdown("", FromBlocks(once), MarkdownOptions{})
		twice := MarkdownBlocks(written)
		again := PageMarkdown("", FromBlocks(twice), MarkdownOptions{})
		if got, want := shape(MarkdownBlocks(again)), shape(twice); !slices.Equal(got, want) {
			t.Errorf("%q wrote\n%s\nread back as\n%q\nwant\n%q", md, again, got, want)
		}
		// A paragraph only of spaces is a blank line the first time, and
		// no line the next.
		if blankRuns(again) != blankRuns(written) {
			t.Errorf("%q wrote\n%s\nthen\n%s", md, written, again)
		}
	})
}

// blankRuns is Markdown with each run of blank lines one blank line.
func blankRuns(md string) string {
	lines := strings.Split(strings.Trim(md, "\n"), "\n")
	return strings.Join(slices.CompactFunc(lines, func(a, b string) bool { return a == "" && b == "" }), "\n")
}

// A text block reads back from a page's Markdown as it was: of its type,
// where it was, with its text, but for the spaces Markdown can't keep at
// the start of a line or the end of a block.
func FuzzPageMarkdownRoundTrip(f *testing.F) {
	for _, s := range markdownSeeds {
		f.Add(s, uint8(0), 7)
	}
	f.Add("  lead\n- x\n<details>\n</summary>", uint8(1), 3)
	f.Add("1000. x\n|a|b|\n$$\n---\n> q", uint8(5), 0)
	f.Add("a\n\nb\\\n`c`", uint8(9), 2)
	// What fuzzing found: a heading's code over a line break, and an empty
	// paragraph in a list item.
	f.Add("00 0\n", uint8(5), 2)
	f.Add("", uint8(9), 7)
	// A list item's "--", which "- " before would make a rule, and lines
	// a rule only for spaces other than spaces and tabs.
	f.Add("--", uint8(1), -1)
	f.Add("- -", uint8(3), -1)
	f.Add("\v---", uint8(0), -1)
	f.Add("\u0085---", uint8(0), -1)
	f.Add("-\t-\t-", uint8(0), -1)
	// A line after a break that starts with another kind of space, before
	// what would end a toggle or start math.
	f.Add("\n\v</detAils>", uint8(0), 12)
	f.Add("a\n　$$", uint8(1), -1)
	f.Fuzz(func(t *testing.T, text string, kind uint8, code int) {
		text = strings.ReplaceAll(text, "\r", "")
		if !utf8.ValidString(text) || len(text) > 2000 || strings.ContainsRune(text, 0) {
			t.Skip()
		}
		types := []string{"paragraph", "bulleted_list", "numbered_list", "todo_list", "quote", "heading", "toggle_list", "callout", "code", "nested"}
		typ := types[int(kind)%len(types)]
		// Part of the text, from a rune on, is code.
		runes := []rune(text)
		cut := len(runes)
		if code >= 0 && code < len(runes) && typ != "code" {
			cut = code
		}
		d := Delta{{Text: string(runes[:cut])}, {Text: string(runes[cut:]), Attrs: Attrs{Code: true}}}.Compact()
		var n *Node
		switch typ {
		case "nested":
			n = block("paragraph", d)
			n = block("bulleted_list", Delta{{Text: "item"}}, block("bulleted_list", Delta{{Text: "child"}}), n)
		case "code":
			n = CodeNode(text, "go")
		default:
			n = block(typ, d)
		}
		if typ == "heading" {
			n.Set("level", 2)
		}
		got := readBack(block("paragraph", Delta{{Text: "before"}}), n, block("paragraph", Delta{{Text: "after"}}))
		want := n
		if typ == "nested" {
			want = n.Children[1]
		}
		wantText := keptText(want, d)
		if wantText == "" && want.Type == "paragraph" {
			// An empty paragraph is no block in Markdown.
			if typ == "nested" && (len(got) != 3 || len(got[1].Children) != 1) || typ != "nested" && len(got) != 2 {
				t.Errorf("%s %q read back as %q", typ, text, shape(got))
			}
			return
		}
		if typ == "nested" && len(got) == 3 && len(got[1].Children) == 2 {
			got = []*Node{got[0], got[1].Children[1], got[2]}
		}
		if len(got) != 3 || got[0].Delta().Plain() != "before" || got[2].Delta().Plain() != "after" ||
			got[1].Type != want.Type || got[1].Delta().Plain() != wantText {
			t.Errorf("%s %q (code from %d) wrote\n%s\nread back as %q, want %q", typ, text, cut,
				PageMarkdown("", FromBlocks([]*Node{n}), MarkdownOptions{}), shape(got), wantText)
		}
	})
}

// keptText is what of a text block's text its Markdown keeps: a heading
// is one line, and spaces outside code at the start of a line, or at the
// end of the block, go, but a callout's at the start, which follow its
// lines' >, and a toggle's at the start of its summary; a callout's go at
// the end of each line. Line breaks at the end go too, but a callout's
// and a toggle's.
func keptText(n *Node, d Delta) string {
	switch n.Type {
	case "code":
		return n.Delta().Plain()
	case "heading":
		return strings.ReplaceAll(d.Plain(), "\n", " ")
	}
	type char struct {
		r    rune
		code bool
	}
	var line []char
	var lines [][]char
	for _, r := range d {
		for _, c := range r.Text {
			if c == '\n' {
				lines, line = append(lines, line), nil
				continue
			}
			line = append(line, char{c, r.Attrs.Code})
		}
	}
	lines = append(lines, line)
	blank := func(l []char) bool {
		return !slices.ContainsFunc(l, func(c char) bool { return c.code || !unicode.IsSpace(c.r) })
	}
	for n.Type != "callout" && n.Type != "toggle_list" && len(lines) > 1 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	var out []string
	for i, l := range lines {
		start, end := 0, len(l)
		if n.Type != "callout" && n.Type != "toggle_list" && blank(l) {
			start = end
		}
		for n.Type != "callout" && (n.Type != "toggle_list" || i > 0) {
			if start == end || l[start].code || l[start].r != ' ' && l[start].r != '\t' {
				break
			}
			start++
		}
		if n.Type == "callout" || i == len(lines)-1 && n.Type != "toggle_list" {
			for end > start && !l[end-1].code && l[end-1].r == ' ' {
				end--
			}
		}
		var b strings.Builder
		for _, c := range l[start:end] {
			b.WriteRune(c.r)
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}

// A table's cell reads back from a page's Markdown as it was, but for the
// spaces at its ends, which a cell of a Markdown table can't keep.
func FuzzTableCellRoundTrip(f *testing.F) {
	for _, s := range []string{"a|b", "`x|y`", "`a\\|b`", `x\|y`, "two\nlines", "`\\`", "<br>", "\\<br> stays", "-", "---", ":-:",
		"`code` and | pipe", "a\\", "**bold** | *it*", "[link](https://x.dev/a|b)", "`` ` ``|``",
		// What fuzzing found: code over a line break, which a cell's code
		// has as a space, and a backtick in a link's address.
		"`a\nb`", "` x\n`", "[0](`)\n`"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		if !utf8.ValidString(md) || len(md) > 1000 || strings.ContainsAny(md, "\r\x00") {
			t.Skip()
		}
		table, err := TableNode([][]string{{"head", md}, {md, "x"}}, "")
		if err != nil {
			t.Skip()
		}
		got := readBack(table)
		if len(got) != 1 || got[0].Type != "table" {
			t.Fatalf("%q read back as %q", md, shape(got))
		}
		// What a cell's Markdown reads as, and ToMarkdown writes it again,
		// its ends' spaces gone, and a line break in its code a space, as
		// a code span in a table's cell has it.
		cells := func(rows [][]string) [][]string {
			for _, row := range rows {
				for i, cell := range row {
					d := FromMarkdown(strings.TrimSpace(cell))
					for k := range d {
						if d[k].Attrs.Code {
							d[k].Text = strings.ReplaceAll(d[k].Text, "\n", " ")
						}
					}
					row[i] = FromMarkdown(strings.TrimSpace(d.ToMarkdown())).ToMarkdown()
				}
			}
			return rows
		}
		want, rows := cells(ReadableTableRows(table)), cells(TableRows(got[0]))
		if !slices.EqualFunc(rows, want, slices.Equal) {
			t.Errorf("%q wrote\n%s\nread back as %q, want %q", md, PageMarkdown("", FromBlocks([]*Node{table}), MarkdownOptions{}), rows, want)
		}
	})
}

// A merge of a change one side made to a page the other left as it was
// is that change, with no conflict, whichever side made it.
func FuzzMergeOneSidedChange(f *testing.F) {
	for i, s := range markdownSeeds {
		f.Add(s, uint8(i), uint8(i*7))
	}
	f.Fuzz(func(t *testing.T, md string, op, at uint8) {
		if !utf8.ValidString(md) || len(md) > 2000 {
			t.Skip()
		}
		base := FromBlocks(MarkdownBlocks(md))
		var ids []string
		base.Walk(func(n, parent *Node) {
			if parent == nil || parent.Type != "table" {
				ids = append(ids, n.ID)
			}
		})
		if len(ids) == 0 {
			t.Skip()
		}
		changed := base.Copy()
		id := ids[int(at)%len(ids)]
		switch op % 3 {
		case 0:
			changed.Remove(id)
		case 1:
			n := changed.Find(id)
			if !n.IsText() {
				t.Skip()
			}
			n.SetDelta(Delta{{Text: "changed"}})
			changed.Blocks[id]++
		case 2:
			last := changed.Children[len(changed.Children)-1].ID
			if last == id || changed.Move(id, Position{After: last}) != nil {
				t.Skip()
			}
		}
		for _, sides := range [][2]*Content{{changed, base.Copy()}, {base.Copy(), changed}} {
			got, conflicts := Merge(base, sides[0], sides[1])
			if g, w := tree(got), tree(changed); !slices.Equal(g, w) || len(conflicts) != 0 {
				t.Errorf("%q, op %d on %s: merged\n%q\nwant\n%q\nconflicts %v", md, op%3, id, g, w, conflicts)
			}
		}
	})
}

// tree is a page's blocks as lines of their IDs, types and text, indented
// under what holds them.
func tree(c *Content) []string {
	var out []string
	var visit func([]*Node, string)
	visit = func(nodes []*Node, indent string) {
		for _, n := range nodes {
			out = append(out, fmt.Sprintf("%s%s %s %q", indent, n.ID, n.Type, n.Delta().Plain()))
			visit(n.Children, indent+"  ")
		}
	}
	visit(c.Children, "")
	return out
}

// fuzzBytes reads a fuzz input a byte at a time, 0 once it runs out.
type fuzzBytes []byte

func (b *fuzzBytes) next() int {
	if len(*b) == 0 {
		return 0
	}
	v := (*b)[0]
	*b = (*b)[1:]
	return int(v)
}

// twoSidedBase is a page for two sides to change: lists nested in lists,
// paragraphs, a toggle and a table, its blocks named by their places so
// that a merge's ties come out alike each run.
func twoSidedBase() *Content {
	md := "# Plan\n\n- one\n  - one.a\n  - one.b\n- two\n\npara x\n\npara y\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n\n" +
		"<details>\n<summary>more</summary>\n\nhidden\n\n</details>\n\nlast"
	c := FromBlocks(MarkdownBlocks(md))
	i := 0
	c.Walk(func(n, _ *Node) {
		n.ID = fmt.Sprintf("b%d", i)
		i++
	})
	c.Blocks = map[string]int64{}
	c.Walk(func(n, _ *Node) { c.Blocks[n.ID] = 1000 })
	return c
}

// editSide makes up to four edits to c as one side would, as b says:
// deleting, editing, moving and adding blocks, and growing the table and
// editing its cells. It returns the blocks whose text it edited.
func editSide(c *Content, b *fuzzBytes, side string) map[string]string {
	edited := map[string]string{}
	pick := func(ok func(n, parent *Node) bool) *Node {
		var all []*Node
		var visit func(nodes []*Node, parent *Node)
		visit = func(nodes []*Node, parent *Node) {
			for _, n := range nodes {
				if ok(n, parent) {
					all = append(all, n)
				}
				if n.Type != "table" {
					visit(n.Children, n)
				}
			}
		}
		visit(c.Children, nil)
		if len(all) == 0 {
			return nil
		}
		return all[b.next()%len(all)]
	}
	any := func(n, _ *Node) bool { return true }
	var table *Node
	for _, n := range c.Children {
		if n.Type == "table" {
			table = n
		}
	}
	for k := range b.next()%4 + 1 {
		switch b.next() % 8 {
		case 0:
			if n := pick(any); n != nil {
				c.Remove(n.ID)
			}
		case 1:
			if n := pick(func(n, _ *Node) bool { return n.IsText() }); n != nil {
				text := fmt.Sprintf("%s edit %d", side, k)
				n.SetDelta(Delta{{Text: text}})
				edited[n.ID] = text
			}
		case 2:
			if n, to := pick(any), pick(any); n != nil && to != nil {
				_ = c.Move(n.ID, Position{After: to.ID})
			}
		case 3:
			if n, to := pick(any), pick(func(n, _ *Node) bool { return NestableTypes[n.Type] }); n != nil && to != nil {
				_ = c.Move(n.ID, Position{Parent: to.ID})
			}
		case 4:
			if n := pick(any); n != nil {
				_ = c.Insert(Position{After: n.ID}, para(fmt.Sprintf("%s new %d", side, k), "new"))
			}
		case 5, 6:
			if table == nil || c.Find(table.ID) == nil {
				continue
			}
			rows, cols := TableSize(table)
			if b.next()%2 == 0 {
				for col := range cols {
					table.Children = append(table.Children, TableCellNode(rows, col, Delta{{Text: side}}))
				}
				table.Set("rowsLen", rows+1)
			} else {
				for r := range rows {
					table.Children = append(table.Children, TableCellNode(r, cols, Delta{{Text: side}}))
				}
				table.Set("colsLen", cols+1)
			}
			SortTableCells(table)
		case 7:
			if table == nil || c.Find(table.ID) == nil || len(table.Children) == 0 {
				continue
			}
			cell := table.Children[b.next()%len(table.Children)]
			if len(cell.Children) > 0 {
				text := fmt.Sprintf("%s cell %d", side, k)
				cell.Children[0].SetDelta(Delta{{Text: text}})
				edited[cell.Children[0].ID] = text
			}
		}
	}
	return edited
}

// Changes made on both sides at once merge to the same page whichever
// side is local, with no block twice and no table's cells at one place,
// keeping every block both sides kept and every edit only one side made.
func FuzzMergeTwoSides(f *testing.F) {
	for _, s := range []string{"", "\x01\x02\x03\x04\x05", "\x03\x02\x00\x01\x03\x05\x00\x02\x06\x01", "\x00\x05\x00\x00\x00\x05\x00\x00",
		"\x02\x03\x01\x02\x03\x02\x02\x00", "\x03\x07\x04\x07\x09\x03\x07\x04\x07\x02", "\x01\x01\x03\x01\x01\x02\x05\x01"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		b := fuzzBytes(data)
		base := twoSidedBase()
		local, remote := base.Copy(), base.Copy()
		localEdits := editSide(local, &b, "local")
		remoteEdits := editSide(remote, &b, "remote")
		local = local.Stamped(base, 2000)
		remote = remote.Stamped(base, int64(2000+b.next()%2*10))
		got, _ := Merge(base, local, remote)
		swapped, _ := Merge(base, remote, local)
		if g, s := tree(got), tree(swapped); !slices.Equal(g, s) {
			t.Fatalf("merged\n%s\nas the sides swap\n%s", strings.Join(g, "\n"), strings.Join(s, "\n"))
		}
		ids := map[string]bool{}
		got.Walk(func(n, _ *Node) {
			if ids[n.ID] {
				t.Errorf("%s is in the page twice", n.ID)
			}
			ids[n.ID] = true
			if n.Type != "table" {
				return
			}
			at := map[[2]int]bool{}
			for _, cell := range n.Children {
				pos := [2]int{cell.Int("rowPosition", -1), cell.Int("colPosition", -1)}
				if at[pos] {
					t.Errorf("table %s has two cells at %v", n.ID, pos)
				}
				at[pos] = true
			}
		})
		remote.Walk(func(n, _ *Node) {
			if local.Find(n.ID) != nil && !ids[n.ID] {
				t.Errorf("%s, which both sides kept, is gone", n.ID)
			}
		})
		for edits, other := range map[*map[string]string]map[string]string{&localEdits: remoteEdits, &remoteEdits: localEdits} {
			for id, text := range *edits {
				if _, both := other[id]; both || local.Find(id) == nil || remote.Find(id) == nil {
					continue
				}
				if n := got.Find(id); n == nil || n.Delta().Plain() != text {
					t.Errorf("%s's edit %q is lost: %q", id, text, n.Delta().Plain())
				}
			}
		}
	})
}
