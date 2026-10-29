package editor

import (
	"runtime"
	"time"
	"unicode/utf16"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

// The editor is the system's text-input client for the block the caret is
// in: input methods see that block's text, in UTF-16 units.

func (e *Editor) focusNode() *doc.Node {
	n := e.node(e.focus.ID)
	if n == nil || !n.IsText() {
		return nil
	}
	return n
}

func (e *Editor) focusText() string {
	if n := e.focusNode(); n != nil {
		return n.Delta().Plain()
	}
	return ""
}

// TextForRange returns text of the focus block.
func (e *Editor) TextForRange(r ui.TextInputRange) (string, ui.TextInputRange) {
	s := e.focusText()
	total := ui.UTF16Len(s)
	r.Start, r.End = min(max(r.Start, 0), total), min(max(r.End, 0), total)
	if r.End < r.Start {
		r.End = r.Start
	}
	a, z := ui.UTF16ByteOffset(s, r.Start), ui.UTF16ByteOffset(s, r.End)
	return s[a:z], r
}

// Selection reports the selection within the focus block.
func (e *Editor) Selection() ui.TextInputSelection {
	n := e.focusNode()
	if n == nil {
		return ui.TextInputSelection{}
	}
	caret := utf16Off(n, e.focus.Off)
	if e.anchor.ID != e.focus.ID {
		return ui.TextInputSelection{Range: ui.TextInputRange{Start: caret, End: caret}}
	}
	anchor := utf16Off(n, e.anchor.Off)
	return ui.TextInputSelection{Range: ui.TextInputRange{Start: min(anchor, caret), End: max(anchor, caret)}, Reversed: caret < anchor}
}

// MarkedRange reports an input method's composition.
func (e *Editor) MarkedRange() (ui.TextInputRange, bool) {
	n := e.focusNode()
	if !e.marked || n == nil {
		return ui.TextInputRange{}, false
	}
	return ui.TextInputRange{Start: utf16Off(n, e.markStart), End: utf16Off(n, e.markEnd)}, true
}

func (e *Editor) target(r *ui.TextInputRange) (int, int, bool) {
	n := e.focusNode()
	if n == nil {
		return 0, 0, false
	}
	switch {
	case r != nil:
		return runeOff(n, r.Start), runeOff(n, r.End), true
	case e.marked:
		return e.markStart, e.markEnd, true
	}
	return 0, 0, false
}

// ReplaceText commits text, as typing does.
func (e *Editor) ReplaceText(r *ui.TextInputRange, text string) {
	if e.ReadOnly {
		return
	}
	// Without a range the text replaces the composition, so find it
	// before ending it.
	from, to, ok := e.target(r)
	wasMarked := e.marked
	e.marked = false
	if text == "\n" || text == "\r" {
		if ok && from != to {
			n := e.focusNode()
			n.SetDelta(n.Delta().Delete(from, to))
			e.focus, e.anchor = Pos{n.ID, from}, Pos{n.ID, from}
		}
		e.Enter()
		return
	}
	if text == "\t" {
		return
	}
	if ok {
		n := e.focusNode()
		if wasMarked {
			// The composition was in the text as it went; take it out and
			// type what it became, as one step.
			n.SetDelta(n.Delta().Delete(from, to))
			e.invalidate(n.ID)
			e.anchor, e.focus = Pos{n.ID, from}, Pos{n.ID, from}
			if text == "" {
				e.changed()
				return
			}
		} else {
			e.anchor, e.focus = Pos{n.ID, from}, Pos{n.ID, to}
		}
	}
	if text == "" {
		if !e.Collapsed() {
			e.pushUndo(false)
			e.deleteSelection()
			e.Select(e.focus, e.focus)
			e.changed()
		}
		return
	}
	e.InsertText(text)
}

// SetMarkedText shows an input method's composition in the text.
func (e *Editor) SetMarkedText(r *ui.TextInputRange, text string, selected ui.TextInputRange) {
	if e.ReadOnly {
		return
	}
	n := e.focusNode()
	if n == nil {
		return
	}
	from, to, ok := e.target(r)
	if !ok {
		if !e.Collapsed() {
			e.pushUndo(false)
			e.deleteSelection()
		}
		from, to = e.focus.Off, e.focus.Off
		n = e.focusNode()
	}
	if !e.marked {
		e.pushUndo(false)
	}
	attrs := n.Delta().AttrsAt(from)
	if e.hasTyping {
		attrs = e.typing
	}
	n.SetDelta(n.Delta().Delete(from, to).Insert(from, text, attrs))
	e.invalidate(n.ID)
	runes := len([]rune(text))
	if runes == 0 {
		e.marked = false
	} else {
		e.marked, e.markStart, e.markEnd = true, from, from+runes
	}
	sel := []rune(text)
	toRune := func(units int) int {
		u := 0
		for i, r := range sel {
			if u >= units {
				return i
			}
			u += utf16.RuneLen(r)
		}
		return len(sel)
	}
	e.anchor = Pos{n.ID, from + toRune(selected.Start)}
	e.focus = Pos{n.ID, from + toRune(selected.End)}
	e.blink = time.Now()
	e.services.Invalidate()
}

// UnmarkText ends the composition, keeping its text.
func (e *Editor) UnmarkText() {
	if e.marked {
		e.marked = false
		e.changed()
	}
}

// BoundsForRange returns where text of the focus block is, relative to the
// editor.
func (e *Editor) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	n := e.focusNode()
	if n == nil {
		return ui.Rect{}, r, false
	}
	l := e.layouts[n.ID]
	box, ok := e.geom[n.ID]
	if l == nil || !ok {
		return ui.Rect{}, r, false
	}
	b := l.layout.Caret(r.Start)
	if r.End > r.Start {
		if rects := l.layout.SelectionRects(r); len(rects) > 0 {
			b = rects[0]
		}
	}
	b.X += box.X + l.x - e.root.X
	b.Y += box.Y + l.y - e.root.Y
	return b, r, true
}

// IndexForPoint hit-tests the focus block.
func (e *Editor) IndexForPoint(p ui.Point) (int, bool) {
	n := e.focusNode()
	if n == nil {
		return 0, false
	}
	l := e.layouts[n.ID]
	box, ok := e.geom[n.ID]
	if l == nil || !ok {
		return 0, false
	}
	wx, wy := p.X+e.root.X, p.Y+e.root.Y
	return l.layout.IndexAt(ui.Point{X: wx - box.X - l.x, Y: wy - box.Y - l.y}), true
}

// hit finds the place under a point of the window.
func (e *Editor) hit(wx, wy float32) (Pos, bool) {
	var best *doc.Node
	bestDist := float32(1e9)
	for _, n := range e.units() {
		r, ok := e.geom[n.ID]
		if !ok {
			continue
		}
		var d float32
		switch {
		case wy < r.Y:
			d = r.Y - wy
		case wy > r.Y+r.H:
			d = wy - r.Y - r.H
		}
		if d < bestDist {
			best, bestDist = n, d
		}
		if d == 0 {
			break
		}
	}
	if best == nil {
		return Pos{}, false
	}
	if !best.IsText() {
		return Pos{best.ID, 0}, true
	}
	l := e.layouts[best.ID]
	r := e.geom[best.ID]
	if l == nil {
		return Pos{best.ID, 0}, true
	}
	_, h := l.layout.Size()
	y := min(max(wy-r.Y-l.y, 0), max(h-1, 0))
	idx := l.layout.IndexAt(ui.Point{X: wx - r.X - l.x, Y: y})
	return Pos{best.ID, runeOff(best, idx)}, true
}

// caretRect is the caret of a place, in window coordinates.
func (e *Editor) caretRect(p Pos) (ui.Rect, bool) {
	n := e.node(p.ID)
	box, ok := e.geom[p.ID]
	if n == nil || !ok {
		return ui.Rect{}, false
	}
	if !n.IsText() {
		return ui.Rect{X: box.X, Y: box.Y, W: 1, H: box.H}, true
	}
	l := e.layouts[n.ID]
	if l == nil {
		return ui.Rect{}, false
	}
	c := l.layout.Caret(utf16Off(n, p.Off))
	c.X += box.X + l.x
	c.Y += box.Y + l.y
	return c, true
}

// RangeRect is the box in the window of the text from one position to
// another: the lines it is on, across its first block's width. A block
// that isn't text gives its whole box.
func (e *Editor) RangeRect(from, to Pos) (ui.Rect, bool) {
	box, ok := e.geom[from.ID]
	if !ok {
		return ui.Rect{}, false
	}
	a, okA := e.caretRect(from)
	b, okB := e.caretRect(to)
	if !okA || !okB {
		return box, true
	}
	if n := e.node(from.ID); n == nil || !n.IsText() {
		a = box
	}
	if n := e.node(to.ID); n == nil || !n.IsText() {
		b = e.geom[to.ID]
	}
	return ui.Rect{X: box.X, Y: a.Y, W: box.W, H: b.Y + b.H - a.Y}, true
}

// moveTo moves the caret, extending the selection with Shift. Typing
// after it is an undo step of its own.
func (e *Editor) moveTo(p Pos, extend bool) {
	e.BreakUndo()
	desired, has := e.desiredX, e.hasDesired
	if extend {
		e.Select(e.anchor, p)
	} else {
		e.Select(p, p)
	}
	e.desiredX, e.hasDesired = desired, has
}

func (e *Editor) horizontal(dir int, word, line, extend bool) {
	if !extend && !e.Collapsed() && !word && !line {
		a, b := e.ordered()
		if dir < 0 {
			e.moveTo(a, false)
		} else {
			e.moveTo(b, false)
		}
		return
	}
	p := e.focus
	n := e.node(p.ID)
	us := e.units()
	i := e.index(p.ID)
	switch {
	case n == nil:
		return
	case line:
		if dir < 0 {
			p.Off = 0
		} else {
			p.Off = textLen(n)
		}
	case dir < 0 && p.Off > 0:
		if word {
			p.Off = wordStart([]rune(n.Delta().Plain()), p.Off)
		} else if l := e.layoutOf(n); l != nil {
			p.Off = runeOff(n, l.layout.PreviousBoundary(utf16Off(n, p.Off)))
		} else {
			p.Off--
		}
		// A mention is passed over whole.
		if start, _, ok := n.Delta().MentionAround(p.Off); ok {
			p.Off = start
		}
	case dir > 0 && p.Off < textLen(n):
		if word {
			p.Off = wordEnd([]rune(n.Delta().Plain()), p.Off)
		} else if l := e.layoutOf(n); l != nil {
			p.Off = runeOff(n, l.layout.NextBoundary(utf16Off(n, p.Off)))
		} else {
			p.Off++
		}
		if _, end, ok := n.Delta().MentionAround(p.Off); ok {
			p.Off = end
		}
	case dir < 0 && i > 0:
		prev := us[i-1]
		p = Pos{prev.ID, textLen(prev)}
	case dir > 0 && i+1 < len(us):
		p = Pos{us[i+1].ID, 0}
	}
	e.hasDesired = false
	e.moveTo(p, extend)
	e.hasDesired = false
}

func (e *Editor) vertical(dir int, extend bool) {
	if !extend && !e.Collapsed() {
		a, b := e.ordered()
		if dir < 0 {
			e.moveTo(a, false)
		} else {
			e.moveTo(b, false)
		}
		return
	}
	caret, ok := e.caretRect(e.focus)
	if !ok {
		e.horizontal(dir, false, false, extend)
		return
	}
	if !e.hasDesired {
		e.desiredX, e.hasDesired = caret.X, true
	}
	x := e.desiredX
	n := e.node(e.focus.ID)
	// Within the block, to the line above or below.
	if n.IsText() {
		if l := e.layouts[n.ID]; l != nil {
			box := e.geom[n.ID]
			_, h := l.layout.Size()
			y := caret.Y - box.Y - l.y + caret.H/2 + float32(dir)*caret.H
			if y >= 0 && y < h {
				idx := l.layout.IndexAt(ui.Point{X: x - box.X - l.x, Y: y})
				e.moveTo(Pos{n.ID, runeOff(n, idx)}, extend)
				return
			}
		}
	}
	us := e.units()
	i := e.index(n.ID) + dir
	if i < 0 {
		e.moveTo(Pos{n.ID, 0}, extend)
		return
	}
	if i >= len(us) {
		e.moveTo(Pos{n.ID, textLen(n)}, extend)
		return
	}
	next := us[i]
	if !next.IsText() {
		e.moveTo(Pos{next.ID, 0}, extend)
		return
	}
	l := e.layouts[next.ID]
	box, ok := e.geom[next.ID]
	if l == nil || !ok {
		off := 0
		if dir < 0 {
			off = textLen(next)
		}
		e.moveTo(Pos{next.ID, off}, extend)
		return
	}
	_, h := l.layout.Size()
	y := float32(1)
	if dir < 0 {
		y = max(h-2, 0)
	}
	idx := l.layout.IndexAt(ui.Point{X: x - box.X - l.x, Y: y})
	e.moveTo(Pos{next.ID, runeOff(next, idx)}, extend)
}

// menuKey handles keys while the / or @ menu is open.
func (e *Editor) menuKey(ev ui.InputEvent) bool {
	if e.trigger == nil {
		return false
	}
	switch ev.Key {
	case ui.KeyDown:
		if len(e.menuItems) > 0 {
			e.menuIndex = (e.menuIndex + 1) % len(e.menuItems)
		}
		return true
	case ui.KeyUp:
		if len(e.menuItems) > 0 {
			e.menuIndex = (e.menuIndex - 1 + len(e.menuItems)) % len(e.menuItems)
		}
		return true
	case ui.KeyEnter, ui.KeyTab:
		if e.menuIndex < len(e.menuItems) {
			t, item := *e.trigger, e.menuItems[e.menuIndex]
			e.trigger = nil
			e.Host.Pick(t, item)
			return true
		}
		e.trigger = nil
		return false
	case ui.KeyEscape:
		e.trigger = nil
		return true
	}
	return false
}

// HandleInput takes keys, the pointer and the menus' edit commands.
func (e *Editor) HandleInput(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputPointerDown:
		wx, wy := ev.X+e.root.X, ev.Y+e.root.Y
		if ev.Button != 0 {
			// The context menu acts on the block it opened on.
			e.menuBlock, e.menuAt = "", Pos{}
			if p, ok := e.hit(wx, wy); ok {
				e.menuBlock, e.menuAt = p.ID, p
			}
			return false
		}
		p, ok := e.hit(wx, wy)
		if !ok {
			return false
		}
		e.trigger = nil
		e.linkOpen = false
		if n := e.node(p.ID); n != nil && (e.inCodeBar(n, wy) || e.inGutter(n, wx, wy)) || e.onChip(wx, wy) {
			return false
		}
		if n := e.node(p.ID); n != nil && e.marker(n, wx, wy) {
			switch n.Type {
			case "todo_list":
				e.ToggleChecked(n.ID)
				return true
			case "callout":
				e.CycleTone(n.ID)
				return true
			case "toggle_list":
				e.ToggleCollapsed(n.ID)
				return true
			}
		}
		// A press on a block such as a subpage, chart or table selects it
		// and goes on to the block's own controls: its card, buttons, cells.
		if n := e.node(p.ID); n != nil && !n.IsText() && ev.Mods&ui.Shift == 0 {
			if box, ok := e.geom[n.ID]; ok && wy >= box.Y && wy < box.Y+box.H {
				e.Select(p, p)
				e.reveal = "" // it is under the pointer; scrolling would lose the click
				e.BreakUndo()
				return false
			}
		}
		// A page link opens the page, as a click on a page in the sidebar
		// does; a web link wants Cmd, so that a click can place the caret.
		if ev.Clicks == 1 && (ev.Mods == 0 || ev.Mods == ui.Cmd) {
			link := e.linkAtPos(p)
			if id, ok := doc.PageLinkID(link); ok {
				if block := doc.LinkBlock(link); block != "" {
					e.Host.OpenBlock(id, block)
				} else {
					e.Host.OpenPage(id)
				}
				return true
			}
		}
		if ev.Mods&ui.Cmd != 0 {
			if url := e.linkAtPos(p); url != "" {
				e.services.OpenURL(url)
				return true
			}
		}
		switch {
		case ev.Clicks >= 3:
			n := e.node(p.ID)
			e.Select(Pos{p.ID, 0}, Pos{p.ID, textLen(n)})
		case ev.Clicks == 2:
			n := e.node(p.ID)
			if n.IsText() {
				a, b := wordAt([]rune(n.Delta().Plain()), p.Off)
				e.Select(Pos{p.ID, a}, Pos{p.ID, b})
			}
		case ev.Mods&ui.Shift != 0:
			e.Select(e.anchor, p)
		default:
			e.Select(p, p)
		}
		e.BreakUndo()
		e.dragging = ev.Clicks < 2
		return true
	case ui.InputPointerMove:
		if !e.dragging {
			return false
		}
		if p, ok := e.hit(ev.X+e.root.X, ev.Y+e.root.Y); ok && p != e.focus {
			e.Select(e.anchor, p)
		}
		return true
	case ui.InputPointerUp:
		if e.dragging {
			e.dragging = false
			return true
		}
		return false
	case ui.InputCommand:
		return e.command(ev.Text)
	case ui.InputKeyDown:
		return e.key(ev)
	}
	return false
}

func (e *Editor) linkAtPos(p Pos) string {
	n := e.node(p.ID)
	if n == nil || !n.IsText() {
		return ""
	}
	at := 0
	for _, r := range n.Delta() {
		l := len([]rune(r.Text))
		if p.Off >= at && p.Off < at+l {
			return r.Attrs.Href
		}
		at += l
	}
	return ""
}

func (e *Editor) command(name string) bool {
	switch name {
	case "copy", "cut":
		e.copySelection()
		if name == "cut" && !e.Collapsed() {
			e.Backspace(false)
		}
		return true
	case "paste", "pasteAndMatchStyle":
		e.pasteClipboard(name == "pasteAndMatchStyle")
		return true
	case "selectAll":
		e.SelectAll()
		return true
	case "undo":
		e.Undo()
		return true
	case "redo":
		e.Redo()
		return true
	case "delete":
		e.Backspace(false)
		return true
	}
	return false
}

// emacsKeys are the Control keys that act as other keys on macOS, as in its
// text fields, after Emacs.
var emacsKeys = map[ui.Key]ui.Key{ui.KeyB: ui.KeyLeft, ui.KeyF: ui.KeyRight, ui.KeyP: ui.KeyUp, ui.KeyN: ui.KeyDown,
	ui.KeyH: ui.KeyBackspace, ui.KeyD: ui.KeyDelete, ui.KeyJ: ui.KeyEnter}

// killed is what Control-K took out last, for Control-Y.
var killed string

// emacsKey handles the other Control keys of macOS: to the start or end of
// the block, killing to its end and yanking that back, and bringing the
// caret into view.
func (e *Editor) emacsKey(key ui.Key, shift bool) bool {
	switch key {
	case ui.KeyA:
		e.horizontal(-1, false, true, shift)
	case ui.KeyE:
		e.horizontal(1, false, true, shift)
	case ui.KeyK:
		n := e.focusNode()
		if e.ReadOnly || n == nil {
			break
		}
		e.Select(e.focus, e.focus)
		if off := e.focus.Off; off < textLen(n) {
			killed = string([]rune(n.Delta().Plain())[off:])
			e.Select(e.focus, Pos{n.ID, textLen(n)})
			e.Backspace(false)
		} else {
			e.Delete(false) // at the end, join the next block
		}
	case ui.KeyY:
		e.InsertText(killed)
	case ui.KeyL:
		e.reveal = e.focus.ID
	default:
		return false
	}
	e.services.Invalidate()
	return true
}

func (e *Editor) key(ev ui.InputEvent) bool {
	if runtime.GOOS == "darwin" && ev.Mods&^ui.Shift == ui.Ctrl {
		if to, ok := emacsKeys[ev.Key]; ok {
			ev.Key, ev.Mods = to, ev.Mods&ui.Shift
		} else if e.emacsKey(ev.Key, ev.Mods&ui.Shift != 0) {
			return true
		}
	}
	if e.menuKey(ev) {
		return true
	}
	shift := ev.Mods&ui.Shift != 0
	cmd := ev.Mods&ui.Cmd != 0
	alt := ev.Mods&ui.Alt != 0
	plain := ev.Mods&^ui.Shift == 0
	switch ev.Key {
	case ui.KeyEnter:
		if cmd {
			return false
		}
		if shift {
			e.SoftBreak()
		} else {
			e.Enter()
		}
		return true
	case ui.KeyBackspace:
		if cmd {
			if n := e.focusNode(); n != nil && e.Collapsed() {
				e.Select(Pos{n.ID, 0}, e.focus)
			}
		}
		e.Backspace(alt)
		return true
	case ui.KeyDelete:
		e.Delete(alt)
		return true
	case ui.KeyTab:
		if ev.Mods&^ui.Shift != 0 {
			return false
		}
		if e.inCode() && !e.ReadOnly {
			e.indentCode(e.focusNode(), shift)
			return true
		}
		if shift {
			e.Outdent()
		} else {
			e.Indent()
		}
		return true
	case ui.KeyLeft, ui.KeyRight:
		dir := -1
		if ev.Key == ui.KeyRight {
			dir = 1
		}
		e.horizontal(dir, alt, cmd, shift)
		return true
	case ui.KeyUp, ui.KeyDown:
		dir := -1
		if ev.Key == ui.KeyDown {
			dir = 1
		}
		if cmd {
			us := e.units()
			if dir < 0 {
				e.moveTo(Pos{us[0].ID, 0}, shift)
			} else {
				last := us[len(us)-1]
				e.moveTo(Pos{last.ID, textLen(last)}, shift)
			}
			return true
		}
		e.vertical(dir, shift)
		return true
	case ui.KeyHome:
		e.horizontal(-1, false, true, shift)
		return true
	case ui.KeyEnd:
		e.horizontal(1, false, true, shift)
		return true
	case ui.KeyEscape:
		if e.linkOpen {
			e.linkOpen = false
			return true
		}
		if !e.Collapsed() {
			e.Select(e.focus, e.focus)
			return true
		}
		return false
	}
	if !cmd || alt {
		_ = plain
		return false
	}
	switch ev.Key {
	case ui.KeyB:
		e.ToggleBold()
	case ui.KeyI:
		e.ToggleItalic()
	case ui.KeyU:
		e.ToggleUnderline()
	case ui.KeyE:
		e.ToggleCode()
	case ui.KeyX:
		if !shift {
			return false
		}
		e.ToggleStrike()
	case ui.KeyK:
		if e.Collapsed() {
			return false
		}
		e.linkDraft = e.LinkAt()
		e.linkOpen = true
	case ui.KeyZ:
		if shift {
			e.Redo()
		} else {
			e.Undo()
		}
	case ui.KeyA:
		e.SelectAll()
	default:
		return false
	}
	e.services.Invalidate()
	return true
}
