package app

import (
	"time"

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
	a.refreshTray()
	a.follow()
	// The page, and beside it the agent and comments panel when open.
	page := func() {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			main := ui.Column(c.Key("main")).Grow(1).FillHeight().Background(pal.Surface)
			if a.router == nil {
				main.Children(func() { a.main(c) })
			} else {
				// The router's view: its page, titled for Back's list.
				main.Children(func() {
					a.router.View(c, func(r *ui.Route) {
						r.Title(a.routeTitle())
						a.main(c)
					}).Fill()
				})
			}
			if a.panelOpen && a.route.Kind == "page" && a.page != nil {
				ui.Column(c.Key("panel")).Width(panelWidth).FillHeight().Background(pal.Panel).
					BorderWidth(0, 0, 0, 1).BorderColor(pal.Divider).Children(func() { a.panel(c) })
			}
		})
	}
	if !a.settings.SidebarOpen {
		page()
	} else {
		// MyGo's split: its line, dragged or moved with the arrows,
		// resizes the sidebar.
		split := ui.Split(c, &a.sidebarW, func() {
			bar := ui.Column(c.Key("sidebar")).Fill()
			if !c.Vibrancy() || pal.Opaque {
				bar.Background(pal.Sidebar)
			}
			bar.Children(func() { a.sidebar(c) })
		}, page).Fill()
		if split.Changed() {
			a.sidebarW = min(max(a.sidebarW, sidebarMin), sidebarMax)
			a.saveSidebarSoon()
		}
	}
	a.dialogs.view(c, a)
	a.toastView(c)
}

// The sidebar's width stays between these.
const sidebarMin, sidebarMax = 200, 420

var sidebarTimer *time.Timer

// saveSidebarSoon keeps the sidebar's width once it stops moving.
func (a *App) saveSidebarSoon() {
	if sidebarTimer != nil {
		sidebarTimer.Stop()
	}
	sidebarTimer = time.AfterFunc(400*time.Millisecond, func() {
		a.update(func() {
			a.settings.SidebarWidth = a.sidebarW
			a.saveSettings()
		})
	})
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
	case "todos":
		a.todosView(c)
		return
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
		tools(c, func() {
			if !a.settings.SidebarOpen {
				if iconButton(c, "show-sidebar", "panel-left", "Show sidebar").Clicked() {
					a.toggleSidebar()
				}
			}
			if a.router != nil {
				ui.BackButton(c, a.router)
				ui.ForwardButton(c, a.router)
			}
		})
		fn()
	})
}

func (a *App) shortcuts(c *ui.Context) {
	c.OnShortcut(ui.Cmd, ui.KeyP, a.openSearch)
	c.OnShortcut(ui.Cmd, ui.KeyBackslash, a.toggleSidebar)
	c.OnShortcut(ui.Cmd, ui.KeyN, func() { a.newPage("", "") })
	c.OnShortcut(ui.Cmd, ui.KeyJ, func() { a.togglePanel(0) })
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
