package sync

import (
	"errors"
	"log"
	"maps"
	"slices"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/store"
)

// storeMerge stores a page's content merged with what came from elsewhere.
// merge makes it from the content stored here, kept false when there is
// none, or returns nil to store nothing; put stores it only over the
// content merge was given, and returns store.ErrContentChanged otherwise.
// The user or an agent may save while this merges: merge runs again on
// what they saved, up to three times. It returns the content stored before
// ("" for none) and the merge it stored, and tells the app when they
// differ.
func (s *Service) storeMerge(pageID string, merge func(row store.ContentRow, kept bool) (*doc.Content, error),
	put func(over string, c *doc.Content) error) (before string, stored *doc.Content, err error) {
	for range 3 {
		row, err := s.st.ContentRow(pageID)
		if err != nil && !store.IsNotFound(err) {
			return "", nil, err
		}
		next, err := merge(row, err == nil)
		if err != nil || next == nil {
			return row.Content, nil, err
		}
		beforeStore(pageID)
		err = put(row.Content, next)
		if errors.Is(err, store.ErrContentChanged) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		if next.Encode() != row.Content && s.OnPageContent != nil {
			s.OnPageContent(pageID)
		}
		return row.Content, next, nil
	}
	return "", nil, errKeptChanging
}

// errKeptChanging is a merge never stored, as the page's content here
// changed each time before it could be.
var errKeptChanging = errors.New("the page kept changing here while it synced")

// beforeStore runs between merging a page's content and storing the merge;
// tests save there, standing in for the user typing.
var beforeStore = func(pageID string) {}

// errUnreadable is a page whose content here cannot be read. It is neither
// sent nor merged, as an empty page would take its place: the copies
// elsewhere stay as they are, and the change stays queued.
var errUnreadable = errors.New("the page's content on this device could not be read, so it was not synced")

// unreadable reports whether a page's content was not synced as a version
// of it cannot be read, this device's or another's: the page keeps its
// content here, and its change stays queued, until they can be.
func unreadable(err error) bool {
	return errors.Is(err, errUnreadable) || errors.Is(err, errServerUnreadable) || errors.Is(err, errPeerUnreadable)
}

// keepsContent reports whether this device keeps a page's content up to
// date as it changes elsewhere: the page is open, or kept offline, as all
// pages are with allOffline. Others come up to date as they are opened.
func (s *Service) keepsContent(p store.Page, allOffline bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open[p.ID] || allOffline || p.KeepOffline
}

// KeepContent has the content of pages this device keeps from now on
// brought up to date soon, as a pass runs: pageIDs, or every page when it
// names none, as once all pages are kept. What changed elsewhere while
// they were not kept, the passes did not bring.
func (s *Service) KeepContent(pageIDs ...string) {
	guard.Go(func() {
		// The pass running keeps the pages that wait as it read them: these
		// join them once it is done.
		s.running.Lock()
		err := s.waitForContent(pageIDs)
		s.running.Unlock()
		if err != nil {
			log.Printf("keep pages: %v", err)
		}
		s.Kick()
	})
}

// waitForContent has the content of pages wait to be brought up to date
// (waiting), every page for none. Through iCloud Drive, a page a batch
// named waits until this device keeps it, so they wait already; the
// server's pull passes over a page not kept, which waits from now.
func (s *Service) waitForContent(pageIDs []string) error {
	if s.cloudOn.Load() {
		return nil
	}
	if len(pageIDs) == 0 {
		pages, err := s.st.AllPages()
		if err != nil {
			return err
		}
		for _, p := range pages {
			// One kept already came as it changed.
			if p.DeletedAt.IsZero() && !p.KeepOffline {
				pageIDs = append(pageIDs, p.ID)
			}
		}
	}
	waiting, err := s.waiting()
	if err != nil {
		return err
	}
	for _, id := range pageIDs {
		waiting[id] = true
	}
	return s.setWaiting(waiting)
}

// waiting are the pages whose content changed elsewhere and waits to be
// brought up to date here: their row has not come yet, or, through iCloud
// Drive, the version a batch named.
func (s *Service) waiting() (map[string]bool, error) { return s.ids("sync.content_waiting") }

func (s *Service) setWaiting(ids map[string]bool) error { return s.setIDs("sync.content_waiting", ids) }

// ids reads a set of IDs kept under key. One that cannot be read is an
// error, not an empty set, which the caller would write over it.
func (s *Service) ids(key string) (map[string]bool, error) {
	v, _, err := s.st.Lookup(key)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if v != "" {
		for id := range strings.SplitSeq(v, ",") {
			out[id] = true
		}
	}
	return out, nil
}

// setIDs keeps a set of IDs under key. A set as it is kept already is not
// written again: each write is a commit to the disk, and tells the app
// that key_values changed, which a pass would otherwise do every time.
func (s *Service) setIDs(key string, ids map[string]bool) error {
	v := strings.Join(slices.Sorted(maps.Keys(ids)), ",")
	if kept, ok, err := s.st.Lookup(key); err == nil && (ok && kept == v || !ok && v == "") {
		return nil
	}
	return s.st.SetValue(key, v)
}
