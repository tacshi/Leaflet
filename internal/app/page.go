package app

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

func (a *App) pageView(c *ui.Context) {
	ps := a.page
	p := a.pageByID(ps.id)
	if p == nil {
		a.homeView(c)
		return
	}
	ps.editor.SetWidth(a.contentWidth(c))
	ps.editor.Size = float32(a.settings.TextSize)
	a.titleBar(c, func() {
		a.breadcrumbs(c, *p)
		ui.Spacer(c)
		a.syncStatus(c)
		if a.rec.Active() {
			a.recordingPill(c)
		}
		tools(c, func() {
			if !a.rec.Active() && !ps.editor.ReadOnly {
				if iconButton(c, "record", "record", locales.PageRecord()).Clicked() {
					a.startRecording(ps.id)
				}
			}
			// A public link needs no server, so every page but a template
			// can be shared. While its link is live the button is a globe
			// in the accent color: on the web, not pressed.
			if !p.Template {
				live := a.pub != nil && a.pub.page == ps.id && a.pub.state == "live"
				icon, label := "share", locales.PageShare()
				if live {
					icon, label = "globe", locales.PageSharedPublicly()
				}
				share := iconButton(c, "share", icon, label)
				if live {
					share.TextColor(theme.Of(c).Accent)
				}
				if share.Clicked() {
					a.openShare(ps.id)
				}
			}
			more := iconButton(c, "page-more", "ellipsis", locales.PageActions())
			more.Menu(func(m *ui.Menu) { a.pageMenu(m, *p, true) })
			// The toggle stays here while the panel is open, leaving the
			// panel's top bar to its tabs: on Windows and Linux the window
			// controls take the bar's right end.
			label := locales.PageShowPanel()
			if a.panelOpen {
				label = locales.PanelHide()
			}
			toggle := iconButton(c, "toggle-panel", "panel-right", label)
			if a.panelOpen {
				pal := theme.Of(c)
				toggle.Background(pal.NavSelected).TextColor(pal.Text)
			}
			if toggle.Clicked() {
				a.panelOpen = !a.panelOpen
			}
		})
	})
	// The page menu's choice runs as the title bar draws: Delete closes
	// the page and Duplicate opens another, so the next frame draws what
	// is open now.
	if a.page != ps {
		if a.win != nil {
			a.win.Invalidate()
		}
		return
	}
	sess := a.hub.Session(ps.id)
	ui.Box(c.Key("page-body")).Grow(1).Children(func() {
		if ps.fullDiagram != "" {
			if n := ps.editor.Content.Find(ps.fullDiagram); n != nil && n.Type == "diagram" {
				a.fullDiagram(c, n)
				return
			}
			// The block went: by undo, an agent or another device.
			a.leaveFullDiagram()
		}
		a.scrollToBlock(c)
		scroll := ui.Scroll(c.Key("page-scroll:" + ps.id)).TrackScroll(&ps.scroll).Fill().Bind(&ps.view)
		scroll.Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(40, pagePadX, 160).Children(func() {
				col := ui.Column(c).Width(a.contentWidth(c)).Gap(0)
				col.Children(func() {
					a.pageTitle(c, *p)
					a.byline(c, *p)
					if notices := a.pageNotices(c, ps.id); notices {
						ui.Box(c).Height(8)
					}
					a.dimLit(c)
					ps.editor.View(c)
					if ps.editor.Focused() {
						ps.caretSet = true
					}
					a.dropVideos()
					a.dropEmbeds()
					a.dropGonePlayers()
					if ps.editor.Content.IsEmpty() && !ps.editor.ReadOnly && a.dialogs.inline == nil {
						a.emptyActions(c)
					}
					a.inlinePrompt(c)
					// Clicking below the last block writes at the end.
					tail := ui.Box(c.Key("page-tail")).Height(220).FillWidth().Cursor(ui.CursorText)
					if tail.Clicked() && !ps.editor.ReadOnly {
						a.caretAtEnd()
					}
				})
			})
		})
		// One bar at a time, and none under a toast.
		if sess.Running {
			a.runningBar(c, sess)
		} else if run := sess.Latest(); run != nil && time.Since(run.At) < 10*time.Minute && run.Status != "running" && time.Now().After(a.toastUntil) {
			a.runBar(c, run)
		}
	})
}

// breadcrumbs is MyGo's path of the page: its folder, the pages above it,
// and it, where a click on the folder shows it open in the sidebar's
// Pages tab and one on a page above opens that page. Its marks follow it.
func (a *App) breadcrumbs(c *ui.Context, p store.Page) {
	pal := theme.Of(c)
	var path []string
	var open []func()
	// The folder is the page's at the top's: a subpage is in none.
	ancestors := a.ancestors(p.ID)
	top := p
	if len(ancestors) > 0 {
		top = ancestors[0]
	}
	if top.FolderID != "" {
		for _, f := range a.folders {
			if f.ID == top.FolderID {
				path = append(path, f.Name)
				open = append(open, func() { a.showInTree("folder", f.ID) })
			}
		}
	}
	for _, anc := range ancestors {
		path = append(path, anc.DisplayTitle())
		open = append(open, func() { a.openPage(anc.ID) })
	}
	path = append(path, p.DisplayTitle())
	ui.Row(c.Key("crumbs")).Gap(6).AlignItems(ui.Center).Margin(0, 0, 0, 6).Shrink(1).ClipX().Children(func() {
		chosen := -1
		crumbs := ui.Breadcrumbs(c, path, &chosen).Label(locales.PageBreadcrumbsLabel()).FontSize(13).Shrink(1)
		if crumbs.Changed() && chosen >= 0 && chosen < len(open) {
			open[chosen]()
		}
		// What the page is, as icons; that it is shared with the user
		// is in its byline.
		mark := func(icon, label string) {
			ui.Icon(c, theme.Icon(icon)).FontSize(14).TextColor(pal.TextMuted).Shrink(0).Label(label).Tooltip(label)
		}
		if p.Template {
			mark("layout-template", locales.TemplatePill())
		}
		if a.page.editor.ReadOnly {
			mark("eye", locales.PageViewOnly())
		}
	})
}

// syncStatus shows an icon only when saving needs the user: offline with
// edits waiting, or a sync that failed. A page that saved says so in its
// byline; the account row says how syncing goes.
func (a *App) syncStatus(c *ui.Context) {
	pal := theme.Of(c)
	if !a.syncOn() {
		return
	}
	var tip, icon string
	switch {
	case a.offlineNow():
		tip, icon = locales.PageSyncOffline(), "cloud-off"
		if a.pending > 0 {
			tip = locales.PageSyncOfflineQueued(a.pending)
		}
		if a.settings.WorkOffline {
			tip = locales.PageSyncGoOnline(tip)
		}
	case a.syncError != "":
		tip, icon = locales.PageSyncFailed(a.syncError), "circle-alert"
	default:
		return
	}
	b := ui.ButtonBase(c.Key("sync-status")).Size(28, 28).Center().Radius(7).Label(tip).Tooltip(tip).TextColor(pal.WarnText)
	if b.Hovered() {
		b.Background(pal.Hover)
	}
	b.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(15) })
	if b.Clicked() {
		if a.settings.WorkOffline {
			a.settings.WorkOffline = false
			a.saveSettings()
		}
		a.sync.Kick()
	}
}

func (a *App) pageTitle(c *ui.Context, p store.Page) {
	pal := theme.Of(c)
	ps := a.page
	size := float32(a.settings.TextSize) * 2.15
	// A text area, so a long title wraps rather than scrolls out of view.
	// It holds one line: Enter, and Down at its end, go on to the page.
	in := ui.TextAreaBase(c.Key("title:"+p.ID), &ps.title).Bind(&ps.titleIn).Lines(1, 8).FillWidth().
		Placeholder(locales.CommonUntitled()).
		Font(theme.PageFamily(ps.title)).FontSize(size).FontWeight(700).TextColor(pal.Text).ReadOnly(ps.editor.ReadOnly).
		Label(locales.PageTitleLabel()).Margin(0, 0, 6).
		OnTextKey(func(k ui.TextKey) bool {
			leave := k.Mods == 0 && (k.Key == ui.KeyEnter || k.Key == ui.KeyDown && ps.titleAtEnd)
			if leave {
				ps.titleLeave = true
				a.win.Invalidate()
			}
			return leave || k.Key == ui.KeyEnter // no line breaks in a title
		})
	if ps.focusTitle > 0 {
		if in.Focused() {
			ps.focusTitle = 0
		} else {
			ps.focusTitle--
			in.Focus()
			if ps.selectTitle {
				in.SetTextSelection(0, utf8.RuneCountInString(ps.title))
			}
		}
	}
	if in.Changed() {
		// A pasted title keeps to one line.
		if strings.ContainsAny(ps.title, "\r\n") {
			ps.title = strings.Join(strings.Fields(ps.title), " ")
			a.win.Invalidate()
		}
		// Rename shortly after typing stops, as the content saves.
		a.saveTitleSoon()
	}
	start, end := in.TextSelection()
	ps.titleAtEnd = start == end && end == utf8.RuneCountInString(ps.title)
	if ps.titleLeave {
		ps.titleLeave = false
		a.saveTitle()
		// A page left with no blocks gets one to type in.
		if ch := ps.editor.Content.Children; len(ch) > 0 {
			ps.editor.PlaceCaret(ch[0].ID, 0)
			ps.editor.Focus()
		} else {
			a.caretAtEnd()
		}
	}
}

// saveTitleSoon renames the open page shortly after its title's typing
// stops, as its content saves; a page closed meanwhile saved its title
// as it closed.
func (a *App) saveTitleSoon() {
	ps := a.page
	if ps.titleTimer != nil {
		ps.titleTimer.Stop()
	}
	ps.titleTimer = a.later(600*time.Millisecond, func() {
		if a.page == ps {
			a.saveTitle()
		}
	})
}

func (a *App) byline(c *ui.Context, p store.Page) {
	pal := theme.Of(c)
	who := ""
	if p.CreatedBy == "" || p.CreatedBy == a.session.UserID {
		who = a.session.DisplayName
	}
	if who == "" {
		who = locales.PageBylineSomeone()
	}
	row := ui.Row(c.Key("byline")).Gap(8).AlignItems(ui.Center).Margin(0, 0, 22).Wrap()
	hovered := row.Hovered()
	row.Children(func() {
		// The page keeps who created it, not who edited it last.
		when := locales.PageBylineEditedAt(humanTime(p.UpdatedAt))
		if a.page.live.Dirty() {
			when = locales.PageBylineSaving()
		}
		ui.Text(c, locales.PageBylineCreated(who)).FontSize(13).TextColor(pal.TextMuted)
		ui.Text(c, "·").TextColor(pal.ChartMuted)
		ui.Text(c, when).FontSize(13).TextColor(pal.TextMuted)
		if a.isShared(p) {
			ui.Text(c, "·").TextColor(pal.ChartMuted)
			ui.Text(c, locales.PageBylineSharedWithYou()).FontSize(13).TextColor(pal.TextMuted)
		} else if n := a.sharedWith(); n > 0 {
			ui.Text(c, "·").TextColor(pal.ChartMuted)
			ui.Text(c, locales.PageBylineSharedWith(n)).FontSize(13).TextColor(pal.TextMuted)
		}
		a.bylineTags(c, p, hovered)
	})
}

// sharedWith counts who the open page is shared with: its members but
// the user, who is among them as its owner even on a page only synced.
func (a *App) sharedWith() int {
	n := 0
	for _, m := range a.page.members {
		if m.UserID != a.session.UserID {
			n++
		}
	}
	return n
}

// bylineTags are the page's tags in its byline, a click on one showing
// its pages in the sidebar. On a page the user may change, a button shown
// while the byline is hovered edits them in MyGo's token field, which
// saves each change and closes as it loses the focus.
func (a *App) bylineTags(c *ui.Context, p store.Page, hovered bool) {
	pal := theme.Of(c)
	ps := a.page
	if ps.tagsEditing {
		in := ui.TokenField(c.Key("tags"), &ps.tags, a.tagNames()).Label(locales.PageTagsLabel()).
			FontSize(13).MinWidth(220).AutoFocus()
		if in.Changed() {
			if err := a.st.SetPageTags(ps.id, ps.tags); err != nil {
				a.toast(locales.SidebarCouldNotSave(err.Error()))
			}
			if q, err := a.st.Page(ps.id); err == nil {
				ps.tags = slices.Clone(q.Tags)
			}
		}
		switch {
		case in.Focused():
			ps.tagsFocused = true
		case ps.tagsFocused:
			ps.tagsEditing, ps.tagsFocused = false, false
		}
		if in.Shortcut(0, ui.KeyEscape) {
			ps.tagsEditing, ps.tagsFocused = false, false
		}
		return
	}
	if len(p.Tags) > 0 {
		ui.Text(c, "·").TextColor(pal.ChartMuted)
		for _, t := range p.Tags {
			if tagButton(c, t).Clicked() {
				a.showTag(t)
			}
		}
	}
	if !a.canEdit(p.ID) {
		return
	}
	tools(c, func() {
		// Shown as Tab reaches it too, as nothing else adds a tag.
		b := iconButton(c, "add-tag", "tag", locales.PageAddTag()).Size(24, 24).Padding(4)
		if !hovered && !b.Focused() {
			b.Opacity(0)
		}
		if b.Clicked() {
			ps.tags, ps.tagsEditing, ps.tagsFocused = slices.Clone(p.Tags), true, false
		}
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// humanTime says when t was, as of now (locales.When).
func humanTime(t time.Time) string { return locales.When(t, time.Now()) }

// caretAtEnd puts the caret at the end of the page, after a new paragraph
// when the last block has no text or there is no block.
func (a *App) caretAtEnd() {
	ed := a.page.editor
	var last *doc.Node
	if children := ed.Content.Children; len(children) > 0 {
		last = children[len(children)-1]
		for len(last.Children) > 0 && last.Type != "table" {
			last = last.Children[len(last.Children)-1]
		}
	}
	if last == nil || !last.IsText() {
		p := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		p.SetDelta(nil)
		ed.Edit(func(c *doc.Content) { c.Children = append(c.Children, p) })
		last = p
	}
	ed.PlaceCaret(last.ID, -1)
	ed.Focus()
}

// emptyActions are the ways to start an empty page.
func (a *App) emptyActions(c *ui.Context) {
	pal := theme.Of(c)
	ui.Column(c.Key("empty")).Gap(6).Margin(18, 0, 0).AlignItems(ui.Start).Children(func() {
		ui.Text(c, locales.PageStartWith()).FontSize(12).FontWeight(600).TextColor(pal.TextMuted).Margin(0, 0, 4, 2)
		// A row with a menu shows its button while hovered, as the
		// sidebar's rows do.
		row := func(key, icon, label, hint string, fn func(), menu func(m *ui.Menu)) {
			b := ui.ButtonBase(c.Key(key)).Padding(8, 10).Gap(12).Radius(8).AlignItems(ui.Center).Justify(ui.Start).FillWidth().MaxWidth(420)
			hovered := b.Hovered()
			if hovered {
				b.Background(pal.Hover)
			}
			b.Children(func() {
				ui.Box(c).Size(30, 30).Radius(8).Center().Background(pal.Ground).Border(1, pal.Divider).Children(func() {
					ui.Icon(c, theme.Icon(icon)).FontSize(15).TextColor(pal.Icon)
				})
				ui.Column(c).Grow(1).Shrink(1).Children(func() {
					ui.Text(c, label).FontSize(14).FontWeight(550).TextColor(pal.Text).SingleLine()
					ui.Text(c, hint).FontSize(12).TextColor(pal.TextMuted).SingleLine()
				})
				if menu != nil {
					tools(c, func() {
						more := iconButton(c, "more", "ellipsis", locales.PageTemplateActions()).Size(24, 24).Padding(4).IgnoreInput(!hovered)
						if !hovered {
							more.Opacity(0)
						}
						more.Menu(menu)
					})
				}
			})
			if menu != nil {
				b.PopoverMenu(menu)
			}
			if b.Clicked() {
				fn()
			}
		}
		row("draft", "sparkles", locales.PageDraftWithAgent(), locales.PageDraftWithAgentHint(), func() {
			a.panelOpen, a.panelTab = true, 0
			a.dialogs.focusChat = true
		}, nil)
		row("subpage", "file-plus", locales.PageAddSubpage(), locales.PageAddSubpageHint(), func() { a.addSubpageBlock() }, nil)
		row("upload", "upload", locales.PageUpload(), locales.PageUploadHint(), func() { a.uploadIntoPage() }, nil)
		// A template starts from the others, not from itself.
		if p := a.pageByID(a.page.id); p != nil && !p.Template {
			a.templateActions(c, row)
		}
	})
}

// undoRun takes back an agent's run on the open page, or says why it
// could not: the run is then left as it was.
func (a *App) undoRun(run *agents.Run) {
	if err := a.hub.UndoRun(a.page.id, run.ID); err != nil {
		a.toast(locales.PageUndoRunFailed(run.AgentName, err.Error()))
	}
}

// redoRun makes an undone run's edits again, or says why it could not:
// the run then stays undone.
func (a *App) redoRun(run *agents.Run) {
	if err := a.hub.RedoRun(a.page.id, run.ID); err != nil {
		a.toast(locales.PageRedoRunFailed(run.AgentName, err.Error()))
	}
}

// runBar offers to undo the latest agent run.
func (a *App) runBar(c *ui.Context, run *agents.Run) {
	pal := theme.Of(c)
	if run.Error != "" || a.dialogs.hideRun == run.ID {
		return
	}
	ui.Row(c.Key("run-bar")).Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(18).Padding(6, 6, 6, 14).Gap(10).Radius(10).
		Background(pal.Inverse).TextColor(pal.OnInverse).Shadow(0, 10, 30, 0, pal.Shadow).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, theme.Icon("sparkles")).FontSize(14)
		text := locales.PageRunEdited(run.AgentName, len(uniq(run.BlockIDs())))
		if run.Undone {
			text = locales.PageRunUndone(run.AgentName)
		}
		ui.Text(c, text).FontSize(13).FontWeight(500)
		btn := func(key, label string) bool {
			b := ui.ButtonBase(c.Key(key)).Height(26).Padding(0, 10).Radius(7).Background(pal.OnInverse.Alpha(0.12))
			if b.Hovered() {
				b.Background(pal.OnInverse.Alpha(0.2))
			}
			b.Children(func() { ui.Text(c, label).FontSize(12.5).FontWeight(600).TextColor(pal.OnInverse) })
			return b.Clicked()
		}
		if !run.Undone && btn("undo-run", locales.CommonUndo()) {
			a.undoRun(run)
		}
		if run.Undone && btn("redo-run", locales.PageRedo()) {
			a.redoRun(run)
		}
		x := ui.ButtonBase(c.Key("hide-run")).Size(24, 24).Center().Radius(6).Label(locales.PageDismiss())
		x.Children(func() { ui.Icon(c, theme.Icon("x")).FontSize(13).TextColor(pal.OnInverse.Alpha(0.7)) })
		if x.Clicked() {
			a.dialogs.hideRun = run.ID
			for k := range a.page.tinted {
				delete(a.page.tinted, k)
			}
		}
	})
}

func (a *App) runningBar(c *ui.Context, s *agents.Session) {
	pal := theme.Of(c)
	var run *agents.Run
	for _, r := range s.Runs {
		if r.Status == "running" || r.Status == "queued" {
			run = r
		}
	}
	// A run from Ask agent shows how it goes in its own prompt, and one
	// from the chat in the chat, while it is open.
	ip := a.dialogs.inline
	switch {
	case run == nil, ip != nil && ip.runID == run.ID:
		return
	case run.Surface == "sidebar" && a.panelOpen && a.panelTab == 0:
		return
	}
	bar := ui.Row(c.Key("running-bar")).Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(18).Padding(8, 14).Gap(10).Radius(10).
		Background(pal.Surface).Border(1, pal.AccentSoftBorder).Shadow(0, 10, 30, 0, pal.Shadow).AlignItems(ui.Center)
	// A run on this device can be stopped; one on the server goes on.
	stoppable := run.Stoppable()
	if stoppable {
		bar.Padding(6, 6, 6, 14)
	}
	bar.Children(func() {
		ui.Spinner(c).Size(14, 14)
		ui.Text(c, runProgress(run, locales.PageAgentWorking(run.AgentName))).FontSize(13).TextColor(pal.Text)
		if stoppable && button(c, "stop-run", "square-stop", locales.PageStopRun()).Height(26).Clicked() {
			a.hub.Stop(a.page.id, run.ID)
		}
	})
}

// runProgress is what a running agent is doing: its latest edit, or
// working while it has made none.
func runProgress(run *agents.Run, working string) string {
	if n := len(run.Edits); n > 0 {
		return run.Edits[n-1].Summary + "…"
	}
	return working
}

func uniq(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// pageNotices shows conflicts on the open page, or that its content
// could not be read.
func (a *App) pageNotices(c *ui.Context, pageID string) bool {
	if a.page.live.Unreadable() {
		notice(c, "warn", "triangle-alert", locales.PageUnreadable(), nil)
		return true
	}
	n := 0
	for _, cf := range a.conflicts {
		if cf.PageID == pageID {
			n++
		}
	}
	if n == 0 {
		return false
	}
	text := locales.PageConflicts(n)
	notice(c, "warn", "git-compare", text, func() {
		if button(c, "review", "", locales.PageReview()).Clicked() {
			a.openSettings("offline")
		}
	})
	return true
}
