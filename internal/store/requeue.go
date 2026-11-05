package store

import (
	"database/sql"
	"errors"
	"path"
)

// Requeue queues a page (with its content), folder or comment to go up
// whole, as stored now, in one transaction: sending everything again
// takes a while, and a change made meanwhile, as a deletion, goes up with
// it, not under the copy read before. A row that is gone has nothing to
// send. A row with a change of some columns still queued goes as stored
// now too, but still as a change of only those (requeueRow).
func (s *Store) Requeue(table, id string) error {
	pageID := ""
	err := s.inTx(func(tx *sql.Tx) error {
		switch table {
		case "pages":
			p, err := pageIn(tx, id)
			if err != nil {
				return err
			}
			pageID = p.ID
			if err := requeueRow(tx, "pages", p.ID, nil, adopting(PageToRemote(p), p.UpdatedAt), "", p.ID); err != nil {
				return err
			}
			return queue(tx, "page_content", p.ID, map[string]any{"page_id": p.ID}, "page from this device", p.ID)
		case "folders":
			f, err := folderIn(tx, id)
			if err != nil {
				return err
			}
			return requeueRow(tx, "folders", f.ID, nil, adopting(FolderToRemote(f), f.UpdatedAt), "", "")
		case "comments":
			c, err := commentIn(tx, id)
			if err != nil {
				return err
			}
			pageID = c.PageID
			return requeueRow(tx, "comments", c.ID, nil, adopting(CommentToRemote(c), c.UpdatedAt), "", c.PageID)
		}
		return errors.New("store: no requeue of " + table)
	})
	if IsNotFound(err) {
		return nil
	}
	if err == nil {
		s.Notify("outbox", pageID)
	}
	return err
}

// requeueRow queues a row to go up whole, unless a change of some of its
// columns is still queued (ChangedKey): that change stays one of only
// those columns, as stored now. Sent whole, it would go as newer than
// every change to the row this device has seen, and undo one another
// device made to its other columns, as a move held here while this
// device's rename waits (sync's iCloud batches). Those columns went with
// an earlier entry, or come with the row the server or the other devices
// have. old is the row as stored before the requeue changed it, or nil:
// the columns it changed go with that change too.
func requeueRow(tx *sql.Tx, entity, id string, old, row map[string]any, label, pageID string) error {
	cols, _, err := queuedCols(tx, entity, id)
	if err != nil {
		return err
	}
	if cols != nil {
		delete(row, AdoptKey)
		if old != nil {
			cols = withChanged(cols, old, row)
		}
		row[ChangedKey] = cols
	}
	return queue(tx, entity, id, row, label, pageID)
}

// RequeueFile queues a file to go up whole in space spaceID, as stored
// now, as Requeue does a page, with its contents when this device has
// them (a deleted file's row goes alone). A change of some of its columns
// still queued stays one of those, as in requeueRow, and of the space.
func (s *Store) RequeueFile(id, spaceID string) error {
	var f File
	err := s.inTx(func(tx *sql.Tx) error {
		files, err := filesIn(tx, s.Dir, `id = ?`, id)
		if err != nil || len(files) == 0 {
			return err
		}
		f = files[0]
		old := FileToRemote(f)
		f.SpaceID = spaceID
		f.StoragePath = requeuedKey(f, spaceID)
		if err := putFile(tx, s.Dir, f); err != nil {
			return err
		}
		if err := requeueRow(tx, "files", f.ID, old, adopting(FileToRemote(f), f.UpdatedAt), "file", f.PageID); err != nil {
			return err
		}
		if !f.DeletedAt.IsZero() || !OnDevice(f.LocalPath) {
			return nil
		}
		return queue(tx, "storage", "files/"+f.ID, fileUpload(s.Dir, f), "file waiting to upload", f.PageID)
	})
	if err != nil || f.ID == "" {
		return err
	}
	// Deleted meanwhile: the copy brought here to send goes too.
	if !f.DeletedAt.IsZero() {
		s.removeCopy(f.ID)
	}
	s.Notify("outbox", f.PageID)
	return nil
}

// requeuedKey is the key of a file sent again in space spaceID. One that
// goes up from here gets a key Storage takes; any other stays by the name
// it is stored under, which is where the devices find it, as a file in
// iCloud Drive this device has not brought here.
func requeuedKey(f File, spaceID string) string {
	if f.StoragePath == "" || f.DeletedAt.IsZero() && OnDevice(f.LocalPath) {
		return StorageKey(spaceID, f.ID, f.Name)
	}
	return spaceID + "/" + f.ID + "/" + path.Base(f.StoragePath)
}

// RequeueRecording queues a finished recording to go up whole, as stored
// now, as Requeue does a page, with its audio when this device has it. It
// goes marked with when it last changed here (AdoptKey), so it goes over
// the server's only when it changed here later: the server may have
// transcribed it since, as for another device.
func (s *Store) RequeueRecording(id string) error {
	var r Recording
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		if r, err = recordingIn(tx, s.Dir, id); err != nil {
			return err
		}
		if r.Status != "ready" && r.Status != "failed" {
			return nil
		}
		if err := requeueRow(tx, "recordings", r.ID, nil, adopting(RecordingToRemote(r), r.UpdatedAt), "", r.PageID); err != nil {
			return err
		}
		if !OnDevice(r.LocalPath) {
			return nil
		}
		return queue(tx, "storage", "recordings/"+r.ID, audioUpload(s.Dir, r), "audio waiting to upload", r.PageID)
	})
	if IsNotFound(err) {
		return nil
	}
	if err == nil {
		s.Notify("outbox", r.PageID)
	}
	return err
}
