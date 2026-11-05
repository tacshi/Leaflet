package app

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/core"
	"leaflet/internal/icloud"
	"leaflet/internal/store"
	"leaflet/locales"
)

// syncOn reports whether pages sync: signed in, or through iCloud Drive.
func (a *App) syncOn() bool { return a.session.SignedIn || a.sync.ICloudOn() }

// supabaseTip says why signing in with Supabase can't be done: pages sync
// one way at a time, and iCloud sync is on. It is "" when it can.
func (a *App) supabaseTip() string {
	if a.settings.ICloudSync {
		return locales.SettingsSupabaseICloudOn()
	}
	return ""
}

// signInButton opens the sign-in page, disabled while iCloud sync is on.
func (a *App) signInButton(c *ui.Context) {
	tip := a.supabaseTip()
	tipped(c, "signin-tip", tip, func() {
		if primary(c, "signin", "", locales.CommonSignIn()).Disabled(tip != "").Clicked() {
			a.navigate(Route{Kind: "signin"})
		}
	})
}

// offlineNow reports whether syncing waits for the user or a connection.
// iCloud Drive takes what Leaflet writes without one, and uploads it once
// it has one.
func (a *App) offlineNow() bool {
	return a.settings.WorkOffline || !a.sync.ICloudOn() && !a.online
}

// syncMessage says what went wrong syncing, in words for the user. A
// pass hands on no error of the network, which it waits out; a refusal
// says what the server said, after words of its own. An error with no
// words here is as it came, which the status shows after words of its own.
func (a *App) syncMessage(err error) string {
	if msg, ok := core.SyncMessage(err); ok {
		return msg
	}
	if msg, ok := core.ICloudMessage(err); ok {
		return msg
	}
	switch {
	case err == nil:
		return ""
	case errors.Is(err, icloud.ErrNoAccess):
		return locales.SettingsICloudNoAccess()
	}
	return err.Error()
}

// iCloudFailed says why iCloud sync didn't turn on, in a toast of its own.
func (a *App) iCloudFailed(err error) string {
	if msg := a.syncMessage(err); msg != err.Error() {
		return msg
	}
	return locales.SettingsICloudTurnOnFailed(err.Error())
}

// setICloud turns iCloud sync on or off from Settings, one turn at a
// time: turning it off while it still turns on could leave it on. The
// switch shows the turn asked for while it goes.
func (a *App) setICloud(on bool) {
	if a.icloudBusy {
		return
	}
	a.icloudBusy = true
	a.settings.ICloudSync = on
	a.saveSettings()
	a.syncErr, a.syncError = nil, ""
	c := a.core()
	go func() {
		err := c.SwitchICloud(on)
		a.update(func() {
			a.icloudBusy = false
			c.KeepICloud(on && err == nil)
			// A public link served now says so in iCloud Drive, or there
			// no more.
			a.leasePub()
			if err != nil {
				a.toast(a.iCloudFailed(err))
			}
		})
	}()
}

// watchICloud has a sync run soon after another device writes to the
// Leaflet folder, while iCloud sync is on; the 30-second passes catch
// what it misses.
func (a *App) watchICloud(on bool) {
	if a.icloudStop != nil {
		a.icloudStop()
		a.icloudStop = nil
	}
	if !on {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.icloudStop = cancel
	root, own := a.sync.ICloudFolder().Root, &icloudOwn{st: a.st}
	go func() {
		for ctx.Err() == nil {
			own.read()
			w, err := mygo.WatchFiles(ctx, root, mygo.FileWatchOptions{
				Recursive: true, HintsOnly: true, Debounce: 2 * time.Second,
				Ignore: own.ignores,
			})
			if err == nil {
				for {
					hints, err := w.Next(ctx)
					if err != nil {
						break
					}
					if own.othersWrote(hints) {
						a.sync.Kick()
					}
				}
				_ = w.Close()
			}
			// The folder is not there yet, as before the first pass makes
			// it, or went: look again soon.
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

// icloudOwn is what the watcher of the Leaflet folder ignores: this
// device's own writes, under its ID, and copies of files, which the
// batches that name them follow. A sync pass can give the device a new
// ID, as when it finds another device writing as it (a backup restored
// to a new Mac): its folder is read again after each change, off the
// watch's own goroutine, which must be quick.
type icloudOwn struct {
	st  *store.Store
	dir atomic.Pointer[string]
}

// read reads the device's folder again.
func (o *icloudOwn) read() {
	dir := "devices/" + o.st.DeviceID()
	o.dir.Store(&dir)
}

// ignores reports whether a change at p, a path in the Leaflet folder, is
// one the watcher ignores.
func (o *icloudOwn) ignores(p string, _ bool) bool {
	own := ""
	if dir := o.dir.Load(); dir != nil {
		own = *dir
	}
	under := func(dir string) bool { return dir != "" && (p == dir || strings.HasPrefix(p, dir+"/")) }
	return under(own) || under("blobs")
}

// othersWrote reports whether hints are of a change another device made:
// a change under the folder of this device's ID since then is its own.
func (o *icloudOwn) othersWrote(hints []mygo.FileEvent) bool {
	o.read()
	return slices.ContainsFunc(hints, func(h mygo.FileEvent) bool { return !o.ignores(h.Path, true) })
}

// openPrivacySettings opens the privacy settings where macOS lets Leaflet
// into iCloud Drive.
func openPrivacySettings() {
	if runtime.GOOS == "darwin" {
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_FilesAndFolders")
	}
}
