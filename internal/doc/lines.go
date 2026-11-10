package doc

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui/markdown/parser"
)

func MarkdownBlocks(text string) []*Node {
	return ParseLines(strings.Trim(lineEnds.Replace(text), "\n"))
}

var lineEnds = strings.NewReplacer("\r\n", "\n", "\r", "\n")

func ParseLines(text string) []*Node {
	text = strings.ToValidUTF8(lineEnds.Replace(text), "\uFFFD")
	var p lineParser
	for _, line := range strings.Split(text, "\n") {
		p.line(line)
	}
	p.flushTable()
	if p.code != nil {
		p.closeCode()
	}
	for _, t := range p.texts {
		t.n.SetDelta(FromMarkdown(strings.Join(t.md, "\n")))
	}
	return p.out
}

type lineParser struct {
	out               []*Node
	open              []openBlock
	texts             []*textBlock
	text              *textBlock
	summary, awaiting *textBlock

	// deep counts the toggles left unopened where a page nests no deeper, each still to meet its </details>.
	deep int

	// cur is the line being read, which every part of it still to read ends, and spans its code spans.
	cur   string
	spans codeSpans[byte]

	table   [][]string
	tableIn *[]*Node
	ruled   bool

	numbers map[*Node]int

	code              *Node
	codeLines         []string
	fence, codeIndent string
	codeIn            *[]*Node
}

type openBlock struct {
	n      *Node
	toggle bool
	min    int
}

type textBlock struct {
	n            *Node
	md           []string
	quoted, item bool
	min          int
	hard         bool
}

func (p *lineParser) line(line string) {
	p.cur, p.spans = line, codeSpans[byte]{}
	for more := true; more; {
		line, more = p.part(line)
	}
}

// part reads a line up to where a block begins in the middle of it, after a <details> or a </summary>, and returns
// the rest of it, still to read.
func (p *lineParser) part(line string) (next string, more bool) {
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
		return p.summaryLine(trimmed)
	}
	if strings.TrimSpace(line) == "" {
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
			return p.summaryLine(rest)
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
		if !p.opens(openBlock{n: n, toggle: true, min: indent}) {
			p.deep++
		}
		p.cur = strings.TrimRightFunc(p.cur, unicode.IsSpace)
		t := p.newText(n, false, false, 0)
		if summary, ok := cutFold(rest, "<summary>"); ok {
			p.summary = t
			return p.summaryLine(summary)
		}
		p.awaiting = t
		return rest, rest != ""
	}
	if strings.EqualFold(strings.TrimSpace(trimmed), "</details>") {
		if p.deep > 0 {
			p.deep--
			return
		}
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
		p.opens(openBlock{n: n, min: indent + 2})
		p.text = p.newText(n, false, true, indent+2)
		p.text.addLine(rest)
		return
	}
	if level, rest, ok := headingLine(trimmed); ok {
		n := textNode("heading", nil)
		n.Set("level", level)
		p.add(n, indent)
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
	return
}

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

func (p *lineParser) counts(line string, indent int) bool {
	if !longNumber(line) {
		return true
	}
	kids := *p.container(indent)
	return len(kids) > 0 && p.numbers[kids[len(kids)-1]] == itemNumber(line)-1
}

func (p *lineParser) add(n *Node, indent int) {
	kids := p.container(indent)
	*kids = append(*kids, n)
}

func (p *lineParser) container(indent int) *[]*Node {
	for len(p.open) > 0 && p.deep == 0 {
		top := p.open[len(p.open)-1]
		if top.toggle || indent >= top.min {
			break
		}
		p.open = p.open[:len(p.open)-1]
	}
	return p.kids()
}

// opens lets the blocks that follow nest in b's, unless they would be deeper than a page nests: there, and in a
// toggle left unopened for it, they follow the block.
func (p *lineParser) opens(b openBlock) bool {
	if p.deep > 0 || len(p.open)+2 > maxDepth {
		return false
	}
	p.open = append(p.open, b)
	return true
}

func (p *lineParser) kids() *[]*Node {
	if len(p.open) == 0 {
		return &p.out
	}
	return &p.open[len(p.open)-1].n.Children
}

func (p *lineParser) min() int {
	if len(p.open) == 0 {
		return 0
	}
	return p.open[len(p.open)-1].min
}

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

func (p *lineParser) summaryLine(s string) (next string, more bool) {
	t := p.summary
	i := p.summaryEnd(s)
	if i < 0 {
		t.md = append(t.md, s)
		return
	}
	t.md = append(t.md, s[:i])
	p.summary = nil
	next = s[i+len("</summary>"):]
	return next, strings.TrimSpace(next) != ""
}

func (p *lineParser) flushTable() {
	if len(p.table) > 0 {
		if t, err := TableNode(p.table, ""); err == nil {
			*p.tableIn = append(*p.tableIn, t)
		} else {
			// Too large for a table: its rows stay, as lines of text.
			for _, row := range p.table {
				*p.tableIn = append(*p.tableIn, textNode("paragraph", FromMarkdown(strings.Join(row, " | "))))
			}
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

func (t *textBlock) addLine(s string) {
	if t.hard {
		last := len(t.md) - 1
		t.md[last] = strings.TrimSuffix(t.md[last], `\`)
		if line, ok := strings.CutSuffix(t.md[last], " "); ok && endsInAddress(strings.TrimRight(line, " ")) {
			t.md[last] = line
		}
	}
	t.md = append(t.md, s)
	t.hard = hardBreak(s)
}

func hardBreak(s string) bool {
	slashes := len(s) - len(strings.TrimRight(s, `\`))
	return slashes%2 == 1
}

var addressStart = regexp.MustCompile(`[Hh][Tt][Tt][Pp][Ss]?://|[Ww][Ww][Ww]\.`)

func endsInAddress(s string) bool {
	return addressStart.MatchString(s[strings.LastIndexAny(s, " \t")+1:])
}

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
	if i := strings.IndexAny(s, ".)"); i > 0 && i <= 9 && isDigits(s[:i]) && (len(s) == i+1 || s[i+1] == ' ') {
		return "numbered_list", strings.TrimPrefix(s[i+1:], " "), false, true
	}
	return "", "", false, false
}

func itemNumber(line string) int {
	n, _ := strconv.Atoi(line[:strings.IndexAny(line, ".)")])
	return n
}

func longNumber(line string) bool { return strings.IndexAny(line, ".)") > 3 }

func headingLine(s string) (level int, rest string, ok bool) {
	hashes := len(s) - len(strings.TrimLeft(s, "#"))
	if hashes < 1 || hashes > 6 || len(s) > hashes && s[hashes] != ' ' {
		return 0, "", false
	}
	return hashes, strings.TrimPrefix(s[hashes:], " "), true
}

func quoteLine(s string) (string, bool) {
	if s == ">" {
		return "", true
	}
	return strings.CutPrefix(s, "> ")
}

func detailsOpen(s string) (open bool, rest string, ok bool) {
	if rest, ok := cutFold(s, "<details>"); ok {
		return false, strings.TrimSpace(rest), true
	}
	if rest, ok := cutFold(s, "<details open>"); ok {
		return true, strings.TrimSpace(rest), true
	}
	return false, "", false
}

// summaryEnd finds the </summary> in s, the rest of the line being read, that is not in code.
func (p *lineParser) summaryEnd(s string) int {
	at := len(p.cur) - len(s)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '`':
			if p.spans.s == nil {
				p.spans.s = []byte(p.cur)
			}
			n := p.spans.span(at + i)
			if n == 0 {
				n = ticks(p.spans.s[at+i:])
			}
			i += n - 1
		case '<':
			if _, ok := cutFold(s[i:], "</summary>"); ok {
				return i
			}
		}
	}
	return -1
}

func cutFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

func tableRow(line string) (cells []string, rule bool) {
	s := []rune(strings.TrimPrefix(strings.TrimSpace(line), "|"))
	var cell strings.Builder
	spans := codeSpans[rune]{s: s}
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
			if n := spans.span(i); n > 0 {
				code = i + n
			} else {
				code = i + ticks(s[i:])
			}
			cell.WriteRune(s[i])
		default:
			cell.WriteRune(s[i])
		}
	}
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

var brTags = []string{"<br>", "<br/>", "<br />"}

// cellBreaks reads the <br> of a cell as line breaks, but for those in code and in a link's address.
func cellBreaks(md string) string { return breaksIn(md, 0) }

// breaksIn is cellBreaks for text depth links deep, which reads into links as deep as parseInline does.
func breaksIn(md string, depth int) string {
	if depth > maxInlineDepth || !strings.Contains(strings.ToLower(md), "<br") {
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
				b.WriteString("[" + breaksIn(string(text), depth+1) + string(s[i+1+len(text):i+n]))
				i += n
				continue
			}
		case '`':
			_, n := links.codeAt(i)
			if n == 0 {
				n = ticks(s[i:])
			}
			b.WriteString(string(s[i : i+n]))
			i += n
			continue
		case '<':
			if tag := brTagAt(tagText(s, i)); tag != "" {
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

func tagText(s []rune, i int) string {
	return string(s[i:min(i+len("</summary>"), len(s))])
}

func brTagAt(s string) string {
	for _, tag := range brTags {
		if _, ok := cutFold(s, tag); ok {
			return s[:len(tag)]
		}
	}
	return ""
}

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

func textNode(typ string, d Delta) *Node {
	n := &Node{ID: NewBlockID(), Type: typ}
	n.SetDelta(d)
	return n
}
