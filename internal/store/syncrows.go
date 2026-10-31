package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What sync writes: rows that came from the server, and the space a
// device's pages move into after sign-in.

// AllPages returns every page, deleted ones too.
func (s *Store) AllPages() ([]Page, error) { return s.queryPages(`1 = 1 ORDER BY sort_key`) }

// PutSpace stores a space row.
func (s *Store) PutSpace(id, name, owner string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO spaces (id, name, owner_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, owner_id = excluded.owner_id, updated_at = excluded.updated_at`,
		id, name, owner, now, now)
	return err
}

// MoveSpace moves everything of one space into another, in one
// transaction, so nothing made meanwhile is left in the space it deletes.
func (s *Store) MoveSpace(from, to string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		for _, q := range []string{
			`UPDATE pages SET space_id = ? WHERE space_id = ?`,
			`UPDATE folders SET space_id = ? WHERE space_id = ?`,
			`UPDATE files SET space_id = ? WHERE space_id = ?`,
		} {
			if _, err := tx.Exec(q, to, from); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`DELETE FROM spaces WHERE id = ?`, from)
		return err
	})
	s.Notify("*", "")
	return err
}

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
			if err := requeueRow(tx, "pages", p.ID, PageToRemote(p), p.ID); err != nil {
				return err
			}
			return queue(tx, "page_content", p.ID, map[string]any{"page_id": p.ID}, "page from this device", p.ID)
		case "folders":
			f, err := scanFolder(tx.QueryRow(`SELECT `+folderCols+` FROM folders WHERE id = ?`, id))
			if err != nil {
				return err
			}
			return requeueRow(tx, "folders", f.ID, FolderToRemote(f), "")
		case "comments":
			c, err := scanComment(tx.QueryRow(`SELECT `+commentCols+` FROM comments WHERE id = ?`, id))
			if err != nil {
				return err
			}
			pageID = c.PageID
			return requeueRow(tx, "comments", c.ID, CommentToRemote(c), c.PageID)
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
// have.
func requeueRow(tx *sql.Tx, entity, id string, row map[string]any, pageID string) error {
	var queued string
	switch err := tx.QueryRow(`SELECT payload FROM outbox WHERE entity = ? AND entity_id = ? AND op = 'upsert'`, entity, id).Scan(&queued); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		var prev map[string]any
		if err := json.Unmarshal([]byte(queued), &prev); err != nil {
			return err
		}
		if cols, ok := prev[ChangedKey]; ok {
			row[ChangedKey] = cols
		}
	}
	return queue(tx, entity, id, row, "", pageID)
}

// RequeueFile queues a file to go up whole in space spaceID, as stored
// now, as Requeue does a page, with its contents when this device has
// them (a deleted file's row goes alone).
func (s *Store) RequeueFile(id, spaceID string) error {
	var f File
	err := s.inTx(func(tx *sql.Tx) error {
		files, err := filesIn(tx, `id = ?`, id)
		if err != nil || len(files) == 0 {
			return err
		}
		f = files[0]
		f.SpaceID = spaceID
		f.StoragePath = spaceID + "/" + f.ID + "/" + f.Name
		if err := putFile(tx, f); err != nil {
			return err
		}
		if err := queue(tx, "files", f.ID, FileToRemote(f), "file", f.PageID); err != nil {
			return err
		}
		if !f.DeletedAt.IsZero() || !onDevice(f.LocalPath) {
			return nil
		}
		ct := f.MimeType
		if ct == "" {
			ct = "application/octet-stream"
		}
		return queue(tx, "storage", "files/"+f.ID, map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": f.LocalPath, "content_type": ct}, "file waiting to upload", f.PageID)
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

// RequeueRecording queues a finished recording to go up whole, as stored
// now, as Requeue does a page, with its audio when this device has it.
func (s *Store) RequeueRecording(id string) error {
	var r Recording
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		if r, err = scanRecording(tx.QueryRow(`SELECT `+recordingCols+` FROM recordings WHERE id = ?`, id)); err != nil {
			return err
		}
		if r.Status != "ready" && r.Status != "failed" {
			return nil
		}
		if err := queue(tx, "recordings", r.ID, RecordingToRemote(r), "", r.PageID); err != nil {
			return err
		}
		if !onDevice(r.LocalPath) {
			return nil
		}
		return queue(tx, "storage", "recordings/"+r.ID, uploadOf(r), "audio waiting to upload", r.PageID)
	})
	if IsNotFound(err) {
		return nil
	}
	if err == nil {
		s.Notify("outbox", r.PageID)
	}
	return err
}

// onDevice reports whether a file is on this device.
func onDevice(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// pulled are the tables a pull from the server stores rows of, by id,
// with the time each last changed there.
var pulled = map[string]bool{"pages": true, "folders": true, "files": true, "comments": true, "recordings": true, "model_providers": true}

// HasRowAt reports whether this device has a row pulled from the server as
// it was at a time, to the millisecond kept here.
func (s *Store) HasRowAt(table, id string, at time.Time) bool {
	if !pulled[table] {
		return false
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ? AND updated_at = ?`, id, at.UnixMilli()).Scan(&n) // #nosec G202 -- table is one of pulled.
	return n > 0
}

// PutRemoteFolder stores a folder from the server, unless it has a change
// waiting here, as PutRemotePage does a page.
func (s *Store) PutRemoteFolder(r map[string]any) error {
	f := FolderFromRemote(r)
	return s.inTx(func(tx *sql.Tx) error {
		if pending(tx, "folders", f.ID) {
			return nil
		}
		return writeFolder(tx, f)
	})
}

// PutRemoteComment stores a comment from the server, unless it has a
// change waiting here, as PutRemotePage does a page.
func (s *Store) PutRemoteComment(r map[string]any, authorName string) error {
	c := CommentFromRemote(r, authorName)
	return s.inTx(func(tx *sql.Tx) error {
		if pending(tx, "comments", c.ID) {
			return nil
		}
		return putComment(tx, c)
	})
}

// PutRemoteFile stores a file from the server, unless it has a change
// waiting here, keeping its copy here until the file is deleted.
func (s *Store) PutRemoteFile(r map[string]any) error {
	f := FileFromRemote(r)
	stored := false
	err := s.inTx(func(tx *sql.Tx) error {
		if pending(tx, "files", f.ID) {
			return nil
		}
		stored = true
		if err := putFile(tx, f); err != nil || !f.DeletedAt.IsZero() {
			return err
		}
		// A file that lives keeps its stored copy: its deletion here gave
		// way to the server's row, as one the user may no longer delete
		// (sync's pushRow), and the removal queued with it would take
		// the copy the row still names.
		_, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'storage_delete' AND entity_id = ?`, "files/"+f.ID)
		return err
	})
	if err != nil {
		return err
	}
	if stored && !f.DeletedAt.IsZero() {
		s.removeCopy(f.ID)
	}
	return nil
}

// PutRemoteRecording stores a recording from the server, unless it has a
// change waiting here, keeping its audio on this device.
func (s *Store) PutRemoteRecording(r map[string]any) error {
	rec := Recording{ID: S(r, "id"), PageID: S(r, "page_id"), BlockID: S(r, "block_id"), AudioPath: S(r, "audio_path"),
		SttProvider: S(r, "stt_provider"), SttModel: S(r, "stt_model"), Status: S(r, "status"), Language: S(r, "language"),
		CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"])}
	if d, ok := r["duration_ms"].(float64); ok {
		rec.DurationMs = int(d)
	}
	if rec.Status == "" {
		rec.Status = "ready"
	}
	if raw, err := json.Marshal(r["transcript"]); err == nil {
		_ = json.Unmarshal(raw, &rec.Transcript)
	}
	return s.inTx(func(tx *sql.Tx) error {
		if pending(tx, "recordings", rec.ID) {
			return nil
		}
		if old, err := scanRecording(tx.QueryRow(`SELECT `+recordingCols+` FROM recordings WHERE id = ?`, rec.ID)); err == nil {
			rec.LocalPath, rec.UploadedAt = old.LocalPath, old.UploadedAt
		}
		return putRecording(tx, rec)
	})
}

// PutRemoteProvider stores a cloud model connection from the server.
func (s *Store) PutRemoteProvider(r map[string]any) error {
	return s.PutProvider(ProviderFromRemote(r))
}

// RemoveSharedPages removes from this device a page shared with the user
// whose share was withdrawn, with the pages under it and everything kept
// of them: content, history, comments, recordings and files with their
// copies here, members, conflicts, chats, and changes waiting, which the
// server would refuse now. A page of the user's own space stays, as does
// everything when the page is not here. It returns the pages removed.
func (s *Store) RemoveSharedPages(rootID string) ([]string, error) {
	own := s.Session().SpaceID
	var ids, copies []string
	err := s.inTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`WITH RECURSIVE tree(id) AS (
				SELECT id FROM pages WHERE id = ? AND space_id != ?
				UNION SELECT p.id FROM pages p JOIN tree t ON p.parent_id = t.id WHERE p.space_id != ?)
			SELECT id FROM tree`, rootID, own, own)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(ids) == 0 {
			return err
		}
		in := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err = tx.Query(`SELECT local_path FROM files WHERE local_path IS NOT NULL AND page_id IN (`+in+`)
			UNION ALL SELECT local_path FROM recordings WHERE local_path IS NOT NULL AND page_id IN (`+in+`)`, append(args, args...)...) // #nosec G202 -- placeholders only.
		if err != nil {
			return err
		}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				_ = rows.Close()
				return err
			}
			copies = append(copies, path)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		// The files' entries are dropped by the files: ones queued before
		// they went with their page's have no page_id.
		files, err := filesIn(tx, `page_id IN (`+in+`)`, args...)
		if err != nil {
			return err
		}
		for _, f := range files {
			if _, err := tx.Exec(`DELETE FROM outbox WHERE (entity = 'files' AND entity_id = ?)
				OR (entity IN ('storage', 'storage_delete') AND entity_id = ?)`, f.ID, "files/"+f.ID); err != nil {
				return err
			}
		}
		for _, q := range []string{
			`DELETE FROM pages WHERE id IN (` + in + `)`, `DELETE FROM page_contents WHERE page_id IN (` + in + `)`,
			`DELETE FROM comments WHERE page_id IN (` + in + `)`, `DELETE FROM recordings WHERE page_id IN (` + in + `)`,
			`DELETE FROM files WHERE page_id IN (` + in + `)`, `DELETE FROM ai_jobs WHERE page_id IN (` + in + `)`,
			`DELETE FROM page_history WHERE page_id IN (` + in + `)`, `DELETE FROM conflicts WHERE page_id IN (` + in + `)`,
			`DELETE FROM page_members WHERE page_id IN (` + in + `)`, `DELETE FROM peer_contents WHERE page_id IN (` + in + `)`,
			`DELETE FROM outbox WHERE page_id IN (` + in + `)`,
		} {
			if _, err := tx.Exec(q, args...); err != nil { // #nosec G202 -- placeholders only.
				return err
			}
		}
		// The page's own rows may be queued without its page_id.
		if _, err := tx.Exec(`DELETE FROM outbox WHERE entity IN ('pages', 'page_content', 'page_history') AND entity_id IN (`+in+`)`, args...); err != nil { // #nosec G202 -- placeholders only.
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec(`DELETE FROM key_values WHERE key IN (?, ?)`, "chat."+id, "chat.past."+id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	for _, path := range copies {
		s.removeLocalCopy(path)
	}
	s.Notify("*", "")
	return ids, nil
}

// removeLocalCopy removes a file or recording's copy here, only from the
// app's folders, where it keeps them.
func (s *Store) removeLocalCopy(path string) {
	for _, dir := range []string{filepath.Join(s.Dir, "files"), filepath.Join(s.Dir, "recordings")} {
		if rel, err := filepath.Rel(dir, path); err == nil && filepath.IsLocal(rel) {
			_ = os.Remove(path)
			if parent := filepath.Dir(path); parent != dir {
				_ = os.Remove(parent) // only once it is empty
			}
			return
		}
	}
}

// ClearLocalPath forgets a recording's audio on this device.
func (s *Store) ClearLocalPath(id string) error {
	_, err := s.db.Exec(`UPDATE recordings SET local_path = NULL WHERE id = ?`, id)
	return err
}

// PushedContent records what the server stored for a page: the content
// this device now has, the base both agree on, and its time and revision.
// It stores them only while over is still the stored content, checked in
// the statement that writes, and returns ErrContentChanged otherwise.
func (s *Store) PushedContent(pageID, over, content, plain, base string, remoteAt time.Time, revision int64) error {
	res, err := s.db.Exec(`UPDATE page_contents SET content = ?, plain_text = ?, base_content = ?, remote_updated_at = ?, remote_revision = ?
		WHERE page_id = ? AND content = ?`,
		content, plain, base, remoteAt.UnixMilli(), revision, pageID, over)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrContentChanged
	}
	return nil
}

// MembersOrNil returns a page's members, or nil.
func (s *Store) MembersOrNil(pageID string) []Member {
	m, _ := s.Members(pageID)
	return m
}

// CanEdit reports whether the user may change a page: their own, or one
// shared with them as an editor. The members of the nearest page with the
// user among them say, it or one it is inside: the server gives each page
// the roles of the pages above it too. A shared page is read-only until
// members have loaded for it or a page it is inside.
func (s *Store) CanEdit(p Page) bool {
	sess := s.Session()
	if p.SpaceID == sess.SpaceID {
		return true
	}
	seen := map[string]bool{}
	for !seen[p.ID] {
		seen[p.ID] = true
		for _, m := range s.MembersOrNil(p.ID) {
			if m.UserID == sess.UserID {
				return m.Role == "editor" || m.Role == "owner"
			}
		}
		parent, err := s.Page(p.ParentID)
		if p.ParentID == "" || err != nil {
			return false
		}
		p = parent
	}
	return false
}
