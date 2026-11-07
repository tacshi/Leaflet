package store

import (
	"database/sql"
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
	if err := addConflicts(s.db, pageID, []doc.Conflict{c}); err != nil {
		return err
	}
	s.Notify("conflicts", pageID)
	return nil
}

// addConflicts records the conflicts of a merge of a page, for the methods
// that store the merge to record them in its transaction: recorded apart,
// a merge stored without them would never meet them again.
func addConflicts(q querier, pageID string, conflicts []doc.Conflict) error {
	_, err := insertConflicts(q, pageID, conflicts)
	return err
}

// insertConflicts records conflicts of a page and returns the IDs of those
// it recorded. One the same as a conflict of the page not yet handled, the
// same block with the same two sides, is that one met again, as a merge
// tried again meets it, and is not recorded twice.
func insertConflicts(q querier, pageID string, conflicts []doc.Conflict) ([]string, error) {
	now := time.Now().UnixMilli()
	var ids []string
	for _, c := range conflicts {
		local, err := json.Marshal(c.Local)
		if err != nil {
			return nil, err
		}
		remote, err := json.Marshal(c.Remote)
		if err != nil {
			return nil, err
		}
		id := doc.NewRowID()
		res, err := q.Exec(`INSERT INTO conflicts (id, page_id, block_id, local_node, remote_node, kept, local_edited_at, remote_edited_at, created_at)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM conflicts WHERE page_id = ? AND block_id = ? AND local_node = ? AND remote_node = ? AND resolved_at IS NULL)`,
			id, pageID, c.BlockID, string(local), string(remote), c.Kept(), c.LocalEditedAt, c.RemoteEditedAt, now,
			pageID, c.BlockID, string(local), string(remote))
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, err
		} else if n > 0 {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// HoldConflicts records the conflicts of a merge before it goes to the
// server, as once the server has it the side each conflict lost is
// nowhere else, whether or not this device then stores the merge. It
// returns the IDs of those it recorded, for DropConflicts to take back
// should the server not take the merge.
func (s *Store) HoldConflicts(pageID string, conflicts []doc.Conflict) ([]string, error) {
	if len(conflicts) == 0 {
		return nil, nil
	}
	var ids []string
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		ids, err = insertConflicts(tx, pageID, conflicts)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		s.Notify("conflicts", pageID)
	}
	return ids, nil
}

// DropConflicts takes back the conflicts HoldConflicts recorded, of a merge
// the server did not take: the merge tried again meets its own.
func (s *Store) DropConflicts(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	err := s.inTx(func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`DELETE FROM conflicts WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Notify("conflicts", "")
	return nil
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
