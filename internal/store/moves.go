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

func (s *Store) ParentLoop(id, parentID string) []string {
	loop := []string{id}
	seen := map[string]bool{id: true}
	for cursor := parentID; cursor != ""; {
		if cursor == id {
			return loop
		}
		if seen[cursor] {
			return nil
		}
		seen[cursor] = true
		loop = append(loop, cursor)
		p, err := s.Page(cursor)
		if err != nil {
			return nil
		}
		cursor = p.ParentID
	}
	return nil
}

func (s *Store) DetachPage(id string) error {
	_, err := s.db.Exec(`UPDATE pages SET parent_id = NULL, folder_id = NULL WHERE id = ?`, id)
	return err
}

var ErrMoveIntoSelf = errors.New("a page cannot move inside itself")

var ErrMoveAcrossSpaces = errors.New("a page cannot move into another space")

var ErrMoveIntoDeleted = errors.New("a page cannot move inside a deleted page")

func (s *Store) MovePage(id, parentID, folderID, afterID string) error {
	return s.movePage(id, parentID, folderID, func(q querier, spaceID string) (string, error) {
		return keyAfter(q, parentID, spaceID, folderID, afterID, id)
	})
}

func (s *Store) MovePageBefore(id, parentID, folderID, beforeID string) error {
	return s.movePage(id, parentID, folderID, func(q querier, spaceID string) (string, error) {
		return keyBefore(q, parentID, spaceID, folderID, beforeID, id)
	})
}

func (s *Store) PlacePage(id, parentID, folderID, sortKey string) error {
	return s.movePage(id, parentID, folderID, func(q querier, spaceID string) (string, error) {
		if sortKey != "" {
			return sortKey, nil
		}
		return keyAfter(q, parentID, spaceID, folderID, "", id)
	})
}

func (s *Store) movePage(id, parentID, folderID string, key func(q querier, spaceID string) (string, error)) error {
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
			case err == nil && f.SpaceID != p.SpaceID:
				return false, ErrMoveAcrossSpaces
			case err != nil && !errors.Is(err, sql.ErrNoRows):
				return false, err
			}
		}
		k, err := key(tx, p.SpaceID)
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
