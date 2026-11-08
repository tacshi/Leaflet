package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"leaflet/internal/doc"
)

// ContentRow is a page's stored content.
type ContentRow struct {
	PageID    string
	Content   string
	PlainText string
	UpdatedAt time.Time
	// BaseContent is the server version this device last synced with, the
	// merge base; RemoteUpdatedAt is its time and RemoteRevision its
	// revision.
	BaseContent     string
	RemoteUpdatedAt time.Time
	RemoteRevision  int64
	// WriteID tells this write of the content apart from every other
	// (WriteID).
	WriteID int64
}

// ContentRow returns a page's stored content.
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

// StoredContent returns a page's content as stored, and its write
// (WriteID), without the rest of its row, as a page open in the app reads
// it after each save, and its base, as large, is of no use to it.
func (s *Store) StoredContent(pageID string) (content string, writeID int64, err error) {
	var written sql.NullInt64
	err = s.db.QueryRow(`SELECT c.content, w.write_id FROM page_contents c
		LEFT JOIN text_writes w ON w.kind = 'page' AND w.id = c.page_id WHERE c.page_id = ?`, pageID).Scan(&content, &written)
	return content, written.Int64, err
}

// RemoteAt is the time and revision of the server's version of a page's
// content this device last merged, read from its index, not its row: zero
// before the first, and for a page whose content this device does not keep.
func (s *Store) RemoteAt(pageID string) (time.Time, int64, error) {
	var at sql.NullInt64
	var revision int64
	err := s.db.QueryRow(`SELECT remote_updated_at, remote_revision FROM page_contents INDEXED BY page_contents_remote WHERE page_id = ?`, pageID).Scan(&at, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, 0, nil
	}
	return fromMs(at), revision, err
}

// MergedRemoteAt records that the server's version of a page's content at
// revision, which this device has merged, is the server's as of at too: the
// server stamps content again without changing it, as a move of its page or
// a save of the same content does, and RemoteAt then says the device has
// it as of then. It moves the time only on, and only while the device
// still has that revision; the content and its base stay as they are.
func (s *Store) MergedRemoteAt(pageID string, revision int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE page_contents SET remote_updated_at = ?
		WHERE page_id = ? AND remote_revision = ? AND remote_updated_at IS NOT NULL AND remote_updated_at < ?`,
		at.UnixMilli(), pageID, revision, at.UnixMilli())
	return err
}

// WriteID tells the write of a page's content stored now apart from every
// other, its content unread: every write of it, this process's or
// another's, draws a new one (textTriggers). It is sql.ErrNoRows for a
// page whose content this device does not keep.
func (s *Store) WriteID(pageID string) (int64, error) { return writeID(s.db, pageID) }

func writeID(q querier, pageID string) (int64, error) {
	var id int64
	err := q.QueryRow(`SELECT write_id FROM text_writes WHERE kind = 'page' AND id = ?`, pageID).Scan(&id)
	return id, err
}

// savedWrite is the write a save stored, or 0, which tells nothing, where
// text_writes has no row of the page, as one removed by hand: the save
// stands all the same.
func savedWrite(q querier, pageID string) (int64, error) {
	id, err := writeID(q, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// Content returns a page's content on this device, or nil when it is not
// kept here or cannot be read, as content of a newer app: read as an empty
// page, it would be copied or saved as one.
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

// writeContent stores a new page's content, as never synced.
func writeContent(q querier, pageID string, c *doc.Content, at time.Time) error {
	_, err := q.Exec(`INSERT INTO page_contents (page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision)
		VALUES (?, ?, ?, ?, NULL, NULL, 0) ON CONFLICT(page_id) DO UPDATE SET content = excluded.content,
		plain_text = excluded.plain_text, updated_at = excluded.updated_at, base_content = NULL,
		remote_updated_at = NULL, remote_revision = 0`,
		pageID, c.Encode(), c.PlainText(), ms(at))
	return err
}

// ErrContentChanged is SaveContentOver's when the stored content is no
// longer what the caller read.
var ErrContentChanged = errors.New("store: page content changed")

// SaveContent saves an editor's content: stamps the blocks that changed,
// stores it, and queues it for the server. It returns the stamped content
// and whether anything changed.
func (s *Store) SaveContent(pageID string, c *doc.Content) (*doc.Content, bool, error) {
	saved, err := s.saveContent(pageID, c, nil)
	return saved.Content, saved.Changed, err
}

// SaveContentOver is SaveContent for content made from the stored content
// over ("" when there was none): it saves only while that is still what is
// stored, and returns ErrContentChanged otherwise.
func (s *Store) SaveContentOver(pageID, over string, c *doc.Content) (*doc.Content, bool, error) {
	saved, err := s.saveContent(pageID, c, &over)
	return saved.Content, saved.Changed, err
}

// Saved is what a save stored.
type Saved struct {
	// Content is the content stamped, and Encoded it as stored: what is
	// stored now, whether or not the save changed it.
	Content *doc.Content
	Encoded string
	// WriteID is the write stored now (Store.WriteID).
	WriteID int64
	// Changed reports whether the save changed what was stored.
	Changed bool
}

// SaveOver is SaveContentOver for a caller that keeps the content as
// stored, as a page open in the app does: what it stored comes encoded,
// with its write, so the caller need not encode it again.
func (s *Store) SaveOver(pageID, over string, c *doc.Content) (Saved, error) {
	return s.saveContent(pageID, c, &over)
}

// ErrUnreadable is UpdateContent's for stored content that cannot be
// read, as a newer app's, which it leaves as it is.
var ErrUnreadable = errors.New("store: page content cannot be read")

// UpdateContent changes a page's stored content with edit, which changes
// c and reports whether to save it: c is the content stored, or an empty
// page when this device keeps none (kept false). It saves only over what
// edit was given, so content stored in between, by the app, a sync or
// MCP, is not written over: edit runs again on that, up to five times,
// and then it returns ErrContentChanged. An error edit returns stops it.
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

// saveContent reads the stored content, stores the new content and queues
// it in one transaction, which holds the write lock throughout: a pull
// cannot store the server's content in between, finding nothing queued,
// and a save that fails leaves nothing half done. The content is encoded
// once, and only what a save changes is written: the merge base stays as
// it is, unread but for its blocks' times, which the label counts.
func (s *Store) saveContent(pageID string, c *doc.Content, over *string) (Saved, error) {
	var saved Saved
	err := s.inTx(func(tx *sql.Tx) error {
		var stored string
		var base sql.NullString
		err := tx.QueryRow(`SELECT content, base_content FROM page_contents WHERE page_id = ?`, pageID).Scan(&stored, &base)
		// No content is stored as "", so "" can only mean none.
		none := errors.Is(err, sql.ErrNoRows)
		if err != nil && !none {
			return err
		}
		if over != nil && (*over == "" && !none || *over != "" && (none || stored != *over)) {
			return ErrContentChanged
		}
		var previous *doc.Content
		if !none {
			// Content that cannot be read, as a newer app's, is not saved
			// over as if it were an empty page.
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

// changedSince counts the blocks of c edited since base, encoded, the
// version last synced: those whose time differs, added or deleted since.
// A block's time moves on exactly as it changes (doc.Content.Stamped), so
// only base's times are read, not its blocks. ok is false for a base that
// cannot be read.
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

// PutSyncedContentOver stores content that came from the server, merged
// from the stored content over ("" when there was none), with the base it
// now agrees on, and records the conflicts the merge met in the same
// transaction: once the base is stored, the server's revision is merged,
// so a conflict not recorded with it would never be met again. It stores
// only while over is still what is stored, checked in the statement that
// writes, and returns ErrContentChanged otherwise.
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

// TakeSyncedContentOver stores the server's content of a page over this
// device's change to it, entry e, which the server refused, as
// PutSyncedContentOver does, with e off the queue in the same
// transaction: stored apart, a failure in between would leave the change
// neither queued nor replaced. A change made here since, which replaced
// e, stays queued, and the content stays out, to merge with the server's
// as it goes.
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

// PutMergedContentOver is PutSyncedContentOver for content merged from
// another device's version of a page, from, through iCloud Drive, which
// has no server base. In the same transaction it records from, the base
// the next merge with that device starts from, and the conflicts the merge
// met, and, unless label is "", queues the merge to go out: stored without
// its entry, this device's merge would never reach the others, and the
// version recorded without its conflicts, they would never be met again.
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

// PushedContent records what the server stored for a page: the content
// this device now has, the base both agree on, and its time and revision.
// It stores them only while over is still the stored content, checked in
// the statement that writes, and returns ErrContentChanged otherwise.
//
// The conflicts the merge met are recorded in the same transaction: once
// the server's revision is stored as merged, a conflict not recorded with
// it would never be met again.
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
