package app

import (
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/theme"
	"leaflet/locales"
)

const (
	pageMaxWidth = 720
	pagePadX     = 56
)

func (a *App) view(c *ui.Context) {
	defer func() {
		if a.images != nil {
			now := time.Now()
			a.images.releasePlayers(now)
			a.images.releasePictures(now)
		}
	}()
	theme.Apply(c)
	pal := theme.Of(c)
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}
	a.refreshTray()
	a.follow()
	page := func() {
		main := func() {
			main := ui.Column(c.Key("main")).Fill().Background(pal.Surface)
			if a.router == nil {
				main.Children(func() { a.main(c) })
			} else {
				main.Children(func() {
					a.router.View(c, func(r *ui.Route) {
						r.Title(a.routeTitle())
						a.main(c)
					}).Fill()
				})
			}
		}
		if !a.panelShown() {
			main()
			return
		}
		room := a.pageRoom(c)
		minW := a.panelMinW(c)
		a.panelW = min(max(a.panelW, minW), panelMax)
		mainW := room - a.panelWidth(c) - 1
		split := ui.Split(c, &mainW, main, func() {
			ui.Column(c.Key("panel")).Fill().Background(pal.Panel).Children(func() { a.panel(c) })
		}).Fill()
		if split.Changed() {
			a.panelW = min(max(room-mainW-1, minW), panelMax)
			a.saveWidthsSoon()
		}
	}
	if a.preview != nil {
		a.previewView(c)
	} else if !a.settings.SidebarOpen {
		page()
	} else {
		sw := a.sidebarWidth(c)
		split := ui.Split(c, &sw, func() {
			bar := ui.Column(c.Key("sidebar")).Fill()
			if opaqueSidebar(c, pal) {
				bar.Background(pal.Sidebar)
			}
			bar.Children(func() { a.sidebar(c) })
		}, page).Fill()
		if split.Changed() {
			a.sidebarW = min(max(sw, sidebarMin), sidebarMax)
			a.saveWidthsSoon()
		}
	}
	if v := a.recView(); v != nil {
		ui.WebView(c.Key("rec-capture"), v).Absolute().Left(0).Top(0).Size(1, 1).Opacity(0.01).PassThrough()
	}
	a.dialogs.view(c, a)
	a.replaceNotesAlert(c)
	if a.settingsOpen {
		a.settingsDialog(c)
	}
	a.signInAlert(c)
	a.signOutAlert(c)
	a.keepName()
	a.toastView(c)
}

const headerHeight = 46

const (
	sidebarMin, sidebarMax = 200, 420
	panelMin, panelMax     = 280, 640
	pageMin                = 320
	windowMinW             = sidebarMin + panelMin + pageMin + 2
)

func (a *App) sidebarWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	keep := float32(pageMin)
	if a.panelShown() {
		keep += a.panelMinW(c) + 1
	}
	return max(min(a.sidebarW, w-keep-1), sidebarMin)
}

func (a *App) panelWidth(c *ui.Context) float32 {
	return max(min(a.panelW, a.pageRoom(c)-pageMin-1), a.panelMinW(c))
}

const panelTopPadL, panelTopPadR = 12, 10

func (a *App) panelMinW(c *ui.Context) float32 {
	top := panelTopPadL + a.panelTabsW + c.TitleBar().Right + panelTopPadR
	return min(max(panelMin, top), panelMax)
}

func (a *App) saveWidthsSoon() {
	if a.widthsTimer != nil {
		a.widthsTimer.Stop()
	}
	a.widthsTimer = a.later(400*time.Millisecond, func() {
		a.settings.SidebarWidth = a.sidebarW
		a.settings.PanelWidth = a.panelW
		a.saveSettings()
	})
}

func (a *App) panelShown() bool {
	return a.panelOpen && a.route.Kind != "signin"
}

func (a *App) pageRoom(c *ui.Context) float32 {
	w, _ := c.Size()
	if a.settings.SidebarOpen {
		w -= a.sidebarWidth(c) + 1
	}
	return w
}

func (a *App) contentWidth(c *ui.Context) float32 {
	w := a.pageRoom(c)
	if a.panelOpen {
		w -= a.panelWidth(c) + 1
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
	case "signin":
		a.signInView(c)
		return
	}
	a.homeView(c)
}

func (a *App) titleBar(c *ui.Context, fn func()) {
	pal := theme.Of(c)
	bar := c.TitleBar()
	left, right := float32(12), float32(12)
	if !a.settings.SidebarOpen {
		left = bar.Left + 12
	}
	if !a.panelShown() {
		right = bar.Right + 12
	}
	ui.Row(c.Key("titlebar")).Height(max(bar.Height, headerHeight)).Padding(0, right, 0, left).Gap(4).AlignItems(ui.Center).
		DragWindow().BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
		tools(c, func() {
			if !a.settings.SidebarOpen {
				if iconButton(c, "show-sidebar", "panel-left", locales.ShellShowSidebar()).Clicked() {
					a.toggleSidebar()
				}
			}
			if a.router != nil {
				back, forward := locales.ShellHistoryBack(), locales.ShellHistoryForward()
				ui.BackButton(c, a.router).Label(back).Tooltip(back)
				ui.ForwardButton(c, a.router).Label(forward).Tooltip(forward)
			}
		})
		fn()
	})
}

func (a *App) homeView(c *ui.Context) {
	pal := theme.Of(c)
	a.titleBar(c, func() {
		ui.Spacer(c)
		tools(c, func() {
			if !a.rec.Active() && iconButton(c, "record", "record", locales.TrayNewRecording()).Clicked() {
				a.newRecordingPage()
			}
			a.panelToggle(c)
		})
	})
	ui.Column(c).Grow(1).Center().Gap(14).Children(func() {
		ui.Icon(c, theme.Icon("file-text")).FontSize(36).TextColor(pal.ChartMuted)
		ui.Text(c, locales.ShellNoPageOpen()).FontSize(18).FontWeight(650).TextColor(pal.Text)
		ui.Text(c, locales.ShellNoPageHint()).TextColor(pal.TextMuted).Balance()
		ui.Row(c).Gap(8).Children(func() {
			if primary(c, "new", "plus", locales.ShellNewPage()).Clicked() {
				a.newPage("", "")
			}
			if button(c, "ask-agent", "sparkles", locales.ShellAskAgent()).Clicked() {
				a.openWorkspace()
			}
		})
	})
}

func (a *App) toastView(c *ui.Context) {
	for _, t := range a.toasts {
		if t.action != "" {
			c.AddToast(ui.Toast{Title: t.text, Timeout: actionToastTime, Action: t.action, OnAction: t.fn})
		} else {
			c.Toast(t.text)
		}
	}
	a.toasts = nil
}
