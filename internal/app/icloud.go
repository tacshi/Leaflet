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

func (a *App) syncOn() bool { return a.session.SignedIn || a.sync.ICloudOn() }

func (a *App) supabaseTip() string {
	if a.settings.ICloudSync {
		return locales.SettingsSupabaseICloudOn()
	}
	return ""
}

func (a *App) signInButton(c *ui.Context) {
	tip := a.supabaseTip()
	tipped(c, "signin-tip", tip, func() {
		if primary(c, "signin", "", locales.CommonSignIn()).Disabled(tip != "").Clicked() {
			a.navigate(Route{Kind: "signin"})
		}
	})
}

func (a *App) offlineNow() bool {
	return a.settings.WorkOffline || !a.sync.ICloudOn() && !a.online
}

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

func (a *App) iCloudFailed(err error) string {
	if msg := a.syncMessage(err); msg != err.Error() {
		return msg
	}
	return locales.SettingsICloudTurnOnFailed(err.Error())
}

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
			a.leasePub()
			if err != nil {
				a.toast(a.iCloudFailed(err))
			}
		})
	}()
}

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
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

type icloudOwn struct {
	st  *store.Store
	dir atomic.Pointer[string]
}

func (o *icloudOwn) read() {
	dir := "devices/" + o.st.DeviceID()
	o.dir.Store(&dir)
}

func (o *icloudOwn) ignores(p string, _ bool) bool {
	own := ""
	if dir := o.dir.Load(); dir != nil {
		own = *dir
	}
	under := func(dir string) bool { return dir != "" && (p == dir || strings.HasPrefix(p, dir+"/")) }
	return under(own) || under("blobs")
}

func (o *icloudOwn) othersWrote(hints []mygo.FileEvent) bool {
	o.read()
	return slices.ContainsFunc(hints, func(h mygo.FileEvent) bool { return !o.ignores(h.Path, true) })
}

func openPrivacySettings() {
	if runtime.GOOS == "darwin" {
		_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_FilesAndFolders")
	}
}
