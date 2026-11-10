package core

func (c *Core) SetKeepOffline(id string, keep bool) error {
	if err := c.Store.SetKeepOffline(id, keep); err != nil || !keep {
		return err
	}
	c.Sync.KeepContent(id)
	return nil
}

func (c *Core) SetAllPagesOffline(on bool) {
	s := c.settings()
	s.AllPagesOffline = on
	c.saveSettings(s)
	if on {
		c.Sync.KeepContent()
	}
}
