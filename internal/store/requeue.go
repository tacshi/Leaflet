package store

import (
	"database/sql"
	"errors"
	"path"
)

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
	if !f.DeletedAt.IsZero() {
		s.removeCopy(f.ID)
	}
	s.Notify("outbox", f.PageID)
	return nil
}

func requeuedKey(f File, spaceID string) string {
	if f.StoragePath == "" || f.DeletedAt.IsZero() && OnDevice(f.LocalPath) {
		return StorageKey(spaceID, f.ID, f.Name)
	}
	return spaceID + "/" + f.ID + "/" + path.Base(f.StoragePath)
}

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
