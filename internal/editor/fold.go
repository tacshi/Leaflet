package editor

import (
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/rules"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// A folded heading hides its section: the blocks after it among its
// siblings, up to the next heading of its level or higher. They stay in
// the page, out of the caret's way and the eye's, and go with the heading
// when a selection over it is copied or deleted.

func foldedHeading(n *doc.Node) bool { return n.Type == "heading" && n.Bool("collapsed") }

func headingLevel(n *doc.Node) int { return n.Int("level", 1) }

// ends reports whether block n ends the section of heading h.
func ends(n, h *doc.Node) bool { return n.Type == "heading" && headingLevel(n) <= headingLevel(h) }

// hidden returns the blocks folds hide (rules.Hidden): those in a closed
// toggle, and in the section of a folded heading, with the blocks under
// them.
func (e *Editor) hidden() map[string]bool {
	size := len(e.shaped.entries)
	nodes := make([]*doc.Node, 0, size)
	folds := make([]rules.Fold, 0, size)
	var visit func(nodes []*doc.Node, depth int)
	visit = func(ns []*doc.Node, depth int) {
		for _, n := range ns {
			nodes = append(nodes, n)
			folds = append(folds, rules.Fold{Type: n.Type, Depth: depth, Level: headingLevel(n), Collapsed: n.Bool("collapsed")})
			// A table's cells are the table's.
			if n.Type != "table" {
				visit(n.Children, depth+1)
			}
		}
	}
	visit(e.Content.Children, 0)
	hidden := rules.Hidden(folds)
	count := 0
	for _, h := range hidden {
		if h {
			count++
		}
	}
	out := make(map[string]bool, count)
	for i, h := range hidden {
		if h {
			out[nodes[i].ID] = true
		}
	}
	return out
}

// folder returns the folded heading that hides block id, or a block it is
// in; nil when it shows.
func (e *Editor) folder(id string) *doc.Node {
	for loc := e.Content.Locate(id); loc != nil; {
		var by *doc.Node
		for _, n := range loc.Siblings[:loc.Index] {
			if by != nil && ends(n, by) {
				by = nil
			}
			if by == nil && foldedHeading(n) {
				by = n
			}
		}
		if by != nil && !ends(loc.Node, by) {
			return by
		}
		if loc.Parent == nil {
			return nil
		}
		loc = e.Content.Locate(loc.Parent.ID)
	}
	return nil
}

// ToggleFold folds a heading's section away, or shows it. The caret, when
// in what folds away, goes to the end of the heading.
func (e *Editor) ToggleFold(id string) {
	n := e.node(id)
	if n == nil || n.Type != "heading" {
		return
	}
	n.Set("collapsed", !n.Bool("collapsed"))
	e.invalidate(id)
	if foldedHeading(n) {
		hid := e.hidden()
		if hid[e.anchor.ID] || hid[e.focus.ID] || e.folder(e.anchor.ID) != nil || e.folder(e.focus.ID) != nil {
			e.Select(Pos{id, textLen(n)}, Pos{id, textLen(n)})
		}
	}
	// Folding is how the page shows, not an edit to undo, but it is kept.
	e.keepFold(n)
	e.changed()
}

// keepFold carries how block n is folded, as the user just folded or
// unfolded it, into the steps of undo and redo: going back to one of
// them, the page is as it was then, but folded as it is now. The folds
// an edit made, as indenting under a folded toggle unfolds it, are its
// own, and go back with it.
func (e *Editor) keepFold(n *doc.Node) {
	folded := n.Bool("collapsed")
	for _, steps := range [][]snapshot{e.undo, e.redo, e.markRedo} {
		for _, s := range steps {
			for _, c := range []*doc.Content{s.content, s.after} {
				if c == nil {
					continue
				}
				if m := c.Find(n.ID); m != nil && m.Type == n.Type && m.Bool("collapsed") != folded {
					m.Set("collapsed", folded)
				}
			}
		}
	}
}

// Reveal unfolds the headings that hide a block, so it can be shown: as
// the user's own unfolding, it is kept in the steps of undo too.
func (e *Editor) Reveal(id string) {
	opened := e.unfold(id)
	for _, h := range opened {
		e.keepFold(h)
	}
	if len(opened) > 0 {
		e.changed()
	}
}

// unfold unfolds the headings that hide a block, and returns them.
func (e *Editor) unfold(id string) []*doc.Node {
	var opened []*doc.Node
	for h := e.folder(id); h != nil; h = e.folder(id) {
		h.Set("collapsed", false)
		e.invalidate(h.ID)
		opened = append(opened, h)
	}
	return opened
}

// section returns the blocks heading h's section holds, and the blocks
// under them.
func (e *Editor) section(h *doc.Node) map[string]bool {
	out := map[string]bool{}
	loc := e.Content.Locate(h.ID)
	if loc == nil {
		return out
	}
	var add func(n *doc.Node)
	add = func(n *doc.Node) {
		out[n.ID] = true
		for _, k := range n.Children {
			add(k)
		}
	}
	for _, n := range loc.Siblings[loc.Index+1:] {
		if ends(n, h) {
			break
		}
		add(n)
	}
	return out
}

// selectedUnits are the units from a selection's start, a, with the
// sections of the folded headings it takes, to copy or delete it. A
// folded heading it starts inside of stays, and so does its section.
func (e *Editor) selectedUnits(a Pos) []*doc.Node {
	us := e.withSections()
	h := e.node(a.ID)
	if h == nil || !foldedHeading(h) || a.Off == 0 {
		return us
	}
	keep := e.section(h)
	out := make([]*doc.Node, 0, len(us))
	for _, n := range us {
		if !keep[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

// selectedEnd is the index in us, the units selectedUnits gives, of the
// last unit a selection ending at b takes: b's, or, for a folded heading
// taken to its end, the last of its section, which goes with it as it
// does when the heading is taken whole.
func (e *Editor) selectedEnd(us []*doc.Node, b Pos) int {
	ib := indexIn(us, b.ID)
	if ib < 0 || !foldedHeading(us[ib]) || b.Off < textLen(us[ib]) {
		return ib
	}
	sec := e.section(us[ib])
	for ib+1 < len(us) && sec[us[ib+1].ID] {
		ib++
	}
	return ib
}

// Headings are the page's headings in order, with their levels and text,
// shown or folded away.
func (e *Editor) Headings() []*doc.Node {
	var out []*doc.Node
	e.Content.Walk(func(n, _ *doc.Node) {
		if n.Type == "heading" {
			out = append(out, n)
		}
	})
	return out
}

// BlockTop is where a block shows, in window coordinates: where the page
// last laid it out in the page, drawn or not, as below the fold of a page
// just opened or pushed out of view by a picture loaded above it, put
// where the page was last drawn; false for one a folded heading hides. A
// block not laid out yet is where it was drawn.
func (e *Editor) BlockTop(id string) (float32, bool) {
	if e.foldedAway(id) {
		return 0, false
	}
	if r := e.laid[id]; r.H > 0 {
		return e.root.Y + r.Y - e.laidTop, true
	}
	r, ok := e.box(id)
	return r.Y, ok
}

// foldedAway reports whether folds hide block id: as the page last built
// worked it out, while nothing changed since, and from the page as it is
// otherwise. The outline asks it of every heading at every frame.
func (e *Editor) foldedAway(id string) bool {
	if e.hid[id] {
		return true
	}
	return (e.hidAt != e.gen || e.hid == nil) && e.folder(id) != nil
}

// BlockRect is a block's box in window coordinates, as last drawn, or
// as laid out for one out of view and never drawn; false for one not in
// the page, or that a folded heading hides.
func (e *Editor) BlockRect(id string) (ui.Rect, bool) {
	r, ok := e.box(id)
	if !ok {
		r = e.laid[id]
		ok = r.H > 0
	}
	return r, ok && e.HasBlock(id) && !e.foldedAway(id)
}

// gutterWidth is the room left of a heading for its fold and link
// buttons.
const gutterWidth = 52

// inGutter reports whether a point in the window is left of a heading's
// text, where its buttons take the press.
func (e *Editor) inGutter(n *doc.Node, wx, wy float32) bool {
	box, ok := e.box(n.ID)
	return ok && n.Type == "heading" && wx < box.X && wx >= box.X-gutterWidth && wy >= box.Y && wy < box.Y+box.H
}

// headingGutter builds a heading's buttons, left of its text: one that
// copies a link to it, and one that folds its section. They show while the
// heading or they are under the pointer; the fold one also while folded.
func (e *Editor) headingGutter(c *ui.Context, n *doc.Node, top float32, hovered bool, pal *theme.Palette) {
	button := func(key, icon, label string, left float32, always bool) bool {
		b := ui.ButtonBase(c.Key(key)).Absolute().Top(top).Left(left).Size(22, 22).Center().Radius(6).
			TextColor(pal.TextMuted).Label(label).Tooltip(label).Cursor(ui.CursorPointer)
		if b.Hovered() {
			b.Background(pal.Hover).TextColor(pal.Text)
		} else if !hovered && !always {
			b.Opacity(0)
		}
		b.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(13) })
		if !b.Clicked() {
			return false
		}
		// The keys still go to the page.
		e.Focus()
		return true
	}
	// The link button shows a check for a moment once it copied.
	copied := e.copied == n.ID && time.Since(e.copiedAt) < 1500*time.Millisecond
	icon, label := "link", locales.EditorCopyHeadingLink()
	if copied {
		icon, label = "check", locales.CommonCopied()
	}
	if button("link:"+n.ID, icon, label, -gutterWidth, copied) {
		e.services.WriteClipboard(e.Host.BlockLink(n.ID))
		e.copied, e.copiedAt = n.ID, time.Now()
	}
	if copied {
		// Show the link icon again once the check has been seen.
		wait := 1500*time.Millisecond - time.Since(e.copiedAt)
		ui.Box(c.Key("link-wait:" + n.ID)).Absolute().Draw(func(p *ui.Painter, _ ui.Rect) { p.After(wait) })
	}
	// A page that can't be changed folds too, as its toggles do: here
	// alone, as it saves nothing, for a section its owner folded to show.
	folded := foldedHeading(n)
	icon, label = "chevron-down", locales.EditorFoldSection()
	if folded {
		icon, label = "chevron-right", locales.EditorUnfoldSection()
	}
	if button("fold:"+n.ID, icon, label, -26, folded) {
		e.ToggleFold(n.ID)
	}
}
