package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// OutboxEntry is a change waiting for the server.
type OutboxEntry struct {
	ID int64
	// Entity is the Supabase table (pages, page_content, comments…),
	// "storage" for a file upload, or "storage_delete" for the removal of
	// a deleted file's stored copy.
	Entity   string
	EntityID string
	Op       string // upsert | delete
	Payload  string
	// Revision counts the times the payload was replaced while queued, so
	// sending an older payload does not take a newer one off the queue.
	Revision  int64
	Label     string // shown in Sync and storage, e.g. "2 blocks edited"
	PageID    string
	Attempts  int
	LastError string
	CreatedAt time.Time
}

// Queue queues an upsert for the server. A pending upsert of the same row
// is replaced instead of queued twice, so the queue shows one entry per
// changed thing; the replacement gets the next revision. Replacing goes
// by the row, in one transaction, so sync sending the entry meanwhile
// cannot leave the change out of the queue.
func (s *Store) Queue(entity, id string, payload map[string]any, label, pageID string) error {
	if err := s.inTx(func(tx *sql.Tx) error { return queue(tx, entity, id, payload, label, pageID) }); err != nil {
		return err
	}
	s.Notify("outbox", pageID)
	return nil
}

// queue is Queue in a transaction of the caller's, which stores the change
// it queues in the same one.
func queue(q querier, entity, id string, payload map[string]any, label, pageID string) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	set := `payload = ?, revision = revision + 1, attempts = 0, last_error = NULL`
	args := []any{string(raw)}
	if label != "" {
		set += `, label = ?`
		args = append(args, label)
	}
	res, err := q.Exec(`UPDATE outbox SET `+set+` WHERE entity = ? AND entity_id = ? AND op = 'upsert'`, append(args, entity, id)...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		if _, err := q.Exec(`INSERT INTO outbox (entity, entity_id, op, payload, label, page_id, created_at) VALUES (?, ?, 'upsert', ?, ?, ?, ?)`,
			entity, id, string(raw), label, ns(pageID), time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

// Pending returns the queue, oldest first.
func (s *Store) Pending() ([]OutboxEntry, error) {
	rows, err := s.db.Query(`SELECT id, entity, entity_id, op, payload, revision, label, page_id, attempts, last_error, created_at FROM outbox ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		var page, lastErr sql.NullString
		var created int64
		if err := rows.Scan(&e.ID, &e.Entity, &e.EntityID, &e.Op, &e.Payload, &e.Revision, &e.Label, &page, &e.Attempts, &lastErr, &created); err != nil {
			return nil, err
		}
		e.PageID, e.LastError, e.CreatedAt = page.String, lastErr.String, time.UnixMilli(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PendingCount is how many changes wait for the server.
func (s *Store) PendingCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&n)
	return n
}

// HasPending reports whether a row has a change waiting.
func (s *Store) HasPending(entity, id string) bool { return pending(s.db, entity, id) }

func pending(q querier, entity, id string) bool {
	var n int
	_ = q.QueryRow(`SELECT COUNT(*) FROM outbox WHERE entity = ? AND entity_id = ?`, entity, id).Scan(&n)
	return n > 0
}

// Dequeue removes an entry once the server has it, unless it was replaced
// since it was read: the newer payload still has to go.
func (s *Store) Dequeue(e OutboxEntry) error {
	_, err := s.db.Exec(`DELETE FROM outbox WHERE id = ? AND revision = ?`, e.ID, e.Revision)
	if err == nil {
		s.Notify("outbox", "")
	}
	return err
}

// MarkFailed records a failed attempt, unless the entry was replaced since
// it was read.
func (s *Store) MarkFailed(e OutboxEntry, msg string) error {
	_, err := s.db.Exec(`UPDATE outbox SET attempts = attempts + 1, last_error = ? WHERE id = ? AND revision = ?`, msg, e.ID, e.Revision)
	if err == nil {
		s.Notify("outbox", "")
	}
	return err
}
