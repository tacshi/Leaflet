package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
)

type OutboxEntry struct {
	ID        int64
	Entity    string
	EntityID  string
	Op        string
	Payload   string
	Revision  int64
	Label     string
	PageID    string
	Attempts  int
	LastError string
	CreatedAt time.Time
}

func (s *Store) Queue(entity, id string, payload map[string]any, label, pageID string) error {
	if err := s.inTx(func(tx *sql.Tx) error { return queue(tx, entity, id, payload, label, pageID) }); err != nil {
		return err
	}
	s.Notify("outbox", pageID)
	return nil
}

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
	res, err := q.Exec(`UPDATE outbox SET `+set+` WHERE entity = ? AND entity_id = ? AND op = 'upsert'`, append(args, entity, id)...) // #nosec G202
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

const ChangedKey = "_changed"

const AdoptKey = "_adopt"

func adopting(row map[string]any, at time.Time) map[string]any {
	row[AdoptKey] = at.UTC().Format(time.RFC3339Nano)
	return row
}

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

func withChanged(cols []string, old, row map[string]any) []string {
	for k, v := range row {
		if k != "changed_at" && !slices.Contains(cols, k) && !sameJSON(v, old[k]) {
			cols = append(cols, k)
		}
	}
	slices.Sort(cols)
	return cols
}

func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

func (s *Store) Pending() ([]OutboxEntry, error) {
	return s.pending(`payload`)
}

func (s *Store) Queued() ([]OutboxEntry, error) {
	return s.pending(`''`)
}

func (s *Store) pending(payload string) ([]OutboxEntry, error) {
	rows, err := s.db.Query(`SELECT id, entity, entity_id, op, ` + payload + `, revision, label, page_id, attempts, last_error, created_at FROM outbox ORDER BY id`) // #nosec G202
	return collect(rows, err, func(sc scanner) (OutboxEntry, error) {
		var e OutboxEntry
		var page, lastErr sql.NullString
		var created int64
		err := sc.Scan(&e.ID, &e.Entity, &e.EntityID, &e.Op, &e.Payload, &e.Revision, &e.Label, &page, &e.Attempts, &lastErr, &created)
		e.PageID, e.LastError, e.CreatedAt = page.String, lastErr.String, time.UnixMilli(created)
		return e, err
	})
}

func (s *Store) LoadPayload(e *OutboxEntry) (bool, error) {
	err := s.db.QueryRow(`SELECT payload, revision FROM outbox WHERE id = ?`, e.ID).Scan(&e.Payload, &e.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) PendingCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&n)
	return n
}

func (s *Store) HasPending(entity, id string) bool {
	waits, err := pending(s.db, entity, id)
	return waits || err != nil
}

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

func (s *Store) Dequeue(e OutboxEntry) error {
	_, err := s.db.Exec(`DELETE FROM outbox WHERE id = ? AND revision = ?`, e.ID, e.Revision)
	if err == nil {
		s.Notify("outbox", "")
	}
	return err
}

func (s *Store) MarkFailed(e OutboxEntry, msg string) error {
	_, err := s.db.Exec(`UPDATE outbox SET attempts = attempts + 1, last_error = ? WHERE id = ? AND revision = ?`, msg, e.ID, e.Revision)
	if err == nil {
		s.Notify("outbox", "")
	}
	return err
}
