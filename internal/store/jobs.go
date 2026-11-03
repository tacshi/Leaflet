package store

import (
	"encoding/json"
	"time"
)

// Job is an agent run.
type Job struct {
	ID             string
	PageID         string
	Surface        string // inline | selection | sidebar | comment
	FocusBlockIDs  []string
	CommentID      string
	ConversationID string
	ProviderID     string
	Model          string
	Status         string // queued | running | done | failed | reverted
	Request        map[string]any
	Result         map[string]any
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PutJob stores an agent run.
func (s *Store) PutJob(j Job) error {
	focus, _ := json.Marshal(j.FocusBlockIDs)
	if j.FocusBlockIDs == nil {
		focus = []byte("[]")
	}
	req, _ := json.Marshal(j.Request)
	var res any
	if j.Result != nil {
		raw, _ := json.Marshal(j.Result)
		res = string(raw)
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now()
	}
	j.UpdatedAt = time.Now()
	_, err := s.db.Exec(`INSERT INTO ai_jobs (id, page_id, surface, focus_block_ids, comment_id, conversation_id, provider_id, model, status, request, result, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET status = excluded.status,
		result = excluded.result, error = excluded.error, updated_at = excluded.updated_at`,
		j.ID, j.PageID, j.Surface, string(focus), ns(j.CommentID), ns(j.ConversationID), j.ProviderID, j.Model, j.Status,
		string(req), res, ns(j.Error), ms(j.CreatedAt), ms(j.UpdatedAt))
	if err == nil {
		s.Notify("ai_jobs", j.PageID)
	}
	return err
}

// SetJobStatus changes an agent run's status, and reports whether this
// device keeps its job: a local run's, which has no row on the server.
func (s *Store) SetJobStatus(id, status string) (bool, error) {
	res, err := s.db.Exec(`UPDATE ai_jobs SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now().UnixMilli(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
