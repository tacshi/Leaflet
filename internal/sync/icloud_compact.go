package sync

import (
	"errors"
	"reflect"
	"time"

	"leaflet/internal/icloud"
	"leaflet/internal/store"
)

// quietFor is how long a device has not been active before the others take what was on pages deleted for good out of
// its log: it compacts its log no more, which would keep that in iCloud Drive for good. One that comes back finds its
// log as it left it, but for that.
var quietFor = store.BinKept

// Kept on the device, so that a run cut short, or an app quit before the next pass, still leaves nothing of a page
// deleted for good in iCloud Drive: whether this device's log has yet to be compacted, when this device last heard of
// a page deleted for good, and when it last read every batch of the devices gone quiet.
const (
	compactSoonKey = "icloud.compact_soon"
	redactSinceKey = "icloud.redact_since"
	redactedKey    = "icloud.redacted_at"
)

// redactAgain is how often the logs of quiet devices are read again while a device that has not heard of a page
// deleted for good may yet go quiet: one never seen again goes quiet within quietFor.
const redactAgain = 24 * time.Hour

// redactDue reports whether to take what was on pages deleted for good out of the logs of quiet devices: once this
// device has heard of one since it last read them all, and then daily until every device that was active as it heard
// has gone quiet, or come back and compacted its own log.
func (s *Service) redactDue(now time.Time) bool {
	since := s.intValue(redactSinceKey)
	if since == 0 {
		return false
	}
	done := s.intValue(redactedKey)
	if done < since {
		return true
	}
	return now.Before(time.UnixMilli(since).Add(quietFor+redactAgain)) && now.Sub(time.UnixMilli(done)) >= redactAgain
}

func (s *Service) compact(me string) error {
	seqs, _, err := s.folder().Batches(me)
	if err != nil || len(seqs) <= compactAfter && s.intValue(compactSoonKey) == 0 {
		return err
	}
	gone := s.purgedCheck()
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
		entries, _ := withoutPurged(b.Entries, gone)
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
	return s.setInt(compactSoonKey, 0)
}

// redactQuiet takes what was on pages deleted for good, and what files deleted for good were, out of the logs of the
// devices quiet for quietFor, each batch kept as it was written but for those entries, with their versions of those
// pages, and reports whether it is done for now: it read every batch of theirs, or leaves them to another device. One
// device rewrites them, the one with the lowest ID among those active within redactAgain, so that no two write a batch
// at once, which iCloud would keep both versions of, the one beside the other as a copy: a copy of a quiet device's
// batch goes, as no one reads it, or, of another batch than the one it is beside, is redacted as the batches are. A
// batch not on this device yet is asked for, and left for the next time; one that does not parse holds nothing to take
// out.
func (s *Service) redactQuiet(me string) (bool, error) {
	peers, err := s.devices(me)
	if err != nil {
		return false, err
	}
	whole := true
	notHere := func(rel string) {
		s.folder().Download(rel)
		whole = false
	}
	var quiet []string
	for _, peer := range peers {
		rel, err := icloud.DevicePath(peer)
		if err != nil {
			continue
		}
		var d deviceFile
		switch err := s.folder().ReadJSON(rel, &d); {
		case errors.Is(err, icloud.ErrDamaged):
			continue
		case errors.Is(err, icloud.ErrNotReady):
			notHere(rel)
			continue
		case err != nil:
			continue
		}
		switch away := cloudNow().Sub(d.LastActiveAt); {
		case away >= quietFor:
			quiet = append(quiet, peer)
		case away < redactAgain && peer < me:
			return true, nil
		}
	}
	gone := s.purgedCheck()
	var failed error
	for _, peer := range quiet {
		pages, err := s.folder().Pages(peer)
		failed = errors.Join(failed, err)
		for _, id := range pages {
			if gone.page(id) {
				s.dropPageFile(peer, id)
			}
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
			case errors.Is(err, icloud.ErrDamaged):
				continue
			case errors.Is(err, icloud.ErrNotReady):
				notHere(rel)
				continue
			case err != nil:
				continue
			}
			if entries, changed := withoutPurged(b.Entries, gone); changed {
				b.Entries = entries
				failed = errors.Join(failed, s.folder().WriteJSON(rel, b))
			}
		}
		copies, err := s.folder().BatchCopies(peer)
		failed = errors.Join(failed, err)
		for _, c := range copies {
			if c.Seq == 0 {
				continue
			}
			var b icloud.Batch
			switch err := s.folder().ReadJSON(c.Path, &b); {
			case errors.Is(err, icloud.ErrDamaged):
			case errors.Is(err, icloud.ErrNotReady):
				notHere(c.Path)
				continue
			case err != nil:
				continue
			case !s.copyOfBatch(peer, c.Seq, b):
				if entries, changed := withoutPurged(b.Entries, gone); changed {
					b.Entries = entries
					failed = errors.Join(failed, s.folder().WriteJSON(c.Path, b))
				}
				continue
			}
			failed = errors.Join(failed, s.folder().Remove(c.Path))
		}
	}
	return whole && failed == nil, failed
}

// copyOfBatch reports whether b, found beside the device's batch seq as a copy, is a version of that batch: the batch
// the device wrote then, as written or with what was on pages deleted for good taken out. A damaged batch, or one not
// here, has none.
func (s *Service) copyOfBatch(device string, seq int64, b icloud.Batch) bool {
	batch, err := s.readBatch(device, seq)
	return err == nil && b.Device == device && b.Seq == seq && batch.Device == device && batch.Seq == seq &&
		b.WrittenAt.Equal(batch.WrittenAt)
}

// purged reports whether pages and files were deleted for good here, asking the store once each.
type purged struct {
	st           *store.Store
	pages, files map[string]bool
}

func (s *Service) purgedCheck() *purged {
	return &purged{st: s.st, pages: map[string]bool{}, files: map[string]bool{}}
}

func (p *purged) page(id string) bool {
	gone, seen := p.pages[id]
	if !seen {
		gone = p.st.IsPurged(id)
		p.pages[id] = gone
	}
	return gone
}

func (p *purged) file(id string) bool {
	gone, seen := p.files[id]
	if !seen {
		f, err := p.st.File(id)
		gone = err == nil && !f.PurgedAt.IsZero()
		p.files[id] = gone
	}
	return gone
}

// withoutPurged is the entries but for what was on pages deleted for good, the rows of those pages, and of files
// deleted for good, as they are now, and whether that changed any.
func withoutPurged(entries []icloud.Entry, gone *purged) ([]icloud.Entry, bool) {
	out := make([]icloud.Entry, 0, len(entries))
	changed := false
	for _, e := range entries {
		if ofPurgedPage(e, gone) {
			changed = true
			continue
		}
		if e.Kind == "row" {
			row := e.Row
			switch {
			case e.Table == "pages" && gone.page(e.ID):
				row = store.TombstoneRow(e.Row)
			case e.Table == "files" && gone.file(e.ID):
				row = store.FileTombstoneRow(e.Row)
			}
			if !reflect.DeepEqual(row, e.Row) {
				e.Row, changed = row, true
			}
		}
		out = append(out, e)
	}
	return out, changed
}

// ofPurgedPage reports whether the entry is something on a page deleted for good: its content, or a row on it. A
// page deleted for good leaves nothing of what was on it in the log.
func ofPurgedPage(e icloud.Entry, gone *purged) bool {
	switch {
	case e.Kind == "content":
		return gone.page(e.PageID)
	case e.Kind == "row" && e.Table != "pages":
		pageID, _ := e.Row["page_id"].(string)
		return pageID != "" && gone.page(pageID)
	}
	return false
}
