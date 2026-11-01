package mobile

import (
	"errors"

	"leaflet/internal/icloud"
	"leaflet/locales"
)

// SetICloud turns iCloud sync on or off, through the Leaflet folder at
// root: the folder in iCloud Drive the user picked, which the app holds
// access to while the core runs. An empty root keeps the folder the core
// has. Signed in with Supabase, pages sync through the server instead,
// and it fails to turn on. Turning on fails, and stays off, when the
// folder can't be used, with why in words for the user.
func (c *Core) SetICloud(root string, on bool) error {
	var err error
	c.q.do(func() {
		if root != "" && root != c.core.Sync.ICloud.Root {
			// Between passes, so none reads half of one folder and half
			// of the other.
			c.core.Sync.Hold(func() { c.core.Sync.ICloud.Root = root })
		}
		if on && c.core.Store.Session().SignedIn {
			err = errors.New(locales.SettingsICloudSupabaseOn())
			return
		}
		if on {
			if err = c.core.Sync.EnableICloud(); err != nil {
				err = iCloudError(err)
				on = false
			}
		}
		if !on {
			c.core.Sync.SetICloud(false)
		}
		s := c.core.Store.Settings()
		if s.ICloudSync != on {
			s.ICloudSync = on
			if serr := c.core.Store.SaveSettings(s); serr != nil && err == nil {
				err = serr
			}
		}
		c.plat.WatchICloud(on)
	})
	return err
}

// iCloudError says why iCloud sync can't turn on, in words for the user,
// where the desktop has them. No access is left as it is: on iOS it means
// the app lost the folder, and asks for it again.
func iCloudError(err error) error {
	switch {
	case errors.Is(err, icloud.ErrUnavailable):
		return errors.New(locales.SettingsICloudUnavailable())
	case errors.Is(err, icloud.ErrOtherLibrary):
		return errors.New(locales.SettingsICloudOtherLibrary())
	case errors.Is(err, icloud.ErrNewerFormat):
		return errors.New(locales.SettingsICloudNewer())
	}
	return err
}
