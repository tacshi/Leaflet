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

var errKeptChanging = errors.New("the page kept changing here while it synced")

var beforeStore = func(pageID string) {}

var errUnreadable = errors.New("the page's content on this device could not be read, so it was not synced")

func unreadable(err error) bool {
	return errors.Is(err, errUnreadable) || errors.Is(err, errServerUnreadable) || errors.Is(err, errPeerUnreadable)
}

func (s *Service) keepsContent(p store.Page, allOffline bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open[p.ID] || allOffline || p.KeepOffline
}

func (s *Service) KeepContent(pageIDs ...string) {
	guard.Go(func() {
		s.running.Lock()
		err := s.waitForContent(pageIDs)
		s.running.Unlock()
		if err != nil {
			log.Printf("keep pages: %v", err)
		}
		s.Kick()
	})
}

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

func (s *Service) waiting() (map[string]bool, error) { return s.ids("sync.content_waiting") }

func (s *Service) setWaiting(ids map[string]bool) error { return s.setIDs("sync.content_waiting", ids) }

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

func (s *Service) setIDs(key string, ids map[string]bool) error {
	v := strings.Join(slices.Sorted(maps.Keys(ids)), ",")
	if kept, ok, err := s.st.Lookup(key); err == nil && (ok && kept == v || !ok && v == "") {
		return nil
	}
	return s.st.SetValue(key, v)
}
