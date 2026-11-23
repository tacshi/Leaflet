package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

type Job struct {
	ID             string
	PageID         string
	Surface        string
	FocusBlockIDs  []string
	CommentID      string
	ConversationID string
	ProviderID     string
	Model          string
	Status         string
	Request        map[string]any
	Result         map[string]any
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

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

// CommentJobs is the jobs of the runs asked in the comment thread commentID
// starts, the oldest first.
func (s *Store) CommentJobs(commentID string) ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, page_id, surface, focus_block_ids, comment_id, conversation_id, provider_id, model,
		status, request, result, error, created_at, updated_at FROM ai_jobs WHERE comment_id = ? ORDER BY created_at`, commentID)
	return collect(rows, err, scanJob)
}

func scanJob(sc scanner) (Job, error) {
	var j Job
	var focus, req string
	var comment, conversation, res, failed sql.NullString
	var created, updated int64
	if err := sc.Scan(&j.ID, &j.PageID, &j.Surface, &focus, &comment, &conversation, &j.ProviderID, &j.Model,
		&j.Status, &req, &res, &failed, &created, &updated); err != nil {
		return j, err
	}
	j.CommentID, j.ConversationID, j.Error = comment.String, conversation.String, failed.String
	j.CreatedAt, j.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	_ = json.Unmarshal([]byte(focus), &j.FocusBlockIDs)
	_ = json.Unmarshal([]byte(req), &j.Request)
	if res.Valid {
		_ = json.Unmarshal([]byte(res.String), &j.Result)
	}
	return j, nil
}

func (s *Store) SetJobStatus(id, status string) (bool, error) {
	res, err := s.db.Exec(`UPDATE ai_jobs SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now().UnixMilli(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
