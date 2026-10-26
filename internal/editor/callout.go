package editor

import (
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

// Callouts and toggles: text blocks with a control before the text, an
// icon that changes the callout's tone and a chevron that folds the
// toggle's children away.

const (
	// calloutMargin is the room above and below a callout's box, and
	// calloutPad the room around its text in it.
	calloutMargin = 6
	calloutPad    = 12
)

// calloutLook is the tint, border, icon color and icon of a tone.
func calloutLook(tone string, pal *theme.Palette) (bg, border, fg ui.Color, icon string) {
	switch doc.CalloutTone(tone) {
	case "tip":
		return pal.OkBg, pal.OkBorder, pal.OkText, "lightbulb"
	case "warning":
		return pal.WarnBg, pal.WarnBorder, pal.WarnText, "triangle-alert"
	case "danger":
		return pal.DangerBg, pal.DangerBorder, pal.DangerText, "octagon-alert"
	}
	return pal.AccentSoft, pal.AccentSoftBorder, pal.AccentText, "info"
}

// collapsed reports whether a block is a toggle folded shut, its children
// hidden.
func collapsed(n *doc.Node) bool { return n.Type == "toggle_list" && n.Bool("collapsed") }

// CycleTone gives a callout the next tone.
func (e *Editor) CycleTone(id string) {
	n := e.node(id)
	if n == nil || n.Type != "callout" || e.ReadOnly {
		return
	}
	e.pushUndo(false)
	tone := doc.CalloutTone(n.Str("tone"))
	for i, t := range doc.CalloutTones {
		if t == tone {
			n.Set("tone", doc.CalloutTones[(i+1)%len(doc.CalloutTones)])
		}
	}
	e.invalidate(id)
	e.changed()
}

// ToggleCollapsed folds a toggle's children away, or shows them. The
// caret, when in what folds away, goes to the end of the toggle.
func (e *Editor) ToggleCollapsed(id string) {
	n := e.node(id)
	if n == nil || n.Type != "toggle_list" {
		return
	}
	n.Set("collapsed", !n.Bool("collapsed"))
	e.invalidate(id)
	if collapsed(n) {
		inside := func(p Pos) bool {
			for loc := e.Content.Locate(p.ID); loc != nil && loc.Parent != nil; loc = e.Content.Locate(loc.Parent.ID) {
				if loc.Parent.ID == id {
					return true
				}
			}
			return false
		}
		if inside(e.anchor) || inside(e.focus) {
			e.Select(Pos{id, textLen(n)}, Pos{id, textLen(n)})
		}
	}
	// Folding is how the page shows, not an edit to undo, but it is kept.
	e.changed()
}

// marker reports whether a point in the window is on the control before
// a block's text: a to-do's box, a callout's icon or a toggle's chevron.
func (e *Editor) marker(n *doc.Node, wx, wy float32) bool {
	box, ok := e.geom[n.ID]
	if !ok || wy < box.Y || wy >= box.Y+box.H {
		return false
	}
	x := box.X + float32(e.depth(n.ID))*indentStep
	return wx >= x && wx < x+markerWidth(n)
}
