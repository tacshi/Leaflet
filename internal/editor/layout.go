package editor

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

// blockLayout is a text block laid out: its text's layout and where the
// text sits in the block's box.
type blockLayout struct {
	key    string
	layout *ui.TextLayout
	// x and y are the text's origin in the block's box; h the box's height.
	x, y, h float32
	// font is the block's base font.
	font ui.Font
}

const indentStep = 26

// depth is how deep a block is nested in lists.
func (e *Editor) depth(id string) int {
	if e.order != nil {
		if d, ok := e.depths[id]; ok {
			return d
		}
	}
	d := 0
	loc := e.Content.Locate(id)
	for loc != nil && loc.Parent != nil {
		d++
		loc = e.Content.Locate(loc.Parent.ID)
	}
	return d
}

// style returns a text block's font and the room above and below it.
func (e *Editor) style(n *doc.Node) (font ui.Font, top, bottom float32) {
	size := e.Size
	plain := n.Delta().Plain()
	font = ui.Font{Family: theme.PageFamily(plain), Size: size, LineHeight: leading(plain)}
	top, bottom = 3, 3
	switch n.Type {
	case "heading":
		font.Weight, font.LineHeight = 650, 0
		switch n.Int("level", 1) {
		case 1:
			font.Size, top, bottom = size*1.75, 22, 6
		case 2:
			font.Size, top, bottom = size*1.4, 18, 4
		case 3:
			font.Size, top, bottom = size*1.18, 14, 2
		default:
			font.Size, top, bottom = size*1.05, 10, 2
		}
	case "quote":
		font.Italic = true
		top, bottom = 6, 6
	case "bulleted_list", "numbered_list", "todo_list":
		top, bottom = 2, 2
	case "code":
		font.Family, font.Size, font.LineHeight = theme.CodeFamily(plain), size*0.8, 1.5
		top, bottom = codeMargin+codeBar, codePad+codeMargin
	case "callout":
		top, bottom = calloutMargin+calloutPad, calloutPad+calloutMargin
	case "toggle_list":
		top, bottom = 2, 2
	}
	return
}

// leading is the height of a line of prose, as a multiple of its size:
// more for text mostly in Chinese, Japanese or Korean, whose characters
// fill the em square, than for Latin text. One of those characters counts
// as much as a word of four letters, so a few English terms or a link in
// a Chinese paragraph leave it Chinese.
func leading(text string) float32 {
	cjk, other := 0, 0
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			cjk++
		case unicode.IsLetter(r):
			other++
		}
	}
	if cjk > 0 && cjk*4 >= other {
		return 1.8
	}
	return 1.5
}

// markerWidth is the room before a block's text for its bullet, number or
// box.
func markerWidth(n *doc.Node) float32 {
	switch n.Type {
	case "bulleted_list", "numbered_list", "todo_list", "toggle_list":
		return 26
	case "callout":
		return 44
	case "quote":
		return 18
	case "code":
		return codePad
	}
	return 0
}

// spans styles a block's runs, with an input method's composition in
// place.
func (e *Editor) spans(n *doc.Node, pal *theme.Palette, font ui.Font) []ui.Span {
	if n.Type == "code" {
		return codeSpans(n.Delta().Plain(), n.Str("language"), font, pal)
	}
	d := n.Delta()
	muted := n.Type == "todo_list" && n.Bool("checked")
	code := theme.CodeFamily(d.Plain())
	var out []ui.Span
	for _, r := range d {
		s := ui.Span{Text: r.Text}
		if r.Attrs.Bold {
			s.Weight = 700
		}
		if r.Attrs.Italic {
			s.Italic = true
		}
		if r.Attrs.Under {
			s.Underline = true
		}
		if r.Attrs.Strike || muted {
			s.Strikethrough = true
		}
		if muted {
			s.Color = pal.TextMuted
		}
		if r.Attrs.Code {
			s.Font = code
			s.Size = font.Size * 0.86
			s.Background = pal.Code
			s.Radius = 4
		}
		if r.Attrs.Href != "" {
			s.Color = pal.Accent
			s.Underline = true
			s.DecorationColor = pal.AccentSoftBorder
		}
		if r.Attrs.Color != "" {
			s.Color = hexColor(r.Attrs.Color)
		}
		if r.Attrs.Background != "" {
			s.Background = hexColor(r.Attrs.Background)
		}
		if r.Attrs.Mention != nil {
			s.Color = pal.AccentText
			s.Weight = 600
			s.Background = pal.AccentSoft
			s.Radius = 4
			s.Underline = false // a page mention is a link, shown as a mention
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		// An empty block still has a line's height.
		out = []ui.Span{{Text: ""}}
	}
	return out
}

func (e *Editor) layoutOf(n *doc.Node) *blockLayout {
	if n == nil || !n.IsText() {
		return nil
	}
	return e.layouts[n.ID]
}

// layout lays a text block out at the editor's width, reusing the last
// layout while the block and its look stay the same.
func (e *Editor) layout(n *doc.Node, pal *theme.Palette, dark bool) *blockLayout {
	font, top, bottom := e.style(n)
	depth := e.depth(n.ID)
	x := float32(depth)*indentStep + markerWidth(n)
	width := max(e.width-x, 40)
	switch n.Type {
	case "code":
		width = max(width-codePad, 40)
	case "callout":
		width = max(width-calloutPad, 40)
	}
	raw, _ := json.Marshal(n.Data)
	key := n.Type + string(raw) + ftoa(width) + ftoa(font.Size) + ftoa(font.LineHeight) + font.Family + theme.CodeFamily(n.Delta().Plain())
	if dark {
		key += "d"
	}
	if l := e.layouts[n.ID]; l != nil && l.key == key {
		return l
	}
	spans := e.spans(n, pal, font)
	tl := ui.ShapeRichText(spans, font, width)
	_, h := tl.Size()
	if h == 0 {
		h = font.Size * max(font.LineHeight, 1.3)
	}
	l := &blockLayout{key: key, layout: tl, x: x, y: top, h: top + h + bottom, font: font}
	e.layouts[n.ID] = l
	return l
}

func ftoa(f float32) string {
	raw, _ := json.Marshal(f)
	return string(raw)
}

// utf16Off converts a rune offset in a block's text to UTF-16 units.
func utf16Off(n *doc.Node, off int) int {
	units := 0
	i := 0
	for _, r := range n.Delta().Plain() {
		if i >= off {
			break
		}
		units += utf16.RuneLen(r)
		i++
	}
	return units
}

// runeOff converts UTF-16 units in a block's text to a rune offset.
func runeOff(n *doc.Node, units int) int {
	u := 0
	i := 0
	for _, r := range n.Delta().Plain() {
		if u >= units {
			break
		}
		u += utf16.RuneLen(r)
		i++
	}
	return i
}

// clip is what Leaflet last copied: the text it put on the clipboard and
// the blocks that text stands for. Pasting that text again pastes the
// blocks, with the tables, charts, images, embeds, recordings and nesting
// that markdown loses on the way.
var clip struct {
	text   string
	blocks []*doc.Node
	// delta is the formatted text of a selection within one block.
	delta doc.Delta
}

// copyBlocks returns the blocks of a selection over more than one block,
// or of one block selected whole that is not text, cut to the selection,
// with their nesting; nil for text within one block.
func (e *Editor) copyBlocks() []*doc.Node {
	if e.Collapsed() {
		return nil
	}
	a, b := e.ordered()
	if a.ID == b.ID {
		if n := e.node(a.ID); n == nil || n.IsText() {
			return nil
		}
	}
	// A folded heading's section goes with it.
	us := e.withSections()
	var out []*doc.Node
	type open struct {
		depth int
		n     *doc.Node
	}
	var stack []open
	for i := indexIn(us, a.ID); i >= 0 && i <= indexIn(us, b.ID) && i < len(us); i++ {
		n := us[i].Clone()
		// A folded toggle takes its hidden children with it.
		if n.Type != "table" && !collapsed(n) {
			n.Children = nil
		}
		if n.IsText() {
			from, to := 0, textLen(n)
			if n.ID == a.ID {
				from = a.Off
			}
			if n.ID == b.ID {
				to = b.Off
			}
			n.SetDelta(n.Delta().Slice(from, to))
		}
		depth := e.depth(n.ID)
		for len(stack) > 0 && stack[len(stack)-1].depth >= depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 && doc.NestableTypes[stack[len(stack)-1].n.Type] {
			p := stack[len(stack)-1].n
			p.Children = append(p.Children, n)
		} else {
			out = append(out, n)
		}
		stack = append(stack, open{depth, n})
	}
	return out
}

// inTable reports whether a block is inside a table's cell.
func (e *Editor) inTable(id string) bool {
	for loc := e.Content.Locate(id); loc != nil && loc.Parent != nil; loc = e.Content.Locate(loc.Parent.ID) {
		if loc.Parent.Type == "table" {
			return true
		}
	}
	return false
}

// freshBlocks copies blocks with new IDs, to paste them again.
func freshBlocks(nodes []*doc.Node) []*doc.Node {
	out := make([]*doc.Node, len(nodes))
	for i, n := range nodes {
		c := n.Clone()
		var renew func(*doc.Node)
		renew = func(n *doc.Node) {
			n.ID = doc.NewBlockID()
			for _, k := range n.Children {
				renew(k)
			}
		}
		renew(c)
		out[i] = c
	}
	return out
}

// copyText returns the selection as markdown, a line per block.
func (e *Editor) copyText() string {
	if e.Collapsed() {
		return ""
	}
	a, b := e.ordered()
	if a.ID == b.ID {
		n := e.node(a.ID)
		if n.IsText() {
			return n.Delta().Slice(a.Off, b.Off).Plain()
		}
	}
	us := e.withSections()
	var lines []string
	for i := indexIn(us, a.ID); i >= 0 && i <= indexIn(us, b.ID) && i < len(us); i++ {
		n := us[i]
		if !n.IsText() {
			sub := &doc.Content{Children: []*doc.Node{n.Clone()}}
			lines = append(lines, strings.TrimRight(doc.PageMarkdown("", sub, e.Host.PageTitle), "\n"))
			continue
		}
		from, to := 0, textLen(n)
		if n.ID == a.ID {
			from = a.Off
		}
		if n.ID == b.ID {
			to = b.Off
		}
		if n.Type == "code" || n.Type == "callout" {
			part := n.Clone()
			part.SetDelta(n.Delta().Slice(from, to))
			if n.Type == "code" {
				lines = append(lines, doc.CodeMarkdown(part))
			} else {
				lines = append(lines, doc.CalloutMarkdown(part))
			}
			continue
		}
		text := n.Delta().Slice(from, to).ToMarkdown()
		prefix := blockPrefix(n, e.depth(n.ID))
		if prefix == "" {
			text = doc.EscapeBlockStart(text)
		}
		lines = append(lines, prefix+text)
	}
	return strings.Join(lines, "\n")
}

func blockPrefix(n *doc.Node, depth int) string {
	ind := strings.Repeat("  ", depth)
	switch n.Type {
	case "heading":
		return strings.Repeat("#", min(max(n.Int("level", 1), 1), 6)) + " "
	case "bulleted_list":
		return ind + "- "
	case "numbered_list":
		return ind + "1. "
	case "todo_list":
		if n.Bool("checked") {
			return ind + "- [x] "
		}
		return ind + "- [ ] "
	case "quote":
		return "> "
	case "toggle_list":
		return ind + "▸ "
	}
	return ""
}

// MarkdownBlocks reads Markdown as blocks, as pasting it does.
func MarkdownBlocks(text string) []*doc.Node {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return parseLines(strings.Trim(text, "\n"))
}

// parseLines reads pasted text as blocks: a line each, with markdown
// block prefixes. List items indented under one nest in it, lines of a
// markdown table make a table, and a fenced block a code block.
func parseLines(text string) []*doc.Node {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []*doc.Node
	// lists are the list items open at each depth, for nesting.
	var lists []*doc.Node
	var table [][]string
	flushTable := func() {
		if len(table) > 0 {
			if t, err := doc.TableNode(table, ""); err == nil {
				out = append(out, t)
			}
			table = nil
		}
	}
	// code is the fenced block open, and fence the backticks it opened
	// with; its indent comes off each of its lines.
	// A Mermaid fence makes a diagram, and lines between "$$" math: the
	// block in its place in out, at codeAt, once it closes.
	var code *doc.Node
	var codeLines []string
	fence, fenceIndent := "", ""
	codeAt := 0
	closeCode := func() {
		text := strings.Join(codeLines, "\n")
		switch {
		case fence == "$$":
			out[codeAt] = doc.MathNode(text)
		case code.Str("language") == "mermaid":
			out[codeAt] = doc.DiagramNode(text)
		default:
			code.SetDelta(doc.Delta{{Text: text}}.Compact())
		}
		code = nil
	}
	// callout is the GitHub alert being read: its lines go in it.
	var callout *doc.Node
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if code != nil {
			if fence == "$$" && strings.TrimSpace(trimmed) == "$$" || fence != "$$" && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, "` ") == "" {
				closeCode()
				continue
			}
			codeLines = append(codeLines, strings.TrimPrefix(line, fenceIndent))
			continue
		}
		if tone, ok := doc.AlertTone(trimmed); ok {
			flushTable()
			lists = nil
			callout = newText("callout", nil)
			callout.Set("tone", tone)
			out = append(out, callout)
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, ">"); ok && callout != nil {
			d := callout.Delta()
			if d.Len() > 0 {
				d = d.Concat(doc.Delta{{Text: "\n"}})
			}
			callout.SetDelta(d.Concat(doc.FromMarkdown(strings.TrimPrefix(rest, " "))))
			continue
		}
		callout = nil
		if strings.TrimSpace(line) == "" {
			// A blank line only separates blocks, as in Markdown; it makes
			// no empty paragraph. A list goes on across it.
			flushTable()
			continue
		}
		if language, ok := doc.CodeFence(trimmed); ok || strings.TrimSpace(trimmed) == "$$" {
			flushTable()
			lists = nil
			code, codeLines = doc.CodeNode("", language), nil
			fenceIndent = line[:len(line)-len(trimmed)]
			fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "`"))]
			if !ok {
				fence = "$$"
			}
			codeAt = len(out)
			out = append(out, code)
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			lists = nil
			if row := tableRow(trimmed); row != nil {
				table = append(table, row)
			}
			continue
		}
		flushTable()
		indent := strings.Count(line[:len(line)-len(trimmed)], " ") + 2*strings.Count(line[:len(line)-len(trimmed)], "\t")
		typ, rest := "paragraph", trimmed
		level, checked := 0, false
		switch {
		case strings.HasPrefix(trimmed, "### "):
			typ, level, rest = "heading", 3, trimmed[4:]
		case strings.HasPrefix(trimmed, "## "):
			typ, level, rest = "heading", 2, trimmed[3:]
		case strings.HasPrefix(trimmed, "# "):
			typ, level, rest = "heading", 1, trimmed[2:]
		case strings.HasPrefix(trimmed, "- [ ] "), strings.HasPrefix(trimmed, "* [ ] "):
			typ, rest = "todo_list", trimmed[6:]
		case strings.HasPrefix(trimmed, "- [x] "), strings.HasPrefix(trimmed, "* [x] "):
			typ, rest, checked = "todo_list", trimmed[6:], true
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			typ, rest = "bulleted_list", trimmed[2:]
		case strings.HasPrefix(trimmed, "> "):
			typ, rest = "quote", trimmed[2:]
		case doc.IsRule(trimmed):
			lists = nil
			out = append(out, &doc.Node{ID: doc.NewBlockID(), Type: "divider"})
			continue
		default:
			if i := strings.Index(trimmed, ". "); i > 0 && i < 4 && isDigits(trimmed[:i]) {
				typ, rest = "numbered_list", trimmed[i+2:]
			}
		}
		n := newText(typ, doc.FromMarkdown(rest))
		if level > 0 {
			n.Set("level", level)
		}
		if typ == "todo_list" {
			n.Set("checked", checked)
		}
		if !doc.NestableTypes[typ] {
			lists = nil
			out = append(out, n)
			continue
		}
		// Two spaces, or a tab, a level: under the item open one level up.
		depth := min(indent/2, len(lists))
		lists = append(lists[:depth], n)
		if depth == 0 {
			out = append(out, n)
		} else {
			parent := lists[depth-1]
			parent.Children = append(parent.Children, n)
		}
	}
	flushTable()
	if code != nil {
		// A fence left open runs to the end.
		closeCode()
	}
	return out
}

// tableRow reads a line of a markdown table, or nil for the line under the
// header ("| --- | --- |"). As in GitHub's tables, a backslash keeps the
// character after it in the cell, and \| is a | of the cell's text.
func tableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	var cells []string
	var cell strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line):
			i++
			if line[i] != '|' {
				cell.WriteByte('\\')
			}
			cell.WriteByte(line[i])
		case line[i] == '|':
			cells = append(cells, cell.String())
			cell.Reset()
		default:
			cell.WriteByte(line[i])
		}
	}
	// A closing | ends the last cell; without one, what is after the last
	// | is a cell.
	if rest := cell.String(); rest != "" || len(cells) == 0 {
		cells = append(cells, rest)
	}
	rule := true
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
		if strings.Trim(cells[i], "-: ") != "" || cells[i] == "" {
			rule = false
		}
	}
	if rule {
		return nil
	}
	return cells
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// joins reports whether a pasted block's text joins the line it is pasted
// on.
func joins(n *doc.Node) bool { return n.IsText() && n.Type != "code" }

// paste inserts text at the caret: a line inline, several as blocks. The
// text Leaflet itself last copied pastes as the blocks it came from, a
// YouTube or Vimeo link or a link to a video file pasted on an empty line
// as a video, and a link to
// a picture there as an image.
func (e *Editor) paste(text string) {
	if e.ReadOnly || text == "" {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if a, _ := e.ordered(); e.node(a.ID) != nil && e.node(a.ID).Type == "code" && (clip.blocks == nil || text != clip.text) {
		// Into code, text goes as it is.
		e.pushUndo(false)
		e.coalescing = false
		e.deleteSelection()
		n := e.node(e.focus.ID)
		n.SetDelta(doc.Delta{{Text: string([]rune(n.Delta().Plain())[:e.focus.Off]) + text + string([]rune(n.Delta().Plain())[e.focus.Off:])}})
		e.invalidate(n.ID)
		off := e.focus.Off + utf8.RuneCountInString(text)
		e.Select(Pos{n.ID, off}, Pos{n.ID, off})
		e.changed()
		return
	}
	if n := e.node(e.focus.ID); n != nil && n.Type == "paragraph" && textLen(n) == 0 && e.Collapsed() && !e.inTable(n.ID) {
		if v, err := doc.VideoNode(text, ""); err == nil {
			e.InsertBlocks(v)
			return
		}
		if doc.ImageLink(text) {
			img, _ := doc.ImageNode(text)
			e.InsertBlocks(img)
			return
		}
	}
	var blocks []*doc.Node
	if e.inTable(e.focus.ID) {
		// A cell holds one line: the lines join.
		text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	} else if clip.blocks != nil && text == clip.text {
		blocks = freshBlocks(clip.blocks)
	} else if clip.delta != nil && text == clip.text {
		blocks = []*doc.Node{newText("paragraph", clip.delta)}
	}
	if blocks == nil && !strings.Contains(text, "\n") {
		// A web link pasted on selected text links it.
		if a, b := e.ordered(); doc.WebURL(text) && a.ID == b.ID && a.Off < b.Off && e.node(a.ID).IsText() {
			e.SetLink(strings.TrimSpace(text))
			return
		}
		e.pushUndo(false)
		e.coalescing = false
		e.deleteSelection()
		n := e.node(e.focus.ID)
		if n != nil && n.IsText() {
			attrs := n.Delta().AttrsAt(e.focus.Off)
			if doc.WebURL(text) {
				// A web link pasted goes in as a link to itself.
				attrs.Href = strings.TrimSpace(text)
			}
			n.SetDelta(n.Delta().Insert(e.focus.Off, text, attrs))
			e.invalidate(n.ID)
			off := e.focus.Off + utf8.RuneCountInString(text)
			e.Select(Pos{n.ID, off}, Pos{n.ID, off})
			e.changed()
		}
		return
	}
	if blocks == nil {
		blocks = parseLines(strings.TrimRight(text, "\n"))
	}
	e.pasteBlocks(blocks)
}

// pasteBlocks puts blocks at the caret, in place of the selection: the
// first joins the text before the caret, and the last takes the text
// after it.
func (e *Editor) pasteBlocks(blocks []*doc.Node) {
	if len(blocks) == 0 {
		return
	}
	e.pushUndo(false)
	e.coalescing = false
	e.deleteSelection()
	n := e.node(e.focus.ID)
	if n == nil || !n.IsText() {
		e.InsertBlocks(blocks...)
		return
	}
	d := n.Delta()
	head, tail := d.Slice(0, e.focus.Off), d.Slice(e.focus.Off, d.Len())
	first := blocks[0]
	if !joins(first) {
		// A table, chart, code and the like cannot join a line: the blocks
		// go before the caret's block, or after its text before the caret.
		if head.Len() == 0 {
			for _, b := range blocks {
				_ = e.Content.Insert(doc.Position{Before: n.ID}, b)
			}
			e.layouts = map[string]*blockLayout{}
			e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
			e.changed()
			return
		}
		n.SetDelta(head)
		e.invalidate(n.ID)
		at := n.ID
		for _, b := range blocks {
			_ = e.Content.Insert(doc.Position{After: at}, b)
			at = b.ID
		}
		p := newText("paragraph", tail)
		_ = e.Content.Insert(doc.Position{After: at}, p)
		e.layouts = map[string]*blockLayout{}
		e.Select(Pos{p.ID, 0}, Pos{p.ID, 0})
		e.changed()
		return
	}
	// The first line joins the caret's block, the last takes what followed
	// the caret, and the lines between become blocks of their own.
	n.SetDelta(head.Concat(first.Delta()))
	if head.Len() == 0 && first.Type != "paragraph" {
		n.Type = first.Type
		for k, v := range first.Data {
			if k != "delta" {
				n.Data[k] = v
			}
		}
	}
	e.invalidate(n.ID)
	at := n.ID
	if len(first.Children) > 0 {
		if NestableTypes(n) {
			n.Children = append(first.Children, n.Children...)
		} else {
			for _, k := range first.Children {
				_ = e.Content.Insert(doc.Position{After: at}, k)
				at = k.ID
			}
		}
	}
	last := n
	if len(first.Children) > 0 {
		last = first.Children[len(first.Children)-1]
	}
	for _, b := range blocks[1:] {
		_ = e.Content.Insert(doc.Position{After: at}, b)
		at, last = b.ID, b
	}
	// What followed the caret goes on the last line pasted.
	for len(last.Children) > 0 && last.Type != "table" {
		last = last.Children[len(last.Children)-1]
	}
	if !joins(last) {
		p := newText("paragraph", tail)
		_ = e.Content.Insert(doc.Position{After: at}, p)
		last = p
		tail = nil
	}
	off := textLen(last)
	last.SetDelta(last.Delta().Concat(tail))
	e.layouts = map[string]*blockLayout{}
	e.Select(Pos{last.ID, off}, Pos{last.ID, off})
	e.changed()
}
