package editor

import (
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

const (
	calloutMargin = 6
	calloutPad    = 12
)

func CalloutLook(tone string, pal *theme.Palette) (bg, border, fg ui.Color, icon string) {
	icon = doc.CalloutIcon(tone)
	switch doc.CalloutTone(tone) {
	case "tip":
		return pal.OkBg, pal.OkBorder, pal.OkText, icon
	case "warning":
		return pal.WarnBg, pal.WarnBorder, pal.WarnText, icon
	case "danger":
		return pal.DangerBg, pal.DangerBorder, pal.DangerText, icon
	}
	return pal.AccentSoft, pal.AccentSoftBorder, pal.AccentText, icon
}

func collapsed(n *doc.Node) bool { return n.Type == "toggle_list" && n.Bool("collapsed") }

func (e *Editor) CycleTone(id string) {
	n := e.node(id)
	if n == nil || n.Type != "callout" || e.ReadOnly {
		return
	}
	e.pushUndo(false)
	n.Set("tone", doc.NextTone(n.Str("tone")))
	e.invalidate(id)
	e.changed()
}

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
	e.keepFold(n)
	e.changed()
}

func (e *Editor) marker(n *doc.Node, wx, wy float32) bool {
	box, ok := e.box(n.ID)
	if !ok || wy < box.Y || wy >= box.Y+box.H {
		return false
	}
	x := box.X + float32(e.depth(n.ID))*indentStep
	return wx >= x && wx < x+markerWidth(n)
}
