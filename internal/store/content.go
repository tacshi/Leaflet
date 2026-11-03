package store

import (
	"database/sql"
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
}

// ContentRow returns a page's stored content.
func (s *Store) ContentRow(pageID string) (ContentRow, error) { return contentRow(s.db, pageID) }

func contentRow(q querier, pageID string) (ContentRow, error) {
	var r ContentRow
	var updated int64
	var base sql.NullString
	var remote sql.NullInt64
	err := q.QueryRow(`SELECT page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision FROM page_contents WHERE page_id = ?`, pageID).
		Scan(&r.PageID, &r.Content, &r.PlainText, &updated, &base, &remote, &r.RemoteRevision)
	r.UpdatedAt, r.BaseContent, r.RemoteUpdatedAt = time.UnixMilli(updated), base.String, fromMs(remote)
	return r, err
}

// Content returns a page's content on this device, or nil when it is not
// kept here or cannot be read, as content of a newer app: read as an empty
// page, it would be copied or saved as one.
func (s *Store) Content(pageID string) *doc.Content {
	r, err := s.ContentRow(pageID)
	if err != nil {
		return nil
	}
	c, err := doc.Decode(r.Content)
	if err != nil {
		return nil
	}
	return c
}

func writeContent(q querier, pageID string, c *doc.Content, at time.Time, keep *ContentRow) error {
	var base any
	var remote any
	var revision int64
	if keep != nil {
		base, remote, revision = ns(keep.BaseContent), nt(keep.RemoteUpdatedAt), keep.RemoteRevision
	}
	_, err := q.Exec(`INSERT INTO page_contents (page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(page_id) DO UPDATE SET content = excluded.content,
		plain_text = excluded.plain_text, updated_at = excluded.updated_at, base_content = excluded.base_content,
		remote_updated_at = excluded.remote_updated_at, remote_revision = excluded.remote_revision`,
		pageID, c.Encode(), c.PlainText(), ms(at), base, remote, revision)
	return err
}

// ErrContentChanged is SaveContentOver's when the stored content is no
// longer what the caller read.
var ErrContentChanged = errors.New("store: page content changed")

// SaveContent saves an editor's content: stamps the blocks that changed,
// stores it, and queues it for the server. It returns the stamped content
// and whether anything changed.
func (s *Store) SaveContent(pageID string, c *doc.Content) (*doc.Content, bool, error) {
	return s.saveContent(pageID, c, nil)
}

// SaveContentOver is SaveContent for content made from the stored content
// over ("" when there was none): it saves only while that is still what is
// stored, and returns ErrContentChanged otherwise.
func (s *Store) SaveContentOver(pageID, over string, c *doc.Content) (*doc.Content, bool, error) {
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
		row, err := s.ContentRow(pageID)
		switch {
		case err == nil:
			if c, err = doc.Decode(row.Content); err != nil {
				return fmt.Errorf("%w: %w", ErrUnreadable, err)
			}
			over = row.Content
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
// and a save that fails leaves nothing half done.
func (s *Store) saveContent(pageID string, c *doc.Content, over *string) (*doc.Content, bool, error) {
	var stamped *doc.Content
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		row, err := contentRow(tx, pageID)
		if over != nil {
			// No content is stored as "", so "" can only mean none.
			none := errors.Is(err, sql.ErrNoRows)
			if *over == "" && !none || *over != "" && (err != nil || row.Content != *over) {
				return ErrContentChanged
			}
		}
		var previous *doc.Content
		switch {
		case err == nil:
			// Content that cannot be read, as a newer app's, is not saved
			// over as if it were an empty page.
			if previous, err = doc.Decode(row.Content); err != nil {
				return fmt.Errorf("stored content: %w", err)
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		now := time.Now()
		stamped = c.Stamped(previous, now.UnixMilli())
		if previous != nil && previous.Encode() == stamped.Encode() {
			return nil
		}
		if err := writeContent(tx, pageID, stamped, now, &row); err != nil {
			return err
		}
		blocks := len(stamped.Blocks)
		if base, err := doc.Decode(row.BaseContent); row.BaseContent != "" && err == nil {
			blocks = len(doc.ChangedBlocks(base, stamped))
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
		changed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if changed {
		s.Notify("outbox", pageID)
		s.Notify("page_contents", pageID)
	}
	return stamped, changed, nil
}

// PutSyncedContentOver stores content that came from the server, merged
// from the stored content over ("" when there was none), with the base it
// now agrees on: it stores only while over is still what is stored,
// checked in the statement that writes, and returns ErrContentChanged
// otherwise.
func (s *Store) PutSyncedContentOver(pageID, over string, c *doc.Content, base string, remoteAt time.Time, revision int64) error {
	if err := putSyncedOver(s.db, pageID, over, c, base, remoteAt, revision); err != nil {
		return err
	}
	s.Notify("page_contents", pageID)
	return nil
}

// PutMergedContentOver is PutSyncedContentOver for content merged from
// another device's through iCloud Drive, which has no server base, and,
// unless label is "", queues it to go out in the same transaction: stored
// without its entry, this device's merge would never reach the others.
func (s *Store) PutMergedContentOver(pageID, over string, c *doc.Content, label string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		if err := putSyncedOver(tx, pageID, over, c, "", time.Time{}, 0); err != nil || label == "" {
			return err
		}
		return queue(tx, "page_content", pageID, map[string]any{"page_id": pageID}, label, pageID)
	})
	if err != nil {
		return err
	}
	if label != "" {
		s.Notify("outbox", pageID)
	}
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
