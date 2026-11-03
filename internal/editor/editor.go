// Package editor is Leaflet's block editor: a page of blocks with stable
// IDs (internal/doc), edited in one control that owns the selection across
// blocks, input-method composition and undo, drawn with MyGo's text
// layouts. Charts, tables, embeds, recordings and subpages are blocks drawn
// with widgets of their own.
package editor

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/menu"
	"leaflet/internal/shortcut"
)

// Pos is a place in the page: a block and a rune offset in its text. On a
// block without text, offset 0 selects the whole block.
type Pos struct {
	ID  string
	Off int
}

// MenuItem is a choice of the / or @ menu.
type MenuItem = menu.Item

// Trigger is an open / or @ menu: where its character was typed and what
// was typed after it.
type Trigger struct {
	// Kind is '/' or '@', the menu its character opens, as typed or one
	// shortcut.MenuKind takes for it, as "／".
	Kind  rune
	ID    string
	Off   int
	Query string
}

// Host is what the editor needs from the app: pages, images, embeds,
// videos, recordings, comments and agents.
type Host interface {
	PageTitle(id string) string
	OpenPage(id string)
	// Image returns an image block's picture, a bitmap or an SVG, or nil
	// while it loads or when it failed to load, as failed says; for an
	// animated GIF, also its player, once made, which plays it in the
	// block.
	Image(n *doc.Node) (pic ui.ImageSource, video *ui.VideoPlayer, failed bool)
	// Embed builds the web view of an embed block.
	Embed(c *ui.Context, n *doc.Node, width float32)
	// Video builds a video block.
	Video(c *ui.Context, n *doc.Node, width float32)
	// Card builds a link card or a file's card.
	Card(c *ui.Context, n *doc.Node)
	// Recording builds a recording block.
	Recording(c *ui.Context, n *doc.Node)
	// CommentCount is the number of open threads on a block.
	CommentCount(blockID string) int
	// OpenComments shows a block's threads in the Comments panel, and
	// CommentPreview builds them in brief, for its badge's hover.
	OpenComments(blockID string)
	CommentPreview(c *ui.Context, blockID string)
	// SlashItems are the / menu's choices for a query; MentionItems the @
	// menu's.
	SlashItems(query string) []MenuItem
	MentionItems(query string) []MenuItem
	// Pick runs a menu choice. The trigger's text is still in the block.
	Pick(t Trigger, item MenuItem)
	// Comment and AskAgent act on the selected text of a block.
	Comment(blockID, quote string)
	AskAgent(blockID, quote string)
	// EditBlock opens what a block is made from: a chart's data, a math
	// or diagram block's source, an image's or video's link, or a picker
	// for another file in place of an uploaded image.
	EditBlock(blockID string)
	// OpenBlock opens a page at one of its blocks, for a link to a block.
	OpenBlock(pageID, blockID string)
	// BlockLink is the link to a block of this page.
	BlockLink(blockID string) string
}

// snapshot is a step of undo or redo: the page as it was, to go back to.
// One with after marks a change from outside, as sync brings: after is
// the page as the steps under it left it, before that change, which
// going back to one of them keeps.
type snapshot struct {
	content *doc.Content
	anchor  Pos
	focus   Pos
	after   *doc.Content
}

// Editor is the state of a page's editor, kept from frame to frame.
type Editor struct {
	Content *doc.Content
	Host    Host
	// Size is the size of the page's text, whose fonts theme.PageFamily
	// and theme.CodeFamily choose.
	Size float32
	// ReadOnly shows the page without letting the user change it.
	ReadOnly bool
	// Tinted blocks were changed by the latest agent run.
	Tinted map[string]bool
	// OnChange runs after each change of the content.
	OnChange func()

	anchor, focus Pos
	// affinity is the caret's side where its place shows twice, as at the
	// end of a line that wraps, which is the start of the next: upstream,
	// it shows after the text before it.
	affinity ui.TextAffinity
	// typing holds formats toggled at a collapsed caret.
	typing    doc.Attrs
	hasTyping bool
	// marked is an input method's composition, in the focus block.
	// markStep is set while the step of undo on top is the one it began
	// with and changes nothing but it, and markRedo the steps of redo that
	// step cleared, to keep when it is given up.
	marked     bool
	markStart  int
	markEnd    int
	markStep   bool
	markRedo   []snapshot
	undo, redo []snapshot
	// runStep is the page the step of undo an agent run's ops began with
	// keeps, to tell that step from those made since.
	runStep  *doc.Content
	lastEdit time.Time
	// coalescing is set while typing joins the undo step open, which
	// began in block coalesceIn, and typed is how many characters typing
	// has put in it.
	coalescing bool
	coalesceIn string
	typed      int

	layouts map[string]*blockLayout
	// looks are what the text blocks' texts give their layouts (lookOf).
	looks map[string]*look
	// rel is each block's box as last drawn, relative to root, the page's
	// box as last drawn: box puts it in the window, for hit-testing, where
	// the page has moved it since, as a block scrolled out of view is not
	// drawn. laid is each block's box in the window as the last frame
	// laid it out, drawn or not: a block never drawn has no rel. Each
	// build keeps them, and the layouts and looks, for the blocks it
	// builds alone.
	rel     map[string]ui.Rect
	laid    map[string]ui.Rect
	root    ui.Rect
	width   float32
	focused bool

	dragging   bool
	desiredX   float32
	hasDesired bool
	reveal     string
	blink      time.Time

	trigger   *Trigger
	menuIndex int
	// menuItems are the choices of the menu open, as the host gave them
	// for menuFor.
	menuItems []MenuItem
	menuFor   Trigger
	linkOpen  bool
	linkDraft string
	// copied is the code block whose code was copied last, at copiedAt.
	copied   string
	copiedAt time.Time
	// plainCopiedAt is when the toolbar last copied the selection as
	// plain text.
	plainCopiedAt time.Time

	// cellEdit is the table cell being edited, as cellKey names it: it
	// shows its Markdown in an input, and the others their text
	// formatted. cellFocused is set once that input has had the focus,
	// to tell when it loses it; cellMove is the move Tab, Shift-Tab or
	// Enter asked for in it, made as its table builds.
	cellEdit    string
	cellFocused bool
	cellMove    int
	// cellText is the Markdown typed in the cell being edited, cellFor,
	// kept as typed: read back from the cell's text, a "[" or a "*" not
	// yet closed would show escaped under the caret. cellSaved is that
	// text as the cell stored it, to tell a change made elsewhere (undo,
	// another device), which the input then shows.
	cellText, cellFor, cellSaved string

	// hid are the blocks folded headings hide, as the page last built;
	// menuBlock is the block a context menu was opened on, and menuAt
	// where in it.
	hid map[string]bool
	// order is each unit's place in the page and depths each block's
	// depth, kept while the blocks build unless a change comes first:
	// each block asks where it is against the selection and how deep it
	// is, which walking the page for would make a frame quadratic.
	order  map[string]int
	depths map[string]int
	// building is set while the blocks build: a change made then, as
	// typing in a table's cell, finds order and depths again for the
	// blocks after it.
	building  bool
	menuBlock string
	menuAt    Pos
	// bar is the selection toolbar, kept while the focus is in it or its
	// colors are open.
	bar       ui.Handle
	colorOpen bool
	// dueOpen is the to-do whose calendar is open, and dues the drafts of
	// the to-dos whose calendars are open or closing, saved as they close;
	// chips are where the to-dos' dates and the blocks' comment badges
	// show, which take presses.
	dueOpen string
	dues    map[string]*dueDraft
	chips   map[string]ui.Rect
	// peek is the block whose comments show under its badge, peekOver
	// whether the pointer is on them, and peekLeft when it left both;
	// peekShut is the badge clicked, which shows none until the pointer
	// leaves it.
	peek     string
	peekOver bool
	peekLeft time.Time
	peekShut string

	handle   ui.Handle
	services ui.Services
}

// New returns an editor of content.
func New(content *doc.Content, host Host) *Editor {
	e := &Editor{Content: content, Host: host, Size: 17,
		layouts: map[string]*blockLayout{}, looks: map[string]*look{}, rel: map[string]ui.Rect{}, laid: map[string]ui.Rect{}, chips: map[string]ui.Rect{}, width: 680}
	refill(content)
	first := e.units()[0]
	e.anchor, e.focus = Pos{first.ID, 0}, Pos{first.ID, 0}
	return e
}

// refill puts an empty paragraph in a page that has no blocks left: the
// caret is always in one.
func refill(c *doc.Content) {
	if len(c.Children) == 0 {
		p := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		p.SetDelta(nil)
		c.Children = []*doc.Node{p}
	}
}

// Focus asks for the keyboard focus.
func (e *Editor) Focus() { e.handle.Focus() }

// Focused reports whether the editor had the focus as it last built.
func (e *Editor) Focused() bool { return e.focused }

// SetWidth sets the width the text wraps at.
func (e *Editor) SetWidth(w float32) {
	if w != e.width && w > 0 {
		e.width = w
		e.layouts = map[string]*blockLayout{}
	}
}

// units are the blocks the caret goes through, in document order: text
// blocks and blocks selected whole, nested list items after their parent.
// Table cells belong to their table, and a folded heading's section is
// out of the way.
func (e *Editor) units() []*doc.Node { return e.walkUnits(e.hidden()) }

// withSections are the units with the sections of folded headings: what a
// selection over a folded heading copies and deletes.
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
			// A folded toggle's children are out of the caret's way.
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

// unitOrder is each unit's place among units.
func unitOrder(units []*doc.Node) map[string]int {
	order := make(map[string]int, len(units))
	for i, n := range units {
		order[n.ID] = i
	}
	return order
}

// indexed runs fn with each unit's place in the page known, as it is while
// the blocks build: what asks where many blocks are against the selection,
// as the selection toolbar's Comment and Ask agent do, then walks the page
// once, not once for each block.
func (e *Editor) indexed(fn func()) {
	if e.order != nil {
		fn()
		return
	}
	// The depths of the frame before may be out of date.
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

// before reports whether a comes before b in the page.
func (e *Editor) before(a, b Pos) bool {
	if a.ID == b.ID {
		return a.Off < b.Off
	}
	return e.index(a.ID) < e.index(b.ID)
}

// ordered returns the selection's start and end.
func (e *Editor) ordered() (Pos, Pos) {
	if e.before(e.focus, e.anchor) {
		return e.focus, e.anchor
	}
	return e.anchor, e.focus
}

// Collapsed reports whether the selection is a caret.
func (e *Editor) Collapsed() bool { return e.anchor == e.focus }

// blockSelected reports whether the selection is a block selected whole,
// one without text: a caret on it, which copies and cuts the block.
func (e *Editor) blockSelected() bool {
	n := e.node(e.focus.ID)
	return e.Collapsed() && n != nil && !n.IsText()
}

// Range returns the selection's start and end.
func (e *Editor) Range() (Pos, Pos) { return e.ordered() }

// Caret returns where the caret is.
func (e *Editor) Caret() Pos { return e.focus }

// place records a block's box as drawn.
func (e *Editor) place(id string, r ui.Rect) {
	e.rel[id] = ui.Rect{X: r.X - e.root.X, Y: r.Y - e.root.Y, W: r.W, H: r.H}
}

// box is a block's box in the window, as last drawn, moved with the page
// since; false for one not drawn.
func (e *Editor) box(id string) (ui.Rect, bool) {
	r, ok := e.rel[id]
	return ui.Rect{X: e.root.X + r.X, Y: e.root.Y + r.Y, W: r.W, H: r.H}, ok
}

// FocusBlock is the block the caret is in.
func (e *Editor) FocusBlock() string { return e.focus.ID }

// Select sets the selection. A caret moved elsewhere ends the step of
// undo typing makes, as a click or an arrow key does.
func (e *Editor) Select(anchor, focus Pos) {
	if anchor, focus = e.clamp(anchor), e.clamp(focus); anchor != e.anchor || focus != e.focus {
		e.BreakUndo()
	}
	e.setSelection(anchor, focus)
}

// setSelection sets the selection, keeping the step of undo typing makes:
// as typing that takes text out moves the caret.
func (e *Editor) setSelection(anchor, focus Pos) {
	if e.marked {
		// Moving the caret ends a composition where it is, keeping its
		// text, as a click does in other editors: composing on starts
		// afresh at the caret, and never writes over another block.
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
}

// PlaceCaret puts the caret in a block, at its end when off is negative.
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
	// A table's cells are edited in an input of their own, never with the
	// caret: one asked for in a cell selects its table.
	if t := e.tableOf(p.ID); t != nil {
		return Pos{t.ID, 0}
	}
	// A change from outside can fold the block away: the caret goes to the
	// end of what folds it, as folding does.
	if h := e.folding(p.ID); h != nil {
		return Pos{h.ID, textLen(h)}
	}
	p.Off = min(max(p.Off, 0), textLen(n))
	// A mention is one unit: the caret goes to its nearer end.
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

// folding returns the shown block that folds block id away: the toggle it
// is in, or the heading whose folded section holds it, or one that folds
// those away in turn; nil when it shows.
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

// SelectedText returns the selection's first block, and the selected text
// of the blocks it spans, a line each.
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

// SelectedBlocks returns the blocks the selection spans, in order: the
// caret's block when it is collapsed.
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

// selectedNodes are the blocks of SelectedBlocks.
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

// maxTyped is the most characters typing puts in one step of undo.
const maxTyped = 40

// pushUndo keeps the page as it is before an edit. Typing in one block
// within a second and a half joins the step before it, unless the caret
// was moved in between, up to the end of a word (InsertText) or maxTyped
// characters: a step takes back a word or so, not all typed in one go.
func (e *Editor) pushUndo(typing bool) {
	e.markStep = false
	now := time.Now()
	if typing && e.coalescing && e.coalesceIn == e.focus.ID && now.Sub(e.lastEdit) < 1500*time.Millisecond && e.typed < maxTyped {
		e.lastEdit = now
		return
	}
	e.undo = append(e.undo, snapshot{content: e.Content.Copy(), anchor: e.anchor, focus: e.focus})
	if len(e.undo) > 200 {
		e.undo = e.undo[len(e.undo)-200:]
		if e.undo[0].after != nil {
			e.undo = e.undo[1:]
		}
	}
	e.redo = nil
	e.coalescing, e.coalesceIn, e.typed = typing, e.focus.ID, 0
	e.lastEdit = now
}

// dropStep takes back the step of undo a composition began with, once the
// page is as that step keeps it again, and brings back the steps of redo
// it cleared.
func (e *Editor) dropStep() {
	if n := len(e.undo); n > 0 {
		e.undo = e.undo[:n-1]
	}
	e.redo, e.markStep, e.markRedo = e.markRedo, false, nil
}

// BreakUndo makes the next edit a step of its own.
func (e *Editor) BreakUndo() { e.coalescing = false }

// Undo goes back a step. On a page that has become read-only it changes
// nothing, as every other edit: the steps kept from before would put back
// the page as it was then, and a later save would store it.
func (e *Editor) Undo() {
	if !e.ReadOnly {
		e.step(&e.undo, &e.redo)
	}
}

// Redo goes forward a step, as Undo goes back.
func (e *Editor) Redo() {
	if !e.ReadOnly {
		e.step(&e.redo, &e.undo)
	}
}

// step goes to the page of the step on top of from, keeping the page as
// it is on to. Past a change from outside, it takes back only what the
// step changed, and what came from outside stays.
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
	*to = append(*to, snapshot{content: e.Content.Copy(), anchor: e.anchor, focus: e.focus})
	if after != nil {
		old := s.content
		s.content = doc.Rebase(old, after, e.Content)
		// The steps under it are now under the page as it was, with
		// what came from outside.
		mark(from, old)
	}
	e.restore(s)
}

// mark marks a change from outside on top of steps, made to the page
// after, as the steps left it. Under a mark already there, the steps are
// under that one's page, which a change since only adds to.
func mark(steps *[]snapshot, after *doc.Content) {
	if markable(*steps) {
		*steps = append(*steps, snapshot{after: after})
	}
}

// markable reports whether mark would mark steps: they have a step on
// top that is not a mark.
func markable(steps []snapshot) bool {
	n := len(steps)
	return n > 0 && steps[n-1].after == nil
}

func (e *Editor) restore(s snapshot) {
	e.Content.Children = s.content.Copy().Children
	e.coalescing = false
	e.marked = false
	e.layouts = map[string]*blockLayout{}
	e.Select(s.anchor, s.focus)
	e.changed()
}

func (e *Editor) changed() {
	if e.building {
		e.hid = e.hidden()
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

// Doc is the content the editor shows, with what is being typed: Content,
// for what takes the page as an agents.LiveDoc.
func (e *Editor) Doc() *doc.Content { return e.Content }

// Replace swaps in content from outside the editor (sync, restores),
// keeping the caret where it can, and the undo steps: going back one
// takes back what it changed on the page as it is, keeping the change.
func (e *Editor) Replace(c *doc.Content) {
	l := e.liftComposition()
	before := e.Content.Copy()
	mark(&e.undo, before)
	mark(&e.redo, before)
	e.Content.Children = c.Children
	e.Content.Blocks, e.Content.Deleted = c.Blocks, c.Deleted
	e.order = nil
	e.layouts = map[string]*blockLayout{}
	e.coalescing = false
	refill(e.Content)
	e.keepSelection()
	e.putBack(l)
	e.services.Invalidate()
}

// ApplyOps applies an agent run's ops as one undo step.
func (e *Editor) ApplyOps(ops []doc.Op) error {
	l := e.liftComposition()
	e.pushUndo(false)
	e.coalescing = false
	var first error
	for _, o := range ops {
		if err := e.Content.Apply(o); err != nil && first == nil {
			first = err
		}
	}
	e.layouts = map[string]*blockLayout{}
	refill(e.Content)
	e.keepSelection()
	e.putBack(l)
	e.changed()
	return first
}

// keepSelection keeps the selection in the page after ops from outside
// change it, without scrolling to it: the user's view stays theirs while
// an agent writes elsewhere.
func (e *Editor) keepSelection() {
	reveal := e.reveal
	e.Select(e.anchor, e.focus)
	e.reveal = reveal
}

// ApplyOp applies one op of an agent run as it streams in. The first op of
// a run starts an undo step that the rest join, while it is the step on
// top: past an edit of the user's since, the op is a change from outside,
// which undoing that edit keeps.
func (e *Editor) ApplyOp(o doc.Op, newStep bool) error {
	l := e.liftComposition()
	if newStep {
		e.pushUndo(false)
		e.coalescing = false
		e.runStep = e.undo[len(e.undo)-1].content
	} else if n := len(e.undo); n == 0 || e.undo[n-1].content != e.runStep {
		// Past the first op since that edit, the steps have their mark
		// already: the page is copied only for one to take.
		if markable(e.undo) || markable(e.redo) {
			before := e.Content.Copy()
			mark(&e.undo, before)
			mark(&e.redo, before)
		}
		e.coalescing = false
	}
	err := e.Content.Apply(o)
	e.layouts = map[string]*blockLayout{}
	refill(e.Content)
	e.keepSelection()
	e.putBack(l)
	e.changed()
	return err
}

// Edit runs fn on the content as one undo step, as a menu choice does.
func (e *Editor) Edit(fn func(c *doc.Content)) {
	e.pushUndo(false)
	e.coalescing = false
	fn(e.Content)
	e.layouts = map[string]*blockLayout{}
	// Taking out the page's only block, as its one subpage's card, leaves
	// it none.
	refill(e.Content)
	e.Select(e.anchor, e.focus)
	e.changed()
}

// SetData sets a block's data outside the undo history: what the app
// learns about a block after it is in the page, as a video's title.
func (e *Editor) SetData(id, key string, v any) {
	n := e.node(id)
	if n == nil || e.ReadOnly {
		return
	}
	n.Set(key, v)
	e.invalidate(id)
	e.changed()
}

// Trigger returns the open / or @ menu, if any.
func (e *Editor) Trigger() *Trigger { return e.trigger }

// CloseMenu closes the / or @ menu.
func (e *Editor) CloseMenu() { e.trigger = nil }

// RemoveTrigger deletes a trigger's character and query from its block.
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

// updateTrigger opens, follows or closes the / and @ menus as the text
// and caret change.
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
	// A space right after the "/" or "@" is writing, as "1 / 2" or "a @ b",
	// not a search: the menu goes, as the iOS editor's does, and Enter
	// breaks the line.
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
