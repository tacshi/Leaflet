package doc

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// MarkdownBlocks reads Markdown as blocks, as pasting it does.
func MarkdownBlocks(text string) []*Node {
	return ParseLines(strings.Trim(lineEnds.Replace(text), "\n"))
}

// lineEnds writes the ends of lines Markdown has, \r\n and \r, as \n.
var lineEnds = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// ParseLines reads pasted text as blocks: a line each, with Markdown
// block prefixes, as PageMarkdown writes them. A line ending in a hard
// break, a backslash, goes on in the block on the next line, a
// line of a quote or an alert after another is more of it, and a line
// right after a list item's, indented under it, more of the item. A list
// item holds the blocks indented under it, a <details> those up to its
// </details>, the lines of a Markdown table make a table, and a fenced
// block a code block.
func ParseLines(text string) []*Node {
	text = lineEnds.Replace(text)
	var p lineParser
	for _, line := range strings.Split(text, "\n") {
		p.line(line)
	}
	p.flushTable()
	if p.code != nil {
		// A fence left open runs to the end.
		p.closeCode()
	}
	for _, t := range p.texts {
		t.n.SetDelta(FromMarkdown(strings.Join(t.md, "\n")))
	}
	return p.out
}

// lineParser is ParseLines reading text a line at a time.
type lineParser struct {
	out []*Node
	// open are the blocks that hold what follows, outermost first: list
	// items, which hold the lines indented under them, and toggles, which
	// hold all up to their </details>.
	open []openBlock
	// texts are the text blocks read, their Markdown set at the end, and
	// text the one the next line may go on in.
	texts []*textBlock
	text  *textBlock
	// summary is the toggle whose <summary> is being read, over lines, and
	// awaiting one just opened, whose summary may follow.
	summary, awaiting *textBlock

	// table are the rows of the table being read, into tableIn, and ruled
	// is set once the line under its header is.
	table   [][]string
	tableIn *[]*Node
	ruled   bool

	// numbers are the numbered items read, by their numbers.
	numbers map[*Node]int

	// code is the fenced block open, and fence the backticks it opened
	// with, or "$$" for math; its indent comes off each of its lines. A
	// Mermaid fence makes a diagram, and lines between "$$" math: the
	// block in code's place, last in codeIn, once it closes.
	code              *Node
	codeLines         []string
	fence, codeIndent string
	codeIn            *[]*Node
}

// An openBlock is a list item or a toggle that holds the blocks after it.
type openBlock struct {
	n *Node
	// toggle is true for a toggle, which holds all up to its </details>.
	toggle bool
	// min is how far a line is indented to be in it: two columns past a
	// list item's marker, as CommonMark's "- " takes, and as people indent
	// under "1. " too; for a toggle, as far as its <details>.
	min int
}

// A textBlock is a text block being read, a line of Markdown at a time.
type textBlock struct {
	n  *Node
	md []string
	// quoted is true for a quote or an alert, which lines starting with >
	// go on; item for a list item, which lines indented under it right
	// after its own go on.
	quoted, item bool
	// min is how far a line is indented to go on in it.
	min int
	// hard is true when its last line ends in a hard break, so the next
	// goes on in it.
	hard bool
}

func (p *lineParser) line(line string) {
	trimmed := strings.TrimLeft(line, " \t")
	if p.code != nil {
		if p.fence == "$$" && strings.TrimSpace(trimmed) == "$$" || p.fence != "$$" && strings.HasPrefix(trimmed, p.fence) && strings.Trim(trimmed, "` ") == "" {
			p.closeCode()
			return
		}
		p.codeLines = append(p.codeLines, strings.TrimPrefix(line, p.codeIndent))
		return
	}
	if p.summary != nil {
		p.summaryLine(trimmed)
		return
	}
	if strings.TrimSpace(line) == "" {
		// A blank line only separates blocks, as in Markdown; it makes no
		// empty paragraph. A list goes on across it.
		p.flushTable()
		p.text = nil
		return
	}
	lead := line[:len(line)-len(trimmed)]
	indent := strings.Count(lead, " ") + 2*strings.Count(lead, "\t")
	if p.awaiting != nil {
		t := p.awaiting
		p.awaiting = nil
		if rest, ok := cutFold(trimmed, "<summary>"); ok {
			p.summary = t
			p.summaryLine(rest)
			return
		}
	}
	if p.continues(trimmed, indent) {
		return
	}
	p.text = nil
	if tone, ok := AlertTone(trimmed); ok {
		p.flushTable()
		n := textNode("callout", nil)
		n.Set("tone", tone)
		p.add(n, indent)
		p.text = p.newText(n, true, false, p.min())
		return
	}
	if language, ok := CodeFence(trimmed); ok || strings.TrimSpace(trimmed) == "$$" {
		p.flushTable()
		p.code, p.codeLines = CodeNode("", language), nil
		p.codeIndent = lead
		p.fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "`"))]
		if !ok {
			p.fence = "$$"
		}
		p.add(p.code, indent)
		p.codeIn = p.kids()
		return
	}
	if strings.HasPrefix(trimmed, "|") {
		if p.tableIn == nil {
			p.tableIn = p.container(indent)
		}
		// The line of dashes under the header is no row; one further down
		// is, of cells that are dashes.
		if row, rule := tableRow(trimmed); rule && len(p.table) == 1 && !p.ruled {
			p.ruled = true
		} else {
			p.table = append(p.table, row)
		}
		return
	}
	p.flushTable()
	if open, rest, ok := detailsOpen(trimmed); ok {
		n := textNode("toggle_list", nil)
		n.Set("collapsed", !open)
		p.add(n, indent)
		p.open = append(p.open, openBlock{n: n, toggle: true, min: indent})
		t := p.newText(n, false, false, 0)
		if summary, ok := cutFold(rest, "<summary>"); ok {
			p.summary = t
			p.summaryLine(summary)
		} else if p.awaiting = t; rest != "" {
			p.line(rest)
		}
		return
	}
	if strings.EqualFold(strings.TrimSpace(trimmed), "</details>") {
		if i := p.lastToggle(); i >= 0 {
			p.open = p.open[:i]
			return
		}
	}
	if IsRule(trimmed) {
		p.add(&Node{ID: NewBlockID(), Type: "divider"}, indent)
		return
	}
	if typ, rest, checked, ok := listItem(trimmed); ok && (typ != "numbered_list" || p.counts(trimmed, indent)) {
		n := textNode(typ, nil)
		if typ == "todo_list" {
			n.Set("checked", checked)
		}
		if typ == "numbered_list" {
			if p.numbers == nil {
				p.numbers = map[*Node]int{}
			}
			p.numbers[n] = itemNumber(trimmed)
		}
		p.add(n, indent)
		p.open = append(p.open, openBlock{n: n, min: indent + 2})
		p.text = p.newText(n, false, true, indent+2)
		p.text.addLine(rest)
		return
	}
	if level, rest, ok := headingLine(trimmed); ok {
		n := textNode("heading", nil)
		n.Set("level", level)
		p.add(n, indent)
		// A heading is one line.
		p.newText(n, false, false, 0).md = []string{rest}
		return
	}
	if rest, ok := quoteLine(trimmed); ok {
		n := textNode("quote", nil)
		p.add(n, indent)
		p.text = p.newText(n, true, false, p.min())
		p.text.addLine(rest)
		return
	}
	if img := imageLine(trimmed); img != nil {
		p.add(img, indent)
		return
	}
	n := textNode("paragraph", nil)
	p.add(n, indent)
	p.text = p.newText(n, false, false, 0)
	p.text.addLine(trimmed)
}

// continues puts a line in the text block before it when it goes on
// there, and reports whether it did.
func (p *lineParser) continues(trimmed string, indent int) bool {
	t := p.text
	if t == nil {
		return false
	}
	switch rest, quoted := strings.CutPrefix(trimmed, ">"); {
	case t.quoted && quoted && indent >= t.min:
		if _, alert := AlertTone(trimmed); alert {
			return false
		}
		t.addLine(strings.TrimPrefix(rest, " "))
	case startsBlock(trimmed):
		return false
	case t.hard, t.item && indent >= t.min:
		t.addLine(trimmed)
	default:
		return false
	}
	return true
}

// counts reports whether a numbered item's line is one. A number of four
// digits or more, as a year that starts a line, is one only right after
// the item before it, as PageMarkdown numbers a long list.
func (p *lineParser) counts(line string, indent int) bool {
	if !longNumber(line) {
		return true
	}
	kids := *p.container(indent)
	return len(kids) > 0 && p.numbers[kids[len(kids)-1]] == itemNumber(line)-1
}

// add puts a block in what holds the blocks at an indent, and a list
// item or a toggle in what is open.
func (p *lineParser) add(n *Node, indent int) {
	kids := p.container(indent)
	*kids = append(*kids, n)
}

// container closes the list items a line at indent is not under, and
// returns the blocks the line's block goes in.
func (p *lineParser) container(indent int) *[]*Node {
	for len(p.open) > 0 {
		top := p.open[len(p.open)-1]
		if top.toggle || indent >= top.min {
			break
		}
		p.open = p.open[:len(p.open)-1]
	}
	return p.kids()
}

// kids are the blocks the innermost open block holds, or the page's.
func (p *lineParser) kids() *[]*Node {
	if len(p.open) == 0 {
		return &p.out
	}
	return &p.open[len(p.open)-1].n.Children
}

// min is how far a line is indented to be in the innermost open block.
func (p *lineParser) min() int {
	if len(p.open) == 0 {
		return 0
	}
	return p.open[len(p.open)-1].min
}

// lastToggle is where in open the innermost toggle is, or -1.
func (p *lineParser) lastToggle() int {
	for i := len(p.open) - 1; i >= 0; i-- {
		if p.open[i].toggle {
			return i
		}
	}
	return -1
}

func (p *lineParser) newText(n *Node, quoted, item bool, min int) *textBlock {
	t := &textBlock{n: n, quoted: quoted, item: item, min: min}
	p.texts = append(p.texts, t)
	return t
}

// summaryLine reads a line of a toggle's <summary>, up to its </summary>;
// what follows that on the line is a line of its own.
func (p *lineParser) summaryLine(s string) {
	t := p.summary
	i := summaryEnd(s)
	if i < 0 {
		t.md = append(t.md, s)
		return
	}
	t.md = append(t.md, s[:i])
	p.summary = nil
	if rest := s[i+len("</summary>"):]; strings.TrimSpace(rest) != "" {
		p.line(rest)
	}
}

func (p *lineParser) flushTable() {
	if len(p.table) > 0 {
		if t, err := TableNode(p.table, ""); err == nil {
			*p.tableIn = append(*p.tableIn, t)
		}
	}
	p.table, p.tableIn, p.ruled = nil, nil, false
}

func (p *lineParser) closeCode() {
	text := strings.Join(p.codeLines, "\n")
	kids := *p.codeIn
	last := len(kids) - 1
	switch {
	case (p.fence == "$$" || p.code.Str("language") == "mermaid") && strings.TrimSpace(text) == "":
		// An empty formula or diagram has nothing to draw: it makes no
		// block, as a spec of one makes none.
		*p.codeIn = kids[:last]
	case p.fence == "$$":
		kids[last] = MathNode(text)
	case p.code.Str("language") == "mermaid":
		kids[last] = DiagramNode(text)
	default:
		p.code.SetDelta(Delta{{Text: text}}.Compact())
	}
	p.code, p.codeIn = nil, nil
}

// addLine puts a line of Markdown in the block: a hard break that ended
// the line before is the line break between them.
func (t *textBlock) addLine(s string) {
	if t.hard {
		last := len(t.md) - 1
		t.md[last] = strings.TrimSuffix(t.md[last], `\`)
	}
	t.md = append(t.md, s)
	t.hard = hardBreak(s)
}

// hardBreak reports whether a line of Markdown ends in a hard line break,
// a backslash not itself escaped. Two spaces, Markdown's other hard
// break, are too often only what pasted text has left at a line's end.
func hardBreak(s string) bool {
	slashes := len(s) - len(strings.TrimRight(s, `\`))
	return slashes%2 == 1
}

// startsBlock reports whether a line starts a block of its own, so it
// cannot go on in the block before it.
func startsBlock(s string) bool {
	typ, _, _, item := listItem(s)
	item = item && (typ != "numbered_list" || !longNumber(s))
	_, _, heading := headingLine(s)
	_, _, toggle := detailsOpen(s)
	_, fence := CodeFence(s)
	return item || heading || toggle || fence || strings.HasPrefix(s, ">") || strings.HasPrefix(s, "|") ||
		strings.TrimSpace(s) == "$$" || IsRule(s) || strings.EqualFold(strings.TrimSpace(s), "</details>") ||
		imageLine(s) != nil
}

// listItem reads a list item's marker at the start of a line: its type,
// the text after the marker, and whether a to-do is checked. A marker
// that ends the line is an empty item, as PageMarkdown writes one.
func listItem(s string) (typ, rest string, checked, ok bool) {
	if len(s) >= 1 && strings.ContainsRune("-*+", rune(s[0])) && (len(s) == 1 || s[1] == ' ') {
		rest = strings.TrimPrefix(s[1:], " ")
		for _, box := range []string{"[ ]", "[x]", "[X]"} {
			if r, ok := strings.CutPrefix(rest, box); ok && (r == "" || r[0] == ' ') {
				return "todo_list", strings.TrimPrefix(r, " "), box != "[ ]", true
			}
		}
		return "bulleted_list", rest, false, true
	}
	// A number of up to nine digits, as CommonMark's, and EscapeBlockStart's.
	if i := strings.IndexAny(s, ".)"); i > 0 && i <= 9 && isDigits(s[:i]) && (len(s) == i+1 || s[i+1] == ' ') {
		return "numbered_list", strings.TrimPrefix(s[i+1:], " "), false, true
	}
	return "", "", false, false
}

// itemNumber is the number a numbered item's line starts with.
func itemNumber(line string) int {
	n, _ := strconv.Atoi(line[:strings.IndexAny(line, ".)")])
	return n
}

// longNumber reports whether a numbered item's line starts with a number
// of four digits or more.
func longNumber(line string) bool { return strings.IndexAny(line, ".)") > 3 }

// headingLine reads a heading's #s at the start of a line: its level and
// text.
func headingLine(s string) (level int, rest string, ok bool) {
	hashes := len(s) - len(strings.TrimLeft(s, "#"))
	if hashes < 1 || hashes > 6 || len(s) > hashes && s[hashes] != ' ' {
		return 0, "", false
	}
	return hashes, strings.TrimPrefix(s[hashes:], " "), true
}

// quoteLine reads a quote's > at the start of a line, and the text after.
func quoteLine(s string) (string, bool) {
	if s == ">" {
		return "", true
	}
	return strings.CutPrefix(s, "> ")
}

// detailsOpen reads a line that opens a toggle, "<details>" or "<details
// open>": whether it is open, and what follows on the line.
func detailsOpen(s string) (open bool, rest string, ok bool) {
	if rest, ok := cutFold(s, "<details>"); ok {
		return false, strings.TrimSpace(rest), true
	}
	if rest, ok := cutFold(s, "<details open>"); ok {
		return true, strings.TrimSpace(rest), true
	}
	return false, "", false
}

// summaryEnd is where the </summary> that ends a toggle's summary is in
// s, one not escaped nor in a code span, or -1.
func summaryEnd(s string) int {
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		switch r[i] {
		case '\\':
			i++
		case '`':
			if _, n := codeSpanAt(r[i:]); n > 0 {
				i += n - 1
			}
		case '<':
			if _, ok := cutFold(string(r[i:]), "</summary>"); ok {
				return len(string(r[:i]))
			}
		}
	}
	return -1
}

// cutFold is strings.CutPrefix, ignoring case.
func cutFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// tableRow reads a line of a markdown table, and reports whether it is
// a line of dashes, as the one under the header ("| --- | --- |"). As in
// GitHub's tables, a backslash keeps the character after it in the cell,
// and \| is a | of the cell's text, even in a code span; there, where a
// backslash is the code's, one before anything else is only itself. A
// <br> is a line break, as PageMarkdown writes one.
func tableRow(line string) (cells []string, rule bool) {
	s := []rune(strings.TrimPrefix(strings.TrimSpace(line), "|"))
	var cell strings.Builder
	// code is where the code span the line is read in ends.
	code := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '|':
			i++
			cell.WriteRune('|')
		case s[i] == '\\' && i+1 < len(s) && i >= code:
			cell.WriteString(string(s[i : i+2]))
			i++
		case s[i] == '|':
			cells = append(cells, cell.String())
			cell.Reset()
		case s[i] == '`' && i >= code:
			if _, n := codeSpanAt(s[i:]); n > 0 {
				code = i + n
			}
			cell.WriteRune(s[i])
		default:
			cell.WriteRune(s[i])
		}
	}
	// A closing | ends the last cell; without one, what is after the last
	// | is a cell.
	if rest := cell.String(); rest != "" || len(cells) == 0 {
		cells = append(cells, rest)
	}
	rule = true
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
		if strings.Trim(cells[i], "-: ") != "" || cells[i] == "" {
			rule = false
		}
		cells[i] = cellBreaks(cells[i])
	}
	return cells, rule
}

// brTags are how a line break is written in a table's cell.
var brTags = []string{"<br>", "<br/>", "<br />"}

// cellBreaks reads the <br>s in a table cell's Markdown as line breaks;
// one in a code span, or escaped, is text. A link's address holds no
// code span, nor a <br>.
func cellBreaks(md string) string {
	if !strings.Contains(strings.ToLower(md), "<br") {
		return md
	}
	s := []rune(md)
	links := &linkScan{s: s}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\\':
			b.WriteString(string(s[i:min(i+2, len(s))]))
			i += 2
			continue
		case '[':
			if text, _, n, ok := links.link(i); ok {
				b.WriteString("[" + cellBreaks(string(text)) + string(s[i+1+len(text):i+n]))
				i += n
				continue
			}
		case '`':
			if _, n := codeSpanAt(s[i:]); n > 0 {
				b.WriteString(string(s[i : i+n]))
				i += n
				continue
			}
			j := i
			for j < len(s) && s[j] == '`' {
				j++
			}
			b.WriteString(string(s[i:j]))
			i = j
			continue
		case '<':
			rest := string(s[i:])
			if tag := brTagAt(rest); tag != "" {
				b.WriteByte('\n')
				i += len([]rune(tag))
				continue
			}
		}
		b.WriteRune(s[i])
		i++
	}
	return b.String()
}

// brTagAt is the <br> at the start of s, or "".
func brTagAt(s string) string {
	for _, tag := range brTags {
		if _, ok := cutFold(s, tag); ok {
			return s[:len(tag)]
		}
	}
	return ""
}

// imageLine reads a line that is only a picture from the web, as
// "![alt](https://…/a.png)", as an image block; nil for any other line.
func imageLine(line string) *Node {
	if !strings.HasPrefix(line, "![") {
		return nil
	}
	md := parser.New(parser.GFM)
	md.Set(line)
	if md.Len() != 2 || md.Block(1).Kind != parser.Paragraph {
		return nil
	}
	in := md.Inlines(1)
	if len(in) == 0 || in[0].Kind != parser.Image || in[0].Next != len(in) {
		return nil
	}
	// An address with a space in it, a title after a no-break space
	// among them, is no picture's: the server reads the line as text. Nor
	// is one an image block refuses, as one without a host's name.
	if strings.IndexFunc(in[0].Dest, unicode.IsSpace) >= 0 || !agentURL(in[0].Dest) {
		return nil
	}
	n, err := ImageNode(in[0].Dest)
	if err != nil {
		return nil
	}
	n.Set("alt", parser.PlainText(in[1:]))
	return n
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// textNode is a new text block of a type, holding d.
func textNode(typ string, d Delta) *Node {
	n := &Node{ID: NewBlockID(), Type: typ}
	n.SetDelta(d)
	return n
}
