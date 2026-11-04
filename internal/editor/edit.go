package editor

import (
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-text/typesetting/segmenter"

	"leaflet/internal/doc"
	"leaflet/internal/rules"
	"leaflet/internal/shortcut"
)

func newText(typ string, d doc.Delta) *doc.Node {
	n := &doc.Node{ID: doc.NewBlockID(), Type: typ}
	n.SetDelta(d)
	return n
}

// deleteSelection removes what is selected and leaves a caret at its
// start.
func (e *Editor) deleteSelection() {
	if e.Collapsed() {
		return
	}
	a, b := e.ordered()
	if a.ID == b.ID {
		n := e.node(a.ID)
		if n.IsText() {
			n.SetDelta(n.Delta().Delete(a.Off, b.Off))
			e.invalidate(n.ID)
		}
		e.anchor, e.focus = a, a
		return
	}
	// A folded heading's section goes with it, unless the heading stays.
	us := e.selectedUnits(a)
	ia, ib := indexIn(us, a.ID), e.selectedEnd(us, b)
	if ia < 0 || ib < 0 {
		return
	}
	// What shows after the selection still does once it is gone.
	defer e.keepShown(e.unitAfter(b.ID))
	first, last := us[ia], us[ib]
	var tail doc.Delta
	if last.IsText() && last.ID == b.ID {
		tail = last.Delta().Slice(b.Off, textLen(last))
	}
	// Remove everything after the first block, its children outside the
	// selection moving up to its place, but a folded toggle's from between
	// the first block and the last, which go with it. One walk of the page
	// takes them all out: removed one at a time, each would look for its
	// place in the page.
	gone := make(map[string]bool, ib-ia)
	for _, n := range us[ia+1 : ib+1] {
		gone[n.ID] = true
		e.invalidate(n.ID)
	}
	var without func([]*doc.Node) []*doc.Node
	without = func(nodes []*doc.Node) []*doc.Node {
		var out []*doc.Node
		for _, n := range nodes {
			lift := n.Type != "table" && (!collapsed(n) || n == last)
			if n.Type != "table" && (!gone[n.ID] || lift) {
				n.Children = without(n.Children)
			}
			switch {
			case !gone[n.ID]:
				out = append(out, n)
			case lift:
				out = append(out, n.Children...)
				n.Children = nil
			}
		}
		return out
	}
	e.Content.Children = without(e.Content.Children)
	if first.IsText() {
		if first.Type == "code" {
			// Code holds plain text: what joins it loses its formats.
			tail = doc.Delta{{Text: tail.Plain()}}.Compact()
		}
		first.SetDelta(first.Delta().Slice(0, a.Off).Concat(tail))
		e.invalidate(first.ID)
		e.anchor, e.focus = a, a
		return
	}
	// The selection started on a block selected whole: replace it with an
	// empty paragraph holding what was after the selection.
	p := newText("paragraph", tail)
	_ = e.Content.Insert(doc.Position{After: first.ID}, p)
	e.Content.Remove(first.ID)
	e.anchor, e.focus = Pos{p.ID, 0}, Pos{p.ID, 0}
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
		// menus still follow it. In place of a selection, as an input
		// method's commit or autocorrect puts it, it is one step: the
		// selection goes as the step begins, and the keystrokes join it.
		if !e.Collapsed() {
			e.pushUndo(false)
			e.deleteSelection()
			e.coalescing, e.coalesceIn = true, e.focus.ID
		}
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
		// Typing on a block selected whole starts a paragraph after it,
		// where the typing that follows joins this step.
		p := newText("paragraph", nil)
		_ = e.Content.Insert(doc.Position{After: n.ID}, p)
		n = p
		e.focus = Pos{p.ID, 0}
		e.coalesceIn = p.ID
	}
	attrs := n.Delta().AttrsAt(e.focus.Off)
	if e.hasTyping {
		attrs = e.typing
	}
	if n.Type == "code" {
		// Code is typed as it is: no formats, shortcuts or menus.
		attrs = doc.Attrs{}
	}
	n.SetDelta(n.Delta().Insert(e.focus.Off, s, attrs))
	e.invalidate(n.ID)
	off := e.focus.Off + utf8.RuneCountInString(s)
	e.anchor, e.focus = Pos{n.ID, off}, Pos{n.ID, off}
	e.typed++
	r, _ := utf8.DecodeRuneInString(s)
	if endsWord(n, off, r) {
		// The step of undo goes on to the end of the word, the space too:
		// the next word is a step of its own.
		e.coalescing = false
	}
	if n.Type == "code" {
		e.reveal = n.ID
		e.changed()
		return
	}
	// An input method for Chinese or Japanese types a full-width space.
	if s == " " || s == "\u3000" {
		e.markdownShortcut(n)
	}
	e.dividerShortcut(n)
	if shortcut.MenuKind(r) != 0 {
		before := rune(0)
		if at := off - 1; at > 0 {
			before = []rune(n.Delta().Plain())[at-1]
		}
		if kind := shortcut.Menu(r, before); kind != 0 {
			e.trigger = &Trigger{Kind: kind, ID: n.ID, Off: off - 1}
			e.menuIndex = 0
		}
	}
	e.inlineShortcut(n)
	e.updateTrigger()
	e.reveal = n.ID
	e.changed()
}

// endsWord reports whether r, typed in block n before off, ends a word:
// a space after one, or the punctuation that ends a clause in Chinese or
// Japanese, which put no spaces between words.
func endsWord(n *doc.Node, off int, r rune) bool {
	if r > unicode.MaxASCII && unicode.IsPunct(r) {
		return true
	}
	if !unicode.IsSpace(r) || off < 2 {
		return false
	}
	rs := []rune(n.Delta().Plain())
	return off <= len(rs) && !unicode.IsSpace(rs[off-2])
}

// markdownShortcut turns "# ", "- ", "1. ", "[] ", "> ", "``` " and the
// like typed at the start of a paragraph into the block they stand for
// (internal/shortcut); dividerShortcut takes "---".
func (e *Editor) markdownShortcut(n *doc.Node) {
	text := n.Delta().Plain()
	if utf8.RuneCountInString(text) < e.focus.Off {
		return
	}
	typ, data, ok := shortcut.Block(n.Type, string([]rune(text)[:e.focus.Off]))
	if !ok {
		return
	}
	// Undo goes back to the text as typed.
	e.pushUndo(false)
	n.Type = typ
	for _, k := range doc.TypedKeys {
		delete(n.Data, k)
	}
	for k, v := range data {
		n.Set(k, v)
	}
	d := n.Delta().Delete(0, e.focus.Off)
	if typ == "code" {
		d = doc.Delta{{Text: d.Plain()}}.Compact()
	}
	n.SetDelta(d)
	if typ == "code" {
		e.roomAfterCode(n)
	}
	e.invalidate(n.ID)
	e.anchor, e.focus = Pos{n.ID, 0}, Pos{n.ID, 0}
}

// dividerShortcut turns "---" typed alone in a paragraph, full-width too,
// into a divider before it, leaving the caret in the emptied paragraph to
// go on writing.
func (e *Editor) dividerShortcut(n *doc.Node) {
	if e.focus.Off != 3 || !shortcut.Divider(n.Type, n.Delta().Plain()) {
		return
	}
	// Undo goes back to the text as typed.
	e.pushUndo(false)
	_ = e.Content.Insert(doc.Position{Before: n.ID}, &doc.Node{ID: doc.NewBlockID(), Type: "divider"})
	n.SetDelta(nil)
	e.anchor, e.focus = Pos{n.ID, 0}, Pos{n.ID, 0}
}

// inlineShortcut turns **bold**, *italic*, `code` and ~~strike~~ typed in
// full into their formats.
func (e *Editor) inlineShortcut(n *doc.Node) {
	d := n.Delta()
	// A mention is one unit, taken out or formatted whole.
	mentioned := func(from, to int) bool {
		for _, r := range d.Slice(from, to) {
			if r.Attrs.Mention != nil {
				return true
			}
		}
		return false
	}
	m, ok := shortcut.Inline([]rune(d.Plain()), e.focus.Off, mentioned)
	if !ok {
		return
	}
	set := map[string]func(*doc.Attrs){
		"bold":   func(a *doc.Attrs) { a.Bold = true },
		"strike": func(a *doc.Attrs) { a.Strike = true },
		"code":   func(a *doc.Attrs) { a.Code = true },
		"italic": func(a *doc.Attrs) { a.Italic = true },
	}[m.Format]
	e.pushUndo(false)
	d = d.Delete(m.Close, m.End)
	d = d.Format(m.Inner, m.Close, set)
	d = d.Delete(m.Open, m.Inner)
	n.SetDelta(d)
	e.invalidate(n.ID)
	off := m.Close - (m.Inner - m.Open)
	e.anchor, e.focus = Pos{n.ID, off}, Pos{n.ID, off}
	e.typing, e.hasTyping = d.AttrsAt(m.Open), true
	e.typing.Bold, e.typing.Italic, e.typing.Code, e.typing.Strike = false, false, false, false
}

// ruleBlock is block n as the editing rules know it.
func (e *Editor) ruleBlock(n *doc.Node) rules.Block {
	b := rules.Block{
		Type:      n.Type,
		Collapsed: (n.Type == "toggle_list" || n.Type == "heading") && n.Bool("collapsed"),
		Empty:     n.IsText() && n.Delta().Len() == 0,
		Holds:     len(n.Children) > 0,
	}
	if loc := e.Content.Locate(n.ID); loc != nil && loc.Parent != nil && loc.Parent.Type != "table/cell" {
		b.Nested, b.Last = true, loc.Index == len(loc.Siblings)-1
	}
	return b
}

// newLine is the new block Enter makes, as line says, with text d.
func newLine(line rules.Line, d doc.Delta) *doc.Node {
	p := newText(line.Type, d)
	for k, v := range line.Data {
		p.Set(k, v)
	}
	return p
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
	// A paragraph of a Markdown fence becomes its block first.
	if e.fenceEnter(n) {
		return
	}
	d := n.Delta()
	off := e.focus.Off
	line := rules.Enter(e.ruleBlock(n), off == 0)
	switch line.Action {
	case rules.LineAfter:
		p := newLine(line, nil)
		_ = e.Content.Insert(doc.Position{After: n.ID}, p)
		e.Select(Pos{p.ID, 0}, Pos{p.ID, 0})
		e.changed()
		return
	case rules.CodeLine:
		e.codeEnter(n)
		return
	case rules.Outdent, rules.Plain:
		e.unstyle(n, line.Action)
		e.invalidate(n.ID)
		e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
		e.changed()
		return
	case rules.LineBefore:
		p := newLine(line, nil)
		_ = e.Content.Insert(doc.Position{Before: n.ID}, p)
		// Before a heading that ends a folded section, the new line
		// would join it, out of sight.
		e.keepShown(p.ID)
		e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
		e.changed()
		return
	}
	n.SetDelta(d.Slice(0, off))
	p := newLine(line, d.Slice(off, d.Len()))
	if line.Action == rules.SplitInside {
		n.Children = append([]*doc.Node{p}, n.Children...)
	} else {
		if line.Unfold {
			n.Set("collapsed", false)
		}
		_ = e.Content.Insert(doc.Position{After: n.ID}, p)
	}
	e.invalidate(n.ID)
	e.Select(Pos{p.ID, 0}, Pos{p.ID, 0})
	e.changed()
}

// nestable reports whether a block takes children.
func nestable(n *doc.Node) bool { return doc.NestableTypes[n.Type] }

// SoftBreak inserts a line break within the block; on a block selected
// whole, which has no line to break, it does nothing.
func (e *Editor) SoftBreak() {
	if e.ReadOnly || e.blockSelected() {
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
		var from int
		if word {
			from = wordStart([]rune(n.Delta().Plain()), off)
		} else {
			from = e.prevStop(n, off)
		}
		// A mention goes whole.
		from, _ = n.Delta().Widen(from, off)
		n.SetDelta(n.Delta().Delete(from, off))
		e.invalidate(n.ID)
		e.setSelection(Pos{n.ID, from}, Pos{n.ID, from})
		e.changed()
		return
	}
	// At the start of a block. What only moves the caret, or does
	// nothing, is no step of undo. A paragraph last in a toggle goes out
	// of it, as a nested list item goes up a level.
	e.coalescing = false
	us := e.units()
	i := e.index(n.ID)
	var prev *doc.Node
	var before rules.Block
	if i > 0 {
		prev = us[i-1]
		before = rules.Block{Type: prev.Type}
	}
	switch action := rules.Backspace(e.ruleBlock(n), before, prev != nil); action {
	case rules.Outdent, rules.Plain:
		e.pushUndo(false)
		e.unstyle(n, action)
		e.invalidate(n.ID)
		e.Select(Pos{n.ID, 0}, Pos{n.ID, 0})
		e.changed()
		return
	case rules.Nothing:
		return
	case rules.SelectBefore:
		e.Select(Pos{prev.ID, 0}, Pos{prev.ID, 0})
		e.services.Invalidate()
		return
	case rules.Remove:
		e.pushUndo(false)
		e.liftChildren(n)
		e.Content.Remove(n.ID)
		e.Select(Pos{prev.ID, 0}, Pos{prev.ID, 0})
		e.changed()
		return
	}
	e.pushUndo(false)
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
		var to int
		if word {
			to = wordEnd([]rune(n.Delta().Plain()), off)
		} else {
			to = e.nextStop(n, off)
		}
		// A mention goes whole, as the caret is never inside one.
		n.SetDelta(n.Delta().Delete(off, to))
		e.invalidate(n.ID)
		e.changed()
		return
	}
	us := e.units()
	i := e.index(n.ID)
	if i < 0 || i+1 >= len(us) {
		return
	}
	next := us[i+1]
	if rules.Delete(rules.Block{Type: n.Type}, rules.Block{Type: next.Type}) == rules.SelectAfter {
		// Onto a block selected whole: no step of undo.
		e.BreakUndo()
		e.Select(Pos{next.ID, 0}, Pos{next.ID, 0})
		e.services.Invalidate()
		return
	}
	e.pushUndo(false)
	after := e.unitAfter(next.ID)
	n.SetDelta(joined(n, next))
	e.liftChildren(next)
	e.Content.Remove(next.ID)
	e.invalidate(n.ID)
	e.keepShown(after)
	e.changed()
}

// unitAfter is the unit shown after block id, or "" for none.
func (e *Editor) unitAfter(id string) string {
	us := e.units()
	if i := indexIn(us, id); i >= 0 && i+1 < len(us) {
		return us[i+1].ID
	}
	return ""
}

// keepShown unfolds the headings that an edit made fold block id away:
// as a heading that ended a folded section goes, or becomes another kind
// of block, what was after it would join that section, out of sight.
func (e *Editor) keepShown(id string) {
	if id != "" && e.node(id) != nil && len(e.unfold(id)) > 0 {
		e.order = nil
	}
}

// unstyle ends block n as action, rules.Unstyle's, says, as Backspace at
// its start and Enter in it empty do: it goes up a level (Outdent), or
// becomes a paragraph where it is (Plain).
func (e *Editor) unstyle(n *doc.Node, action rules.Action) {
	if action == rules.Outdent {
		e.outdent(n)
	} else {
		e.toParagraph(n)
	}
}

// toParagraph turns a block into a paragraph, without the data of its
// kind; the children a paragraph cannot hold move up after it.
func (e *Editor) toParagraph(n *doc.Node) {
	n.Type = "paragraph"
	for _, k := range doc.TypedKeys {
		delete(n.Data, k)
	}
	e.liftChildren(n)
	// A heading after a folded one ended its section, which would now
	// take the block in.
	e.keepShown(n.ID)
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

// Indent nests the focus block, or each block selected with what is in
// it, under the block before it, when both are list items or the block
// before takes children. The blocks go in together: none does when the
// first can't.
func (e *Editor) Indent() {
	if e.ReadOnly {
		return
	}
	nodes := e.topSelected()
	hid := e.hidden()
	if len(nodes) == 0 || e.indentUnder(nodes[0], hid) == nil {
		return
	}
	// The step keeps the toggles folded, so undoing it folds them again.
	e.pushUndo(false)
	for _, n := range nodes {
		prev := e.indentUnder(n, hid)
		if prev == nil {
			continue
		}
		if collapsed(prev) {
			prev.Set("collapsed", false)
			e.invalidate(prev.ID)
		}
		_ = e.Content.Move(n.ID, doc.Position{Parent: prev.ID})
	}
	e.Select(e.anchor, e.focus)
	e.changed()
}

// indentUnder is the block n would go in as it is indented, or nil when it
// can't: list items nest in list items, and any text in a toggle, but not
// in a block a folded heading hides (hid), where it would be hidden too.
func (e *Editor) indentUnder(n *doc.Node, hid map[string]bool) *doc.Node {
	if n == nil {
		return nil
	}
	loc := e.Content.Locate(n.ID)
	if loc == nil || loc.Index == 0 {
		return nil
	}
	prev := loc.Siblings[loc.Index-1]
	if hid[prev.ID] || !rules.Nests(n.Type, prev.Type) {
		return nil
	}
	return prev
}

// Outdent moves the focus block, or each block selected with what is in
// it, out of its parent, after it.
func (e *Editor) Outdent() {
	if e.ReadOnly {
		return
	}
	var nodes []*doc.Node
	for _, n := range e.topSelected() {
		if loc := e.Content.Locate(n.ID); loc != nil && loc.Parent != nil {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		return
	}
	e.pushUndo(false)
	// In order: each takes those after it as its children, which then
	// come out after it in turn.
	for _, n := range nodes {
		e.outdent(n)
	}
	e.Select(e.anchor, e.focus)
	e.changed()
}

// topSelected are the blocks selected that no block selected holds, in
// order, as a command on the selection moves them, with what is in them:
// the focus block alone for a caret.
func (e *Editor) topSelected() []*doc.Node {
	in := map[string]bool{}
	for _, n := range e.selectedNodes() {
		in[n.ID] = true
	}
	var out []*doc.Node
	var visit func([]*doc.Node)
	visit = func(nodes []*doc.Node) {
		for _, n := range nodes {
			if in[n.ID] {
				out = append(out, n)
				continue
			}
			visit(n.Children)
		}
	}
	visit(e.Content.Children)
	return out
}

func (e *Editor) outdent(n *doc.Node) {
	loc := e.Content.Locate(n.ID)
	if loc == nil || loc.Parent == nil {
		return
	}
	// Siblings after a list item become its children, so the order reads
	// the same. Other blocks take none: the siblings stay in the parent,
	// and the block goes after it.
	var after []*doc.Node
	if nestable(n) {
		after = append(after, loc.Siblings[loc.Index+1:]...)
	}
	for _, s := range after {
		e.Content.Remove(s.ID)
	}
	parent := loc.Parent.ID
	_ = e.Content.Move(n.ID, doc.Position{After: parent})
	n.Children = append(n.Children, after...)
	// A folded toggle unfolds to take them, as indenting under one does,
	// or they would be hidden in it.
	if len(after) > 0 && collapsed(n) {
		n.Set("collapsed", false)
		e.invalidate(n.ID)
	}
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
	var last *doc.Node
	// The blocks as they showed: a heading changed can unfold a section.
	ia, ib := indexIn(us, a.ID), indexIn(us, b.ID)
	for i := ia; i >= 0 && i <= ib && i < len(us); i++ {
		n := us[i]
		if !n.IsText() {
			continue
		}
		last = n
		n.Type = typ
		for _, k := range doc.TypedKeys {
			delete(n.Data, k)
		}
		for k, v := range doc.TypeData(typ, level) {
			n.Set(k, v)
		}
		if typ == "code" {
			n.SetDelta(doc.Delta{{Text: n.Delta().Plain()}}.Compact())
		}
		if !nestable(n) && len(n.Children) > 0 {
			e.liftChildren(n)
		}
		e.invalidate(n.ID)
		e.keepShown(n.ID)
	}
	if typ == "code" && last != nil {
		e.roomAfterCode(last)
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

// checkOrFold is Cmd-Enter: it checks the to-dos selected, or unchecks
// them when all are, as one step of undo, or else folds the toggle or the
// heading at the caret, or unfolds it. It reports whether there was one.
func (e *Editor) checkOrFold() bool {
	var todos []*doc.Node
	e.indexed(func() {
		for _, n := range e.selectedNodes() {
			if n.Type == "todo_list" {
				todos = append(todos, n)
			}
		}
	})
	if len(todos) > 0 {
		if e.ReadOnly {
			return true
		}
		all := true
		for _, n := range todos {
			all = all && n.Bool("checked")
		}
		e.pushUndo(false)
		for _, n := range todos {
			n.Set("checked", !all)
			e.invalidate(n.ID)
		}
		e.changed()
		return true
	}
	switch n := e.node(e.focus.ID); {
	case n == nil:
		return false
	case n.Type == "toggle_list":
		e.ToggleCollapsed(n.ID)
	case n.Type == "heading":
		e.ToggleFold(n.ID)
	default:
		return false
	}
	return true
}

// InsertBlocks puts blocks after the focus block, or in its place when it
// is an empty paragraph, and selects the last.
func (e *Editor) InsertBlocks(nodes ...*doc.Node) {
	if len(nodes) == 0 || e.ReadOnly {
		return
	}
	e.pushUndo(false)
	e.insertBlocks(nodes...)
}

// insertBlocks is InsertBlocks in the step of undo open, as a paste's.
func (e *Editor) insertBlocks(nodes ...*doc.Node) {
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
	// After a folded heading, the blocks would go in its section, out of
	// sight: it shows, as for Enter's new line.
	e.keepShown(nodes[0].ID)
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
	e.Select(Pos{last.ID, textLen(last)}, Pos{last.ID, textLen(last)})
	e.changed()
}

// shareToVideo shows block id, a video's share link pasted on a line of
// its own, as the video the share led to, as that video's link pasted
// there shows: unless the line has changed since, or the share led to no
// video. It is a step of its own to undo, which takes it back to the link.
func (e *Editor) shareToVideo(id, link, video string) {
	n := e.node(id)
	if n == nil || e.ReadOnly || n.Type != "paragraph" || len(n.Children) > 0 ||
		strings.TrimSpace(n.Delta().Plain()) != link ||
		slices.ContainsFunc(n.Delta(), func(r doc.Run) bool { return r.Attrs.Href != link && strings.TrimSpace(r.Text) != "" }) {
		return
	}
	v, err := doc.VideoNode(video, "")
	if err != nil {
		return
	}
	held := e.anchor.ID == id || e.focus.ID == id
	e.pushUndo(false)
	e.coalescing = false
	_ = e.Content.Insert(doc.Position{After: id}, v)
	e.Content.Remove(id)
	e.keepShown(v.ID)
	// As insertBlocks leaves it: somewhere to type after it at the end of
	// the page, where the caret goes.
	last := v
	if us := e.units(); e.index(v.ID)+1 >= len(us) {
		last = newText("paragraph", nil)
		_ = e.Content.Insert(doc.Position{After: v.ID}, last)
	}
	if held {
		e.Select(Pos{last.ID, textLen(last)}, Pos{last.ID, textLen(last)})
	}
	e.changed()
}

// LinkToMedia shows the video or picture the link at p points to as its
// block: in place of the link's line when nothing else on it is words, as
// a clipping's "🎬 Video" line, and after the line otherwise.
func (e *Editor) LinkToMedia(p Pos) {
	n, href := e.node(p.ID), e.linkAtPos(p)
	if n == nil || href == "" || e.ReadOnly {
		return
	}
	var m *doc.Node
	if _, ok := doc.ParseVideoURL(href); ok {
		m, _ = doc.VideoNode(href, "")
	} else if doc.ImageLink(href) {
		m, _ = doc.ImageNode(href)
	}
	if m == nil {
		return
	}
	// The link's text is its runs around p.
	from, to, at := 0, 0, 0
	for _, r := range n.Delta() {
		l := utf8.RuneCountInString(r.Text)
		if r.Attrs.Href != href {
			if to > p.Off {
				break
			}
			from = at + l
		}
		at += l
		if r.Attrs.Href == href {
			to = at
		}
	}
	text := []rune(n.Delta().Plain())
	if label := strings.TrimSpace(string(text[from:to])); m.Type == "image" && label != href {
		m.Set("alt", label)
	}
	rest := string(text[:from]) + string(text[to:])
	e.pushUndo(false)
	_ = e.Content.Insert(doc.Position{After: n.ID}, m)
	e.keepShown(m.ID)
	if !strings.ContainsFunc(rest, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) && len(n.Children) == 0 {
		e.Content.Remove(n.ID)
	}
	if us := e.units(); e.index(m.ID)+1 >= len(us) {
		// Leave somewhere to type after it at the end of the page.
		_ = e.Content.Insert(doc.Position{After: m.ID}, newText("paragraph", nil))
	}
	e.Select(Pos{m.ID, 0}, Pos{m.ID, 0})
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
	a, b := e.ordered()
	if e.index(a.ID) < 0 || e.index(b.ID) < 0 {
		return
	}
	e.pushUndo(false)
	all := true
	e.eachSelected(func(n *doc.Node, from, to int) { all = all && n.Delta().All(from, to, has) })
	e.eachSelected(func(n *doc.Node, from, to int) {
		n.SetDelta(n.Delta().Format(from, to, func(at *doc.Attrs) { set(at, !all) }))
		e.invalidate(n.ID)
	})
	e.changed()
}

// eachSelected calls fn with the selected text of each block the
// selection spans that takes formats: text, but not code.
func (e *Editor) eachSelected(fn func(n *doc.Node, from, to int)) {
	a, b := e.ordered()
	us := e.units()
	ia, ib := e.index(a.ID), e.index(b.ID)
	if ia < 0 || ib < 0 {
		return
	}
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

// Has reports whether the whole selection has a format.
func (e *Editor) Has(has func(doc.Attrs) bool) bool {
	if a, b := e.ordered(); a.ID != b.ID {
		all, some := true, false
		e.eachSelected(func(n *doc.Node, from, to int) {
			some = true
			all = all && n.Delta().All(from, to, has)
		})
		return some && all
	}
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

// SetLink links the selection to url, or unlinks it when url is empty. A
// page mention is a link to its page: linked elsewhere, or not at all, it
// is the text it showed.
func (e *Editor) SetLink(url string) {
	if e.Collapsed() {
		return
	}
	e.Format(func(a doc.Attrs) bool { return a.Href == url }, func(a *doc.Attrs, _ bool) {
		a.Href = url
		if m := a.Mention; m != nil && m.Kind == "page" && url != doc.PageLink(m.ID) {
			a.Mention = nil
		}
	})
}

// linkHref is the link what was typed in the link editor makes, as the
// link opens: a web, mail or page link as it is, a web address without
// its scheme ("example.com") over https, a mail address as one to mail,
// and nothing, or only spaces, "", which takes the link away. ok is false
// for what no link opens, as a file's, an app's or words.
func linkHref(typed string) (href string, ok bool) {
	s := strings.TrimSpace(typed)
	lower := strings.ToLower(s)
	if _, page := doc.PageLinkID(s); s == "" || page || doc.WebURL(s) {
		return s, true
	}
	if strings.ContainsAny(s, " \t\n") {
		return "", false
	}
	if strings.HasPrefix(lower, "mailto:") {
		return s, len(s) > len("mailto:")
	}
	if user, host, at := strings.Cut(s, "@"); at && user != "" && strings.Contains(host, ".") && !strings.ContainsAny(s, "/:") {
		return "mailto:" + s, true
	}
	if strings.Contains(s, "://") {
		return "", false
	}
	u, err := url.Parse("https://" + s)
	if err != nil || !doc.WebURL(u.String()) || !strings.Contains(u.Hostname(), ".") && u.Hostname() != "localhost" {
		return "", false
	}
	return "https://" + s, true
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

// prevStop and nextStop are where the caret stops before and after off in
// a text block, a grapheme cluster away, as its layout has them, so an
// emoji, a flag or a letter with its marks goes whole. A block not laid
// out since it changed, as a second key before the next frame finds it,
// has them from Unicode's rules.
func (e *Editor) prevStop(n *doc.Node, off int) int {
	if l := e.layoutOf(n); l != nil {
		return runeOff(n, l.layout.PreviousBoundary(utf16Off(n, off)))
	}
	prev := 0
	for _, b := range clusters(n) {
		if b >= off {
			break
		}
		prev = b
	}
	return prev
}

func (e *Editor) nextStop(n *doc.Node, off int) int {
	if l := e.layoutOf(n); l != nil {
		return runeOff(n, l.layout.NextBoundary(utf16Off(n, off)))
	}
	for _, b := range clusters(n) {
		if b > off {
			return b
		}
	}
	return textLen(n)
}

// clusters are the rune offsets where a text block's grapheme clusters
// start, and its end.
func clusters(n *doc.Node) []int { return graphemes([]rune(n.Delta().Plain())) }

// graphemes are the offsets where the grapheme clusters of rs start, and
// its end.
func graphemes(rs []rune) []int {
	var seg segmenter.Segmenter
	seg.Init(rs)
	var out []int
	for it := seg.GraphemeIterator(); it.Next(); {
		out = append(out, it.Grapheme().Offset)
	}
	return append(out, len(rs))
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

// wordAt returns the word around an offset, in whole grapheme clusters,
// so a letter keeps its marks, as an "e" its accent: the clusters of
// letters, digits and "_" on either side; between none, the cluster
// after it, as an emoji or a flag.
func wordAt(rs []rune, off int) (int, int) {
	gs := graphemes(rs)
	last := len(gs) - 1 // the end, which starts no cluster
	isWord := func(i int) bool {
		r := rs[gs[i]]
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
	}
	// The cluster off starts, or is in.
	at := 0
	for at < last && gs[at+1] <= off {
		at++
	}
	a, b := at, at
	for a > 0 && isWord(a-1) {
		a--
	}
	for b < last && isWord(b) {
		b++
	}
	if a == b && b < last {
		b++
	}
	return gs[a], gs[b]
}
