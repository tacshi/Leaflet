package app

import (
	"context"
	_ "embed"
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"

	"leaflet/internal/store"
	"leaflet/locales"
)

//go:embed tray.png
var trayIcon []byte

type trayState struct {
	recording bool
	update    string
}

func (a *App) startTray() {
	t, err := mygo.NewTray(mygo.TrayOptions{ID: "leaflet", Icon: trayIcon, IconIsTemplate: true, ToolTip: "Leaflet"})
	if err != nil {
		log.Printf("menu bar icon: %v", err)
		return
	}
	a.tray = t
	a.refreshTray()
}

func (a *App) refreshTray() {
	if a.tray == nil {
		return
	}
	st := trayState{recording: a.rec.Active(), update: a.updateReady}
	if a.trayBuilt && st == a.trayShown {
		return
	}
	a.trayShown, a.trayBuilt = st, true
	record := &mygo.MenuItem{Label: locales.TrayNewRecording(), Click: func(*mygo.MenuItem, *mygo.Window) {
		a.update(func() { a.newRecordingPage(); a.refreshTray() })
	}}
	if st.recording {
		record = &mygo.MenuItem{Label: locales.TrayStopRecording(), Click: func(*mygo.MenuItem, *mygo.Window) {
			a.update(func() { a.rec.Stop(); a.refreshTray() })
		}}
	}
	a.tray.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: locales.AppMenuNewPage(), Click: func(*mygo.MenuItem, *mygo.Window) {
			a.update(func() {
				a.showWindow()
				a.newPage("", "")
			})
		}},
		record,
		mygo.Separator(),
		{Label: locales.TrayShowLeaflet(), Click: func(*mygo.MenuItem, *mygo.Window) { a.update(a.showWindow) }},
		mygo.Separator(),
		a.updateMenuItem(),
		{Label: locales.TrayQuit(), Role: mygo.RoleQuit},
	}))
}

func (a *App) updateMenuItem() *mygo.MenuItem {
	if a.updateReady != "" {
		return &mygo.MenuItem{Label: locales.TrayRestartToUpdate(), Click: func(*mygo.MenuItem, *mygo.Window) { a.restartToUpdate() }}
	}
	return updater.MenuItem()
}

func (a *App) showWindow() {
	if a.win == nil {
		return
	}
	a.win.Show()
	a.win.Focus()
	mygo.App.Focus()
}

func (a *App) newRecordingPage() {
	a.showWindow()
	if !a.flush() {
		return
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: locales.RecBlockTitle(time.Now())})
	if err != nil {
		a.toast(locales.AppErrorCreatePage(err.Error()))
		return
	}
	reread(&a.pages, a.st.Pages)
	a.openPage(p.ID)
	a.startRecording(p.ID, "")
}

var updateEvery = 4 * time.Hour

type newerVersion struct {
	version string
	install func(context.Context) error
}

var lookForUpdate = func(ctx context.Context) (*newerVersion, error) {
	up, err := mygo.Updater.Check(ctx)
	if err != nil || up == nil {
		return nil, err
	}
	return &newerVersion{up.Version, func(ctx context.Context) error { return up.Install(ctx, nil) }}, nil
}

func (a *App) checkForUpdate() {
	time.Sleep(5 * time.Second)
	a.update(a.keepLookingForUpdates)
}

func (a *App) keepLookingForUpdates() {
	if !a.updatable {
		return
	}
	a.findUpdate(false)
	a.later(updateEvery, a.keepLookingForUpdates)
}

func (a *App) findUpdate(asked bool) {
	if !a.updatable || a.updateBusy != "" || a.updateReady != "" {
		return
	}
	a.updateBusy = locales.SidebarCheckingForUpdates()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		up, err := lookForUpdate(ctx)
		var installErr error
		if err == nil && up != nil {
			a.update(func() { a.updateBusy = locales.SidebarDownloadingUpdate(up.version) })
			if installErr = up.install(ctx); installErr != nil {
				log.Printf("update %s: %v", up.version, installErr)
			}
		}
		if err != nil {
			log.Printf("update: %v", err)
		}
		a.update(func() {
			a.updateBusy = ""
			switch {
			case err != nil:
				if asked {
					a.toast(locales.SidebarCheckForUpdatesFailed())
				}
			case installErr != nil:
				if asked || a.updateFailed != up.version {
					a.toast(locales.SidebarInstallUpdateFailed(up.version))
				}
				a.updateFailed = up.version
			case up == nil:
				if asked {
					a.toast(locales.SidebarUpToDate(mygo.App.Version()))
				}
			default:
				a.updateReady = up.version
				a.refreshTray()
			}
		})
	}()
}

func (a *App) restartToUpdate() {
	a.flush()
	mygo.App.Relaunch()
}
