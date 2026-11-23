package sync

import (
	"errors"
	"maps"
	"reflect"

	"leaflet/internal/icloud"
	"leaflet/internal/store"
)

// quietFor is how long a device has not been active before the others take what was on pages deleted for good out of
// its log: it compacts its log no more, which would keep that in iCloud Drive for good. One that comes back finds its
// log as it left it, but for that.
var quietFor = store.BinKept

func (s *Service) compact(me string) error {
	seqs, _, err := s.folder().Batches(me)
	if err != nil || len(seqs) <= compactAfter && !s.compactSoon.Load() {
		return err
	}
	isPurged := s.purgedCheck()
	last := s.intValue("icloud.seq")
	snapshot := icloud.Batch{Format: icloud.Format, Device: me, Seq: last + 1, WrittenAt: cloudNow().UTC(), Snapshot: true}
	var places [][]icloud.Entry
	rows := map[[2]string]int{}
	pages := map[string]int{}
	for _, seq := range seqs {
		if seq > last {
			break
		}
		rel, err := icloud.BatchPath(me, seq)
		if err != nil {
			return err
		}
		var b icloud.Batch
		if err := s.folder().ReadJSON(rel, &b); err != nil {
			if errors.Is(err, icloud.ErrDamaged) && s.passDamaged(me, seq, true, err) {
				continue
			}
			if errors.Is(err, icloud.ErrNotReady) {
				return nil
			}
			return err
		}
		entries, _ := withoutPurged(b.Entries, isPurged)
		for _, e := range entries {
			switch e.Kind {
			case "row":
				k := [2]string{e.Table, e.ID}
				if i, ok := rows[k]; ok {
					places[i] = foldEntry(places[i], e)
					continue
				}
				rows[k] = len(places)
			case "content":
				if i, ok := pages[e.PageID]; ok {
					places[i] = []icloud.Entry{e}
					continue
				}
				pages[e.PageID] = len(places)
			default:
				continue
			}
			places = append(places, []icloud.Entry{e})
		}
	}
	for _, p := range places {
		snapshot.Entries = append(snapshot.Entries, p...)
	}
	if err := s.writeBatch(me, snapshot); err != nil {
		return err
	}
	if err := s.setInt("icloud.log_from", snapshot.Seq); err != nil {
		return err
	}
	if err := s.writeDevice(me, true); err != nil {
		return err
	}
	for _, seq := range seqs {
		if seq >= snapshot.Seq {
			break
		}
		old, _ := icloud.BatchPath(me, seq)
		if err := s.folder().Remove(old); err != nil {
			return err
		}
	}
	s.forgetDamaged(me, snapshot.Seq-1)
	s.compactSoon.Store(false)
	return nil
}

// redactQuiet takes what was on pages deleted for good out of the logs of the devices quiet for quietFor, each batch
// kept as it was written but for those entries, and reports whether it read every batch of theirs. One not on this
// device yet is asked for, and left for the next time; one that does not parse holds nothing to take out.
func (s *Service) redactQuiet(me string) (bool, error) {
	peers, err := s.devices(me)
	if err != nil {
		return false, err
	}
	isPurged := s.purgedCheck()
	whole := true
	notHere := func(rel string) {
		s.folder().Download(rel)
		whole = false
	}
	var failed error
	for _, peer := range peers {
		rel, err := icloud.DevicePath(peer)
		if err != nil {
			continue
		}
		var d deviceFile
		switch err := s.folder().ReadJSON(rel, &d); {
		case errors.Is(err, icloud.ErrNotReady):
			notHere(rel)
			continue
		case err != nil || cloudNow().Sub(d.LastActiveAt) < quietFor:
			continue
		}
		seqs, _, err := s.folder().Batches(peer)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		for _, seq := range seqs {
			rel, err := icloud.BatchPath(peer, seq)
			if err != nil {
				continue
			}
			var b icloud.Batch
			switch err := s.folder().ReadJSON(rel, &b); {
			case errors.Is(err, icloud.ErrNotReady):
				notHere(rel)
				continue
			case err != nil:
				continue
			}
			if entries, changed := withoutPurged(b.Entries, isPurged); changed {
				b.Entries = entries
				failed = errors.Join(failed, s.folder().WriteJSON(rel, b))
			}
		}
	}
	return whole && failed == nil, failed
}

// purgedCheck reports whether a page was deleted for good, asking the store once a page.
func (s *Service) purgedCheck() func(string) bool {
	purged := map[string]bool{}
	return func(pageID string) bool {
		gone, seen := purged[pageID]
		if !seen {
			gone = s.st.IsPurged(pageID)
			purged[pageID] = gone
		}
		return gone
	}
}

// withoutPurged is the entries but for what was on pages deleted for good, the rows of those pages as they are now,
// and whether that changed any.
func withoutPurged(entries []icloud.Entry, isPurged func(string) bool) ([]icloud.Entry, bool) {
	out := make([]icloud.Entry, 0, len(entries))
	changed := false
	for _, e := range entries {
		if ofPurgedPage(e, isPurged) {
			changed = true
			continue
		}
		if e.Kind == "row" && e.Table == "pages" && isPurged(e.ID) {
			if row := tombstoneRow(e.Row); !reflect.DeepEqual(row, e.Row) {
				e.Row, changed = row, true
			}
		}
		out = append(out, e)
	}
	return out, changed
}

// ofPurgedPage reports whether the entry is something on a page deleted for good: its content, or a row on it. A
// page deleted for good leaves nothing of what was on it in the log.
func ofPurgedPage(e icloud.Entry, isPurged func(string) bool) bool {
	switch {
	case e.Kind == "content":
		return isPurged(e.PageID)
	case e.Kind == "row" && e.Table != "pages":
		pageID, _ := e.Row["page_id"].(string)
		return pageID != "" && isPurged(pageID)
	}
	return false
}

// tombstoneRow is a page's row as a page deleted for good keeps it, in a version of it the log keeps: none of what it
// was.
func tombstoneRow(row map[string]any) map[string]any {
	out := maps.Clone(row)
	for k, v := range map[string]any{"title": "", "icon": nil, "tags": []any{}, "default_template_id": nil, "public_link_until": nil} {
		if _, ok := out[k]; ok {
			out[k] = v
		}
	}
	return out
}
