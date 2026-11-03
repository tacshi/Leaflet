package core

import (
	"errors"

	"leaflet/locales"
)

// Turning iCloud sync on or off from Settings, as the desktop and the iOS
// app do: SwitchICloud off the app's main thread, as turning on reads
// iCloud Drive, then KeepICloud on it.

// SwitchICloud turns iCloud sync on or off. Pages sync one way at a time:
// signed in with Supabase, through the server, so it fails to turn on,
// also once signed in meanwhile. Turning on fails, and leaves it off,
// when the Leaflet folder can't be used, with the error as it came, for
// the app to say in words of its own. KeepICloud then keeps what it did.
func (c *Core) SwitchICloud(on bool) error {
	var err error
	switch {
	case !on:
	case c.Store.Session().SignedIn:
		err = errors.New(locales.SettingsICloudSupabaseOn())
	default:
		err = c.Sync.EnableICloud()
		if err == nil && c.Store.Session().SignedIn {
			err = errors.New(locales.SettingsICloudSupabaseOn())
		}
	}
	if !on || err != nil {
		c.Sync.SetICloud(false)
	}
	return err
}

// KeepICloud keeps iCloud sync on or off in the settings, as SwitchICloud
// left it, and watches the Leaflet folder while it is on, on the thread
// the settings are kept on. Signed in since, which turned it off, it
// stays off.
func (c *Core) KeepICloud(on bool) {
	if on && c.Store.Session().SignedIn {
		on = false
		c.Sync.SetICloud(false)
	}
	if s := c.settings(); s.ICloudSync != on {
		s.ICloudSync = on
		c.saveSettings(s)
	}
	c.watch(on)
}
