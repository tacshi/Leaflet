package mobile

import (
	"errors"
	gosync "sync"

	"leaflet/internal/core"
)

// SetICloud turns iCloud sync on or off, through the Leaflet folder at
// root: the folder in iCloud Drive the user picked, which the app holds
// access to while the core runs. An empty root keeps the folder the core
// has. Signed in with Supabase, pages sync through the server instead,
// and it fails to turn on. Turning on fails, and stays off, when the
// folder can't be used, with why in words for the user.
//
// Moving to another folder waits for the pass under way, which can take
// minutes, and turning on reads iCloud Drive: both happen here, off the
// core's queue, so the library and the open page stay usable meanwhile;
// the setting and the watch go back on the queue after.
func (c *Core) SetICloud(root string, on bool) (err error) {
	defer catch(&err)
	setICloudMu.Lock()
	defer setICloudMu.Unlock()
	sync := c.core.Sync
	if root != "" && root != sync.ICloudFolder().Root {
		// Between passes, so none reads half of one folder and half of
		// the other.
		sync.Hold(func() { sync.SetICloudRoot(root) })
	}
	if err = c.core.SwitchICloud(on); err != nil {
		err = iCloudError(err)
	}
	c.q.do(func() { c.core.KeepICloud(on && err == nil) })
	return err
}

// setICloudMu has one SetICloud at a time, in the order they came, as
// the queue had them when they ran on it.
var setICloudMu gosync.Mutex

// iCloudError says why iCloud sync can't turn on, in words for the user,
// where the desktop has them. No access is left as it is: on iOS it means
// the app lost the folder, and asks for it again.
func iCloudError(err error) error {
	if msg, ok := core.ICloudMessage(err); ok {
		return errors.New(msg)
	}
	return err
}
