package app

import (
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
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
		a.toast(locales.OutlineBlockGone())
		return
	}
	a.goToBlock(blockID, true)
}

// goToBlock shows a block of the open page at the top, opening the
// toggles and unfolding the headings that hide it, puts the caret there
// and lights it up for a moment; focus gives the page the keyboard, which
// the outline keeps while its arrows go from heading to heading.
func (a *App) goToBlock(id string, focus bool) {
	ps := a.page
	for loc := ps.editor.Content.Locate(id); loc != nil && loc.Parent != nil; loc = ps.editor.Content.Locate(loc.Parent.ID) {
		if p := loc.Parent; p.Type == "toggle_list" && p.Bool("collapsed") {
			ps.editor.ToggleCollapsed(p.ID)
		}
	}
	ps.editor.Reveal(id)
	ps.editor.PlaceCaret(id, 0)
	if focus {
		ps.editor.Focus()
	}
	ps.scrollTo, ps.scrollWait = id, 0
	if a.win != nil {
		a.win.Invalidate()
	}
	// A block an agent run tinted stays so, as it does once an agent run
	// tints it meanwhile.
	if ps.tinted[id] {
		return
	}
	if ps.lit == nil {
		ps.lit = map[string]time.Time{}
	}
	ps.tinted[id], ps.lit[id] = true, time.Time{}
}

// blockLit is how long goToBlock lights a block.
const blockLit = 1500 * time.Millisecond

// dimLit takes the light off the blocks goToBlock lit once their moment
// is over, from the frame they first showed lit in.
func (a *App) dimLit(c *ui.Context) {
	ps := a.page
	now := c.Now()
	var next time.Duration
	for id, at := range ps.lit {
		if at.IsZero() {
			ps.lit[id], at = now, now
		}
		left := blockLit - now.Sub(at)
		if left <= 0 {
			delete(ps.lit, id)
			delete(ps.tinted, id)
			continue
		}
		if next == 0 || left < next {
			next = left
		}
	}
	if next > 0 {
		c.After(next)
	}
}

// outlinePanel lists the open page's headings, those under a heading
// nested in it: choosing one goes to it, and the one the page is at shows
// chosen.
func (a *App) outlinePanel(c *ui.Context) {
	pal := theme.Of(c)
	ps := a.page
	heads := ps.editor.Headings()
	if len(heads) == 0 {
		ui.Text(c, locales.OutlineEmpty()).FontSize(13).TextColor(pal.TextMuted).Padding(22, 18)
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
	// The heading chosen is the one the page is at, but while the list
	// has the keyboard, which chooses as it scrolls the page there.
	if !ps.outlineKeys {
		current := currentHeading(heads, ps.editor.BlockTop, ps.view.Bounds(c).Y+2*scrollMargin)
		ps.outlineRow = -1
		for i := 0; i < ps.outline.Rows(); i++ {
			if ps.outline.Item(i) == current {
				ps.outlineRow = i
			}
		}
	}
	ps.outline.List.Selected = &ps.outlineRow
	ps.outline.List.Label = func(i int) string { return nodes[ps.outline.Item(i)].Delta().Plain() }
	list := ui.Outline(c, &ps.outline, roots, func(id string) []string { return kids[id] }, func(id string) {
		text := nodes[id].Delta().Plain()
		if text == "" {
			ui.Text(c, locales.OutlineUntitledHeading()).FontSize(13.5).TextColor(pal.TextMuted).SingleLine()
			return
		}
		ui.Text(c, text).FontSize(13.5).SingleLine()
	}).Grow(1).Padding(8)
	ps.outlineKeys = list.Focused()
	// Enter or a double click goes into the page; a row chosen, by the
	// arrows too, only shows it.
	submitted := list.Submitted()
	if (list.Changed() || submitted) && ps.outlineRow >= 0 && ps.outlineRow < ps.outline.Rows() {
		a.goToBlock(ps.outline.Item(ps.outlineRow), submitted)
	}
}

// headingTree is the outline's tree, as doc.Outline nests the headings:
// those at the top, and those in each, nil for a heading with none.
func headingTree(heads []*doc.Node) ([]string, map[string][]string) {
	var roots []string
	kids := map[string][]string{}
	for _, h := range doc.Outline(heads) {
		if h.Parent == "" {
			roots = append(roots, h.ID)
		} else {
			kids[h.Parent] = append(kids[h.Parent], h.ID)
		}
	}
	return roots, kids
}

// scrollMargin is the room left above a block brought to the top of the
// page; a heading within twice that of the top is the one the page is at.
const scrollMargin = 24

// scrollWaitMax is how many frames scrollToBlock waits for a block to be
// laid out, as one hidden never is.
const scrollWaitMax = 10

// scrollToBlock brings the block asked for to the top of the page, once
// the page has laid it out.
func (a *App) scrollToBlock(c *ui.Context) {
	ps := a.page
	if ps.scrollTo == "" {
		return
	}
	top, ok := ps.editor.BlockTop(ps.scrollTo)
	view := ps.view.Bounds(c)
	if !ok || view.H == 0 {
		// Not laid out yet: the next frame has it.
		if ps.scrollWait++; ps.scrollWait > scrollWaitMax {
			ps.scrollTo = ""
			return
		}
		if a.win != nil {
			a.win.Invalidate()
		}
		return
	}
	ps.scroll.Y = max(ps.scroll.Y+top-view.Y-scrollMargin, 0)
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
