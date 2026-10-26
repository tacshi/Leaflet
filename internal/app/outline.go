package app

import (
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

func (h *host) OpenBlock(pageID, blockID string) { h.a.openBlock(pageID, blockID) }
func (h *host) BlockLink(blockID string) string {
	if h.a.page == nil {
		return ""
	}
	return doc.BlockLink(h.a.page.id, blockID)
}

// openBlock opens a page at one of its blocks, for a link to it.
func (a *App) openBlock(pageID, blockID string) {
	a.openPage(pageID)
	if a.page == nil || a.page.id != pageID || blockID == "" {
		return
	}
	if a.page.editor.Content.Find(blockID) == nil {
		a.toast("That part of the page is gone")
		return
	}
	a.goToBlock(blockID)
}

// goToBlock shows a block of the open page at the top, unfolding the
// headings that hide it, puts the caret there and lights it up for a
// moment.
func (a *App) goToBlock(id string) {
	ps := a.page
	ps.editor.Reveal(id)
	ps.editor.PlaceCaret(id, 0)
	ps.editor.Focus()
	ps.scrollTo = id
	if a.win != nil {
		a.win.Invalidate()
	}
	ps.tinted[id] = true
	time.AfterFunc(1500*time.Millisecond, func() {
		a.update(func() { delete(ps.tinted, id) })
	})
}

// outlinePanel lists the open page's headings, those under a heading
// nested in it: choosing one goes to it, and the one the page is at shows
// chosen.
func (a *App) outlinePanel(c *ui.Context) {
	pal := theme.Of(c)
	ps := a.page
	heads := ps.editor.Headings()
	if len(heads) == 0 {
		ui.Text(c, "The page's headings show here, to go to one.").FontSize(13).TextColor(pal.TextMuted).Padding(22, 18)
		return
	}
	nodes := map[string]*doc.Node{}
	for _, h := range heads {
		nodes[h.ID] = h
	}
	roots, kids := headingTree(heads)
	// A heading with others under it shows them, until closed.
	if ps.outlineSeen == nil {
		ps.outlineSeen = map[string]bool{}
	}
	for id := range kids {
		if !ps.outlineSeen[id] {
			ps.outlineSeen[id] = true
			ps.outline.Open.Add(id)
		}
	}
	current := currentHeading(heads, ps.editor.BlockTop, ps.viewTop+2*scrollMargin)
	ps.outlineRow = -1
	for i := 0; i < ps.outline.Rows(); i++ {
		if ps.outline.Item(i) == current {
			ps.outlineRow = i
		}
	}
	ps.outline.List.Selected = &ps.outlineRow
	ps.outline.List.Label = func(i int) string { return nodes[ps.outline.Item(i)].Delta().Plain() }
	list := ui.Outline(c, &ps.outline, roots, func(id string) []string { return kids[id] }, func(id string) {
		text := nodes[id].Delta().Plain()
		if text == "" {
			ui.Text(c, "Untitled heading").FontSize(13.5).TextColor(pal.TextMuted).SingleLine()
			return
		}
		ui.Text(c, text).FontSize(13.5).SingleLine()
	}).Grow(1).Padding(8)
	if list.Changed() && ps.outlineRow >= 0 && ps.outlineRow < ps.outline.Rows() {
		a.goToBlock(ps.outline.Item(ps.outlineRow))
	}
}

// headingTree nests each heading under the nearest heading before it of a
// higher level: the headings at the top, and those under each, nil for a
// heading with none.
func headingTree(heads []*doc.Node) ([]string, map[string][]string) {
	var roots []string
	kids := map[string][]string{}
	type open struct {
		level int
		id    string
	}
	var stack []open
	for _, h := range heads {
		level := h.Int("level", 1)
		for len(stack) > 0 && stack[len(stack)-1].level >= level {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, h.ID)
		} else {
			parent := stack[len(stack)-1].id
			kids[parent] = append(kids[parent], h.ID)
		}
		stack = append(stack, open{level, h.ID})
	}
	return roots, kids
}

// scrollMargin is the room left above a block brought to the top of the
// page; a heading within twice that of the top is the one the page is at.
const scrollMargin = 24

// scrollToBlock brings the block asked for to the top of the page, once
// the page has laid it out.
func (a *App) scrollToBlock() {
	ps := a.page
	if ps.scrollTo == "" {
		return
	}
	top, ok := ps.editor.BlockTop(ps.scrollTo)
	if !ok || ps.viewTop == 0 {
		// Not laid out yet: the next frame has it.
		if a.win != nil {
			a.win.Invalidate()
		}
		return
	}
	ps.scroll.Y = max(ps.scroll.Y+top-ps.viewTop-scrollMargin, 0)
	ps.scrollTo = ""
}

// currentHeading is the last heading shown at or above line, the top of
// the page, or the first shown.
func currentHeading(heads []*doc.Node, top func(id string) (float32, bool), line float32) string {
	current := ""
	for _, h := range heads {
		y, ok := top(h.ID)
		if !ok {
			continue
		}
		if current == "" || y <= line {
			current = h.ID
		}
		if y > line {
			break
		}
	}
	return current
}
