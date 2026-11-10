package store

import (
	"cmp"
	"database/sql"
	"errors"
	"slices"
)

func (s *Store) IsDescendant(candidate, of string) bool {
	under, err := isDescendant(s.db, candidate, of)
	return under || err != nil
}

func isDescendant(q querier, candidate, of string) (bool, error) {
	seen := map[string]bool{}
	for cursor := candidate; cursor != "" && !seen[cursor]; {
		if cursor == of {
			return true, nil
		}
		seen[cursor] = true
		p, err := pageIn(q, cursor)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		cursor = p.ParentID
	}
	return false, nil
}

// SettlePageLoops puts one page of every loop of pages at the top level, on this device only: a page of the loop whose
// change has yet to sync, else the smallest id, so every device picks the same. Call it once all the rows of a pull
// are in: between two rows of one change the pages can pass through a loop.
func (s *Store) SettlePageLoops() error {
	settled := false
	err := s.inTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, parent_id FROM pages WHERE parent_id IS NOT NULL`)
		up := map[string]string{}
		err = each(rows, err, func(sc scanner) (bool, error) {
			var id, parent string
			err := sc.Scan(&id, &parent)
			up[id] = parent
			return true, err
		})
		if err != nil {
			return err
		}
		for _, loop := range loopsOf(up) {
			slices.Sort(loop)
			detach := loop[0]
			for _, id := range loop {
				if waits, err := pending(tx, "pages", id); err != nil {
					return err
				} else if waits {
					detach = id
					break
				}
			}
			if _, err := tx.Exec(`UPDATE pages SET parent_id = NULL, folder_id = NULL WHERE id = ?`, detach); err != nil {
				return err
			}
			settled = true
		}
		return nil
	})
	if err == nil && settled {
		s.Notify("pages", "")
	}
	return err
}

var ErrMoveIntoSelf = errors.New("a page cannot move inside itself")

var ErrMoveAcrossSpaces = errors.New("a page cannot move into another space")

var ErrMoveIntoDeleted = errors.New("a page cannot move inside a deleted page")

func (s *Store) MovePage(id, parentID, folderID, afterID string) error {
	return s.movePage(id, parentID, folderID, func(q querier, spaceID, folderID string) (string, error) {
		return keyAfter(q, parentID, spaceID, folderID, afterID, id)
	})
}

func (s *Store) MovePageBefore(id, parentID, folderID, beforeID string) error {
	return s.movePage(id, parentID, folderID, func(q querier, spaceID, folderID string) (string, error) {
		return keyBefore(q, parentID, spaceID, folderID, beforeID, id)
	})
}

func (s *Store) PlacePage(id, parentID, folderID, sortKey string) error {
	return s.movePage(id, parentID, folderID, func(q querier, spaceID, folderID string) (string, error) {
		if sortKey != "" {
			return sortKey, nil
		}
		return keyAfter(q, parentID, spaceID, folderID, "", id)
	})
}

// movePage puts the page under parentID, or else in folderID, at the sort key that key gives for the folder it ends
// in. A deleted folder leaves the page at the top level, where it would be shown anyway. A folder with no row here is
// kept: it may have yet to be pulled, and the folders of a page shared with this account never are.
func (s *Store) movePage(id, parentID, folderID string, key func(q querier, spaceID, folderID string) (string, error)) error {
	return s.changePage(id, "moved", func(tx *sql.Tx, p *Page) (bool, error) {
		folder := folderID
		if parentID != "" {
			if under, err := isDescendant(tx, parentID, id); err != nil || under {
				return false, cmp.Or(err, ErrMoveIntoSelf)
			}
			switch parent, err := pageIn(tx, parentID); {
			case err == nil && parent.SpaceID != p.SpaceID:
				return false, ErrMoveAcrossSpaces
			case err == nil && !parent.DeletedAt.IsZero():
				return false, ErrMoveIntoDeleted
			case err != nil && !errors.Is(err, sql.ErrNoRows):
				return false, err
			}
			folder = ""
		}
		if folder != "" {
			switch f, err := folderIn(tx, folder); {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return false, err
			case f.SpaceID != p.SpaceID:
				return false, ErrMoveAcrossSpaces
			case !f.DeletedAt.IsZero():
				folder = ""
			}
		}
		k, err := key(tx, p.SpaceID, folder)
		if err != nil {
			return false, err
		}
		p.ParentID, p.FolderID, p.SortKey = parentID, folder, k
		return true, nil
	})
}

func keyAfter(q querier, parentID, spaceID, folderID, afterID, skip string) (string, error) {
	siblings, err := siblings(q, parentID, spaceID, folderID, skip)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(siblings, func(p Page) bool { return p.ID == afterID })
	if afterID == "" || i < 0 {
		i = len(siblings) - 1
	}
	before, after := "", ""
	if i >= 0 {
		before = siblings[i].SortKey
		if i+1 < len(siblings) {
			after = siblings[i+1].SortKey
		}
	}
	if before != "" && after != "" && before >= after {
		after = ""
	}
	return NewKey(before, after), nil
}

func keyBefore(q querier, parentID, spaceID, folderID, beforeID, skip string) (string, error) {
	siblings, err := siblings(q, parentID, spaceID, folderID, skip)
	if err != nil {
		return "", err
	}
	before, after := "", ""
	for i, q := range siblings {
		if q.ID == beforeID {
			after = q.SortKey
			if i > 0 {
				before = siblings[i-1].SortKey
			}
		}
	}
	if after == "" && len(siblings) > 0 {
		after = siblings[0].SortKey
	}
	if before != "" && after != "" && before >= after {
		before = ""
	}
	return NewKey(before, after), nil
}

func siblings(q querier, parentID, spaceID, folderID, skip string) ([]Page, error) {
	if parentID != "" {
		all, err := childrenIn(q, parentID, spaceID)
		return slices.DeleteFunc(all, func(p Page) bool { return p.ID == skip }), err
	}
	all, err := pagesIn(q, `deleted_at IS NULL AND space_id = ? AND (parent_id IS NULL OR NOT EXISTS
		(SELECT 1 FROM pages parent WHERE parent.id = pages.parent_id AND parent.deleted_at IS NULL))
		ORDER BY sort_key, id`, spaceID)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT id FROM folders WHERE deleted_at IS NULL AND space_id = ?`, spaceID)
	folders, err := collect(rows, err, scanString)
	if err != nil {
		return nil, err
	}
	shown := func(folder string) string {
		if slices.Contains(folders, folder) {
			return folder
		}
		return ""
	}
	folderID = shown(folderID)
	return slices.DeleteFunc(all, func(p Page) bool { return p.ID == skip || shown(p.FolderID) != folderID }), nil
}
