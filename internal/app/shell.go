package app

import (
	"github.com/egoist/mygo/ui"

	"leaflet/internal/theme"
)

const (
	pageMaxWidth = 720
	pagePadX     = 56
	panelWidth   = 360
)

// view builds the window.
func (a *App) view(c *ui.Context) {
	theme.Apply(c)
	pal := theme.Of(c)
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}
	a.shortcuts(c)
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		if a.settings.SidebarOpen {
			width := a.sidebarW
			bar := ui.Column(c.Key("sidebar")).Width(width).FillHeight()
			if !c.Vibrancy() {
				bar.Background(pal.Sidebar)
			}
			bar.Children(func() { a.sidebar(c) })
			a.sidebarHandle(c)
		}
		ui.Column(c.Key("main")).Grow(1).FillHeight().Background(pal.Surface).Children(func() { a.main(c) })
		if a.panelOpen && a.route.Kind == "page" && a.page != nil {
			ui.Column(c.Key("panel")).Width(panelWidth).FillHeight().Background(pal.Panel).
				BorderWidth(0, 0, 0, 1).BorderColor(pal.Divider).Children(func() { a.panel(c) })
		}
	})
	a.dialogs.view(c, a)
	a.toastView(c)
}

// sidebarHandle is the edge the user drags to resize the sidebar.
func (a *App) sidebarHandle(c *ui.Context) {
	pal := theme.Of(c)
	h := ui.Box(c.Key("sidebar-edge")).Width(1).FillHeight().Background(pal.Divider).Cursor(ui.CursorResizeColumn)
	grip := ui.Box(c.Key("sidebar-grip")).Width(7).FillHeight().Margin(0, 0, 0, -4).Cursor(ui.CursorResizeColumn).Label("Resize sidebar")
	if dx, _, ok := grip.Dragged(); ok && dx != 0 {
		a.sidebarW = min(max(a.sidebarW+dx, 200), 420)
	}
	if _, _, ok := grip.DragEnded(); ok {
		a.settings.SidebarWidth = a.sidebarW
		a.saveSettings()
	}
	if grip.Hovered() || grip.Dragging() {
		h.Background(pal.Accent)
	}
}

// contentWidth is the width the page's text wraps at.
func (a *App) contentWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	if a.settings.SidebarOpen {
		w -= a.sidebarW + 1
	}
	if a.panelOpen {
		w -= panelWidth + 1
	}
	return max(min(w-2*pagePadX, pageMaxWidth), 280)
}

func (a *App) main(c *ui.Context) {
	switch a.route.Kind {
	case "page":
		if a.page != nil {
			a.pageView(c)
			return
		}
	case "settings":
		a.settingsView(c)
		return
	case "signin":
		a.signInView(c)
		return
	}
	a.homeView(c)
}

// titleBar is the strip at the top of the main area: the window's drag
// region, with the window controls' room when the sidebar is hidden.
func (a *App) titleBar(c *ui.Context, fn func()) {
	pal := theme.Of(c)
	bar := c.TitleBar()
	left := float32(12)
	if !a.settings.SidebarOpen {
		left = bar.Left + 12
	}
	ui.Row(c.Key("titlebar")).Height(max(bar.Height, 46)).Padding(0, 12, 0, left).Gap(4).AlignItems(ui.Center).
		DragWindow().BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
		if !a.settings.SidebarOpen {
			if iconButton(c, "show-sidebar", "panel-left", "Show sidebar").Clicked() {
				a.toggleSidebar()
			}
		}
		back := iconButton(c, "back", "chevron-left", "Back")
		back.Disabled(len(a.history) == 0)
		if len(a.history) == 0 {
			back.Opacity(0.35)
		}
		if back.Clicked() {
			a.back()
		}
		fwd := iconButton(c, "forward", "chevron-right", "Forward")
		fwd.Disabled(len(a.future) == 0)
		if len(a.future) == 0 {
			fwd.Opacity(0.35)
		}
		if fwd.Clicked() {
			a.forward()
		}
		fn()
	})
}

func (a *App) shortcuts(c *ui.Context) {
	c.OnShortcut(ui.Cmd, ui.KeyP, a.openSearch)
	c.OnShortcut(ui.Cmd, ui.KeyBackslash, a.toggleSidebar)
	c.OnShortcut(ui.Cmd, ui.KeyN, func() { a.newPage("", "") })
	c.OnShortcut(ui.Cmd, ui.KeyJ, func() { a.togglePanel(0) })
	c.OnShortcut(ui.Cmd, ui.KeyBracketLeft, a.back)
	c.OnShortcut(ui.Cmd, ui.KeyBracketRight, a.forward)
	c.OnShortcut(ui.Cmd, ui.KeyComma, func() { a.navigate(Route{Kind: "settings", Section: "account"}) })
}

// homeView shows when no page is open.
func (a *App) homeView(c *ui.Context) {
	pal := theme.Of(c)
	a.titleBar(c, func() { ui.Spacer(c) })
	ui.Column(c).Grow(1).Center().Gap(14).Children(func() {
		ui.Icon(c, theme.Icon("file-text")).FontSize(36).TextColor(pal.ChartMuted)
		ui.Text(c, "No page open").FontSize(18).FontWeight(650).TextColor(pal.Text)
		ui.Text(c, "Pick a page in the sidebar, or start a new one.").TextColor(pal.TextMuted)
		if primary(c, "new", "plus", "New page").Clicked() {
			a.newPage("", "")
		}
	})
}

func (a *App) toastView(c *ui.Context) {
	for _, t := range a.toasts {
		if t.action != "" {
			fn := t.fn
			c.ToastAction(t.text, t.action, fn)
		} else {
			c.Toast(t.text)
		}
	}
	a.toasts = nil
}
