package app

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"leaflet/internal/icloud"
	"leaflet/locales"
)

// syncOn reports whether pages sync: signed in, or through iCloud Drive.
func (a *App) syncOn() bool { return a.session.SignedIn || a.sync.ICloudOn() }

// offlineNow reports whether syncing waits for the user or a connection.
// iCloud Drive takes what Leaflet writes without one, and uploads it once
// it has one.
func (a *App) offlineNow() bool {
	return a.settings.WorkOffline || !a.sync.ICloudOn() && !a.online
}

// syncMessage says what went wrong syncing, in words for the user.
func (a *App) syncMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, icloud.ErrUnavailable):
		return locales.SettingsICloudUnavailable()
	case errors.Is(err, icloud.ErrNoAccess):
		return locales.SettingsICloudNoAccess()
	case errors.Is(err, icloud.ErrOtherLibrary):
		return locales.SettingsICloudOtherLibrary()
	case errors.Is(err, icloud.ErrNewerFormat):
		return locales.SettingsICloudNewer()
	}
	return err.Error()
}

// startICloud sets iCloud sync up as the settings have it. Signed in, the
// pages sync through the server instead, and the setting is off, as
// signing in turns it off. With iCloud Drive gone, the passes say so.
func (a *App) startICloud() {
	a.sync.ICloud = icloud.Folder{Root: icloud.Root(), Temp: filepath.Join(a.st.Dir, "tmp")}
	if a.session.SignedIn && a.settings.ICloudSync {
		a.settings.ICloudSync = false
		a.saveSettings()
	}
	on := a.settings.ICloudSync
	a.sync.SetICloud(on)
	a.watchICloud(on)
}

// setICloud turns iCloud sync on or off from Settings, one turn at a
// time: turning it off while it still turns on could leave it on.
func (a *App) setICloud(on bool) {
	if a.icloudBusy {
		return
	}
	a.icloudBusy = true
	a.settings.ICloudSync = on
	a.saveSettings()
	a.syncErr, a.syncError = nil, ""
	go func() {
		var err error
		if on {
			err = a.sync.EnableICloud()
		} else {
			a.sync.SetICloud(false)
		}
		a.update(func() {
			a.icloudBusy = false
			if a.settings.ICloudSync != on {
				// Signing in turned it off meanwhile: it stays off.
				a.sync.SetICloud(a.settings.ICloudSync)
				a.watchICloud(a.settings.ICloudSync)
				return
			}
			if err != nil {
				a.settings.ICloudSync = false
				a.saveSettings()
				a.toast(a.syncMessage(err))
				return
			}
			a.watchICloud(on)
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
	root, own := a.sync.ICloud.Root, "devices/"+a.st.DeviceID()
	go func() {
		for ctx.Err() == nil {
			w, err := mygo.WatchFiles(ctx, root, mygo.FileWatchOptions{
				Recursive: true, HintsOnly: true, Debounce: 2 * time.Second,
				// This device's own writes, and copies of files, which the
				// batches that name them follow.
				Ignore: func(p string, _ bool) bool {
					return p == own || strings.HasPrefix(p, own+"/") || p == "blobs" || strings.HasPrefix(p, "blobs/")
				},
			})
			if err == nil {
				for {
					if _, err := w.Next(ctx); err != nil {
						break
					}
					a.sync.Kick()
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

// openPrivacySettings opens the privacy settings where macOS lets Leaflet
// into iCloud Drive.
func openPrivacySettings() {
	if runtime.GOOS == "darwin" {
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_FilesAndFolders")
	}
}
