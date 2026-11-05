package app

import (
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

func (h *host) OpenBlock(pageID, blockID string, at int) { h.a.openLink(pageID, blockID, at) }
func (h *host) BlockLink(blockID string) string {
	if h.a.page == nil {
		return ""
	}
	return doc.BlockLink(h.a.page.id, blockID)
}

// FollowVideoLink follows a video's share link pasted in the open page to
// the video off the main thread, as the link dialog does, and runs done
// once it has, while that page is still open.
func (h *host) FollowVideoLink(link string, done func(video string)) {
	a := h.a
	if a.page == nil {
		return
	}
	ed := a.page.editor
	go func() {
		defer guard.Recover()
		video := web.VideoLink(link)
		a.update(func() {
			if a.page != nil && a.page.editor == ed {
				done(video)
			}
		})
	}()
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

// openLink opens a page at one of its blocks, for a link to it, and
// plays a video or a recording from at seconds in, for a link to a moment
// of one; at is -1 for none.
func (a *App) openLink(pageID, blockID string, at int) {
	a.openBlock(pageID, blockID)
	if at < 0 || a.page == nil || a.page.id != pageID {
		return
	}
	switch n := a.page.editor.Content.Find(blockID); {
	case n == nil:
	case n.Type == "video":
		a.seekVideo(blockID, at)
	case n.Type == "recording":
		if rec, err := a.st.Recording(n.Str("recording_id")); err == nil {
			a.seek(rec, time.Duration(at)*time.Second)
		}
	}
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
	ps.scrollTo, ps.scrollWait, ps.held = id, 0, ""
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

// scrollHold is how long scrollToBlock keeps a block it brought to the
// top of the page there, as pictures and embeds above it load and push
// it down, unless the page is scrolled meanwhile.
const scrollHold = 5 * time.Second

// scrollToBlock brings the block asked for to the top of the page, once
// the page has laid it out, and holds it there for scrollHold.
func (a *App) scrollToBlock(c *ui.Context) {
	ps := a.page
	if ps.scrollTo == "" {
		a.holdBlock(c)
		return
	}
	y, ok := ps.scrollFor(c, ps.scrollTo)
	if !ok {
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
	ps.scroll.Y = y
	ps.held, ps.heldUntil, ps.heldAt = ps.scrollTo, c.Now().Add(scrollHold), y
	ps.scrollTo = ""
}

// holdBlock keeps the block scrollToBlock brought to the top of the page
// there as what is above it grows, until the page is scrolled from where
// it was put: the page's end, short of that, is where the page keeps it.
func (a *App) holdBlock(c *ui.Context) {
	ps := a.page
	if ps.held == "" {
		return
	}
	atEnd := ps.scroll.Y == ps.scroll.MaxY
	if c.Now().After(ps.heldUntil) || ps.scroll.Y != ps.heldAt && !(atEnd && ps.heldAt > ps.scroll.MaxY) {
		ps.held = ""
		return
	}
	y, ok := ps.scrollFor(c, ps.held)
	if !ok {
		return
	}
	// There already, or as near as the page's end lets it be.
	if d := y - ps.scroll.Y; d > -1 && d < 1 || atEnd && y > ps.scroll.MaxY {
		ps.heldAt = ps.scroll.Y
		return
	}
	ps.scroll.Y, ps.heldAt = y, y
}

// scrollFor is how far to scroll the page for block id to be at its top,
// and whether the page has laid the block out.
func (ps *pageState) scrollFor(c *ui.Context, id string) (float32, bool) {
	top, ok := ps.editor.BlockTop(id)
	view := ps.view.Bounds(c)
	if !ok || view.H == 0 {
		return 0, false
	}
	return max(ps.scroll.Y+top-view.Y-scrollMargin, 0), true
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
