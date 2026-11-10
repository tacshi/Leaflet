package sync

import (
	"errors"

	"leaflet/internal/icloud"
)

func (s *Service) compact(me string) error {
	seqs, _, err := s.folder().Batches(me)
	if err != nil || len(seqs) <= compactAfter {
		return err
	}
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
		for _, e := range b.Entries {
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
	return nil
}
