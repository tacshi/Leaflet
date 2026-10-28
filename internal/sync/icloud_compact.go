package sync

import (
	"errors"

	"leaflet/internal/icloud"
)

// compact folds this device's log into one snapshot batch once it holds
// more than compactAfter batches, and removes the batches before it. A
// device that had not read them reads the snapshot instead, which holds
// the latest of every row and page in them, with their versions: what it
// already had stays, being no older.
func (s *Service) compact(me string) error {
	seqs, _, err := s.ICloud.Batches(me)
	if err != nil || len(seqs) <= compactAfter {
		return err
	}
	last := s.intValue("icloud.seq")
	snapshot := icloud.Batch{Format: icloud.Format, Device: me, Seq: last + 1, WrittenAt: cloudNow().UTC(), Snapshot: true}
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
		if err := s.ICloud.ReadJSON(rel, &b); err != nil {
			if errors.Is(err, icloud.ErrNotReady) {
				return nil // try once iCloud has it again
			}
			return err
		}
		for _, e := range b.Entries {
			switch e.Kind {
			case "row":
				k := [2]string{e.Table, e.ID}
				if i, ok := rows[k]; ok {
					snapshot.Entries[i] = e
					continue
				}
				rows[k] = len(snapshot.Entries)
			case "content":
				if i, ok := pages[e.PageID]; ok {
					snapshot.Entries[i] = e
					continue
				}
				pages[e.PageID] = len(snapshot.Entries)
			default:
				continue
			}
			snapshot.Entries = append(snapshot.Entries, e)
		}
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
		if err := s.ICloud.Remove(old); err != nil {
			return err
		}
	}
	return nil
}
