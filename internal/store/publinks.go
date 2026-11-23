package store

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const (
	servedLinkKey  = "public_link.served"
	publicLinksKey = "public_link.elsewhere"
)

// While it runs, the app reads the public links the user's other devices
// serve into the database every PublicLinksEvery, so what it read is
// PublicLinksFresh old at most, a read missed too. An older read, as of an
// app closed or offline for a while, says nothing.
const (
	PublicLinksEvery = 2 * time.Minute
	PublicLinksFresh = 5 * time.Minute
)

// ServedLinkLease is how long the record of the public link the app on this
// device serves lasts unless the app renews it: an app that ends without
// clearing it leaves one that runs out within it.
const ServedLinkLease = time.Hour

// KeepServedLink records that the app on this device serves page pageID's
// public link, for ServedLinkLease. The agents outside the app run in
// processes of their own, and read it here.
func (s *Store) KeepServedLink(pageID string) error {
	return s.SetValue(servedLinkKey, pageID+"|"+strconv.FormatInt(time.Now().Add(ServedLinkLease).UnixMilli(), 10))
}

// EndServedLink clears the record of page pageID's public link, as the app
// stops serving it, and leaves one of another page as it is.
func (s *Store) EndServedLink(pageID string) error {
	v, ok := s.Value(servedLinkKey)
	if page, _, _ := strings.Cut(v, "|"); !ok || page != pageID {
		return nil
	}
	return s.DeleteValue(servedLinkKey)
}

// ServesLink is whether the app on this device serves page pageID's public
// link, as its record says while the record lasts.
func (s *Store) ServesLink(pageID string) bool {
	v, ok := s.Value(servedLinkKey)
	page, until, found := strings.Cut(v, "|")
	if !ok || !found || page != pageID {
		return false
	}
	ms, err := strconv.ParseInt(until, 10, 64)
	return err == nil && time.UnixMilli(ms).After(time.Now())
}

type publicLinks struct {
	Space   string           `json:"space"`
	Checked int64            `json:"checked"`
	Pages   map[string]int64 `json:"pages,omitempty"`
}

// KeepPublicLinks records the public links the user's other devices serve,
// each page's link until the time it gives, as the app has just read them.
func (s *Store) KeepPublicLinks(links map[string]time.Time) error {
	l := publicLinks{Space: s.Session().SpaceID, Checked: time.Now().UnixMilli(), Pages: map[string]int64{}}
	for id, until := range links {
		l.Pages[id] = until.UnixMilli()
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return s.SetValue(publicLinksKey, string(raw))
}

// PublicLink is whether page pageID has a public link, which the app on
// this device or on another of the user's serves, and known whether that can
// be told. Pages that sync, signed in or through iCloud Drive, can have one
// another device serves, which only the app reads: it is not known when the
// app's last read is too old, of another account's pages, or there is none.
func (s *Store) PublicLink(pageID string) (linked, known bool) {
	if s.ServesLink(pageID) {
		return true, true
	}
	if !s.Session().SignedIn && !s.Settings().ICloudSync {
		return false, true
	}
	var l publicLinks
	if v, ok := s.Value(publicLinksKey); !ok || json.Unmarshal([]byte(v), &l) != nil {
		return false, false
	}
	now, checked := time.Now(), time.UnixMilli(l.Checked)
	if l.Space != s.Session().SpaceID || checked.After(now) || now.Sub(checked) > PublicLinksFresh {
		return false, false
	}
	until, ok := l.Pages[pageID]
	return ok && time.UnixMilli(until).After(now), true
}
