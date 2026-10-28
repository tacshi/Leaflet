package store

import (
	"database/sql"
	"encoding/json"
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

// PutRemoteComment stores a comment from the server.
func (s *Store) PutRemoteComment(r map[string]any, authorName string) error {
	return s.PutComment(CommentFromRemote(r, authorName))
}

// PutRemoteFile stores a file from the server, keeping its copy here
// until the file is deleted.
func (s *Store) PutRemoteFile(r map[string]any) error {
	f := FileFromRemote(r)
	if err := s.PutFile(f); err != nil {
		return err
	}
	if !f.DeletedAt.IsZero() {
		s.removeCopy(f.ID)
	}
	return nil
}

// PutRemoteRecording stores a recording from the server, keeping its
// audio on this device.
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
	if old, err := s.Recording(rec.ID); err == nil {
		rec.LocalPath, rec.UploadedAt = old.LocalPath, old.UploadedAt
	}
	return s.PutRecording(rec)
}

// PutRemoteProvider stores a cloud model connection from the server.
func (s *Store) PutRemoteProvider(r map[string]any) error {
	return s.PutProvider(ProviderFromRemote(r))
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
	if n, err := res.RowsAffected(); err != nil || n == 0 {
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
// shared with them as an editor. A shared page is read-only until its
// members have loaded.
func (s *Store) CanEdit(p Page) bool {
	sess := s.Session()
	if p.SpaceID == sess.SpaceID {
		return true
	}
	for _, m := range s.MembersOrNil(p.ID) {
		if m.UserID == sess.UserID {
			return m.Role == "editor" || m.Role == "owner"
		}
	}
	return false
}
