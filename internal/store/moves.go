package store

import (
	"cmp"
	"database/sql"
	"errors"
	"slices"
)

// IsDescendant reports whether candidate is page of or one under it. Pages
// in a loop of parents, which sync breaks, are under none outside it. One
// whose parents cannot be read counts as under it, so nothing moves into
// itself.
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

// ParentLoop returns the pages of the loop that putting page id under
// parentID would close, id first, or nil when it closes none.
func (s *Store) ParentLoop(id, parentID string) []string {
	loop := []string{id}
	seen := map[string]bool{id: true}
	for cursor := parentID; cursor != ""; {
		if cursor == id {
			return loop
		}
		if seen[cursor] {
			return nil // a loop above, not through id
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

// DetachPage puts a page at the top level here, without queueing it or
// changing when it changed: sync uses it to break a loop of parents the
// same way on every device.
func (s *Store) DetachPage(id string) error {
	_, err := s.db.Exec(`UPDATE pages SET parent_id = NULL, folder_id = NULL WHERE id = ?`, id)
	return err
}

// ErrMoveIntoSelf is returned for a move under the page itself.
var ErrMoveIntoSelf = errors.New("a page cannot move inside itself")

// ErrMoveAcrossSpaces is returned for a move under a page, or into a
// folder, of another space: a page stays in the space it was made in,
// and the server would refuse the move every time it went up.
var ErrMoveAcrossSpaces = errors.New("a page cannot move into another space")

// MovePage moves a page under parentID (or to the top level, into
// folderID), after afterID among its new siblings, or last.
func (s *Store) MovePage(id, parentID, folderID, afterID string) error {
	return s.movePage(id, parentID, folderID, func(q querier, spaceID string) (string, error) {
		return keyAfter(q, parentID, spaceID, folderID, afterID, id)
	})
}

// MovePageBefore moves a page under parentID (or to the top level, into
// folderID), before beforeID among its new siblings, or first.
func (s *Store) MovePageBefore(id, parentID, folderID, beforeID string) error {
	return s.movePage(id, parentID, folderID, func(q querier, spaceID string) (string, error) {
		return keyBefore(q, parentID, spaceID, folderID, beforeID, id)
	})
}

// movePage moves a page under parentID or into folderID, at the sort key
// key gives for its space, reading the page and its new siblings in the
// transaction that saves it.
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

// keyAfter is a sort key for a page put after afterID among the pages
// under parentID, or the top-level pages of folderID, or last when afterID
// is "" or not among them: a key between nothing and nothing is the one
// rows from the server have, which would sort it anywhere among them.
// skip, the page being put, is left out of them.
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

// keyBefore is a sort key for a page put before beforeID among the pages
// keyAfter takes, or first when beforeID is "" or not among them.
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

// siblings are the pages under parentID, or the top-level pages of
// folderID, in order, but skip.
func siblings(q querier, parentID, spaceID, folderID, skip string) ([]Page, error) {
	all, err := childrenIn(q, parentID, spaceID)
	if err != nil {
		return nil, err
	}
	var out []Page
	for _, q := range all {
		if q.ID != skip && (parentID != "" || q.FolderID == folderID) {
			out = append(out, q)
		}
	}
	return out, nil
}
