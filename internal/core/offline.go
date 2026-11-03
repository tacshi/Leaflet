package core

// Keeping pages on this device, as the desktop and the iOS app do from a
// page's menu and from Settings. A page kept from now on comes up to date
// soon: what changed elsewhere while it was not kept, sync did not bring.

// SetKeepOffline keeps a page's content on this device, or lets it go
// until the page is opened.
func (c *Core) SetKeepOffline(id string, keep bool) error {
	if err := c.Store.SetKeepOffline(id, keep); err != nil || !keep {
		return err
	}
	c.Sync.KeepContent(id)
	return nil
}

// SetAllPagesOffline keeps every page's content on this device, or only
// the pages kept and opened, in the settings as the app has them.
func (c *Core) SetAllPagesOffline(on bool) {
	s := c.settings()
	s.AllPagesOffline = on
	c.saveSettings(s)
	if on {
		c.Sync.KeepContent()
	}
}
