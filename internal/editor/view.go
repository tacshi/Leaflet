package editor

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
)

const blinkPeriod = 530 * time.Millisecond

// View builds the editor.
func (e *Editor) View(c *ui.Context) ui.Element {
	pal := theme.Of(c)
	dark := c.Theme().Dark
	e.services = c.Services()
	root := ui.Column(c.Key("page-editor")).Bind(&e.handle).Label(locales.EditorPage()).Cursor(ui.CursorText).FillWidth().FocusRing(false)
	if !e.ReadOnly {
		root.HandleTextInput(e).Role(ui.RoleTextField)
	} else {
		root.Focusable()
	}
	root.HandleInput(e.HandleInput)
	e.focused = root.Focused()
	root.Draw(func(p *ui.Painter, r ui.Rect) {
		// The root paints before its blocks: move every block with it,
		// then the blocks on screen set their exact boxes.
		e.root = r
		for id, b := range e.rel {
			e.geom[id] = ui.Rect{X: r.X + b.X, Y: r.Y + b.Y, W: b.W, H: b.H}
		}
	})
	// The page's own text is drawn here, not in MyGo's text inputs, so
	// MyGo's EditItems would add nothing: cut, copy and paste are keys.
	root.PopoverMenu(func(m *ui.Menu) {
		if e.colored() && m.Item(locales.EditorRemoveColor()).Chosen() {
			e.SetColor("")
			e.SetHighlight("")
		}
		if e.menuBlock != "" && e.node(e.menuBlock) != nil {
			if m.Item(locales.EditorCopyBlockLink()).Chosen() {
				e.services.WriteClipboard(e.Host.BlockLink(e.menuBlock))
				c.Toast(locales.EditorCopiedBlockLink())
			}
			if label := mediaEdit(e.node(e.menuBlock)); label != "" && !e.ReadOnly && m.Item(label).Chosen() {
				e.Host.EditBlock(e.menuBlock)
			}
			if label := mediaLink(e.linkAtPos(e.menuAt)); label != "" && !e.ReadOnly && m.Item(label).Chosen() {
				e.LinkToMedia(e.menuAt)
			}
		}
	})
	e.hid = e.hidden()
	// The chips shown fill this in again as they draw.
	clear(e.chips)
	root.Children(func() {
		e.blocks(c, e.Content.Children, pal, dark)
	})
	e.reveal = ""
	e.overlays(c, pal)
	return root
}

func (e *Editor) blocks(c *ui.Context, nodes []*doc.Node, pal *theme.Palette, dark bool) {
	number := 0
	for _, n := range nodes {
		if e.hid[n.ID] {
			continue
		}
		if n.Type == "numbered_list" {
			number++
		} else {
			number = 0
		}
		if n.IsText() {
			e.textBlock(c, n, number, pal, dark)
		} else {
			e.atomicBlock(c, n, pal)
		}
		if n.Type != "table" && !collapsed(n) && len(n.Children) > 0 {
			e.blocks(c, n.Children, pal, dark)
		}
	}
}

// inSelection returns the part of a block's text the selection covers, in
// runes, and whether it covers any.
func (e *Editor) inSelection(n *doc.Node) (int, int, bool) {
	if e.Collapsed() {
		return 0, 0, false
	}
	a, b := e.ordered()
	i, ia, ib := e.index(n.ID), e.index(a.ID), e.index(b.ID)
	if i < ia || i > ib {
		return 0, 0, false
	}
	from, to := 0, textLen(n)
	if i == ia {
		from = a.Off
	}
	if i == ib {
		to = b.Off
	}
	if i == ib && i != ia && to == 0 && n.IsText() {
		return 0, 0, false
	}
	return from, to, true
}

func placeholder(n *doc.Node, only bool) string {
	switch n.Type {
	case "heading":
		return locales.EditorPhHeading(n.Int("level", 1))
	case "bulleted_list", "numbered_list":
		return locales.EditorPhList()
	case "todo_list":
		return locales.EditorPhTodo()
	case "quote":
		return locales.EditorPhQuote()
	case "code":
		return locales.EditorPhCode()
	case "callout":
		return locales.EditorPhCallout()
	case "toggle_list":
		return locales.EditorPhToggle()
	}
	if only {
		return locales.EditorPhFirst()
	}
	return locales.EditorPhEmpty()
}

func (e *Editor) textBlock(c *ui.Context, n *doc.Node, number int, pal *theme.Palette, dark bool) {
	l := e.layout(n, pal, dark)
	depth := e.depth(n.ID)
	box := ui.Box(c.Key(n.ID)).Height(l.h).FillWidth()
	tinted := e.Tinted[n.ID]
	empty := textLen(n) == 0
	isFocus := e.focus.ID == n.ID
	only := len(e.Content.Children) == 1 && len(n.Children) == 0
	showPlaceholder := empty && !e.ReadOnly && ((isFocus && e.focused) || only) && !e.marked
	selFrom, selTo, selected := e.inSelection(n)
	focused := e.focused
	caretOff := e.focus.Off
	marked, mStart, mEnd := e.marked && isFocus, e.markStart, e.markEnd
	blink := e.blink
	collapsed := e.Collapsed()
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		e.place(n.ID, r)
		ox, oy := r.X+l.x, r.Y+l.y
		if tinted {
			p.Fill(ui.Rect{X: r.X - 8, Y: r.Y, W: r.W + 16, H: r.H}, pal.AgentSection, 6)
		}
		first := l.layout.Caret(0)
		mx := r.X + float32(depth)*indentStep
		switch n.Type {
		case "bulleted_list":
			cy := oy + first.Y + first.H/2
			s := float32(5.5)
			switch depth % 3 {
			case 0:
				p.Fill(ui.Rect{X: mx + 9, Y: cy - s/2, W: s, H: s}, pal.Text, s/2)
			case 1:
				p.Stroke(ui.Rect{X: mx + 9, Y: cy - s/2, W: s, H: s}, pal.Text, s/2, 1.2)
			default:
				p.Fill(ui.Rect{X: mx + 9, Y: cy - s/2, W: s, H: s}, pal.Text, 1)
			}
		case "numbered_list":
			label := ui.Span{Text: fmt.Sprintf("%d.", number), Font: l.font.Family, Size: l.font.Size, Color: pal.TextMuted, Features: "tnum"}
			w, h := p.MeasureText(0, label)
			p.RichText(mx+markerWidth(n)-w-6, oy+first.Y+(first.H-h)/2, 0, label)
		case "todo_list":
			cy := oy + first.Y + first.H/2
			box := ui.Rect{X: mx + 3, Y: cy - 8, W: 16, H: 16}
			if n.Bool("checked") {
				p.Fill(box, pal.Accent, 4)
				p.Icon(theme.Icon("check"), ui.Rect{X: box.X + 2, Y: box.Y + 2, W: 12, H: 12}, pal.OnAccent)
			} else {
				p.Stroke(box, pal.TextMuted, 4, 1.4)
			}
		case "quote":
			p.Fill(ui.Rect{X: mx + 2, Y: r.Y + 4, W: 3, H: r.H - 8}, pal.AccentSoftBorder, 1.5)
		case "code":
			p.Fill(ui.Rect{X: mx, Y: r.Y + codeMargin, W: r.X + r.W - mx, H: r.H - 2*codeMargin}, pal.Code, 8)
		case "callout":
			bg, border, fg, icon := CalloutLook(n.Str("tone"), pal)
			card := ui.Rect{X: mx, Y: r.Y + calloutMargin, W: r.X + r.W - mx, H: r.H - 2*calloutMargin}
			p.Fill(card, bg, 8)
			p.Stroke(card, border, 8, 1)
			cy := oy + first.Y + first.H/2
			p.Icon(theme.Icon(icon), ui.Rect{X: mx + 14, Y: cy - 9, W: 18, H: 18}, fg)
		case "toggle_list":
			cy := oy + first.Y + first.H/2
			icon := "chevron-down"
			if n.Bool("collapsed") {
				icon = "chevron-right"
			}
			p.Icon(theme.Icon(icon), ui.Rect{X: mx + 4, Y: cy - 8, W: 16, H: 16}, pal.TextMuted)
		}
		if selected {
			a, b := utf16Off(n, selFrom), utf16Off(n, selTo)
			if a == b || empty {
				p.Fill(ui.Rect{X: ox, Y: oy + first.Y, W: 6, H: first.H}, pal.Selection, 0)
			}
			for _, s := range l.layout.SelectionRects(ui.TextInputRange{Start: a, End: b}) {
				s.X += ox
				s.Y += oy
				p.Fill(s, pal.Selection, 2)
			}
		}
		if showPlaceholder {
			// A span takes its font's own line height: it is centered on
			// the block's first line, which may be taller.
			hint := ui.Span{Text: placeholder(n, only), Font: l.font.Family, Size: l.font.Size,
				Weight: l.font.Weight, Italic: l.font.Italic, Color: pal.Placeholder}
			_, h := p.MeasureText(r.W-l.x, hint)
			p.RichText(ox, oy+first.Y+(first.H-h)/2, r.W-l.x, hint)
		}
		p.TextLayout(l.layout, ox, oy, pal.Text)
		if marked {
			a, b := utf16Off(n, mStart), utf16Off(n, mEnd)
			for _, s := range l.layout.SelectionRects(ui.TextInputRange{Start: a, End: b}) {
				p.Fill(ui.Rect{X: ox + s.X, Y: oy + s.Y + s.H - 2, W: s.W, H: 1.5}, pal.Accent, 0)
			}
		}
		if focused && isFocus && collapsed {
			since := p.Now().Sub(blink)
			if since < 0 || (since/blinkPeriod)%2 == 0 {
				cr := l.layout.Caret(utf16Off(n, caretOff))
				p.Fill(ui.Rect{X: ox + cr.X - 0.5, Y: oy + cr.Y, W: 2, H: cr.H}, pal.Accent, 1)
			}
			if since >= 0 {
				p.After(blinkPeriod - since%blinkPeriod)
			}
		}
	})
	hovered := box.Hovered()
	box.Children(func() {
		if e.reveal == n.ID {
			revealBelow(c, l.h)
		}
		if n.Type == "code" {
			e.codeBarView(c, n, float32(depth)*indentStep, hovered || isFocus && focused, pal)
		}
		if n.Type == "heading" {
			first := l.layout.Caret(0)
			e.headingGutter(c, n, l.y+first.Y+first.H/2-11, hovered, pal)
		}
		if n.Type == "todo_list" {
			end := l.layout.Caret(utf16Off(n, textLen(n)))
			e.dueChip(c, n, l.x+end.X+8, l.y+end.Y+(end.H-20)/2, hovered || isFocus && focused, pal)
		}
		if count := e.Host.CommentCount(n.ID); count > 0 {
			e.commentBadge(c, n.ID, count, l.y, pal)
		}
	})
}

// peekGrace is how long a badge's preview stays once the pointer leaves
// it and the badge, to cross the gap between them.
const peekGrace = 250 * time.Millisecond

// commentBadge is the count of a block's open threads, in the margin:
// hovered, it shows them in brief below it; clicked, it opens them in the
// Comments panel.
func (e *Editor) commentBadge(c *ui.Context, id string, count int, top float32, pal *theme.Palette) {
	b := ui.ButtonBase(c.Key("comments:"+id)).Absolute().Top(top).Right(-52).Padding(2, 7).Radius(999).
		Gap(4).Background(pal.WarnBg).Border(1, pal.WarnBorder).TextColor(pal.WarnText).Cursor(ui.CursorPointer).
		Label(locales.EditorCommentCount(count))
	// A press on it is the badge's, not the text's beside it.
	b.Draw(func(_ *ui.Painter, r ui.Rect) { e.chips["comments:"+id] = r })
	hovered := b.Hovered()
	if hovered {
		b.Background(pal.WarnBorder)
	}
	if !hovered && e.peekShut == id {
		e.peekShut = ""
	}
	switch over := hovered && e.peekShut != id || e.peek == id && e.peekOver; {
	case over:
		e.peek, e.peekLeft = id, time.Time{}
	case e.peek != id:
	case e.peekLeft.IsZero():
		e.peekLeft = time.Now()
		c.After(peekGrace)
	case time.Since(e.peekLeft) >= peekGrace:
		e.peek, e.peekOver = "", false
	default:
		c.After(peekGrace - time.Since(e.peekLeft))
	}
	if e.peek == id {
		ui.Overlay(c, func() {
			panel := ui.Column(c.Key("comment-peek")).AttachTo(b, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(6, 0, 0).
				Width(300).MaxHeight(340).Padding(12).Gap(12).Radius(10).Background(pal.Surface).Border(1, pal.Border).
				Shadow(0, 10, 30, 0, pal.Shadow)
			e.peekOver = panel.Hovered()
			panel.Children(func() { e.Host.CommentPreview(c, id) })
		})
	}
	b.Children(func() {
		ui.Icon(c, theme.Icon("message-square")).FontSize(12)
		ui.Textf(c, "%d", count).FontSize(12).Bold()
	})
	if b.Clicked() {
		e.peek, e.peekOver, e.peekShut = "", false, id
		e.Host.OpenComments(id)
	}
}

// atomicBlock builds a block selected whole: a divider, image, table,
// chart, embed, video, recording or subpage.
func (e *Editor) atomicBlock(c *ui.Context, n *doc.Node, pal *theme.Palette) {
	_, _, inSel := e.inSelection(n)
	selected := (e.focus.ID == n.ID && e.Collapsed() && e.focused) || inSel
	depth := e.depth(n.ID)
	wrap := ui.Column(c.Key(n.ID)).FillWidth().Padding(6, 0).PaddingStart(float32(depth) * indentStep).Cursor(ui.CursorDefault)
	if e.reveal == n.ID {
		wrap.ScrollIntoView()
	}
	tinted := e.Tinted[n.ID]
	wrap.Draw(func(p *ui.Painter, r ui.Rect) {
		e.place(n.ID, r)
		if tinted {
			p.Fill(ui.Rect{X: r.X - 8, Y: r.Y, W: r.W + 16, H: r.H}, pal.AgentSection, 6)
		}
	})
	if selected {
		wrap.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Stroke(ui.Rect{X: r.X - 4, Y: r.Y + 2, W: r.W + 8, H: r.H - 4}, pal.Accent, 8, 2)
		})
	}
	wrap.Children(func() {
		switch n.Type {
		case "divider":
			ui.Box(c).Height(20).FillWidth().Draw(func(p *ui.Painter, r ui.Rect) {
				p.Fill(ui.Rect{X: r.X, Y: r.Y + r.H/2, W: r.W, H: 1}, pal.Border, 0)
			})
		case "image":
			e.imageBlock(c, n, pal)
		case "table":
			e.tableBlock(c, n, pal)
		case "chart":
			e.chartBlock(c, n, pal)
		case "embed", "math_equation", "diagram":
			e.Host.Embed(c, n, e.width-float32(depth)*indentStep)
		case "video":
			e.Host.Video(c, n, e.width-float32(depth)*indentStep)
		case "link_preview", "file":
			e.Host.Card(c, n)
		case "recording":
			e.Host.Recording(c, n)
		case "subpage":
			e.subpageBlock(c, n, pal)
		default:
			ui.Text(c, locales.EditorUnsupported(n.Type)).TextColor(pal.TextMuted)
		}
		if count := e.Host.CommentCount(n.ID); count > 0 {
			e.commentBadge(c, n.ID, count, 8, pal)
		}
	})
}

// mediaEdit is the context menu's item changing what a block shows: an
// uploaded image takes another file, an image or video from the web
// another link.
func mediaEdit(n *doc.Node) string {
	switch {
	case n.Type == "image" && n.Str("file_id") != "":
		return locales.EditorReplaceImage()
	case n.Type == "image" || n.Type == "video":
		return locales.EditorEditMediaLink()
	}
	return ""
}

// mediaLink is the context menu's item that shows a link to a video or a
// picture as its block, or "".
func mediaLink(href string) string {
	if _, ok := doc.ParseVideoURL(href); ok {
		return locales.EditorShowAsVideo()
	}
	if doc.ImageLink(href) {
		return locales.EditorShowAsImage()
	}
	return ""
}

func (e *Editor) imageBlock(c *ui.Context, n *doc.Node, pal *theme.Palette) {
	pic, video, failed := e.Host.Image(n)
	if pic == nil {
		label := locales.EditorLoadingImage()
		if failed {
			label = locales.EditorImageFailed()
		}
		ui.Row(c).Height(160).FillWidth().Center().Radius(8).Background(pal.Ground).Border(1, pal.Divider).Children(func() {
			ui.Icon(c, theme.Icon("image")).TextColor(pal.TextMuted)
			ui.Text(c, label).TextColor(pal.TextMuted).Margin(0, 0, 0, 8)
		})
		return
	}
	w, h := ui.ImageSize(pic)
	maxW := e.width - float32(e.depth(n.ID))*indentStep
	dw := min(w, maxW)
	dh := dw * h / max(w, 1)
	if mw := n.Int("width", 0); mw > 0 && float32(mw) < dw {
		dw, dh = float32(mw), float32(mw)*h/max(w, 1)
	}
	row := ui.Row(c).FillWidth()
	if n.Str("align") != "left" {
		row.Justify(ui.Center)
	}
	row.Children(func() {
		// An animated GIF plays once its player is made, from the frame
		// its picture shows.
		if video != nil {
			ui.Video(c, video).Size(dw, dh).Fit(ui.Contain).Radius(6).Label(n.Str("alt"))
			return
		}
		ui.Image(c, pic).Size(dw, dh).Fit(ui.Contain).Radius(6).Label(n.Str("alt"))
	})
}

func (e *Editor) subpageBlock(c *ui.Context, n *doc.Node, pal *theme.Palette) {
	id := n.Str("page_id")
	title := e.Host.PageTitle(id)
	card := ui.ButtonBase(c.Key("sub:"+n.ID)).Gap(10).Padding(9, 12).Radius(8).Border(1, pal.Divider).
		Background(pal.Surface).Cursor(ui.CursorPointer).AlignItems(ui.Center)
	if card.Hovered() {
		card.Background(pal.Hover.Over(pal.Surface))
	}
	card.Children(func() {
		ui.Icon(c, theme.Icon("file-text")).TextColor(pal.Icon).FontSize(16)
		if title == "" {
			ui.Text(c, locales.EditorPageNotFound()).TextColor(pal.TextMuted)
		} else {
			ui.Text(c, title).FontWeight(600).TextColor(pal.Text)
		}
		ui.Spacer(c)
		ui.Icon(c, theme.Icon("chevron-right")).TextColor(pal.TextMuted).FontSize(14)
	})
	if card.Clicked() && title != "" {
		e.Host.OpenPage(id)
	}
}

// overlays builds the / and @ menus, the selection toolbar and the link
// editor, above the page.
func (e *Editor) overlays(c *ui.Context, pal *theme.Palette) {
	if e.trigger != nil {
		t := *e.trigger
		if t.Kind == '/' {
			e.menuItems = e.Host.SlashItems(t.Query)
		} else {
			e.menuItems = e.Host.MentionItems(t.Query)
		}
		if e.menuIndex >= len(e.menuItems) {
			e.menuIndex = 0
		}
		if cr, ok := e.caretRect(Pos{t.ID, t.Off}); ok {
			e.menu(c, cr, pal)
		}
		return
	}
	if e.linkOpen {
		a, _ := e.ordered()
		if cr, ok := e.caretRect(a); ok {
			e.linkEditor(c, cr, pal)
		}
		return
	}
	// The toolbar stays while the focus is in it, or its color picker is
	// open.
	if !e.Collapsed() && (e.focused || e.colorOpen || e.bar.FocusWithin(c)) && !e.dragging && !e.ReadOnly {
		a, _ := e.ordered()
		if n := e.node(a.ID); n != nil && n.IsText() {
			if cr, ok := e.caretRect(a); ok {
				e.toolbar(c, cr, pal)
				return
			}
		}
	}
	// With no toolbar, its color picker is closed.
	e.colorOpen = false
}

func (e *Editor) menu(c *ui.Context, at ui.Rect, pal *theme.Palette) {
	_, wh := c.Size()
	ui.Overlay(c, func() {
		panel := ui.Column(c.Key("block-menu")).Absolute().Left(at.X-6).Width(300).Padding(6).Gap(1).
			Radius(10).Background(pal.Surface).Border(1, pal.Border).Shadow(0, 10, 30, 0, pal.Shadow)
		h := float32(min(len(e.menuItems), 9))*34 + 16
		// Above the line when it would go under the bars at the bottom.
		if at.Y+at.H+6+h > wh-revealMargin {
			panel.Top(max(at.Y-6-h, 8))
		} else {
			panel.Top(at.Y + at.H + 6)
		}
		if panel.PressedOutside() {
			e.trigger = nil
		}
		panel.Children(func() {
			if len(e.menuItems) == 0 {
				ui.Text(c, locales.EditorNoMatches()).TextColor(pal.TextMuted).Padding(8, 10)
				return
			}
			ui.Scroll(c).MaxHeight(320).Children(func() {
				group := ""
				for i, item := range e.menuItems {
					if item.Group != "" && item.Group != group {
						group = item.Group
						ui.Text(c, group).FontSize(11).FontWeight(600).TextColor(pal.TextMuted).Padding(8, 10, 4)
					}
					row := ui.ButtonBase(c.Key("item:"+item.Key)).Gap(10).Padding(6, 10).Radius(6).AlignItems(ui.Center).KeepFocus()
					if i == e.menuIndex {
						row.Background(pal.NavSelected)
						if e.reveal == "" {
							row.ScrollIntoView()
						}
					} else if row.Hovered() {
						row.Background(pal.Hover)
					}
					row.Children(func() {
						if item.Icon != "" {
							ui.Box(c).Size(26, 26).Center().Radius(6).Background(pal.Ground).Border(1, pal.Divider).Children(func() {
								ui.Icon(c, theme.Icon(item.Icon)).FontSize(14).TextColor(pal.Icon)
							})
						}
						ui.Column(c).Grow(1).Children(func() {
							ui.Text(c, item.Label).TextColor(pal.Text).SingleLine()
							if item.Hint != "" {
								ui.Text(c, item.Hint).FontSize(12).TextColor(pal.TextMuted).SingleLine()
							}
						})
					})
					if row.Hovered() && c.Modifiers() == 0 {
						_ = i
					}
					if row.Clicked() {
						t := *e.trigger
						e.trigger = nil
						e.Host.Pick(t, item)
					}
				}
			})
		})
	})
}

// toolbar is the selection's: MyGo's Toolbar on a panel in the theme's
// colors, as MyGo's popovers are, its controls leaving the keys in the
// page.
func (e *Editor) toolbar(c *ui.Context, at ui.Rect, pal *theme.Palette) {
	ui.Overlay(c, func() {
		t := c.Theme()
		panel := ui.Row(c.Key("selection-toolbar")).Bind(&e.bar).Absolute().Left(max(at.X-8, 8)).Top(max(at.Y-50, 8)).
			Radius(t.Radius+2).Background(t.Background).Border(1, t.Border).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18))
		panel.Children(func() {
			ui.Toolbar(c, func() {
				toggle := func(icon, label string, on bool, fn func()) {
					v := on
					tg := ui.Toggle(c, &v, "").Label(label).Tooltip(label).KeepFocus()
					tg.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(15) })
					if tg.Changed() {
						fn()
					}
				}
				// Code takes no formats.
				if !e.inCode() {
					toggle("bold", locales.EditorBold(), e.Has(func(a doc.Attrs) bool { return a.Bold }), e.ToggleBold)
					toggle("italic", locales.EditorItalic(), e.Has(func(a doc.Attrs) bool { return a.Italic }), e.ToggleItalic)
					toggle("code", locales.EditorInlineCode(), e.Has(func(a doc.Attrs) bool { return a.Code }), e.ToggleCode)
					toggle("link", locales.EditorLink(), e.LinkAt() != "", func() { e.linkDraft, e.linkOpen = e.LinkAt(), true })
					e.colorButton(c, pal)
				}
				comment := ui.Button(c, "").Label(locales.CommonComment()).Tooltip(locales.CommonComment()).KeepFocus()
				comment.Children(func() { ui.Icon(c, theme.Icon("message-square")).FontSize(15) })
				if comment.Clicked() {
					id, quote := e.SelectedText()
					e.Host.Comment(id, quote)
				}
				action := func(icon, label string, fn func()) {
					b := ui.Button(c, "").Label(label).KeepFocus()
					b.Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Icon(c, theme.Icon(icon)).FontSize(14)
							ui.Text(c, label)
						})
					})
					if b.Clicked() {
						fn()
					}
				}
				action("sparkles", locales.EditorAskAgent(), func() {
					id, quote := e.SelectedText()
					e.Host.AskAgent(id, quote)
				})
			}).Label(locales.EditorFormat())
		})
	})
}

func (e *Editor) linkEditor(c *ui.Context, at ui.Rect, pal *theme.Palette) {
	ui.Overlay(c, func() {
		panel := ui.Row(c.Key("link-editor")).Absolute().Left(max(at.X-8, 8)).Top(at.Y+at.H+8).Width(340).Padding(6).Gap(6).
			Radius(10).Background(pal.Surface).Border(1, pal.Border).Shadow(0, 10, 30, 0, pal.Shadow).AlignItems(ui.Center)
		if panel.PressedOutside() || panel.OverlayShortcut(0, ui.KeyEscape) {
			e.linkOpen = false
			e.Focus()
		}
		panel.Children(func() {
			ui.Icon(c, theme.Icon("link")).TextColor(pal.TextMuted).Margin(0, 2, 0, 6)
			in := ui.TextInputBase(c.Key("link-url"), &e.linkDraft).Placeholder(locales.EditorPasteLink()).Grow(1).AutoFocus()
			apply := func() {
				e.SetLink(e.linkDraft)
				e.linkOpen = false
				e.Focus()
			}
			if in.Submitted() {
				apply()
			}
			if ui.PrimaryButton(c, locales.EditorLink()).Clicked() {
				apply()
			}
			if e.LinkAt() != "" && ui.Button(c, locales.CommonRemove()).Clicked() {
				e.SetLink("")
				e.linkOpen = false
				e.Focus()
			}
		})
	})
}

// revealMargin is the room kept under a block brought into view: the bars
// at the bottom of the page, an agent's run and its Undo, cover what is
// under them.
const revealMargin = 80

// revealBelow scrolls the block of height h being built into view with
// revealMargin under it, as ScrollIntoView alone would leave it at the
// window's edge.
func revealBelow(c *ui.Context, h float32) {
	ui.Box(c.Key("reveal")).Absolute().Left(0).Top(0).Width(1).Height(h + revealMargin).ScrollIntoView()
}
