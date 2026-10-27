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

// trayIcon is the leaf of the app's icon as a template image, which macOS
// tints for light and dark menu bars. docs/icon/tray.svg draws it.
//
//go:embed tray.png
var trayIcon []byte

// trayState is what the menu bar menu shows that can change: the menu is
// made again when it does.
type trayState struct {
	recording bool
	update    string // the version installed and waiting for a restart
}

// startTray puts Leaflet's icon in the menu bar. With it the app keeps
// running when its window closes, which only hides it.
func (a *App) startTray() {
	t, err := mygo.NewTray(mygo.TrayOptions{ID: "leaflet", Icon: trayIcon, IconIsTemplate: true, ToolTip: "Leaflet"})
	if err != nil {
		log.Printf("menu bar icon: %v", err)
		return
	}
	a.tray = t
	a.refreshTray()
}

// refreshTray makes the menu bar menu again when what it shows changed.
// The view calls it every frame; it costs a comparison.
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

// updateMenuItem is "Check for Updates…", or "Restart to Update" once an
// update is installed and waits for the app to start again.
func (a *App) updateMenuItem() *mygo.MenuItem {
	if a.updateReady != "" {
		return &mygo.MenuItem{Label: locales.TrayRestartToUpdate(), Click: func(*mygo.MenuItem, *mygo.Window) { a.restartToUpdate() }}
	}
	return updater.MenuItem()
}

// showWindow brings the window back, from the menu bar or the Dock.
func (a *App) showWindow() {
	if a.win == nil {
		return
	}
	a.win.Show()
	a.win.Focus()
	mygo.App.Focus()
}

// newRecordingPage starts a recording in a page of its own, named after
// when it began.
func (a *App) newRecordingPage() {
	a.showWindow()
	p, err := a.st.CreatePage(store.NewPageOptions{Title: locales.RecBlockTitle(time.Now())})
	if err != nil {
		a.toast(locales.AppErrorCreatePage(err.Error()))
		return
	}
	a.pages, _ = a.st.Pages()
	a.openPage(p.ID)
	a.startRecording(p.ID, "")
}

// checkForUpdate looks for a newer version as the app starts and installs
// it in the background; the update runs once the app starts again, from
// the icon beside Settings or the menu bar. "Check for Updates…" is the
// updater plugin's, with its window.
func (a *App) checkForUpdate() {
	if !mygo.Updater.Enabled() {
		return
	}
	time.Sleep(5 * time.Second) // let the app open first
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	up, err := mygo.Updater.Check(ctx)
	if err != nil || up == nil {
		return // offline, or up to date
	}
	if err := up.Install(ctx, nil); err != nil {
		log.Printf("update %s: %v", up.Version, err)
		return
	}
	a.update(func() {
		a.updateReady = up.Version
		a.refreshTray()
	})
}

// restartToUpdate starts the installed update, saving the open page first.
func (a *App) restartToUpdate() {
	a.flush()
	mygo.App.Relaunch()
}
