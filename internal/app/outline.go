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

func (a *App) openLink(pageID, blockID string, at int) {
	a.closePreview()
	a.openBlock(pageID, blockID)
	if at < 0 || a.page == nil || a.page.id != pageID {
		return
	}
	switch n := a.page.editor.Content.Find(blockID); {
	case n == nil:
	case n.Type == "video", n.Type == "file":
		a.seekVideo(blockID, at)
	case n.Type == "recording":
		if rec, err := a.st.Recording(n.Str("recording_id")); err == nil {
			a.seek(rec, time.Duration(at)*time.Second)
		}
	}
}

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
	if ps.tinted[id] {
		return
	}
	if ps.lit == nil {
		ps.lit = map[string]time.Time{}
	}
	ps.tinted[id], ps.lit[id] = true, time.Time{}
}

const blockLit = 1500 * time.Millisecond

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
	if ps.outlineSeen == nil {
		ps.outlineSeen = map[string]bool{}
	}
	for id := range kids {
		if !ps.outlineSeen[id] {
			ps.outlineSeen[id] = true
			ps.outline.Open.Add(id)
		}
	}
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
	submitted := list.Submitted()
	if (list.Changed() || submitted) && ps.outlineRow >= 0 && ps.outlineRow < ps.outline.Rows() {
		a.goToBlock(ps.outline.Item(ps.outlineRow), submitted)
	}
}

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

const scrollMargin = 24

const scrollWaitMax = 10

const scrollHold = 5 * time.Second

func (a *App) scrollToBlock(c *ui.Context) {
	ps := a.page
	if ps.scrollTo == "" {
		a.holdBlock(c)
		return
	}
	y, ok := ps.scrollFor(c, ps.scrollTo)
	if !ok {
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
	if d := y - ps.scroll.Y; d > -1 && d < 1 || atEnd && y > ps.scroll.MaxY {
		ps.heldAt = ps.scroll.Y
		return
	}
	ps.scroll.Y, ps.heldAt = y, y
}

func (ps *pageState) scrollFor(c *ui.Context, id string) (float32, bool) {
	top, ok := ps.editor.BlockTop(id)
	view := ps.view.Bounds(c)
	if !ok || view.H == 0 {
		return 0, false
	}
	return max(ps.scroll.Y+top-view.Y-scrollMargin, 0), true
}

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
