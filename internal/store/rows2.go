package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"leaflet/internal/doc"
)

// Provider is a model connection. Cloud providers live on the server,
// whose Vault keeps their keys; local ones live on this device only.
type Provider struct {
	ID string
	// Preset is openai | claude | xai | openrouter | deepseek | glm | custom
	// | ollama | lmstudio | local.
	Preset       string
	Name         string
	BaseURL      string
	AuthKind     string // account | api_key | none
	DefaultModel string
	IsLocal      bool
	// SupportsTools is nil until a connection test tells.
	SupportsTools *bool
	Models        []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const providerCols = `id, preset, name, base_url, auth_kind, default_model, is_local, supports_tools, models, created_at, updated_at`

func scanProvider(sc interface{ Scan(...any) error }) (Provider, error) {
	var p Provider
	var tools sql.NullBool
	var models string
	var c, u int64
	err := sc.Scan(&p.ID, &p.Preset, &p.Name, &p.BaseURL, &p.AuthKind, &p.DefaultModel, &p.IsLocal, &tools, &models, &c, &u)
	if tools.Valid {
		v := tools.Bool
		p.SupportsTools = &v
	}
	_ = json.Unmarshal([]byte(models), &p.Models)
	p.CreatedAt, p.UpdatedAt = time.UnixMilli(c), time.UnixMilli(u)
	return p, err
}

// Providers returns the model connections, oldest first.
func (s *Store) Providers() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT ` + providerCols + ` FROM model_providers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Provider returns one model connection.
func (s *Store) Provider(id string) (Provider, error) {
	return scanProvider(s.db.QueryRow(`SELECT `+providerCols+` FROM model_providers WHERE id = ?`, id))
}

// PutProvider stores a model connection.
func (s *Store) PutProvider(p Provider) error {
	if p.ID == "" {
		p.ID = doc.NewRowID()
	}
	models, _ := json.Marshal(p.Models)
	if p.Models == nil {
		models = []byte("[]")
	}
	var tools any
	if p.SupportsTools != nil {
		tools = *p.SupportsTools
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO model_providers (`+providerCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET preset = excluded.preset, name = excluded.name, base_url = excluded.base_url,
		auth_kind = excluded.auth_kind, default_model = excluded.default_model, is_local = excluded.is_local,
		supports_tools = excluded.supports_tools, models = excluded.models, updated_at = excluded.updated_at`,
		p.ID, p.Preset, p.Name, p.BaseURL, p.AuthKind, p.DefaultModel, p.IsLocal, tools, string(models), ms(p.CreatedAt), ms(p.UpdatedAt))
	if err == nil {
		s.Notify("model_providers", "")
	}
	return err
}

// DeleteProvider removes a model connection from this device.
func (s *Store) DeleteProvider(id string) error {
	_, err := s.db.Exec(`DELETE FROM model_providers WHERE id = ?`, id)
	s.Notify("model_providers", "")
	return err
}

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

// ConflictRow is a block both sides changed while apart.
type ConflictRow struct {
	ID      string
	PageID  string
	BlockID string
	// LocalNode or RemoteNode is nil for a block that side deleted and the
	// other edited, which was kept.
	LocalNode      *doc.Node
	RemoteNode     *doc.Node
	Kept           string // local | remote
	LocalEditedAt  time.Time
	RemoteEditedAt time.Time
	CreatedAt      time.Time
	ResolvedAt     time.Time
}

// AddConflict records a merge conflict.
func (s *Store) AddConflict(pageID string, c doc.Conflict) error {
	local, _ := json.Marshal(c.Local)
	remote, _ := json.Marshal(c.Remote)
	_, err := s.db.Exec(`INSERT INTO conflicts (id, page_id, block_id, local_node, remote_node, kept, local_edited_at, remote_edited_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, doc.NewRowID(), pageID, c.BlockID, string(local), string(remote), c.Kept(),
		c.LocalEditedAt, c.RemoteEditedAt, time.Now().UnixMilli())
	if err == nil {
		s.Notify("conflicts", pageID)
	}
	return err
}

// Conflicts returns the unresolved conflicts, newest first.
func (s *Store) Conflicts() ([]ConflictRow, error) {
	rows, err := s.db.Query(`SELECT id, page_id, block_id, local_node, remote_node, kept, local_edited_at, remote_edited_at, created_at FROM conflicts WHERE resolved_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConflictRow
	for rows.Next() {
		var c ConflictRow
		var local, remote string
		var le, re, cr int64
		if err := rows.Scan(&c.ID, &c.PageID, &c.BlockID, &local, &remote, &c.Kept, &le, &re, &cr); err != nil {
			return nil, err
		}
		// A deleted side is stored as null.
		_ = json.Unmarshal([]byte(local), &c.LocalNode)
		_ = json.Unmarshal([]byte(remote), &c.RemoteNode)
		c.LocalEditedAt, c.RemoteEditedAt, c.CreatedAt = time.UnixMilli(le), time.UnixMilli(re), time.UnixMilli(cr)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveConflict marks a conflict handled.
func (s *Store) ResolveConflict(id string) error {
	_, err := s.db.Exec(`UPDATE conflicts SET resolved_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	s.Notify("conflicts", "")
	return err
}

// Member is someone a page is shared with.
type Member struct {
	PageID      string
	UserID      string
	Role        string // owner | editor | viewer
	DisplayName string
	Email       string
	// Accepted is false while the member has not accepted the page's
	// invite.
	Accepted bool
}

// Name is the member's display name, or email.
func (m Member) Name() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Email
}

// Members returns the people a page is shared with.
func (s *Store) Members(pageID string) ([]Member, error) {
	rows, err := s.db.Query(`SELECT page_id, user_id, role, display_name, email, accepted FROM page_members WHERE page_id = ? ORDER BY display_name`, pageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.PageID, &m.UserID, &m.Role, &m.DisplayName, &m.Email, &m.Accepted); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMembers replaces the people a page is shared with.
func (s *Store) SetMembers(pageID string, members []Member) error {
	err := s.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM page_members WHERE page_id = ?`, pageID); err != nil {
			return err
		}
		for _, m := range members {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO page_members (page_id, user_id, role, display_name, email, accepted) VALUES (?, ?, ?, ?, ?, ?)`,
				pageID, m.UserID, m.Role, m.DisplayName, m.Email, m.Accepted); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Notify("page_members", pageID)
	return nil
}
