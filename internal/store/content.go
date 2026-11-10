package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"leaflet/internal/doc"
)

type ContentRow struct {
	PageID          string
	Content         string
	PlainText       string
	UpdatedAt       time.Time
	BaseContent     string
	RemoteUpdatedAt time.Time
	RemoteRevision  int64
	WriteID         int64
}

func (s *Store) ContentRow(pageID string) (ContentRow, error) { return contentRow(s.db, pageID) }

func contentRow(q querier, pageID string) (ContentRow, error) {
	var r ContentRow
	var updated int64
	var base sql.NullString
	var remote, written sql.NullInt64
	err := q.QueryRow(`SELECT c.page_id, c.content, c.plain_text, c.updated_at, c.base_content, c.remote_updated_at, c.remote_revision, w.write_id
		FROM page_contents c LEFT JOIN text_writes w ON w.kind = 'page' AND w.id = c.page_id WHERE c.page_id = ?`, pageID).
		Scan(&r.PageID, &r.Content, &r.PlainText, &updated, &base, &remote, &r.RemoteRevision, &written)
	r.UpdatedAt, r.BaseContent, r.RemoteUpdatedAt, r.WriteID = time.UnixMilli(updated), base.String, fromMs(remote), written.Int64
	return r, err
}

func (s *Store) StoredContent(pageID string) (content string, writeID int64, err error) {
	var written sql.NullInt64
	err = s.db.QueryRow(`SELECT c.content, w.write_id FROM page_contents c
		LEFT JOIN text_writes w ON w.kind = 'page' AND w.id = c.page_id WHERE c.page_id = ?`, pageID).Scan(&content, &written)
	return content, written.Int64, err
}

func (s *Store) RemoteAt(pageID string) (time.Time, int64, error) {
	var at sql.NullInt64
	var revision int64
	err := s.db.QueryRow(`SELECT remote_updated_at, remote_revision FROM page_contents INDEXED BY page_contents_remote WHERE page_id = ?`, pageID).Scan(&at, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, 0, nil
	}
	return fromMs(at), revision, err
}

func (s *Store) MergedRemoteAt(pageID string, revision int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE page_contents SET remote_updated_at = ?
		WHERE page_id = ? AND remote_revision = ? AND remote_updated_at IS NOT NULL AND remote_updated_at < ?`,
		at.UnixMilli(), pageID, revision, at.UnixMilli())
	return err
}

func (s *Store) WriteID(pageID string) (int64, error) { return writeID(s.db, pageID) }

func writeID(q querier, pageID string) (int64, error) {
	var id int64
	err := q.QueryRow(`SELECT write_id FROM text_writes WHERE kind = 'page' AND id = ?`, pageID).Scan(&id)
	return id, err
}

func savedWrite(q querier, pageID string) (int64, error) {
	id, err := writeID(q, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s *Store) Content(pageID string) *doc.Content {
	stored, _, err := s.StoredContent(pageID)
	if err != nil {
		return nil
	}
	c, err := doc.Decode(stored)
	if err != nil {
		return nil
	}
	return c
}

func writeContent(q querier, pageID string, c *doc.Content, at time.Time) error {
	_, err := q.Exec(`INSERT INTO page_contents (page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision)
		VALUES (?, ?, ?, ?, NULL, NULL, 0) ON CONFLICT(page_id) DO UPDATE SET content = excluded.content,
		plain_text = excluded.plain_text, updated_at = excluded.updated_at, base_content = NULL,
		remote_updated_at = NULL, remote_revision = 0`,
		pageID, c.Encode(), c.PlainText(), ms(at))
	return err
}

var ErrContentChanged = errors.New("store: page content changed")

func (s *Store) SaveContent(pageID string, c *doc.Content) (*doc.Content, bool, error) {
	saved, err := s.saveContent(pageID, c, nil)
	return saved.Content, saved.Changed, err
}

func (s *Store) SaveContentOver(pageID, over string, c *doc.Content) (*doc.Content, bool, error) {
	saved, err := s.saveContent(pageID, c, &over)
	return saved.Content, saved.Changed, err
}

type Saved struct {
	Content *doc.Content
	Encoded string
	WriteID int64
	Changed bool
}

func (s *Store) SaveOver(pageID, over string, c *doc.Content) (Saved, error) {
	return s.saveContent(pageID, c, &over)
}

var ErrUnreadable = errors.New("store: page content cannot be read")

func (s *Store) UpdateContent(pageID string, edit func(c *doc.Content, kept bool) (bool, error)) error {
	for range 5 {
		over, c := "", doc.Empty()
		stored, _, err := s.StoredContent(pageID)
		switch {
		case err == nil:
			if c, err = doc.Decode(stored); err != nil {
				return fmt.Errorf("%w: %w", ErrUnreadable, err)
			}
			over = stored
		case !IsNotFound(err):
			return err
		}
		if save, err := edit(c, over != ""); err != nil || !save {
			return err
		}
		if _, _, err := s.SaveContentOver(pageID, over, c); !errors.Is(err, ErrContentChanged) {
			return err
		}
	}
	return ErrContentChanged
}

func (s *Store) saveContent(pageID string, c *doc.Content, over *string) (Saved, error) {
	var saved Saved
	err := s.inTx(func(tx *sql.Tx) error {
		var stored string
		var base sql.NullString
		err := tx.QueryRow(`SELECT content, base_content FROM page_contents WHERE page_id = ?`, pageID).Scan(&stored, &base)
		none := errors.Is(err, sql.ErrNoRows)
		if err != nil && !none {
			return err
		}
		if over != nil && (*over == "" && !none || *over != "" && (none || stored != *over)) {
			return ErrContentChanged
		}
		var previous *doc.Content
		if !none {
			if previous, err = doc.Decode(stored); err != nil {
				return fmt.Errorf("stored content: %w", err)
			}
		}
		now := time.Now()
		saved.Content = c.Stamped(previous, now.UnixMilli())
		saved.Encoded = saved.Content.Encode()
		if !none && stored == saved.Encoded {
			saved.WriteID, err = savedWrite(tx, pageID)
			return err
		}
		if none {
			_, err = tx.Exec(`INSERT INTO page_contents (page_id, content, plain_text, updated_at) VALUES (?, ?, ?, ?)`,
				pageID, saved.Encoded, saved.Content.PlainText(), ms(now))
		} else {
			_, err = tx.Exec(`UPDATE page_contents SET content = ?, plain_text = ?, updated_at = ? WHERE page_id = ?`,
				saved.Encoded, saved.Content.PlainText(), ms(now), pageID)
		}
		if err != nil {
			return err
		}
		blocks := len(saved.Content.Blocks)
		if base.String != "" {
			if n, ok := changedSince(base.String, saved.Content); ok {
				blocks = n
			}
		}
		label := fmt.Sprintf("%d blocks edited", blocks)
		if blocks == 1 {
			label = "1 block edited"
		}
		if err := queue(tx, "page_content", pageID, map[string]any{"page_id": pageID}, label, pageID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE pages SET updated_at = ? WHERE id = ?`, ms(now), pageID); err != nil {
			return err
		}
		saved.Changed = true
		saved.WriteID, err = savedWrite(tx, pageID)
		return err
	})
	if err != nil {
		return Saved{}, err
	}
	if saved.Changed {
		s.Notify("outbox", pageID)
		s.Notify("page_contents", pageID)
	}
	return saved, nil
}

func changedSince(base string, c *doc.Content) (n int, ok bool) {
	var b struct {
		Meta struct {
			Blocks map[string]float64 `json:"blocks"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(base), &b); err != nil {
		return 0, false
	}
	for id, at := range c.Blocks {
		if was, ok := b.Meta.Blocks[id]; !ok || int64(was) != at {
			n++
		}
	}
	for id := range b.Meta.Blocks {
		if _, ok := c.Blocks[id]; !ok {
			n++
		}
	}
	return n, true
}

func (s *Store) PutSyncedContentOver(pageID, over string, c *doc.Content, base string, remoteAt time.Time, revision int64, conflicts ...doc.Conflict) error {
	err := s.inTx(func(tx *sql.Tx) error {
		if err := putSyncedOver(tx, pageID, over, c, base, remoteAt, revision); err != nil {
			return err
		}
		return addConflicts(tx, pageID, conflicts)
	})
	if err != nil {
		return err
	}
	s.Notify("page_contents", pageID)
	if len(conflicts) > 0 {
		s.Notify("conflicts", pageID)
	}
	return nil
}

func (s *Store) TakeSyncedContentOver(e OutboxEntry, pageID, over string, c *doc.Content, base string, remoteAt time.Time, revision int64) error {
	stored := false
	err := s.inTx(func(tx *sql.Tx) error {
		if err := dequeueIn(tx, &e); err != nil {
			return err
		}
		waits, err := pending(tx, "page_content", pageID)
		if err != nil || waits {
			return err
		}
		stored = true
		return putSyncedOver(tx, pageID, over, c, base, remoteAt, revision)
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", pageID)
	if stored {
		s.Notify("page_contents", pageID)
	}
	return nil
}

func (s *Store) PutMergedContentOver(pageID, over string, c *doc.Content, label string, from PeerContent, conflicts ...doc.Conflict) error {
	err := s.inTx(func(tx *sql.Tx) error {
		if err := putSyncedOver(tx, pageID, over, c, "", time.Time{}, 0); err != nil {
			return err
		}
		return putPeerMerge(tx, pageID, from, label, conflicts)
	})
	if err != nil {
		return err
	}
	s.notifyPeerMerge(pageID, label, conflicts)
	s.Notify("page_contents", pageID)
	return nil
}

func putSyncedOver(q querier, pageID, over string, c *doc.Content, base string, remoteAt time.Time, revision int64) error {
	var res sql.Result
	var err error
	if over == "" {
		res, err = q.Exec(`INSERT INTO page_contents (page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(page_id) DO NOTHING`,
			pageID, c.Encode(), c.PlainText(), ms(time.Now()), ns(base), nt(remoteAt), revision)
	} else {
		res, err = q.Exec(`UPDATE page_contents SET content = ?, plain_text = ?, updated_at = ?, base_content = ?,
			remote_updated_at = ?, remote_revision = ? WHERE page_id = ? AND content = ?`,
			c.Encode(), c.PlainText(), ms(time.Now()), ns(base), nt(remoteAt), revision, pageID, over)
	}
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

func (s *Store) PushedContent(pageID, over, content, plain, base string, remoteAt time.Time, revision int64, conflicts ...doc.Conflict) error {
	err := s.inTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE page_contents SET content = ?, plain_text = ?, base_content = ?, remote_updated_at = ?, remote_revision = ?
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
		return addConflicts(tx, pageID, conflicts)
	})
	if err != nil {
		return err
	}
	if content != over {
		s.Notify("page_contents", pageID)
	}
	if len(conflicts) > 0 {
		s.Notify("conflicts", pageID)
	}
	return nil
}
