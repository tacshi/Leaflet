// Package editor is Leaflet's block editor: a page of blocks with stable
// IDs (internal/doc), edited in one control that owns the selection across
// blocks, input-method composition and undo, drawn with MyGo's text
// layouts. Charts, tables, embeds, recordings and subpages are blocks drawn
// with widgets of their own.
package editor

import (
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
	// Image returns an image block's picture, or nil while it loads.
	Image(n *doc.Node) *ui.Bitmap
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
	OpenComments(blockID string)
	// SlashItems are the / menu's choices for a query; MentionItems the @
	// menu's.
	SlashItems(query string) []MenuItem
	MentionItems(query string) []MenuItem
	// Pick runs a menu choice. The trigger's text is still in the block.
	Pick(t Trigger, item MenuItem)
	// Comment and AskAgent act on the selected text of a block.
	Comment(blockID, quote string)
	AskAgent(blockID, quote string)
	// EditBlock opens what a block is made from: a chart's data, or a math
	// or diagram block's source.
	EditBlock(blockID string)
	// OpenBlock opens a page at one of its blocks, for a link to a block.
	OpenBlock(pageID, blockID string)
	// BlockLink is the link to a block of this page.
	BlockLink(blockID string) string
}

type snapshot struct {
	content *doc.Content
	anchor  Pos
	focus   Pos
}

// Editor is the state of a page's editor, kept from frame to frame.
type Editor struct {
	Content *doc.Content
	Host    Host
	// Font is the family and size of the page's text.
	Family string
	Size   float32
	// LineHeight is the height of a line of prose, as a multiple of Size;
	// 0 takes the font's own. Headings and code keep their font's own.
	LineHeight float32
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
	marked     bool
	markStart  int
	markEnd    int
	undo, redo []snapshot
	lastEdit   time.Time
	coalescing bool

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

	// hid are the blocks folded headings hide, as the page last built;
	// menuBlock is the block a context menu was opened on.
	hid       map[string]bool
	menuBlock string
	// bar is the selection toolbar, kept while the focus is in it or its
	// colors are open.
	bar       ui.Handle
	colorOpen bool
	// dueOpen is the to-do whose calendar is open; chips are where the
	// to-dos' dates show, which take presses.
	dueOpen string
	chips   map[string]ui.Rect

	handle   ui.Handle
	services ui.Services
}

// New returns an editor of content.
func New(content *doc.Content, host Host) *Editor {
	e := &Editor{Content: content, Host: host, Family: "Source Serif 4", Size: 17,
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

func (e *Editor) index(id string) int { return indexIn(e.units(), id) }

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
	p.Off = min(max(p.Off, 0), textLen(n))
	return p
}

// SelectedText returns the selected text of the focus block, and the
// block.
func (e *Editor) SelectedText() (string, string) {
	a, b := e.ordered()
	n := e.node(a.ID)
	if n == nil || !n.IsText() {
		return a.ID, ""
	}
	end := textLen(n)
	if b.ID == a.ID {
		end = b.Off
	}
	return a.ID, n.Delta().Slice(a.Off, end).Plain()
}

// pushUndo keeps the page as it is before an edit. Typing in one block
// within a second and a half joins the step before it.
func (e *Editor) pushUndo(typing bool) {
	now := time.Now()
	if typing && e.coalescing && now.Sub(e.lastEdit) < 1500*time.Millisecond {
		e.lastEdit = now
		return
	}
	e.undo = append(e.undo, snapshot{content: e.Content.Copy(), anchor: e.anchor, focus: e.focus})
	if len(e.undo) > 200 {
		e.undo = e.undo[len(e.undo)-200:]
	}
	e.redo = nil
	e.coalescing = typing
	e.lastEdit = now
}

// BreakUndo makes the next edit a step of its own.
func (e *Editor) BreakUndo() { e.coalescing = false }

// CanUndo and CanRedo report whether there are steps to go back or forward.
func (e *Editor) CanUndo() bool { return len(e.undo) > 0 }
func (e *Editor) CanRedo() bool { return len(e.redo) > 0 }

// Undo goes back a step.
func (e *Editor) Undo() {
	if len(e.undo) == 0 {
		return
	}
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.redo = append(e.redo, snapshot{content: e.Content.Copy(), anchor: e.anchor, focus: e.focus})
	e.restore(s)
}

// Redo goes forward a step.
func (e *Editor) Redo() {
	if len(e.redo) == 0 {
		return
	}
	s := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.undo = append(e.undo, snapshot{content: e.Content.Copy(), anchor: e.anchor, focus: e.focus})
	e.restore(s)
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
	e.blink = time.Now()
	if e.OnChange != nil {
		e.OnChange()
	}
	e.services.Invalidate()
}

// Replace swaps in content from outside the editor (sync, restores),
// keeping the caret where it can. The undo steps go: they are whole pages
// from before the change, so going back to one would undo it too.
func (e *Editor) Replace(c *doc.Content) {
	e.Content.Children = c.Children
	e.Content.Blocks, e.Content.Deleted = c.Blocks, c.Deleted
	e.layouts = map[string]*blockLayout{}
	e.marked = false
	e.undo, e.redo = nil, nil
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
// a run starts an undo step that the rest join.
func (e *Editor) ApplyOp(o doc.Op, newStep bool) error {
	if newStep {
		e.pushUndo(false)
		e.coalescing = false
	}
	err := e.Content.Apply(o)
	e.layouts = map[string]*blockLayout{}
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
