package editor

import (
	"encoding/json"
	"strings"
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
	font = ui.Font{Family: e.Family, Size: size}
	top, bottom = 3, 3
	switch n.Type {
	case "heading":
		font.Weight = 650
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
	}
	return
}

// markerWidth is the room before a block's text for its bullet, number or
// box.
func markerWidth(n *doc.Node) float32 {
	switch n.Type {
	case "bulleted_list", "numbered_list", "todo_list":
		return 26
	case "quote":
		return 18
	}
	return 0
}

// spans styles a block's runs, with an input method's composition in
// place.
func (e *Editor) spans(n *doc.Node, pal *theme.Palette, font ui.Font) []ui.Span {
	d := n.Delta()
	muted := n.Type == "todo_list" && n.Bool("checked")
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
			s.Font = "monospace"
			s.Size = font.Size * 0.86
			s.Background = pal.Code
			s.Radius = 4
		}
		if r.Attrs.Href != "" {
			s.Color = pal.Accent
			s.Underline = true
			s.DecorationColor = pal.AccentSoftBorder
		}
		if r.Attrs.Mention != nil {
			s.Color = pal.AccentText
			s.Weight = 600
			s.Background = pal.AccentSoft
			s.Radius = 4
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
	raw, _ := json.Marshal(n.Data)
	key := n.Type + string(raw) + ftoa(width) + ftoa(font.Size) + e.Family
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
		h = font.Size * 1.3
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
	us := e.units()
	var lines []string
	for i := e.index(a.ID); i <= e.index(b.ID) && i < len(us); i++ {
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
		text := n.Delta().Slice(from, to).ToMarkdown()
		lines = append(lines, blockPrefix(n, e.depth(n.ID))+text)
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
	}
	return ""
}

// parseLines reads pasted text as blocks: a line each, with markdown
// block prefixes.
func parseLines(text string) []*doc.Node {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []*doc.Node
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
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
		case trimmed == "---":
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
		out = append(out, n)
	}
	return out
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// paste inserts text at the caret: a line inline, several as blocks.
func (e *Editor) paste(text string) {
	if e.ReadOnly || text == "" {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.Contains(text, "\n") {
		e.pushUndo(false)
		e.coalescing = false
		e.deleteSelection()
		n := e.node(e.focus.ID)
		if n != nil && n.IsText() {
			n.SetDelta(n.Delta().Insert(e.focus.Off, text, n.Delta().AttrsAt(e.focus.Off)))
			e.invalidate(n.ID)
			off := e.focus.Off + utf8.RuneCountInString(text)
			e.Select(Pos{n.ID, off}, Pos{n.ID, off})
			e.changed()
		}
		return
	}
	blocks := parseLines(strings.TrimRight(text, "\n"))
	e.pushUndo(false)
	e.coalescing = false
	e.deleteSelection()
	n := e.node(e.focus.ID)
	if n == nil || !n.IsText() {
		e.InsertBlocks(blocks...)
		return
	}
	// The first line joins the caret's block, the last takes what followed
	// the caret, and the lines between become blocks of their own.
	d := n.Delta()
	head, tail := d.Slice(0, e.focus.Off), d.Slice(e.focus.Off, d.Len())
	first := blocks[0]
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
	last := n
	for _, b := range blocks[1:] {
		e.Content.Insert(doc.Position{After: at}, b)
		at, last = b.ID, b
	}
	off := textLen(last)
	if last.IsText() {
		last.SetDelta(last.Delta().Concat(tail))
	}
	e.layouts = map[string]*blockLayout{}
	e.Select(Pos{last.ID, off}, Pos{last.ID, off})
	e.changed()
}
