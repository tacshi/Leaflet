package core

import (
	"errors"

	"leaflet/locales"
)

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
