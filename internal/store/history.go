package store

import (
	"database/sql"
	"time"

	"leaflet/internal/doc"
)

type HistoryEntry struct {
	ID        string
	PageID    string
	Content   string
	Reason    string
	AIJobID   string
	Label     string
	CreatedAt time.Time
}

func (s *Store) AddHistory(pageID string, c *doc.Content, reason, aiJobID, label string) error {
	return s.AddHistoryEncoded(pageID, c.Encode(), reason, aiJobID, label)
}

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

const newestHistory = `SELECT id FROM page_history WHERE page_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 50`

func pruneHistory(tx *sql.Tx, pageID string) error {
	old := `SELECT id FROM page_history WHERE page_id = ? AND reason <> 'manual' AND id NOT IN (` + newestHistory + `)`
	if _, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'page_history' AND entity_id IN (`+old+`)`, pageID, pageID); err != nil { // #nosec G202
		return err
	}
	_, err := tx.Exec(`DELETE FROM page_history WHERE id IN (`+old+`)`, pageID, pageID) // #nosec G202
	return err
}

func (s *Store) HistoryEntry(id string) (HistoryEntry, error) {
	var h HistoryEntry
	var job sql.NullString
	var created int64
	err := s.db.QueryRow(`SELECT id, page_id, content, reason, ai_job_id, label, created_at FROM page_history WHERE id = ?`, id).
		Scan(&h.ID, &h.PageID, &h.Content, &h.Reason, &job, &h.Label, &created)
	h.AIJobID, h.CreatedAt = job.String, time.UnixMilli(created)
	return h, err
}

func (s *Store) NewestHistory(pageID string) (HistoryEntry, error) {
	h := HistoryEntry{PageID: pageID}
	var job sql.NullString
	var created int64
	err := s.db.QueryRow(`SELECT id, reason, ai_job_id, label, created_at FROM page_history WHERE page_id = ?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, pageID).Scan(&h.ID, &h.Reason, &job, &h.Label, &created)
	h.AIJobID, h.CreatedAt = job.String, time.UnixMilli(created)
	return h, err
}

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

func (s *Store) MaybeAutosave(pageID string, c *doc.Content) error {
	return s.MaybeAutosaveEncoded(pageID, func() string { return c.Encode() })
}

func (s *Store) MaybeAutosaveEncoded(pageID string, encode func() string) error {
	var last int64
	err := s.db.QueryRow(`SELECT created_at FROM page_history WHERE page_id = ? ORDER BY created_at DESC LIMIT 1`, pageID).Scan(&last)
	if err == nil && time.Since(time.UnixMilli(last)) < 10*time.Minute {
		return nil
	}
	return s.AddHistoryEncoded(pageID, encode(), "autosave", "", "Autosave")
}
