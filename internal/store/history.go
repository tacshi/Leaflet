package store

import (
	"database/sql"
	"time"

	"leaflet/internal/doc"
)

// HistoryEntry is a snapshot of a page.
type HistoryEntry struct {
	ID      string
	PageID  string
	Content string
	Reason  string // agent_run | autosave | restore | conflict | manual (named)
	// AIJobID is the local agent run that made the snapshot. It stays on
	// this device, as the run's job does.
	AIJobID   string
	Label     string
	CreatedAt time.Time
}

// AddHistory keeps a snapshot of a page.
func (s *Store) AddHistory(pageID string, c *doc.Content, reason, aiJobID, label string) error {
	return s.AddHistoryEncoded(pageID, c.Encode(), reason, aiJobID, label)
}

// AddHistoryEncoded is AddHistory for content encoded already, as a save
// has it. Its entry in the outbox names the snapshot alone, which the push
// reads (HistoryToRemote): the content is kept once, in page_history.
func (s *Store) AddHistoryEncoded(pageID, content, reason, aiJobID, label string) error {
	h := HistoryEntry{ID: doc.NewRowID(), PageID: pageID, Content: content, Reason: reason, AIJobID: aiJobID, Label: label, CreatedAt: time.Now()}
	err := s.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO page_history (id, page_id, content, reason, ai_job_id, label, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			h.ID, h.PageID, h.Content, h.Reason, ns(h.AIJobID), h.Label, ms(h.CreatedAt)); err != nil {
			return err
		}
		if err := queue(tx, "page_history", h.ID, map[string]any{"id": h.ID, "page_id": pageID}, "page history", pageID); err != nil {
			return err
		}
		return pruneHistory(tx, pageID)
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", pageID)
	s.Notify("page_history", pageID)
	return nil
}

// newestHistory selects the ids of a page's newest 50 snapshots, which
// History shows with its named versions. Of two taken in the same
// millisecond, the one stored later is newer.
const newestHistory = `SELECT id FROM page_history WHERE page_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 50`

// pruneHistory removes the snapshots of a page History no longer shows:
// all but its newest 50 and its named versions. One still waiting to go
// up leaves the queue with it, so the server never gets it.
func pruneHistory(tx *sql.Tx, pageID string) error {
	old := `SELECT id FROM page_history WHERE page_id = ? AND reason <> 'manual' AND id NOT IN (` + newestHistory + `)`
	if _, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'page_history' AND entity_id IN (`+old+`)`, pageID, pageID); err != nil { // #nosec G202 -- old is the literal above.
		return err
	}
	_, err := tx.Exec(`DELETE FROM page_history WHERE id IN (`+old+`)`, pageID, pageID) // #nosec G202 -- old is the literal above.
	return err
}

// HistoryEntry returns one snapshot.
func (s *Store) HistoryEntry(id string) (HistoryEntry, error) {
	var h HistoryEntry
	var job sql.NullString
	var created int64
	err := s.db.QueryRow(`SELECT id, page_id, content, reason, ai_job_id, label, created_at FROM page_history WHERE id = ?`, id).
		Scan(&h.ID, &h.PageID, &h.Content, &h.Reason, &job, &h.Label, &created)
	h.AIJobID, h.CreatedAt = job.String, time.UnixMilli(created)
	return h, err
}

// History returns a page's newest 50 snapshots, and the versions named
// before them.
func (s *Store) History(pageID string) ([]HistoryEntry, error) {
	rows, err := s.db.Query(`SELECT id, page_id, content, reason, ai_job_id, label, created_at FROM page_history
		WHERE page_id = ? AND (reason = 'manual' OR id IN (`+newestHistory+`))
		ORDER BY created_at DESC, rowid DESC`, pageID, pageID)
	return collect(rows, err, func(sc scanner) (HistoryEntry, error) {
		var h HistoryEntry
		var job sql.NullString
		var created int64
		err := sc.Scan(&h.ID, &h.PageID, &h.Content, &h.Reason, &job, &h.Label, &created)
		h.AIJobID, h.CreatedAt = job.String, time.UnixMilli(created)
		return h, err
	})
}

// MaybeAutosave keeps an autosave snapshot at most every ten minutes of
// editing.
func (s *Store) MaybeAutosave(pageID string, c *doc.Content) error {
	return s.MaybeAutosaveEncoded(pageID, func() string { return c.Encode() })
}

// MaybeAutosaveEncoded is MaybeAutosave for content encode encodes, which
// it calls only when it keeps a snapshot, as a save that has the content
// encoded already passes it.
func (s *Store) MaybeAutosaveEncoded(pageID string, encode func() string) error {
	var last int64
	err := s.db.QueryRow(`SELECT created_at FROM page_history WHERE page_id = ? ORDER BY created_at DESC LIMIT 1`, pageID).Scan(&last)
	if err == nil && time.Since(time.UnixMilli(last)) < 10*time.Minute {
		return nil
	}
	return s.AddHistoryEncoded(pageID, encode(), "autosave", "", "Autosave")
}
