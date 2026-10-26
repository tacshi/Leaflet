package editor

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"leaflet/internal/doc"
)

func newText(typ string, d doc.Delta) *doc.Node {
	n := &doc.Node{ID: doc.NewBlockID(), Type: typ}
	n.SetDelta(d)
	return n
}

// deleteSelection removes what is selected and leaves a caret at its
// start. It reports whether anything was selected.
func (e *Editor) deleteSelection() bool {
	if e.Collapsed() {
		return false
	}
	a, b := e.ordered()
	if a.ID == b.ID {
		n := e.node(a.ID)
		if n.IsText() {
			n.SetDelta(n.Delta().Delete(a.Off, b.Off))
			e.invalidate(n.ID)
		}
		e.anchor, e.focus = a, a
		return true
	}
	// A folded heading's section goes with it.
	us := e.withSections()
	ia, ib := indexIn(us, a.ID), indexIn(us, b.ID)
	first, last := us[ia], us[ib]
	var tail doc.Delta
	if last.IsText() {
		tail = last.Delta().Slice(b.Off, textLen(last))
	}
	// Remove everything after the first block, last first, so children
	// outside the selection move up to their parent's place as it goes.
	for i := ib; i > ia; i-- {
		n := us[i]
		if !collapsed(n) || i == ib {
			e.liftChildren(n)
		}
		e.Content.Remove(n.ID)
		e.invalidate(n.ID)
	}
	if first.IsText() {
		first.SetDelta(first.Delta().Slice(0, a.Off).Concat(tail))
		e.invalidate(first.ID)
		e.anchor, e.focus = a, a
		return true
	}
	// The selection started on a block selected whole: replace it with an
	// empty paragraph holding what was after the selection.
	p := newText("paragraph", tail)
	_ = e.Content.Insert(doc.Position{After: first.ID}, p)
	e.Content.Remove(first.ID)
	e.anchor, e.focus = Pos{p.ID, 0}, Pos{p.ID, 0}
	return true
}

// liftChildren moves a block's children up to its place, before removing it.
func (e *Editor) liftChildren(n *doc.Node) {
	if len(n.Children) == 0 || n.Type == "table" {
		return
	}
	kids := n.Children
	n.Children = nil
	at := n.ID
	for _, k := range kids {
		_ = e.Content.Insert(doc.Position{After: at}, k)
		at = k.ID
	}
}

// InsertText types text at the caret, replacing the selection.
func (e *Editor) InsertText(s string) {
	if e.ReadOnly || s == "" {
		return
	}
	if strings.ContainsAny(s, "\n\r") {
		e.paste(s)
		return
	}
	if n := utf8.RuneCountInString(s); n > 1 && n <= 64 {
		// Text typed at once goes in as keystrokes would, so shortcuts and
		// menus still follow it.
		for _, r := range s {
			e.InsertText(string(r))
		}
		return
	}
	e.pushUndo(e.Collapsed())
	e.deleteSelection()
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	if !n.IsText() {
		// Typing on a block selected whole starts a paragraph after it.
		p := newText("paragraph", nil)
		_ = e.Content.Insert(doc.Position{After: n.ID}, p)
		n = p
		e.focus = Pos{p.ID, 0}
	}
	attrs := n.Delta().AttrsAt(e.focus.Off)
	if e.hasTyping {
		attrs = e.typing
	}
	if n.Type == "code" {
		// Code is typed as it is: no formats, shortcuts or menus.
		n.SetDelta(n.Delta().Insert(e.focus.Off, s, doc.Attrs{}))
		e.invalidate(n.ID)
		off := e.focus.Off + utf8.RuneCountInString(s)
		e.anchor, e.focus = Pos{n.ID, off}, Pos{n.ID, off}
		e.reveal = n.ID
		e.changed()
		return
	}
	n.SetDelta(n.Delta().Insert(e.focus.Off, s, attrs))
	e.invalidate(n.ID)
	off := e.focus.Off + utf8.RuneCountInString(s)
	e.anchor, e.focus = Pos{n.ID, off}, Pos{n.ID, off}
	if s == " " {
		e.markdownShortcut(n)
	}
	if s == "-" {
		e.dividerShortcut(n)
	}
	if r, _ := utf8.DecodeRuneInString(s); (r == '/' || r == '@') && utf8.RuneCountInString(s) == 1 {
		runes := []rune(n.Delta().Plain())
		at := off - 1
		if at == 0 || unicode.IsSpace(runes[at-1]) {
			e.trigger = &Trigger{Kind: r, ID: n.ID, Off: at}
			e.menuIndex = 0
		}
	}
	e.inlineShortcut(n)
	e.updateTrigger()
	e.reveal = n.ID
	e.changed()
}

// markdownShortcut turns "# ", "- ", "1. ", "[] " and "> " typed at the
// start of a paragraph into the block they stand for; dividerShortcut
// takes "---".
func (e *Editor) markdownShortcut(n *doc.Node) {
	if n.Type != "paragraph" && n.Type != "bulleted_list" {
		return
	}
	text := n.Delta().Plain()
	if utf8.RuneCountInString(text) < e.focus.Off {
		return
	}
	prefix := string([]rune(text)[:e.focus.Off])
	conv := map[string]func(){
		"# ":   func() { n.Type = "heading"; n.Set("level", 1) },
		"## ":  func() { n.Type = "heading"; n.Set("level", 2) },
		"### ": func() { n.Type = "heading"; n.Set("level", 3) },
		"- ":   func() { n.Type = "bulleted_list" },
		"* ":   func() { n.Type = "bulleted_list" },
		"1. ":  func() { n.Type = "numbered_list" },
		"[] ":  func() { n.Type = "todo_list"; n.Set("checked", false) },
		"[ ] ": func() { n.Type = "todo_list"; n.Set("checked", false) },
		"[x] ": func() { n.Type = "todo_list"; n.Set("checked", true) },
		"> ":   func() { n.Type = "quote" },
	}
	fn, ok := conv[prefix]
	if !ok || (n.Type == "bulleted_list" && prefix != "[] " && prefix != "[ ] " && prefix != "[x] ") {
		return
	}
	// Undo goes back to the text as typed.
	e.pushUndo(false)
	fn()
	n.SetDelta(n.Delta().Delete(0, e.focus.Off))
	e.invalidate(n.ID)
	e.anchor, e.focus = Pos{n.ID, 0}, Pos{n.ID, 0}
}

// dividerShortcut turns "---" typed alone in a paragraph into a divider
// before it, leaving the caret in the emptied paragraph to go on writing.
func (e *Editor) dividerShortcut(n *doc.Node) {
	if n.Type != "paragraph" || e.focus.Off != 3 || n.Delta().Plain() != "---" {
		return
	}
	// Undo goes back to the text as typed.
	e.pushUndo(false)
	_ = e.Content.Insert(doc.Position{Before: n.ID}, &doc.Node{ID: doc.NewBlockID(), Type: "divider"})
	n.SetDelta(nil)
	e.layouts = map[string]*blockLayout{}
	e.anchor, e.focus = Pos{n.ID, 0}, Pos{n.ID, 0}
}

// inlineShortcut turns **bold**, *italic*, `code` and ~~strike~~ typed in
// full into their formats.
func (e *Editor) inlineShortcut(n *doc.Node) {
	d := n.Delta()
	runes := []rune(d.Plain())
	end := e.focus.Off
	if end < 2 || end > len(runes) {
		return
	}
	try := func(marker string, set func(*doc.Attrs)) bool {
		m := []rune(marker)
		if end < 2*len(m)+1 || string(runes[end-len(m):end]) != marker {
			return false
		}
		closeAt := end - len(m)
		for start := closeAt - 1; start >= len(m)-1; start-- {
			openAt := start - len(m) + 1
			if string(runes[openAt:start+1]) != marker {
				continue
			}
			inner := runes[start+1 : closeAt]
			if len(inner) == 0 || unicode.IsSpace(inner[0]) || unicode.IsSpace(inner[len(inner)-1]) {
				return false
			}
			if len(m) == 1 && openAt > 0 && runes[openAt-1] == m[0] {
				return false
			}
			e.pushUndo(false)
			d = d.Delete(closeAt, end)
			d = d.Format(start+1, closeAt, set)
			d = d.Delete(openAt, start+1)
			n.SetDelta(d)
			e.invalidate(n.ID)
			off := closeAt - len(m)
			e.anchor, e.focus = Pos{n.ID, off}, Pos{n.ID, off}
			e.typing, e.hasTyping = d.AttrsAt(openAt), true
			e.typing.Bold, e.typing.Italic, e.typing.Code, e.typing.Strike = false, false, false, false
			return true
		}
		return false
	}
	_ = try("**", func(a *doc.Attrs) { a.Bold = true }) ||
		try("~~", func(a *doc.Attrs) { a.Strike = true }) ||
		try("`", func(a *doc.Attrs) { a.Code = true }) ||
		try("*", func(a *doc.Attrs) { a.Italic = true })
}

// continues is the type a new block after n takes on Enter.
func continues(n *doc.Node) string {
	switch n.Type {
	case "bulleted_list", "numbered_list", "todo_list", "toggle_list":
		return n.Type
	}
	return "paragraph"
}

// Enter splits the block at the caret.
func (e *Editor) Enter() {
	if e.ReadOnly {
		return
	}
	e.pushUndo(false)
	e.coalescing = false
	e.deleteSelection()
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	if !n.IsText() {
		p := newText("paragraph", nil)
		_ = e.Content.Insert(doc.Position{After: n.ID}, p)
		e.Select(Pos{p.ID, 0}, Pos{p.ID, 0})
		e.changed()
		return
	}
	if n.Type == "code" {
		e.codeEnter(n)
		return
	}
	if e.fenceEnter(n) {
		return
	}
	d := n.Delta()
	// Enter in an empty list item, quote or callout ends it.
	if d.Len() == 0 && n.Type != "paragraph" && n.Type != "heading" {
		if loc := e.Content.Locate(n.ID); loc != nil && loc.Parent != nil {
			e.outdent(n)
		} else {
			e.toParagraph(n)
		}
		e.invalidate(n.ID)
		e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
		e.changed()
		return
	}
	off := e.focus.Off
	if off == 0 && d.Len() > 0 {
		// At the start, a new empty block goes before.
		p := newText(continues(n), nil)
		if p.Type == "todo_list" {
			p.Set("checked", false)
		}
		_ = e.Content.Insert(doc.Position{Before: n.ID}, p)
		e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
		e.changed()
		return
	}
	head, tail := d.Slice(0, off), d.Slice(off, d.Len())
	if n.Type == "toggle_list" && !collapsed(n) {
		// In an open toggle, the new line is its first child.
		n.SetDelta(head)
		p := newText("paragraph", tail)
		n.Children = append([]*doc.Node{p}, n.Children...)
		e.invalidate(n.ID)
		e.Select(Pos{p.ID, 0}, Pos{p.ID, 0})
		e.changed()
		return
	}
	n.SetDelta(head)
	p := newText(continues(n), tail)
	if p.Type == "todo_list" {
		p.Set("checked", false)
	}
	if NestableTypes(n) && len(n.Children) > 0 && !collapsed(n) {
		// The new item comes first among the children.
		n.Children = append([]*doc.Node{p}, n.Children...)
	} else {
		// A new line after a folded heading starts its section, which
		// shows.
		if foldedHeading(n) {
			n.Set("collapsed", false)
		}
		_ = e.Content.Insert(doc.Position{After: n.ID}, p)
	}
	e.invalidate(n.ID)
	e.Select(Pos{p.ID, 0}, Pos{p.ID, 0})
	e.changed()
}

// NestableTypes reports whether a block takes children.
func NestableTypes(n *doc.Node) bool { return doc.NestableTypes[n.Type] }

// SoftBreak inserts a line break within the block.
func (e *Editor) SoftBreak() {
	if e.ReadOnly {
		return
	}
	e.pushUndo(false)
	e.deleteSelection()
	n := e.node(e.focus.ID)
	if n == nil || !n.IsText() {
		return
	}
	n.SetDelta(n.Delta().Insert(e.focus.Off, "\n", n.Delta().AttrsAt(e.focus.Off)))
	e.invalidate(n.ID)
	e.Select(Pos{n.ID, e.focus.Off + 1}, Pos{n.ID, e.focus.Off + 1})
	e.changed()
}

// Backspace deletes before the caret, or the selection.
func (e *Editor) Backspace(word bool) {
	if e.ReadOnly {
		return
	}
	if !e.Collapsed() {
		e.pushUndo(false)
		e.deleteSelection()
		e.Select(e.focus, e.focus)
		e.changed()
		return
	}
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	if !n.IsText() {
		e.removeBlock(n, true)
		return
	}
	off := e.focus.Off
	if off > 0 {
		e.pushUndo(true)
		from := off - 1
		if word {
			from = wordStart([]rune(n.Delta().Plain()), off)
		} else if l := e.layoutOf(n); l != nil {
			from = runeOff(n, l.layout.PreviousBoundary(utf16Off(n, off)))
		}
		n.SetDelta(n.Delta().Delete(from, off))
		e.invalidate(n.ID)
		e.Select(Pos{n.ID, from}, Pos{n.ID, from})
		e.changed()
		return
	}
	// At the start of a block.
	e.pushUndo(false)
	e.coalescing = false
	if n.Type != "paragraph" {
		if loc := e.Content.Locate(n.ID); loc != nil && loc.Parent != nil {
			e.outdent(n)
		} else {
			e.toParagraph(n)
		}
		e.invalidate(n.ID)
		e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
		e.changed()
		return
	}
	us := e.units()
	i := e.index(n.ID)
	if i <= 0 {
		return
	}
	prev := us[i-1]
	if !prev.IsText() {
		if n.Delta().Len() == 0 {
			e.liftChildren(n)
			e.Content.Remove(n.ID)
		}
		e.Select(Pos{prev.ID, 0}, Pos{prev.ID, 0})
		e.changed()
		return
	}
	at := textLen(prev)
	prev.SetDelta(joined(prev, n))
	e.liftChildren(n)
	e.Content.Remove(n.ID)
	e.invalidate(prev.ID)
	e.Select(Pos{prev.ID, at}, Pos{prev.ID, at})
	e.changed()
}

// Delete deletes after the caret, or the selection.
func (e *Editor) Delete(word bool) {
	if e.ReadOnly {
		return
	}
	if !e.Collapsed() {
		e.Backspace(false)
		return
	}
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	if !n.IsText() {
		e.removeBlock(n, false)
		return
	}
	off := e.focus.Off
	if off < textLen(n) {
		e.pushUndo(true)
		to := off + 1
		if word {
			to = wordEnd([]rune(n.Delta().Plain()), off)
		} else if l := e.layoutOf(n); l != nil {
			to = runeOff(n, l.layout.NextBoundary(utf16Off(n, off)))
		}
		n.SetDelta(n.Delta().Delete(off, to))
		e.invalidate(n.ID)
		e.changed()
		return
	}
	us := e.units()
	i := e.index(n.ID)
	if i+1 >= len(us) {
		return
	}
	next := us[i+1]
	e.pushUndo(false)
	if !next.IsText() {
		e.Select(Pos{next.ID, 0}, Pos{next.ID, 0})
		return
	}
	n.SetDelta(joined(n, next))
	e.liftChildren(next)
	e.Content.Remove(next.ID)
	e.invalidate(n.ID)
	e.changed()
}

// toParagraph turns a block into a paragraph, without the data of its
// kind; the children a paragraph cannot hold move up after it.
func (e *Editor) toParagraph(n *doc.Node) {
	n.Type = "paragraph"
	for _, k := range []string{"checked", "due", "level", "language", "tone", "collapsed"} {
		delete(n.Data, k)
	}
	e.liftChildren(n)
}

// joined is the text of a block with the next one's after it: plain when
// it is code.
func joined(n, next *doc.Node) doc.Delta {
	if n.Type == "code" {
		return doc.Delta{{Text: n.Delta().Plain() + next.Delta().Plain()}}.Compact()
	}
	return n.Delta().Concat(next.Delta())
}

// removeBlock deletes a block selected whole, and puts the caret beside it.
func (e *Editor) removeBlock(n *doc.Node, back bool) {
	e.pushUndo(false)
	us := e.units()
	i := e.index(n.ID)
	var target *doc.Node
	if back && i > 0 {
		target = us[i-1]
	} else if i+1 < len(us) {
		target = us[i+1]
	} else if i > 0 {
		target = us[i-1]
	}
	e.Content.Remove(n.ID)
	if target == nil {
		target = newText("paragraph", nil)
		e.Content.Children = append(e.Content.Children, target)
	}
	off := 0
	if back {
		off = textLen(target)
	}
	e.Select(Pos{target.ID, off}, Pos{target.ID, off})
	e.changed()
}

// Indent nests the focus block under the block before it, when both are
// list items or the block before takes children.
func (e *Editor) Indent() {
	if e.ReadOnly {
		return
	}
	n := e.node(e.focus.ID)
	if n == nil || !n.IsText() {
		return
	}
	loc := e.Content.Locate(n.ID)
	if loc == nil || loc.Index == 0 {
		return
	}
	// List items nest in list items; any text nests in a toggle.
	prev := loc.Siblings[loc.Index-1]
	if !NestableTypes(prev) || !NestableTypes(n) && prev.Type != "toggle_list" {
		return
	}
	if collapsed(prev) {
		prev.Set("collapsed", false)
		e.invalidate(prev.ID)
	}
	e.pushUndo(false)
	_ = e.Content.Move(n.ID, doc.Position{Parent: prev.ID})
	e.Select(e.anchor, e.focus)
	e.changed()
}

// Outdent moves the focus block out of its parent, after it.
func (e *Editor) Outdent() {
	if e.ReadOnly {
		return
	}
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	if loc := e.Content.Locate(n.ID); loc == nil || loc.Parent == nil {
		return
	}
	e.pushUndo(false)
	e.outdent(n)
	e.Select(e.anchor, e.focus)
	e.changed()
}

func (e *Editor) outdent(n *doc.Node) {
	loc := e.Content.Locate(n.ID)
	if loc == nil || loc.Parent == nil {
		return
	}
	// Siblings after it become its children, so the order reads the same.
	after := append([]*doc.Node{}, loc.Siblings[loc.Index+1:]...)
	for _, s := range after {
		e.Content.Remove(s.ID)
	}
	parent := loc.Parent.ID
	_ = e.Content.Move(n.ID, doc.Position{After: parent})
	n.Children = append(n.Children, after...)
}

// SetType turns the focus block (or the selected blocks) into another
// kind of text block.
func (e *Editor) SetType(typ string, level int) {
	if e.ReadOnly {
		return
	}
	e.pushUndo(false)
	a, b := e.ordered()
	us := e.units()
	for i := e.index(a.ID); i >= 0 && i <= e.index(b.ID) && i < len(us); i++ {
		n := us[i]
		if !n.IsText() {
			continue
		}
		n.Type = typ
		for _, k := range []string{"checked", "due", "level", "language", "tone", "collapsed"} {
			delete(n.Data, k)
		}
		switch typ {
		case "heading":
			n.Set("level", level)
		case "todo_list":
			n.Set("checked", false)
		case "code":
			n.SetDelta(doc.Delta{{Text: n.Delta().Plain()}}.Compact())
			n.Set("language", "")
		case "callout":
			n.Set("tone", "info")
		case "toggle_list":
			n.Set("collapsed", false)
		}
		if !NestableTypes(n) && len(n.Children) > 0 {
			e.liftChildren(n)
		}
		e.invalidate(n.ID)
	}
	e.changed()
}

// ToggleChecked checks or unchecks a to-do.
func (e *Editor) ToggleChecked(id string) {
	n := e.node(id)
	if n == nil || n.Type != "todo_list" || e.ReadOnly {
		return
	}
	e.pushUndo(false)
	n.Set("checked", !n.Bool("checked"))
	e.invalidate(id)
	e.changed()
}

// InsertBlocks puts blocks after the focus block, or in its place when it
// is an empty paragraph, and selects the last.
func (e *Editor) InsertBlocks(nodes ...*doc.Node) {
	if len(nodes) == 0 || e.ReadOnly {
		return
	}
	e.pushUndo(false)
	n := e.node(e.focus.ID)
	pos := doc.Position{At: "end"}
	replace := false
	if n != nil {
		pos = doc.Position{After: n.ID}
		replace = n.Type == "paragraph" && textLen(n) == 0
	}
	_ = e.Content.Insert(pos, nodes...)
	if replace {
		e.Content.Remove(n.ID)
	}
	last := nodes[len(nodes)-1]
	if !last.IsText() {
		// Leave somewhere to type after a block selected whole at the end
		// of the page; elsewhere, typing on it starts a paragraph.
		us := e.units()
		if i := e.index(last.ID); i+1 >= len(us) {
			p := newText("paragraph", nil)
			_ = e.Content.Insert(doc.Position{After: last.ID}, p)
			last = p
		}
	}
	e.layouts = map[string]*blockLayout{}
	e.Select(Pos{last.ID, textLen(last)}, Pos{last.ID, textLen(last)})
	e.changed()
}

// Format toggles an inline format over the selection, or for typing at a
// collapsed caret.
func (e *Editor) Format(has func(doc.Attrs) bool, set func(*doc.Attrs, bool)) {
	if e.ReadOnly {
		return
	}
	if e.Collapsed() {
		n := e.node(e.focus.ID)
		if n == nil || !n.IsText() || n.Type == "code" {
			return
		}
		if !e.hasTyping {
			e.typing, e.hasTyping = n.Delta().AttrsAt(e.focus.Off), true
		}
		set(&e.typing, !has(e.typing))
		return
	}
	e.pushUndo(false)
	a, b := e.ordered()
	us := e.units()
	ia, ib := e.index(a.ID), e.index(b.ID)
	all := true
	each := func(fn func(n *doc.Node, from, to int)) {
		for i := ia; i <= ib; i++ {
			n := us[i]
			if !n.IsText() || n.Type == "code" {
				continue
			}
			from, to := 0, textLen(n)
			if i == ia {
				from = a.Off
			}
			if i == ib {
				to = b.Off
			}
			if from < to {
				fn(n, from, to)
			}
		}
	}
	each(func(n *doc.Node, from, to int) { all = all && n.Delta().All(from, to, has) })
	each(func(n *doc.Node, from, to int) {
		n.SetDelta(n.Delta().Format(from, to, func(at *doc.Attrs) { set(at, !all) }))
		e.invalidate(n.ID)
	})
	e.changed()
}

// Has reports whether the whole selection has a format.
func (e *Editor) Has(has func(doc.Attrs) bool) bool {
	n := e.node(e.focus.ID)
	if n == nil || !n.IsText() {
		return false
	}
	if e.Collapsed() {
		if e.hasTyping {
			return has(e.typing)
		}
		return has(n.Delta().AttrsAt(e.focus.Off))
	}
	a, b := e.ordered()
	if a.ID != b.ID {
		return false
	}
	return n.Delta().All(a.Off, b.Off, has)
}

// ToggleBold, ToggleItalic and the others toggle one format.
func (e *Editor) ToggleBold() {
	e.Format(func(a doc.Attrs) bool { return a.Bold }, func(a *doc.Attrs, on bool) { a.Bold = on })
}
func (e *Editor) ToggleItalic() {
	e.Format(func(a doc.Attrs) bool { return a.Italic }, func(a *doc.Attrs, on bool) { a.Italic = on })
}
func (e *Editor) ToggleUnderline() {
	e.Format(func(a doc.Attrs) bool { return a.Under }, func(a *doc.Attrs, on bool) { a.Under = on })
}
func (e *Editor) ToggleStrike() {
	e.Format(func(a doc.Attrs) bool { return a.Strike }, func(a *doc.Attrs, on bool) { a.Strike = on })
}
func (e *Editor) ToggleCode() {
	e.Format(func(a doc.Attrs) bool { return a.Code }, func(a *doc.Attrs, on bool) { a.Code = on })
}

// SetLink links the selection to url, or unlinks it when url is empty.
func (e *Editor) SetLink(url string) {
	if e.Collapsed() {
		return
	}
	e.Format(func(a doc.Attrs) bool { return a.Href == url }, func(a *doc.Attrs, _ bool) { a.Href = url })
}

// LinkAt returns the link under the selection's start.
func (e *Editor) LinkAt() string {
	a, _ := e.ordered()
	n := e.node(a.ID)
	if n == nil || !n.IsText() {
		return ""
	}
	at := 0
	for _, r := range n.Delta() {
		l := utf8.RuneCountInString(r.Text)
		if a.Off >= at && a.Off < at+l || a.Off == at+l && e.Collapsed() {
			if r.Attrs.Href != "" {
				return r.Attrs.Href
			}
		}
		at += l
	}
	return ""
}

// InsertMention puts a mention in place of an @ trigger.
func (e *Editor) InsertMention(t Trigger, m doc.Mention) {
	n := e.node(t.ID)
	if n == nil {
		return
	}
	e.pushUndo(false)
	end := min(t.Off+1+utf8.RuneCountInString(t.Query), textLen(n))
	d := n.Delta().Delete(t.Off, end)
	d = d.Insert(t.Off, "@"+m.Name, doc.Attrs{Mention: &m})
	at := t.Off + 1 + utf8.RuneCountInString(m.Name)
	d = d.Insert(at, " ", doc.Attrs{})
	n.SetDelta(d)
	e.invalidate(n.ID)
	e.trigger = nil
	e.Select(Pos{n.ID, at + 1}, Pos{n.ID, at + 1})
	e.changed()
}

// InsertPageMention puts a mention of page id, titled title, in place of
// the @ menu's trigger: the page's title linked to it, then a space.
func (e *Editor) InsertPageMention(t Trigger, id, title string) {
	n := e.node(t.ID)
	if n == nil {
		return
	}
	e.pushUndo(false)
	end := min(t.Off+1+utf8.RuneCountInString(t.Query), textLen(n))
	d := n.Delta().Delete(t.Off, end)
	d = d.Insert(t.Off, title, doc.PageMention(id, title))
	at := t.Off + utf8.RuneCountInString(title)
	d = d.Insert(at, " ", doc.Attrs{})
	n.SetDelta(d)
	e.invalidate(n.ID)
	e.trigger = nil
	e.Select(Pos{n.ID, at + 1}, Pos{n.ID, at + 1})
	e.changed()
}

// SelectAll selects the focus block's text, then the whole page.
func (e *Editor) SelectAll() {
	n := e.node(e.focus.ID)
	a, b := e.ordered()
	if n != nil && n.IsText() && a.ID == b.ID && (a.Off != 0 || b.Off != textLen(n)) {
		e.Select(Pos{n.ID, 0}, Pos{n.ID, textLen(n)})
		return
	}
	us := e.units()
	first, last := us[0], us[len(us)-1]
	e.Select(Pos{first.ID, 0}, Pos{last.ID, textLen(last)})
}

func wordStart(rs []rune, off int) int {
	i := off
	for i > 0 && unicode.IsSpace(rs[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(rs[i-1]) && !unicode.IsPunct(rs[i-1]) {
		i--
	}
	if i == off && i > 0 {
		i--
	}
	return i
}

func wordEnd(rs []rune, off int) int {
	i := off
	for i < len(rs) && unicode.IsSpace(rs[i]) {
		i++
	}
	for i < len(rs) && !unicode.IsSpace(rs[i]) && !unicode.IsPunct(rs[i]) {
		i++
	}
	if i == off && i < len(rs) {
		i++
	}
	return i
}

// wordAt returns the word around an offset.
func wordAt(rs []rune, off int) (int, int) {
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	a, b := off, off
	for a > 0 && isWord(rs[a-1]) {
		a--
	}
	for b < len(rs) && isWord(rs[b]) {
		b++
	}
	if a == b && b < len(rs) {
		b++
	}
	return a, b
}
