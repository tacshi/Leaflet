package editor

import (
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/menu"
	"leaflet/internal/shortcut"
)

type Pos struct {
	ID  string
	Off int
}

type MenuItem = menu.Item

type Trigger struct {
	Kind  rune
	ID    string
	Off   int
	Query string
}

type Host interface {
	PageTitle(id string) string
	OpenPage(id string)
	Image(n *doc.Node) (pic ui.ImageSource, video *ui.VideoPlayer, failed bool)
	Embed(c *ui.Context, n *doc.Node, width float32)
	Video(c *ui.Context, n *doc.Node, width float32)
	Card(c *ui.Context, n *doc.Node)
	File(c *ui.Context, n *doc.Node, width float32)
	Recording(c *ui.Context, n *doc.Node)
	CommentCount(blockID string) int
	OpenComments(blockID string)
	CommentPreview(c *ui.Context, blockID string)
	SlashItems(query string) []MenuItem
	MentionItems(query string) []MenuItem
	Pick(t Trigger, item MenuItem)
	Comment(blockID, quote string)
	AskAgent(blockID, quote string)
	EditBlock(blockID string)
	VideoMenu(m *ui.Menu, n *doc.Node)
	FileMenu(c *ui.Context, m *ui.Menu, n *doc.Node)
	OpenBlock(pageID, blockID string, at int)
	BlockLink(blockID string) string
	FollowVideoLink(link string, done func(video string))
}

type snapshot struct {
	content *doc.Content
	anchor  Pos
	focus   Pos
	after   *doc.Content
	size    int
}

type Editor struct {
	Content  *doc.Content
	Host     Host
	Size     float32
	ReadOnly bool
	Tinted   map[string]bool
	OnChange func()
	OnSelect func()

	anchor, focus Pos
	affinity      ui.TextAffinity
	typing        doc.Attrs
	hasTyping     bool
	marked        bool
	markStart     int
	markEnd       int
	markStep      bool
	markRedo      []snapshot
	markID        uint64
	markOut       bool
	undo, redo    []snapshot
	runStep       *doc.Content
	kept          map[string]*doc.Node
	frozen        *doc.Content
	lastEdit      time.Time
	coalescing    bool
	coalesceIn    string
	typed         int

	layouts map[string]*blockLayout
	looks   map[string]*look
	rel     map[string]ui.Rect
	laid    map[string]ui.Rect
	laidTop float32
	root    ui.Rect
	width   float32
	focused bool

	dragging   bool
	desiredX   float32
	hasDesired bool
	reveal     string
	blink      time.Time

	trigger       *Trigger
	menuIndex     int
	menuShown     menuChoice
	menuItems     []MenuItem
	menuFor       Trigger
	linkOpen      bool
	linkDraft     string
	copied        string
	copiedAt      time.Time
	plainCopiedAt time.Time

	cellEdit                     string
	cellFocused                  bool
	cellMove                     int
	cellText, cellFor, cellSaved string

	hid       map[string]bool
	hidAt     uint64
	order     map[string]int
	depths    map[string]int
	building  bool
	gen       uint64
	shaped    shaped
	menuBlock string
	menuAt    Pos
	bar       ui.Handle
	colorOpen bool
	dueOpen   string
	dues      map[string]*dueDraft
	chips     map[string]ui.Rect
	peek      string
	peekOver  bool
	peekLeft  time.Time
	peekShut  string

	handle   ui.Handle
	services ui.Services
}

func New(content *doc.Content, host Host) *Editor {
	e := &Editor{Content: content, Host: host, Size: 17,
		layouts: map[string]*blockLayout{}, looks: map[string]*look{}, rel: map[string]ui.Rect{}, laid: map[string]ui.Rect{}, chips: map[string]ui.Rect{}, width: 680}
	refill(content)
	first := e.units()[0]
	e.anchor, e.focus = Pos{first.ID, 0}, Pos{first.ID, 0}
	return e
}

func refill(c *doc.Content) {
	if len(c.Children) == 0 {
		p := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		p.SetDelta(nil)
		c.Children = []*doc.Node{p}
	}
}

func (e *Editor) Focus() { e.handle.Focus() }

func (e *Editor) Focused() bool { return e.focused }

func (e *Editor) SetWidth(w float32) {
	if w > 0 {
		e.width = w
	}
}

func (e *Editor) units() []*doc.Node { return e.walkUnits(e.hidden()) }

func (e *Editor) withSections() []*doc.Node { return e.walkUnits(nil) }

func (e *Editor) walkUnits(hidden map[string]bool) []*doc.Node {
	var out []*doc.Node
	var visit func([]*doc.Node)
	visit = func(nodes []*doc.Node) {
		for _, n := range nodes {
			if hidden[n.ID] {
				continue
			}
			out = append(out, n)
			if n.Type != "table" && !collapsed(n) {
				visit(n.Children)
			}
		}
	}
	visit(e.Content.Children)
	return out
}

func (e *Editor) index(id string) int {
	if e.order != nil {
		if i, ok := e.order[id]; ok {
			return i
		}
		return -1
	}
	return indexIn(e.units(), id)
}

func unitOrder(units []*doc.Node) map[string]int {
	order := make(map[string]int, len(units))
	for i, n := range units {
		order[n.ID] = i
	}
	return order
}

func (e *Editor) indexed(fn func()) {
	if e.order != nil {
		fn()
		return
	}
	e.order, e.depths = unitOrder(e.units()), nil
	defer func() { e.order = nil }()
	fn()
}

func indexIn(nodes []*doc.Node, id string) int {
	for i, n := range nodes {
		if n.ID == id {
			return i
		}
	}
	return -1
}

func (e *Editor) node(id string) *doc.Node { return e.Content.Find(id) }

func textLen(n *doc.Node) int {
	if n == nil || !n.IsText() {
		return 0
	}
	return n.Delta().Len()
}

func (e *Editor) before(a, b Pos) bool {
	if a.ID == b.ID {
		return a.Off < b.Off
	}
	return e.index(a.ID) < e.index(b.ID)
}

func (e *Editor) ordered() (Pos, Pos) {
	if e.before(e.focus, e.anchor) {
		return e.focus, e.anchor
	}
	return e.anchor, e.focus
}

func (e *Editor) Collapsed() bool { return e.anchor == e.focus }

func (e *Editor) blockSelected() bool {
	n := e.node(e.focus.ID)
	return e.Collapsed() && n != nil && !n.IsText()
}

func (e *Editor) Range() (Pos, Pos) { return e.ordered() }

func (e *Editor) Caret() Pos { return e.focus }

func (e *Editor) place(id string, r ui.Rect) {
	e.rel[id] = ui.Rect{X: r.X - e.root.X, Y: r.Y - e.root.Y, W: r.W, H: r.H}
}

func (e *Editor) box(id string) (ui.Rect, bool) {
	r, ok := e.rel[id]
	return ui.Rect{X: e.root.X + r.X, Y: e.root.Y + r.Y, W: r.W, H: r.H}, ok
}

func (e *Editor) FocusBlock() string { return e.focus.ID }

func (e *Editor) Select(anchor, focus Pos) {
	if anchor, focus = e.clamp(anchor), e.clamp(focus); anchor != e.anchor || focus != e.focus {
		e.BreakUndo()
	}
	e.setSelection(anchor, focus)
}

func (e *Editor) setSelection(anchor, focus Pos) {
	if e.marked {
		e.marked, e.markStep = false, false
		e.changed()
	}
	e.anchor, e.focus = e.clamp(anchor), e.clamp(focus)
	e.affinity = ui.TextDownstream
	e.hasTyping = false
	e.hasDesired = false
	e.blink = time.Now()
	e.reveal = e.focus.ID
	e.updateTrigger()
	e.selected()
}

func (e *Editor) selected() {
	if e.OnSelect != nil {
		e.OnSelect()
	}
}

func (e *Editor) PlaceCaret(id string, off int) {
	n := e.node(id)
	if n == nil {
		return
	}
	if off < 0 {
		off = textLen(n)
	}
	e.Select(Pos{id, off}, Pos{id, off})
}

func (e *Editor) clamp(p Pos) Pos {
	n := e.node(p.ID)
	if n == nil {
		us := e.units()
		if len(us) == 0 {
			return Pos{}
		}
		last := us[len(us)-1]
		return Pos{last.ID, textLen(last)}
	}
	if t := e.tableOf(p.ID); t != nil {
		return Pos{t.ID, 0}
	}
	if h := e.folding(p.ID); h != nil {
		return Pos{h.ID, textLen(h)}
	}
	p.Off = min(max(p.Off, 0), textLen(n))
	if n.IsText() {
		if start, end, ok := n.Delta().MentionAround(p.Off); ok {
			if p.Off-start <= end-p.Off {
				p.Off = start
			} else {
				p.Off = end
			}
		}
	}
	return p
}

func (e *Editor) folding(id string) *doc.Node {
	var out *doc.Node
	for loc := e.Content.Locate(id); loc != nil; {
		if h := e.folder(loc.Node.ID); h != nil {
			out = h
			loc = e.Content.Locate(h.ID)
			continue
		}
		if loc.Parent == nil {
			break
		}
		if collapsed(loc.Parent) {
			out = loc.Parent
		}
		loc = e.Content.Locate(loc.Parent.ID)
	}
	return out
}

func (e *Editor) SelectedText() (first, text string) {
	e.indexed(func() {
		a, _ := e.ordered()
		var lines []string
		for _, n := range e.selectedNodes() {
			if !n.IsText() {
				continue
			}
			from, to, _ := e.inSelection(n)
			lines = append(lines, n.Delta().Slice(from, to).Plain())
		}
		first, text = a.ID, strings.Join(lines, "\n")
	})
	return first, text
}

func (e *Editor) SelectedBlocks() []string {
	if e.Collapsed() {
		return []string{e.focus.ID}
	}
	var out []string
	e.indexed(func() {
		for _, n := range e.selectedNodes() {
			out = append(out, n.ID)
		}
	})
	return out
}

func (e *Editor) selectedNodes() []*doc.Node {
	if e.Collapsed() {
		if n := e.node(e.focus.ID); n != nil {
			return []*doc.Node{n}
		}
		return nil
	}
	var out []*doc.Node
	for _, n := range e.units() {
		if _, _, ok := e.inSelection(n); ok {
			out = append(out, n)
		}
	}
	return out
}

const maxTyped = 40

func (e *Editor) pushUndo(typing bool) {
	e.markStep = false
	now := time.Now()
	if typing && e.coalescing && e.coalesceIn == e.focus.ID && now.Sub(e.lastEdit) < 1500*time.Millisecond && e.typed < maxTyped {
		e.lastEdit = now
		return
	}
	c, size := e.freeze()
	e.undo = append(e.undo, snapshot{content: c, anchor: e.anchor, focus: e.focus, size: size})
	e.redo = nil
	e.trimHistory()
	e.coalescing, e.coalesceIn, e.typed = typing, e.focus.ID, 0
	e.lastEdit = now
}

func (e *Editor) dropStep() {
	if n := len(e.undo); n > 0 {
		e.undo = e.undo[:n-1]
	}
	e.redo, e.markStep, e.markRedo = e.markRedo, false, nil
}

func (e *Editor) BreakUndo() { e.coalescing = false }

func (e *Editor) Undo() {
	if !e.ReadOnly {
		e.step(&e.undo, &e.redo)
	}
}

func (e *Editor) Redo() {
	if !e.ReadOnly {
		e.step(&e.redo, &e.undo)
	}
}

func (e *Editor) step(from, to *[]snapshot) {
	pop := func() (snapshot, bool) {
		if len(*from) == 0 {
			return snapshot{}, false
		}
		s := (*from)[len(*from)-1]
		*from = (*from)[:len(*from)-1]
		return s, true
	}
	s, ok := pop()
	after := s.after
	if after != nil {
		s, ok = pop()
	}
	if !ok {
		return
	}
	c, size := e.freeze()
	*to = append(*to, snapshot{content: c, anchor: e.anchor, focus: e.focus, size: size})
	if after != nil {
		old := s.content
		s.content = doc.Rebase(old, after, e.Content)
		mark(from, old, 0)
	}
	e.restore(s)
	e.trimHistory()
}

func mark(steps *[]snapshot, after *doc.Content, size int) {
	if markable(*steps) {
		*steps = append(*steps, snapshot{after: after, size: size})
	}
}

func (e *Editor) markOutside() {
	if markable(e.undo) || markable(e.redo) {
		before, size := e.freeze()
		if markable(e.undo) {
			mark(&e.undo, before, size)
			size = 0
		}
		mark(&e.redo, before, size)
		e.trimHistory()
	}
}

func markable(steps []snapshot) bool {
	n := len(steps)
	return n > 0 && steps[n-1].after == nil
}

func (e *Editor) restore(s snapshot) {
	e.Content.Children = e.adopt(s.content.Children, false)
	refill(e.Content)
	e.coalescing = false
	e.marked = false
	e.Select(s.anchor, s.focus)
	e.changed()
}

func (e *Editor) changed() {
	e.gen++
	if e.building {
		e.hid, e.hidAt = e.hidden(), e.gen
		e.order, e.depths = unitOrder(e.walkUnits(e.hid)), e.blockDepths()
	} else {
		e.order = nil
	}
	e.blink = time.Now()
	if e.OnChange != nil {
		e.OnChange()
	}
	e.services.Invalidate()
}

func (e *Editor) Doc() *doc.Content {
	if e.marked {
		e.markOut = true
	}
	return e.Content
}

func (e *Editor) Peek() *doc.Content { return e.Content }

var compositions atomic.Uint64

func (e *Editor) Composition() (id uint64, out bool) {
	if !e.marked {
		return 0, false
	}
	return e.markID, e.markOut
}

func (e *Editor) Replace(c *doc.Content) {
	l := e.liftComposition(e.markOut)
	e.markOutside()
	e.Content.Children = e.adopt(c.Children, true)
	e.Content.Blocks, e.Content.Deleted = c.Blocks, c.Deleted
	e.order = nil
	e.gen++
	e.coalescing = false
	refill(e.Content)
	e.keepSelection()
	e.putBack(l)
	e.services.Invalidate()
}

func (e *Editor) ApplyOps(ops []doc.Op) error {
	l := e.liftComposition(false)
	e.pushUndo(false)
	e.coalescing = false
	var first error
	for _, o := range ops {
		if err := e.Content.Apply(o); err != nil && first == nil {
			first = err
		}
	}
	refill(e.Content)
	e.keepSelection()
	e.putBack(l)
	e.changed()
	return first
}

func (e *Editor) keepSelection() {
	reveal := e.reveal
	e.Select(e.anchor, e.focus)
	e.reveal = reveal
}

func (e *Editor) ApplyOp(o doc.Op, newStep bool) error { return e.ApplyOpFrom(o, newStep, 0) }

func (e *Editor) ApplyOpFrom(o doc.Op, newStep bool, read uint64) error {
	l := e.liftComposition(read != 0 && read == e.markID)
	if newStep {
		e.pushUndo(false)
		e.coalescing = false
		e.runStep = e.undo[len(e.undo)-1].content
	} else if n := len(e.undo); n == 0 || e.undo[n-1].content != e.runStep {
		e.markOutside()
		e.coalescing = false
	}
	err := e.Content.Apply(o)
	refill(e.Content)
	e.keepSelection()
	e.putBack(l)
	e.changed()
	return err
}

func (e *Editor) Edit(fn func(c *doc.Content)) {
	e.pushUndo(false)
	e.coalescing = false
	fn(e.Content)
	refill(e.Content)
	e.Select(e.anchor, e.focus)
	e.changed()
}

func (e *Editor) SetData(id, key string, v any) {
	n := e.node(id)
	if n == nil || e.ReadOnly {
		return
	}
	n.Set(key, v)
	e.invalidate(id)
	e.changed()
}

func (e *Editor) Trigger() *Trigger { return e.trigger }

func (e *Editor) CloseMenu() { e.trigger = nil }

func (e *Editor) RemoveTrigger(t Trigger) {
	n := e.node(t.ID)
	if n == nil {
		return
	}
	end := min(t.Off+1+utf8.RuneCountInString(t.Query), textLen(n))
	n.SetDelta(n.Delta().Delete(t.Off, end))
	e.invalidate(t.ID)
	e.trigger = nil
	e.Select(Pos{t.ID, t.Off}, Pos{t.ID, t.Off})
	e.changed()
}

func (e *Editor) invalidate(id string) { delete(e.layouts, id) }

func (e *Editor) updateTrigger() {
	if e.trigger == nil {
		return
	}
	t := e.trigger
	n := e.node(t.ID)
	if n == nil || !e.Collapsed() || e.focus.ID != t.ID || e.focus.Off <= t.Off {
		e.trigger = nil
		return
	}
	runes := []rune(n.Delta().Plain())
	if t.Off >= len(runes) || shortcut.MenuKind(runes[t.Off]) != t.Kind {
		e.trigger = nil
		return
	}
	q := string(runes[t.Off+1 : min(e.focus.Off, len(runes))])
	first, _ := utf8.DecodeRuneInString(q)
	if q != "" && unicode.IsSpace(first) || utf8.RuneCountInString(q) > 40 || (t.Kind == '@' && len(q) > 0 && q[len(q)-1] == ' ' && utf8.RuneCountInString(q) > 24) {
		e.trigger = nil
		return
	}
	if q != t.Query {
		t.Query = q
		e.menuIndex = 0
	}
}
