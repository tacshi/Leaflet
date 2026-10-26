package editor

import (
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

// A folded heading hides its section: the blocks after it among its
// siblings, up to the next heading of its level or higher. They stay in
// the page, out of the caret's way and the eye's, and go with the heading
// when a selection over it is copied or deleted.

func foldedHeading(n *doc.Node) bool { return n.Type == "heading" && n.Bool("collapsed") }

func headingLevel(n *doc.Node) int { return n.Int("level", 1) }

// ends reports whether block n ends the section of heading h.
func ends(n, h *doc.Node) bool { return n.Type == "heading" && headingLevel(n) <= headingLevel(h) }

// hidden returns the blocks folded headings hide; the blocks under them
// are hidden with them, and not listed.
func (e *Editor) hidden() map[string]bool {
	out := map[string]bool{}
	var visit func(nodes []*doc.Node)
	visit = func(nodes []*doc.Node) {
		var by *doc.Node
		for _, n := range nodes {
			if by != nil && ends(n, by) {
				by = nil
			}
			if by != nil {
				out[n.ID] = true
				continue
			}
			if foldedHeading(n) {
				by = n
			}
			if n.Type != "table" && !collapsed(n) {
				visit(n.Children)
			}
		}
	}
	visit(e.Content.Children)
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
	e.changed()
}

// Reveal unfolds the headings that hide a block, so it can be shown.
func (e *Editor) Reveal(id string) {
	changed := false
	for h := e.folder(id); h != nil; h = e.folder(id) {
		h.Set("collapsed", false)
		e.invalidate(h.ID)
		changed = true
	}
	if changed {
		e.changed()
	}
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

// BlockTop is where a block shows, in window coordinates, as last drawn;
// false for one a folded heading hides.
func (e *Editor) BlockTop(id string) (float32, bool) {
	r, ok := e.geom[id]
	return r.Y, ok && !e.hid[id] && e.folder(id) == nil
}

// gutterWidth is the room left of a heading for its fold and link
// buttons.
const gutterWidth = 52

// inGutter reports whether a point in the window is left of a heading's
// text, where its buttons take the press.
func (e *Editor) inGutter(n *doc.Node, wx, wy float32) bool {
	box, ok := e.geom[n.ID]
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
	icon, label := "link", "Copy link to heading"
	if copied {
		icon, label = "check", "Copied"
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
	if e.ReadOnly {
		return
	}
	folded := foldedHeading(n)
	icon, label = "chevron-down", "Fold section"
	if folded {
		icon, label = "chevron-right", "Unfold section"
	}
	if button("fold:"+n.ID, icon, label, -26, folded) {
		e.ToggleFold(n.ID)
	}
}
