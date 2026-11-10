package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
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
	Op       string // always "upsert": a row deleted goes up with deleted_at set
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
	res, err := q.Exec(`UPDATE outbox SET `+set+` WHERE entity = ? AND entity_id = ? AND op = 'upsert'`, append(args, entity, id)...) // #nosec G202 -- set is made of the literals above.
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

// ChangedKey names, in a queued row, the columns changed here since the
// server last had the row. The push changes only those on the server, so
// a change another device made meanwhile to another column is kept: an
// offline rename no longer undoes a move or a deletion made elsewhere. A
// row queued without it, new here or sent again whole, goes up whole.
const ChangedKey = "_changed"

// AdoptKey marks, in a queued row, one sent again whole as this device's
// pages join an account (sync's requeueAll), with when it last changed
// here. The server may have the row already, from a device this one synced
// with through iCloud Drive, and changed there since: the row goes over
// the server's only when it changed here later, and takes the server's
// otherwise. A row with a change of some columns queued has none: that
// change goes as any other.
const AdoptKey = "_adopt"

// adopting is row marked with AdoptKey, changed here at at.
func adopting(row map[string]any, at time.Time) map[string]any {
	row[AdoptKey] = at.UTC().Format(time.RFC3339Nano)
	return row
}

// queueChange queues row, changed here from old, the row as stored before
// the change (nil for a new one), naming the columns that changed, with
// those of any change to it still queued. One still queued whole stays
// whole; a change that changes nothing queues nothing. A row changed is no
// longer the server's copy (unpulled).
func queueChange(q querier, entity, id string, old, row map[string]any, label, pageID string) error {
	if old == nil {
		return queue(q, entity, id, row, label, pageID)
	}
	cols, queued, err := queuedCols(q, entity, id)
	if err != nil {
		return err
	}
	if queued && cols == nil {
		if err := unpulled(q, entity, id); err != nil {
			return err
		}
		return queue(q, entity, id, row, label, pageID)
	}
	if cols = withChanged(cols, old, row); len(cols) == 0 {
		return nil
	}
	if err := unpulled(q, entity, id); err != nil {
		return err
	}
	changed := maps.Clone(row)
	changed[ChangedKey] = cols
	return queue(q, entity, id, changed, label, pageID)
}

// queuedCols reads the change of a row still queued: whether there is one,
// and the columns it changes (ChangedKey), nil for one queued whole.
func queuedCols(q querier, entity, id string) (cols []string, queued bool, err error) {
	var payload string
	switch err := q.QueryRow(`SELECT payload FROM outbox WHERE entity = ? AND entity_id = ? AND op = 'upsert'`, entity, id).Scan(&payload); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	var prev map[string]any
	if err := json.Unmarshal([]byte(payload), &prev); err != nil {
		return nil, false, err
	}
	list, ok := prev[ChangedKey].([]any)
	if !ok {
		return nil, true, nil
	}
	cols = make([]string, 0, len(list))
	for _, c := range list {
		if s, ok := c.(string); ok {
			cols = append(cols, s)
		}
	}
	return cols, true, nil
}

// withChanged is cols with the columns of row whose values differ from
// old's, in order. When the row changed (changed_at) is no change of its
// own: it goes with the columns that did (sync's pushChanged).
func withChanged(cols []string, old, row map[string]any) []string {
	for k, v := range row {
		if k != "changed_at" && !slices.Contains(cols, k) && !sameJSON(v, old[k]) {
			cols = append(cols, k)
		}
	}
	slices.Sort(cols)
	return cols
}

// sameJSON reports whether two column values go up as the same JSON.
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// Pending returns the queue, oldest first, with each entry's payload.
func (s *Store) Pending() ([]OutboxEntry, error) {
	return s.pending(`payload`)
}

// Queued returns the queue, oldest first, without the payloads, which
// LoadPayload reads an entry at a time as it goes: a queue of many changes
// is never all in memory at once.
func (s *Store) Queued() ([]OutboxEntry, error) {
	return s.pending(`''`)
}

// pending lists the queue with payload, the column, or an empty text
// in its place.
func (s *Store) pending(payload string) ([]OutboxEntry, error) {
	rows, err := s.db.Query(`SELECT id, entity, entity_id, op, ` + payload + `, revision, label, page_id, attempts, last_error, created_at FROM outbox ORDER BY id`) // #nosec G202 -- payload is one of the literals above.
	return collect(rows, err, func(sc scanner) (OutboxEntry, error) {
		var e OutboxEntry
		var page, lastErr sql.NullString
		var created int64
		err := sc.Scan(&e.ID, &e.Entity, &e.EntityID, &e.Op, &e.Payload, &e.Revision, &e.Label, &page, &e.Attempts, &lastErr, &created)
		e.PageID, e.LastError, e.CreatedAt = page.String, lastErr.String, time.UnixMilli(created)
		return e, err
	})
}

// LoadPayload reads the payload of an entry Queued listed, as it is queued
// now: one replaced since comes with its revision, so it leaves the queue
// only as sent. It reports false for an entry no longer queued, as one
// sent or dropped meanwhile.
func (s *Store) LoadPayload(e *OutboxEntry) (bool, error) {
	err := s.db.QueryRow(`SELECT payload, revision FROM outbox WHERE id = ?`, e.ID).Scan(&e.Payload, &e.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// PendingCount is how many changes wait for the server, to show: 0 when
// the queue cannot be read. What it would decide, as forgetting an
// account's pages, counts again in its own transaction
// (ForgetSyncedAccount).
func (s *Store) PendingCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&n)
	return n
}

// HasPending reports whether a row has a change waiting, or may have: one
// the queue could not be read to tell of counts as waiting, so nothing
// from elsewhere is stored over it.
func (s *Store) HasPending(entity, id string) bool {
	waits, err := pending(s.db, entity, id)
	return waits || err != nil
}

// PendingFailed reports whether a row's change waits in the queue after
// the server refused it.
func (s *Store) PendingFailed(entity, id string) bool {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE entity = ? AND entity_id = ? AND attempts > 0`, entity, id).Scan(&n)
	return err == nil && n > 0
}

func pending(q querier, entity, id string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT COUNT(*) FROM outbox WHERE entity = ? AND entity_id = ?`, entity, id).Scan(&n)
	return n > 0, err
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
