package sync

import (
	"context"
	"log"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/icloud"
)

// A public link is served by the device that opened it, and only it knows.
// Through the server it says so on the page (agents' ServePublicLink); a
// device that syncs through iCloud Drive says so in its device.json
// instead (docs/protocol.md §8), for runs on the other devices to read
// none of the user's other pages into the page. Each lease this device
// holds is in key_values, under icloud.link.<lease>, as
// "<page>|<until, ms>", and goes into device.json as the passes write it.

const linkPrefix = "icloud.link."

// iCloudLinkRenew is how often this device says again that it serves a
// public link (ICloudLease), and iCloudLinkLease how long it says each
// time that it does for, by its own clock: one missed renewal leaves the
// link counted, and the link of a device that quit without saying stops
// counting by then.
const (
	iCloudLinkRenew = 15 * time.Minute
	iCloudLinkLease = time.Hour
)

// deviceFile is device.json as this device writes it: icloud.Device, and
// when each public link it serves stops counting, by page, as it renews
// them. A Leaflet from before reads the fields it knows and leaves the
// rest.
type deviceFile struct {
	icloud.Device
	PublicLinks map[string]time.Time `json:"public_links,omitempty"`
}

// ICloudLease says in this device's device.json that it serves a public
// link of page pageID, and says it again before each lease runs out
// (iCloudLinkRenew), until stop: the passes write it there while iCloud
// sync is on. Stopping ends this lease alone: another of the page, as of
// the link started again for other addresses, stays. Any goroutine may
// call it, and stop.
func (s *Service) ICloudLease(pageID string) (stop func()) {
	key := linkPrefix + doc.NewRowID()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer guard.Recover()
		defer close(done)
		for {
			s.keepLink(key, pageID+"|"+strconv.FormatInt(cloudNow().Add(iCloudLinkLease).UnixMilli(), 10))
			select {
			case <-ctx.Done():
				return
			case <-time.After(iCloudLinkRenew):
			}
		}
	}()
	var once gosync.Once
	return func() {
		once.Do(func() {
			cancel()
			go func() {
				defer guard.Recover()
				<-done
				s.keepLink(key, "")
			}()
		})
	}
}

// keepLink keeps lease key as v, or forgets it for "", between passes, and
// has the next pass, asked for now, write device.json with it. With iCloud
// sync off, as just turned off or as signing in turns it off, no pass
// writes device.json: it is written at once, so a lease that ends, as the
// link stops or its pages go another way, does not count for the other
// devices till it runs out.
func (s *Service) keepLink(key, v string) {
	s.Hold(func() {
		err := s.st.DeleteValue(key)
		if v != "" {
			err = s.st.SetValue(key, v)
		}
		switch {
		case err != nil:
		case s.cloudOn.Load():
			err = s.setInt("icloud.device_at", 0)
		default:
			err = s.writeDeviceOff()
		}
		if err != nil {
			log.Printf("icloud: public link: %v", err)
		}
	})
	s.Kick()
}

// writeDeviceOff writes device.json at once, outside a pass, iCloud sync
// being off: for a device that joined the folder, which is still there.
func (s *Service) writeDeviceOff() error {
	if joined, _ := s.st.Value("icloud.space"); joined == "" || s.folder().Root == "" {
		return nil
	}
	if err := s.folder().Check(false); err != nil {
		return err
	}
	me := s.st.DeviceID()
	if !icloud.ValidID(me) {
		return nil
	}
	return s.writeDevice(me, true)
}

// ownLinks are the public links this device serves, as device.json says
// them at now: when each page's lease ends, the latest of its leases.
// Leases ended, as of a run of the app that quit without stopping them,
// are forgotten.
func (s *Service) ownLinks(now time.Time) map[string]time.Time {
	var out map[string]time.Time
	for lease, v := range s.st.ValuesWithPrefix(linkPrefix) {
		page, ms, _ := strings.Cut(v, "|")
		n, err := strconv.ParseInt(ms, 10, 64)
		until := time.UnixMilli(n).UTC()
		if err != nil || page == "" || !until.After(now) {
			_ = s.st.DeleteValue(linkPrefix + lease)
			continue
		}
		if out == nil {
			out = map[string]time.Time{}
		}
		if until.After(out[page]) {
			out[page] = until
		}
	}
	return out
}

// ICloudLinksElsewhere are the pages with a public link another device
// says in its device.json that it serves, while iCloud sync is on, with
// when each lease ends, by that device's clock. A device.json iCloud has
// not downloaded yet, which it is asked to, says none until it has.
func (s *Service) ICloudLinksElsewhere() map[string]time.Time {
	f := s.folder()
	if !s.cloudOn.Load() || f.Root == "" {
		return nil
	}
	devices, err := f.Devices()
	if err != nil {
		return nil
	}
	me := s.st.DeviceID()
	out := map[string]time.Time{}
	for _, d := range devices {
		rel, err := icloud.DevicePath(d)
		if d == me || err != nil {
			continue
		}
		var file deviceFile
		if err := f.ReadJSON(rel, &file); err != nil {
			continue
		}
		for page, until := range file.PublicLinks {
			if until.After(out[page]) {
				out[page] = until
			}
		}
	}
	return out
}
