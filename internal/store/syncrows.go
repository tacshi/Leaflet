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
		return putFile(tx, f)
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
