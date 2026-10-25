package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/theme"
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
		} else if !ps.editor.ReadOnly {
			if iconButton(c, "record", "mic", "Record").Clicked() {
				a.startRecording(ps.id, "")
			}
		}
		if iconButton(c, "share", "share", "Share").Clicked() {
			a.openShare(ps.id)
		}
		more := iconButton(c, "page-more", "ellipsis", "Page actions")
		more.Menu(func(m *ui.Menu) { a.pageMenu(m, *p) })
		// The panel's own top bar has the toggle while it is open, as the
		// sidebar does on the left.
		if !a.panelOpen {
			if iconButton(c, "show-panel", "panel-right", "Show agent and comments (⌘J)").Clicked() {
				a.panelOpen = true
			}
		}
	})
	sess := a.hub.Session(ps.id)
	ui.Box(c.Key("page-body")).Grow(1).Children(func() {
		scroll := ui.Scroll(c.Key("page-scroll:" + ps.id)).TrackScroll(&ps.scroll).Fill()
		scroll.Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(40, pagePadX, 160).Children(func() {
				col := ui.Column(c).Width(a.contentWidth(c)).Gap(0)
				col.Children(func() {
					a.pageTitle(c, *p)
					a.byline(c, *p)
					if notices := a.pageNotices(c, ps.id); notices {
						ui.Box(c).Height(8)
					}
					ps.editor.View(c)
					if a.isEmpty() && !ps.editor.ReadOnly {
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
		if run := sess.Latest(); run != nil && time.Since(run.At) < 10*time.Minute && run.Status != "running" {
			a.runBar(c, run)
		}
		if sess.Running {
			a.runningBar(c, sess)
		}
	})
}

func (a *App) breadcrumbs(c *ui.Context, p store.Page) {
	pal := theme.Of(c)
	ui.Row(c.Key("crumbs")).Gap(2).AlignItems(ui.Center).Margin(0, 0, 0, 6).Shrink(1).ClipX().Children(func() {
		if p.FolderID != "" {
			for _, f := range a.folders {
				if f.ID == p.FolderID {
					ui.Text(c, f.Name).FontSize(13).TextColor(pal.TextMuted).SingleLine()
					ui.Icon(c, theme.Icon("chevron-right")).FontSize(11).TextColor(pal.ChartMuted)
				}
			}
		}
		for _, anc := range a.ancestors(p.ID) {
			b := ui.ButtonBase(c.Key("crumb:"+anc.ID)).Padding(3, 6).Radius(6)
			if b.Hovered() {
				b.Background(pal.Hover)
			}
			b.Children(func() {
				ui.Text(c, anc.DisplayTitle()).FontSize(13).TextColor(pal.TextMuted).SingleLine().MaxWidth(160)
			})
			if b.Clicked() {
				a.openPage(anc.ID)
			}
			ui.Icon(c, theme.Icon("chevron-right")).FontSize(11).TextColor(pal.ChartMuted)
		}
		// The page's own title shows here once its big title scrolls away.
		if a.page.scroll.Y > 80 {
			ui.Text(c, p.DisplayTitle()).FontSize(13).FontWeight(600).TextColor(pal.Text).SingleLine().MaxWidth(260).Padding(3, 6)
		}
		if a.isShared(p) {
			ui.Text(c, "Shared").FontSize(11).FontWeight(600).TextColor(pal.AccentText).Padding(1, 6).Radius(999).Background(pal.AccentSoft)
		}
		if a.page.editor.ReadOnly {
			ui.Text(c, "View only").FontSize(11).FontWeight(600).TextColor(pal.TextMuted).Padding(1, 6).Radius(999).Background(pal.Ground)
		}
	})
}

// syncStatus shows whether the page is saved and synced as one icon, with
// the details in its tooltip; a click works offline or online again.
func (a *App) syncStatus(c *ui.Context) {
	pal := theme.Of(c)
	tip, icon, fg := "Saved on this device", "check", pal.TextMuted
	switch {
	case !a.session.SignedIn:
		if a.page != nil && a.page.dirty {
			tip, icon = "Saving…", "refresh-cw"
		}
	case a.settings.WorkOffline || !a.online:
		tip, icon, fg = "Offline", "cloud-off", pal.WarnText
		if a.pending > 0 {
			tip = fmt.Sprintf("Offline · %d %s queued", a.pending, plural(a.pending, "edit", "edits"))
		}
		if a.settings.WorkOffline {
			tip += " · click to go back online"
		}
	case a.syncing || (a.page != nil && a.page.dirty) || a.pending > 0:
		tip, icon = "Syncing…", "refresh-cw"
	default:
		tip = "Saved and synced · click to work offline"
	}
	b := ui.ButtonBase(c.Key("sync-status")).Size(28, 28).Center().Radius(7).Label(tip).Tooltip(tip).TextColor(fg)
	if b.Hovered() && a.session.SignedIn {
		b.Background(pal.Hover)
	}
	b.Children(func() {
		ic := ui.Icon(c, theme.Icon(icon)).FontSize(15)
		if icon == "refresh-cw" {
			ic.Rotate(ic.Loop("spin", 1200*time.Millisecond, ui.Linear) * 360)
		}
	})
	if b.Clicked() && a.session.SignedIn {
		a.settings.WorkOffline = !a.settings.WorkOffline
		a.saveSettings()
		if !a.settings.WorkOffline {
			a.sync.Kick()
		}
	}
}

func (a *App) pageTitle(c *ui.Context, p store.Page) {
	pal := theme.Of(c)
	ps := a.page
	size := float32(a.settings.TextSize) * 2.15
	in := ui.TextInputBase(c.Key("title:"+p.ID), &ps.title).Bind(&ps.titleIn).Placeholder("Untitled").
		Font(theme.PageFont).FontSize(size).FontWeight(700).TextColor(pal.Text).ReadOnly(ps.editor.ReadOnly).
		Label("Page title").Margin(0, 0, 6)
	if in.Changed() {
		// Rename shortly after typing stops, as the content saves.
		a.saveTitleSoon()
	}
	if in.Submitted() {
		a.saveTitle()
		ps.editor.PlaceCaret(ps.editor.Content.Children[0].ID, 0)
		ps.editor.Focus()
	}
	if in.Shortcut(0, ui.KeyDown) {
		ps.editor.PlaceCaret(ps.editor.Content.Children[0].ID, 0)
		ps.editor.Focus()
	}
}

var titleTimer *time.Timer

func (a *App) saveTitleSoon() {
	if titleTimer != nil {
		titleTimer.Stop()
	}
	titleTimer = time.AfterFunc(600*time.Millisecond, func() { a.update(a.saveTitle) })
}

func (a *App) byline(c *ui.Context, p store.Page) {
	pal := theme.Of(c)
	editors := []string{}
	if p.CreatedBy == "" || p.CreatedBy == a.session.UserID {
		editors = append(editors, a.session.DisplayName)
	}
	for _, r := range a.hub.Session(p.ID).Runs {
		if len(r.Ops) > 0 && !contains(editors, r.AgentName) {
			editors = append(editors, r.AgentName)
		}
	}
	who := strings.Join(editors, " and ")
	if who == "" {
		who = "Someone"
	}
	ui.Row(c.Key("byline")).Gap(8).AlignItems(ui.Center).Margin(0, 0, 22).Children(func() {
		ui.Text(c, "Edited by "+who+" · "+humanTime(p.UpdatedAt)).FontSize(13).TextColor(pal.TextMuted)
		if n := len(a.page.members); n > 0 {
			ui.Text(c, "·").TextColor(pal.ChartMuted)
			ui.Textf(c, "Shared with %d", n).FontSize(13).TextColor(pal.TextMuted)
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

func humanTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour && t.Day() == time.Now().Day():
		return "today at " + t.Format("15:04")
	case d < 48*time.Hour:
		return "yesterday at " + t.Format("15:04")
	case t.Year() == time.Now().Year():
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// caretAtEnd puts the caret at the end of the page, after a new paragraph
// when the last block has no text.
func (a *App) caretAtEnd() {
	ed := a.page.editor
	children := ed.Content.Children
	last := children[len(children)-1]
	for len(last.Children) > 0 && last.Type != "table" {
		last = last.Children[len(last.Children)-1]
	}
	if !last.IsText() {
		p := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		p.SetDelta(nil)
		ed.Edit(func(c *doc.Content) { c.Children = append(c.Children, p) })
		last = p
	}
	ed.PlaceCaret(last.ID, -1)
	ed.Focus()
}

func (a *App) isEmpty() bool {
	ch := a.page.editor.Content.Children
	return len(ch) == 1 && ch[0].Type == "paragraph" && ch[0].Delta().Len() == 0 && len(ch[0].Children) == 0
}

// emptyActions are the ways to start an empty page.
func (a *App) emptyActions(c *ui.Context) {
	pal := theme.Of(c)
	ui.Column(c.Key("empty")).Gap(6).Margin(18, 0, 0).Children(func() {
		ui.Text(c, "Start with").FontSize(12).FontWeight(600).TextColor(pal.TextMuted).Margin(0, 0, 4, 2)
		row := func(key, icon, label, hint string, fn func()) {
			b := ui.ButtonBase(c.Key(key)).Padding(8, 10).Gap(12).Radius(8).AlignItems(ui.Center).MaxWidth(420)
			if b.Hovered() {
				b.Background(pal.Hover)
			}
			b.Children(func() {
				ui.Box(c).Size(30, 30).Radius(8).Center().Background(pal.Ground).Border(1, pal.Divider).Children(func() {
					ui.Icon(c, theme.Icon(icon)).FontSize(15).TextColor(pal.Icon)
				})
				ui.Column(c).Children(func() {
					ui.Text(c, label).FontSize(14).FontWeight(550).TextColor(pal.Text)
					ui.Text(c, hint).FontSize(12).TextColor(pal.TextMuted)
				})
			})
			if b.Clicked() {
				fn()
			}
		}
		row("draft", "sparkles", "Draft with an agent", "Say what this page is for", func() {
			a.panelOpen, a.panelTab = true, 0
			a.dialogs.focusChat = true
		})
		row("subpage", "file-plus", "Add a subpage", "Pages nest inside pages", func() { a.addSubpageBlock() })
		row("upload", "upload", "Upload a file", "Images go in the page; other files in the space", func() { a.uploadIntoPage() })
	})
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
		text := fmt.Sprintf("%s edited %d %s", run.AgentName, len(uniq(run.BlockIDs())), plural(len(uniq(run.BlockIDs())), "block", "blocks"))
		if run.Undone {
			text = "Undid " + run.AgentName + "'s edits"
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
		if !run.Undone && btn("undo-run", "Undo run") {
			a.hub.UndoRun(a.page.id, run.ID)
		}
		if run.Undone && btn("redo-run", "Redo") {
			a.hub.RedoRun(a.page.id, run.ID)
		}
		x := ui.ButtonBase(c.Key("hide-run")).Size(24, 24).Center().Radius(6).Label("Dismiss")
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
	if run == nil {
		return
	}
	ui.Row(c.Key("running-bar")).Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(18).Padding(8, 14).Gap(10).Radius(10).
		Background(pal.Surface).Border(1, pal.AccentSoftBorder).Shadow(0, 10, 30, 0, pal.Shadow).AlignItems(ui.Center).Children(func() {
		ui.Spinner(c).Size(14, 14)
		label := run.AgentName + " is working…"
		if n := len(run.Edits); n > 0 {
			label = run.Edits[n-1].Summary + "…"
		}
		ui.Text(c, label).FontSize(13).TextColor(pal.Text)
	})
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

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// pageNotices shows conflicts on the open page.
func (a *App) pageNotices(c *ui.Context, pageID string) bool {
	n := 0
	for _, cf := range a.conflicts {
		if cf.PageID == pageID {
			n++
		}
	}
	if n == 0 {
		return false
	}
	text := fmt.Sprintf("%d %s changed here and on another device while apart. The newer version of each was kept.", n, plural(n, "block", "blocks"))
	notice(c, "warn", "git-compare", text, func() {
		if button(c, "review", "", "Review").Clicked() {
			a.navigate(Route{Kind: "settings", Section: "offline"})
		}
	})
	return true
}
