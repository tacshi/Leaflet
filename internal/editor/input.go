package editor

import (
	"runtime"
	"time"
	"unicode/utf16"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

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
		from, to := runeOff(n, r.Start), runeOff(n, r.End)
		if from >= to {
			at := e.clamp(Pos{n.ID, from}).Off
			return at, at, true
		}
		from, to = n.Delta().Widen(from, to)
		return from, to, true
	case e.marked:
		end := textLen(n)
		return min(e.markStart, end), min(e.markEnd, end), true
	}
	return 0, 0, false
}

func (e *Editor) ReplaceText(r *ui.TextInputRange, text string) {
	if e.ReadOnly {
		return
	}
	from, to, ok := e.target(r)
	wasMarked := e.marked
	e.marked = false
	if text == "\n" || text == "\r" {
		if ok && from != to {
			n := e.focusNode()
			if wasMarked {
				n.SetDelta(n.Delta().Delete(from, to))
				e.focus, e.anchor = Pos{n.ID, from}, Pos{n.ID, from}
			} else {
				e.anchor, e.focus = Pos{n.ID, from}, Pos{n.ID, to}
			}
		}
		if wasMarked && e.markStep {
			e.dropStep()
		}
		e.Enter()
		return
	}
	if text == "\t" {
		e.marked = wasMarked
		return
	}
	if ok {
		n := e.focusNode()
		if wasMarked {
			n.SetDelta(n.Delta().Delete(from, to))
			e.invalidate(n.ID)
			e.anchor, e.focus = Pos{n.ID, from}, Pos{n.ID, from}
			if e.markStep {
				e.dropStep()
			} else {
				e.coalescing, e.coalesceIn, e.lastEdit = true, n.ID, time.Now()
			}
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

func (e *Editor) SetMarkedText(r *ui.TextInputRange, text string, selected ui.TextInputRange) {
	if e.ReadOnly || e.node(e.focus.ID) == nil {
		return
	}
	if !e.marked {
		redo := e.redo
		e.pushUndo(false)
		e.markStep, e.markRedo = e.Collapsed() && (r == nil || r.Start == r.End), redo
		e.markID, e.markOut = compositions.Add(1), false
		if e.focusNode() == nil {
			e.markStep = false
			e.deleteSelection()
			if n := e.node(e.focus.ID); n != nil && !n.IsText() {
				p := newText("paragraph", nil)
				_ = e.Content.Insert(doc.Position{After: n.ID}, p)
				e.anchor, e.focus = Pos{p.ID, 0}, Pos{p.ID, 0}
			}
			r = nil
		}
	}
	n := e.focusNode()
	if n == nil {
		return
	}
	from, to, ok := e.target(r)
	if !ok {
		e.deleteSelection()
		from, to = e.focus.Off, e.focus.Off
		n = e.focusNode()
	}
	attrs := n.Delta().AttrsAt(from)
	if e.hasTyping {
		attrs = e.typing
	}
	if n.Type == "code" {
		attrs = doc.Attrs{}
	}
	n.SetDelta(n.Delta().Delete(from, to).Insert(from, text, attrs))
	e.invalidate(n.ID)
	runes := len([]rune(text))
	if runes == 0 {
		e.marked = false
		if e.markStep {
			e.dropStep()
		}
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
	if runes == 0 {
		// A composition that ends with nothing is an edit like one that
		// ends with text: what it replaced is gone.
		e.changed()
	}
}

func (e *Editor) UnmarkText() {
	if e.marked {
		e.marked = false
		e.changed()
	}
}

type lifted struct {
	id               string
	text, full, rest doc.Delta
	start            int
	anchor, focus    Pos
	holds            bool
}

func (e *Editor) liftComposition(holds bool) *lifted {
	n := e.focusNode()
	if !e.marked || n == nil {
		e.marked = false
		return nil
	}
	d := n.Delta()
	l := &lifted{id: n.ID, text: d.Slice(e.markStart, e.markEnd), full: d, start: e.markStart, anchor: e.anchor, focus: e.focus, holds: holds}
	n.SetDelta(d.Delete(e.markStart, e.markEnd))
	l.rest = n.Delta()
	e.invalidate(n.ID)
	if e.markStep {
		e.dropStep()
	}
	e.marked, e.markStep = false, false
	e.anchor, e.focus = Pos{n.ID, l.start}, Pos{n.ID, l.start}
	return l
}

func (e *Editor) putBack(l *lifted) {
	if l == nil {
		return
	}
	n := e.node(l.id)
	if n == nil || !n.IsText() {
		return
	}
	d, end := n.Delta(), l.start+l.text.Len()
	held := sameDelta(d, l.full)
	if e.focus.ID != l.id || !held && !sameDelta(d, l.rest) {
		if l.holds && end <= d.Len() && sameDelta(d.Slice(l.start, end), l.text) {
			n.SetDelta(d.Delete(l.start, end))
			e.invalidate(n.ID)
			e.Select(e.anchor, e.focus)
		}
		return
	}
	redo := e.redo
	n.SetDelta(l.rest)
	e.pushUndo(false)
	n.SetDelta(l.full)
	e.invalidate(n.ID)
	e.markStep, e.markRedo = true, redo
	e.anchor, e.focus = l.anchor, l.focus
	e.marked, e.markStart, e.markEnd = true, l.start, end
}

func sameDelta(a, b doc.Delta) bool {
	a, b = a.Compact(), b.Compact()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text || !a[i].Attrs.Equal(b[i].Attrs) {
			return false
		}
	}
	return true
}

func (e *Editor) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	n := e.focusNode()
	if n == nil {
		return ui.Rect{}, r, false
	}
	l := e.layoutOf(n)
	box, ok := e.box(n.ID)
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

func (e *Editor) IndexForPoint(p ui.Point) (int, bool) {
	n := e.focusNode()
	if n == nil {
		return 0, false
	}
	l := e.layoutOf(n)
	box, ok := e.box(n.ID)
	if l == nil || !ok {
		return 0, false
	}
	wx, wy := p.X+e.root.X, p.Y+e.root.Y
	return l.layout.IndexAt(ui.Point{X: wx - box.X - l.x, Y: wy - box.Y - l.y}), true
}

func (e *Editor) hit(wx, wy float32) (Pos, ui.TextAffinity, bool) {
	var best *doc.Node
	bestDist := float32(1e9)
	for _, n := range e.units() {
		r, ok := e.box(n.ID)
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
		return Pos{}, ui.TextDownstream, false
	}
	if !best.IsText() {
		return Pos{best.ID, 0}, ui.TextDownstream, true
	}
	l := e.layoutOf(best)
	r, _ := e.box(best.ID)
	if l == nil {
		return Pos{best.ID, 0}, ui.TextDownstream, true
	}
	_, h := l.layout.Size()
	y := min(max(wy-r.Y-l.y, 0), max(h-1, 0))
	at := l.layout.PositionAt(ui.Point{X: wx - r.X - l.x, Y: y})
	return Pos{best.ID, runeOff(best, at.Index)}, at.Affinity, true
}

func (e *Editor) caretRect(p Pos) (ui.Rect, bool) {
	n := e.node(p.ID)
	box, ok := e.box(p.ID)
	if n == nil || !ok {
		return ui.Rect{}, false
	}
	if !n.IsText() {
		return ui.Rect{X: box.X, Y: box.Y, W: 1, H: box.H}, true
	}
	l := e.layoutOf(n)
	if l == nil {
		return ui.Rect{}, false
	}
	c := l.layout.CaretAt(e.caretPosition(n, p))
	c.X += box.X + l.x
	c.Y += box.Y + l.y
	return c, true
}

func (e *Editor) caretPosition(n *doc.Node, p Pos) ui.TextCaretPosition {
	at := ui.TextCaretPosition{Index: utf16Off(n, p.Off)}
	if p == e.focus {
		at.Affinity = e.affinity
	}
	return at
}

func (e *Editor) RangeRect(from, to Pos) (ui.Rect, bool) {
	box, ok := e.box(from.ID)
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
		b, _ = e.box(to.ID)
	}
	return ui.Rect{X: box.X, Y: a.Y, W: box.W, H: b.Y + b.H - a.Y}, true
}

func (e *Editor) moveTo(p Pos, extend bool) { e.moveToSide(p, ui.TextDownstream, extend) }

func (e *Editor) moveToSide(p Pos, side ui.TextAffinity, extend bool) {
	e.BreakUndo()
	desired, has := e.desiredX, e.hasDesired
	if extend {
		e.Select(e.anchor, p)
	} else {
		e.Select(p, p)
	}
	e.affinity = side
	e.desiredX, e.hasDesired = desired, has
}

func (e *Editor) rtl(n *doc.Node) bool {
	l := e.layoutOf(n)
	if l == nil {
		return false
	}
	lines := l.layout.Lines()
	return len(lines) > 0 && lines[0].RTL
}

func (e *Editor) lineEdges(n *doc.Node, off int, side ui.TextAffinity) (start, end int, wraps bool) {
	l := e.layoutOf(n)
	if l == nil {
		return 0, textLen(n), false
	}
	u := utf16Off(n, off)
	lines := l.layout.Lines()
	for i, line := range lines {
		wraps = i+1 < len(lines) && lines[i+1].Start == line.End
		if u < line.End || u == line.End && (!wraps || side == ui.TextUpstream) || i == len(lines)-1 {
			return runeOff(n, line.Start), runeOff(n, line.End), wraps
		}
	}
	return 0, textLen(n), false
}

func (e *Editor) horizontal(dir int, word, line, extend bool) {
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	forward := dir
	if e.rtl(n) {
		forward = -dir
	}
	if !extend && !e.Collapsed() && !word && !line {
		a, b := e.ordered()
		back := dir < 0
		if a.ID == b.ID {
			back = forward < 0
		}
		if back {
			e.moveTo(a, false)
		} else {
			e.moveTo(b, false)
		}
		return
	}
	if line {
		e.lineEdge(forward, extend)
		return
	}
	p, side := e.focus, ui.TextDownstream
	us := e.units()
	i := e.index(p.ID)
	edge := forward < 0 && p.Off == 0 || forward > 0 && p.Off == textLen(n)
	switch {
	case word && !edge:
		if forward < 0 {
			p.Off = wordStart([]rune(n.Delta().Plain()), p.Off)
		} else {
			p.Off = wordEnd([]rune(n.Delta().Plain()), p.Off)
		}
	case !word && e.layoutOf(n) != nil:
		l := e.layoutOf(n).layout
		from := e.caretPosition(n, p)
		to := l.MoveCaret(from, dir)
		if edge = to == from; !edge {
			p.Off, side = runeOff(n, to.Index), to.Affinity
		}
	case !word && !edge && forward < 0:
		p.Off = e.prevStop(n, p.Off)
	case !word && !edge:
		p.Off = e.nextStop(n, p.Off)
	}
	switch {
	case !edge:
	case forward < 0 && i > 0:
		prev := us[i-1]
		p = Pos{prev.ID, textLen(prev)}
	case forward > 0 && i+1 < len(us):
		p = Pos{us[i+1].ID, 0}
	}
	if m := e.node(p.ID); m != nil && m.IsText() && p.ID == e.focus.ID {
		if start, end, ok := m.Delta().MentionAround(p.Off); ok {
			if p.Off < e.focus.Off {
				p.Off = start
			} else {
				p.Off = end
			}
		}
	}
	e.hasDesired = false
	e.moveToSide(p, side, extend)
	e.hasDesired = false
}

func (e *Editor) lineEdge(forward int, extend bool) {
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	p, side := e.focus, ui.TextDownstream
	start, end, wraps := e.lineEdges(n, p.Off, e.affinity)
	if forward < 0 {
		p.Off = start
	} else if p.Off = end; wraps {
		side = ui.TextUpstream
	}
	e.hasDesired = false
	e.moveToSide(p, side, extend)
}

func (e *Editor) blockEdge(dir int, further, extend bool) {
	n := e.node(e.focus.ID)
	if n == nil {
		return
	}
	p := e.focus
	us := e.units()
	i := e.index(p.ID)
	switch {
	case dir < 0 && (p.Off > 0 || !further || i <= 0):
		p.Off = 0
	case dir < 0:
		p = Pos{us[i-1].ID, 0}
	case p.Off < textLen(n) || !further || i+1 >= len(us):
		p.Off = textLen(n)
	default:
		next := us[i+1]
		p = Pos{next.ID, textLen(next)}
	}
	e.hasDesired = false
	e.moveTo(p, extend)
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
		if e.rtl(e.node(e.focus.ID)) {
			dir = -dir
		}
		e.horizontal(dir, false, false, extend)
		return
	}
	if !e.hasDesired {
		e.desiredX, e.hasDesired = caret.X, true
	}
	x := e.desiredX
	n := e.node(e.focus.ID)
	if n.IsText() {
		if l := e.layoutOf(n); l != nil {
			box, _ := e.box(n.ID)
			_, h := l.layout.Size()
			y := caret.Y - box.Y - l.y + caret.H/2 + float32(dir)*caret.H
			if y >= 0 && y < h {
				at := l.layout.PositionAt(ui.Point{X: x - box.X - l.x, Y: y})
				e.moveToSide(Pos{n.ID, runeOff(n, at.Index)}, at.Affinity, extend)
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
	l := e.layoutOf(next)
	box, ok := e.box(next.ID)
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
	at := l.layout.PositionAt(ui.Point{X: x - box.X - l.x, Y: y})
	e.moveToSide(Pos{next.ID, runeOff(next, at.Index)}, at.Affinity, extend)
}

func (e *Editor) menuKey(ev ui.InputEvent) bool {
	if e.trigger == nil {
		return false
	}
	if k := ev.Key; (k == ui.KeyDown || k == ui.KeyUp || k == ui.KeyEnter || k == ui.KeyTab) && e.menuFor != *e.trigger {
		e.findMenuItems()
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

func (e *Editor) HandleInput(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputPointerDown:
		wx, wy := ev.X+e.root.X, ev.Y+e.root.Y
		if ev.Button != 0 {
			e.menuBlock, e.menuAt = "", Pos{}
			if p, _, ok := e.hit(wx, wy); ok {
				e.menuBlock, e.menuAt = p.ID, p
			}
			return false
		}
		p, side, ok := e.hit(wx, wy)
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
		if n := e.node(p.ID); n != nil && !n.IsText() && ev.Mods&ui.Shift == 0 {
			if box, ok := e.box(n.ID); ok && wy >= box.Y && wy < box.Y+box.H {
				e.Select(p, p)
				e.reveal = ""
				e.BreakUndo()
				return false
			}
		}
		if ev.Clicks == 1 && (ev.Mods == 0 || ev.Mods == ui.Cmd) {
			link := e.linkUnder(p.ID, wx, wy)
			if id, ok := doc.PageLinkID(link); ok {
				if block := doc.LinkBlock(link); block != "" {
					at, moment := doc.LinkMoment(link)
					if !moment {
						at = -1
					}
					e.Host.OpenBlock(id, block, at)
				} else {
					e.Host.OpenPage(id)
				}
				return true
			}
		}
		if ev.Mods&ui.Cmd != 0 {
			if url := doc.SafeHref(e.linkUnder(p.ID, wx, wy)); url != "" {
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
			e.affinity = side
		default:
			e.Select(p, p)
			e.affinity = side
		}
		e.BreakUndo()
		e.dragging = ev.Clicks < 2
		return true
	case ui.InputPointerMove:
		if !e.dragging {
			return false
		}
		if p, side, ok := e.hit(ev.X+e.root.X, ev.Y+e.root.Y); ok && (p != e.focus || side != e.affinity) {
			e.Select(e.anchor, p)
			e.affinity = side
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

func (e *Editor) linkUnder(id string, wx, wy float32) string {
	n := e.node(id)
	l := e.layoutOf(n)
	r, ok := e.box(id)
	if n == nil || !n.IsText() || l == nil || !ok {
		return ""
	}
	x, y := wx-r.X-l.x, wy-r.Y-l.y
	at := 0
	for _, run := range n.Delta() {
		k := len([]rune(run.Text))
		if run.Attrs.Href != "" {
			for _, b := range l.layout.SelectionRects(ui.TextInputRange{Start: utf16Off(n, at), End: utf16Off(n, at+k)}) {
				if b.Contains(x, y) {
					return run.Attrs.Href
				}
			}
		}
		at += k
	}
	return ""
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
		if name == "cut" && (!e.Collapsed() || e.blockSelected()) {
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

var emacsKeys = map[ui.Key]ui.Key{ui.KeyB: ui.KeyLeft, ui.KeyF: ui.KeyRight, ui.KeyP: ui.KeyUp, ui.KeyN: ui.KeyDown,
	ui.KeyH: ui.KeyBackspace, ui.KeyD: ui.KeyDelete, ui.KeyJ: ui.KeyEnter}

var killed string

func (e *Editor) emacsKey(key ui.Key, shift bool) bool {
	switch key {
	case ui.KeyA:
		e.blockEdge(-1, false, shift)
	case ui.KeyE:
		e.blockEdge(1, false, shift)
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
			e.Delete(false)
		}
	case ui.KeyY:
		e.insertInline(killed, "")
	case ui.KeyL:
		e.reveal = e.focus.ID
	default:
		return false
	}
	e.services.Invalidate()
	return true
}

func ctrlKeys(ev ui.InputEvent) ui.InputEvent {
	if ev.Mods&^ui.Shift != ui.Ctrl {
		return ev
	}
	shift := ev.Mods & ui.Shift
	switch ev.Key {
	case ui.KeyLeft, ui.KeyRight, ui.KeyBackspace, ui.KeyDelete, ui.KeyUp, ui.KeyDown:
		ev.Mods = shift | ui.Alt
	case ui.KeyHome:
		ev.Key, ev.Mods = ui.KeyUp, shift|ui.Cmd
	case ui.KeyEnd:
		ev.Key, ev.Mods = ui.KeyDown, shift|ui.Cmd
	}
	return ev
}

func (e *Editor) key(ev ui.InputEvent) bool {
	if runtime.GOOS != "darwin" {
		ev = ctrlKeys(ev)
	}
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
	switch ev.Key {
	case ui.KeyEnter:
		if cmd {
			return !shift && !alt && e.checkOrFold()
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
				start, _, _ := e.lineEdges(n, e.focus.Off, e.affinity)
				e.Select(Pos{n.ID, start}, e.focus)
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
		switch {
		case cmd:
			us := e.units()
			if dir < 0 {
				e.moveTo(Pos{us[0].ID, 0}, shift)
			} else {
				last := us[len(us)-1]
				e.moveTo(Pos{last.ID, textLen(last)}, shift)
			}
		case alt:
			e.blockEdge(dir, true, shift)
		default:
			e.vertical(dir, shift)
		}
		return true
	case ui.KeyHome:
		e.lineEdge(-1, shift)
		return true
	case ui.KeyEnd:
		e.lineEdge(1, shift)
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
		if e.Collapsed() || e.ReadOnly {
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
