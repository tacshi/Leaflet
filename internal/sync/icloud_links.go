package sync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/icloud"
)

const linkPrefix = "icloud.link."

const (
	iCloudLinkRenew = 15 * time.Minute
	iCloudLinkLease = time.Hour
)

type deviceFile struct {
	icloud.Device
	PublicLinks map[string]time.Time `json:"public_links,omitempty"`
}

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

func (s *Service) ICloudLinksElsewhere() (map[string]time.Time, error) {
	f := s.folder()
	if !s.cloudOn.Load() || f.Root == "" {
		return nil, nil
	}
	devices, err := f.Devices()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLinksUnknown, err)
	}
	me := s.st.DeviceID()
	out := map[string]time.Time{}
	var unread error
	for _, d := range devices {
		rel, err := icloud.DevicePath(d)
		if d == me || err != nil {
			continue
		}
		var file deviceFile
		err = f.ReadJSON(rel, &file)
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
		case !s.linksEnded(rel):
			unread = errors.Join(unread, fmt.Errorf("device %s: %w", d, err))
		}
		for page, until := range file.PublicLinks {
			if until.After(out[page]) {
				out[page] = until
			}
		}
	}
	if unread != nil {
		return out, fmt.Errorf("%w: %w", ErrLinksUnknown, unread)
	}
	return out, nil
}

var ErrLinksUnknown = errors.New("icloud: the public links another device serves could not be read")

const iCloudLinkDrift = 15 * time.Minute

func (s *Service) linksEnded(rel string) bool {
	stamp, ok := s.folder().Stamp(rel)
	return ok && cloudNow().Sub(stamp.ModTime) > iCloudLinkLease+2*iCloudLinkDrift
}
