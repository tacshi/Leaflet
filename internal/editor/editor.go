// Package editor is Leaflet's block editor: a page of blocks with stable
// IDs (internal/doc), edited in one control that owns the selection across
// blocks, input-method composition and undo, drawn with MyGo's text
// layouts. Charts, tables, embeds, recordings and subpages are blocks drawn
// with widgets of their own.
package editor

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

// Pos is a place in the page: a block and a rune offset in its text. On a
// block without text, offset 0 selects the whole block.
type Pos struct {
	ID  string
	Off int
}

// MenuItem is a choice of the / or @ menu.
type MenuItem struct {
	Key   string
	Label string
	Hint  string
	Icon  string
	// Group heads a run of items, as "Agents".
	Group string
}

// Trigger is an open / or @ menu: where its character was typed and what
// was typed after it.
type Trigger struct {
	Kind  rune // '/' or '@'
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
	// began in block coalesceIn.
	coalescing bool
	coalesceIn string

	layouts map[string]*blockLayout
	// geom is each block's box in the window, for hit-testing; rel is the
	// same box relative to root, which keeps blocks scrolled out of view
	// (and so not drawn) where the page has moved them.
	geom    map[string]ui.Rect
	rel     map[string]ui.Rect
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
	menuItems []MenuItem
	linkOpen  bool
	linkDraft string
	// copied is the code block whose code was copied last, at copiedAt.
	copied   string
	copiedAt time.Time

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
	order     map[string]int
	depths    map[string]int
	menuBlock string
	menuAt    Pos
	// bar is the selection toolbar, kept while the focus is in it or its
	// colors are open.
	bar       ui.Handle
	colorOpen bool
	// dueOpen is the to-do whose calendar is open, and dueDraft the date
	// in it, with its time of day when dueTimed, which is saved as it
	// closes when dueEdited; chips are where the to-dos' dates and the
	// blocks' comment badges show, which take presses.
	dueOpen   string
	dueDraft  time.Time
	dueTimed  bool
	dueEdited bool
	chips     map[string]ui.Rect
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
		layouts: map[string]*blockLayout{}, geom: map[string]ui.Rect{}, rel: map[string]ui.Rect{}, chips: map[string]ui.Rect{}, width: 680}
	if len(content.Children) == 0 {
		p := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		p.SetDelta(nil)
		content.Children = append(content.Children, p)
	}
	first := e.units()[0]
	e.anchor, e.focus = Pos{first.ID, 0}, Pos{first.ID, 0}
	return e
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
	e.geom[id] = r
	e.rel[id] = ui.Rect{X: r.X - e.root.X, Y: r.Y - e.root.Y, W: r.W, H: r.H}
}

// FocusBlock is the block the caret is in.
func (e *Editor) FocusBlock() string { return e.focus.ID }

// Select sets the selection.
func (e *Editor) Select(anchor, focus Pos) {
	if e.marked {
		// Moving the caret ends a composition where it is, keeping its
		// text, as a click does in other editors: composing on starts
		// afresh at the caret, and never writes over another block.
		e.marked, e.markStep = false, false
		e.changed()
	}
	e.anchor, e.focus = e.clamp(anchor), e.clamp(focus)
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
func (e *Editor) SelectedText() (string, string) {
	a, _ := e.ordered()
	var lines []string
	for _, id := range e.SelectedBlocks() {
		n := e.node(id)
		if n == nil || !n.IsText() {
			continue
		}
		from, to, _ := e.inSelection(n)
		lines = append(lines, n.Delta().Slice(from, to).Plain())
	}
	return a.ID, strings.Join(lines, "\n")
}

// SelectedBlocks returns the blocks the selection spans, in order: the
// caret's block when it is collapsed.
func (e *Editor) SelectedBlocks() []string {
	if e.Collapsed() {
		return []string{e.focus.ID}
	}
	var out []string
	for _, n := range e.units() {
		if _, _, ok := e.inSelection(n); ok {
			out = append(out, n.ID)
		}
	}
	return out
}

// pushUndo keeps the page as it is before an edit. Typing in one block
// within a second and a half joins the step before it, unless the caret
// was moved in between.
func (e *Editor) pushUndo(typing bool) {
	e.markStep = false
	now := time.Now()
	if typing && e.coalescing && e.coalesceIn == e.focus.ID && now.Sub(e.lastEdit) < 1500*time.Millisecond {
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
	e.coalescing, e.coalesceIn = typing, e.focus.ID
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

// CanUndo and CanRedo report whether there are steps to go back or forward.
func (e *Editor) CanUndo() bool { return len(e.undo) > 0 }
func (e *Editor) CanRedo() bool { return len(e.redo) > 0 }

// Undo goes back a step.
func (e *Editor) Undo() { e.step(&e.undo, &e.redo) }

// Redo goes forward a step.
func (e *Editor) Redo() { e.step(&e.redo, &e.undo) }

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
	e.order = nil
	e.blink = time.Now()
	if e.OnChange != nil {
		e.OnChange()
	}
	e.services.Invalidate()
}

// Replace swaps in content from outside the editor (sync, restores),
// keeping the caret where it can, and the undo steps: going back one
// takes back what it changed on the page as it is, keeping the change.
func (e *Editor) Replace(c *doc.Content) {
	before := e.Content.Copy()
	mark(&e.undo, before)
	mark(&e.redo, before)
	e.Content.Children = c.Children
	e.Content.Blocks, e.Content.Deleted = c.Blocks, c.Deleted
	e.order = nil
	e.layouts = map[string]*blockLayout{}
	e.marked = false
	e.coalescing = false
	if len(e.Content.Children) == 0 {
		p := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		p.SetDelta(nil)
		e.Content.Children = []*doc.Node{p}
	}
	e.Select(e.anchor, e.focus)
	e.services.Invalidate()
}

// ApplyOps applies an agent run's ops as one undo step.
func (e *Editor) ApplyOps(ops []doc.Op) error {
	e.pushUndo(false)
	e.coalescing = false
	var first error
	for _, o := range ops {
		if err := e.Content.Apply(o); err != nil && first == nil {
			first = err
		}
	}
	e.layouts = map[string]*blockLayout{}
	e.marked = false
	if len(e.Content.Children) == 0 {
		p := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		p.SetDelta(nil)
		e.Content.Children = []*doc.Node{p}
	}
	e.Select(e.anchor, e.focus)
	e.changed()
	return first
}

// ApplyOp applies one op of an agent run as it streams in. The first op of
// a run starts an undo step that the rest join, while it is the step on
// top: past an edit of the user's since, the op is a change from outside,
// which undoing that edit keeps.
func (e *Editor) ApplyOp(o doc.Op, newStep bool) error {
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
	e.marked = false
	if len(e.Content.Children) == 0 {
		p := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		p.SetDelta(nil)
		e.Content.Children = []*doc.Node{p}
	}
	e.Select(e.anchor, e.focus)
	e.changed()
	return err
}

// Edit runs fn on the content as one undo step, as a menu choice does.
func (e *Editor) Edit(fn func(c *doc.Content)) {
	e.pushUndo(false)
	e.coalescing = false
	fn(e.Content)
	e.layouts = map[string]*blockLayout{}
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
	if t.Off >= len(runes) || runes[t.Off] != t.Kind {
		e.trigger = nil
		return
	}
	q := string(runes[t.Off+1 : min(e.focus.Off, len(runes))])
	if utf8.RuneCountInString(q) > 40 || (t.Kind == '@' && len(q) > 0 && q[len(q)-1] == ' ' && len(q) > 24) {
		e.trigger = nil
		return
	}
	if q != t.Query {
		t.Query = q
		e.menuIndex = 0
	}
}
