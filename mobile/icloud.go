package mobile

import (
	"errors"
	gosync "sync"

	"leaflet/internal/core"
)

func (c *Core) SetICloud(root string, on bool) (err error) {
	defer catch(&err)
	setICloudMu.Lock()
	defer setICloudMu.Unlock()
	sync := c.core.Sync
	if root != "" && root != sync.ICloudFolder().Root {
		sync.Hold(func() { sync.SetICloudRoot(root) })
	}
	if err = c.core.SwitchICloud(on); err != nil {
		err = iCloudError(err)
	}
	c.q.do(func() { c.core.KeepICloud(on && err == nil) })
	return err
}

var setICloudMu gosync.Mutex

func iCloudError(err error) error {
	if msg, ok := core.ICloudMessage(err); ok {
		return errors.New(msg)
	}
	return err
}
