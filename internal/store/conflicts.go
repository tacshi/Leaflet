package store

import (
	"encoding/json"
	"time"

	"leaflet/internal/doc"
)

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

// Conflicts returns the unresolved conflicts, newest first, but those of
// pages deleted, which come back with their pages.
func (s *Store) Conflicts() ([]ConflictRow, error) {
	rows, err := s.db.Query(`SELECT id, page_id, block_id, local_node, remote_node, kept, local_edited_at, remote_edited_at, created_at FROM conflicts
		WHERE resolved_at IS NULL AND page_id NOT IN (SELECT id FROM pages WHERE deleted_at IS NOT NULL) ORDER BY created_at DESC`)
	return collect(rows, err, func(sc scanner) (ConflictRow, error) {
		var c ConflictRow
		var local, remote string
		var le, re, cr int64
		err := sc.Scan(&c.ID, &c.PageID, &c.BlockID, &local, &remote, &c.Kept, &le, &re, &cr)
		// A deleted side is stored as null.
		_ = json.Unmarshal([]byte(local), &c.LocalNode)
		_ = json.Unmarshal([]byte(remote), &c.RemoteNode)
		c.LocalEditedAt, c.RemoteEditedAt, c.CreatedAt = time.UnixMilli(le), time.UnixMilli(re), time.UnixMilli(cr)
		return c, err
	})
}

// ResolveConflict lets a conflict handled go: nothing reads it again, and
// kept, each conflict ever met would stay with its two blocks.
func (s *Store) ResolveConflict(id string) error {
	if _, err := s.db.Exec(`DELETE FROM conflicts WHERE id = ?`, id); err != nil {
		return err
	}
	s.Notify("conflicts", "")
	return nil
}
