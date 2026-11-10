package store

import (
	"database/sql"
)

type Member struct {
	PageID      string
	UserID      string
	Role        string
	DisplayName string
	Email       string
	Accepted    bool
}

func (m Member) Name() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Email
}

func (s *Store) Members(pageID string) ([]Member, error) {
	rows, err := s.db.Query(`SELECT page_id, user_id, role, display_name, email, accepted FROM page_members WHERE page_id = ? ORDER BY display_name`, pageID)
	return collect(rows, err, func(sc scanner) (Member, error) {
		var m Member
		err := sc.Scan(&m.PageID, &m.UserID, &m.Role, &m.DisplayName, &m.Email, &m.Accepted)
		return m, err
	})
}

func (s *Store) MembersOrNil(pageID string) []Member {
	m, _ := s.Members(pageID)
	return m
}

func (s *Store) SharedBeyond(userID string) (map[string]bool, error) {
	return pageSet(s.db.Query(`SELECT DISTINCT page_id FROM page_members WHERE user_id != ?`, userID))
}

func pageSet(rows *sql.Rows, err error) (map[string]bool, error) {
	ids, err := collect(rows, err, func(sc scanner) (string, error) {
		var id string
		err := sc.Scan(&id)
		return id, err
	})
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, err
}

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

func (s *Store) CanEdit(p Page) bool {
	return canEdit(s.Session(), p, s.MembersOrNil, func(id string) (Page, bool) {
		p, err := s.Page(id)
		return p, err == nil
	})
}

func canEdit(sess Session, p Page, members func(pageID string) []Member, page func(id string) (Page, bool)) bool {
	if p.SpaceID == sess.SpaceID {
		return true
	}
	seen := map[string]bool{}
	for !seen[p.ID] {
		seen[p.ID] = true
		for _, m := range members(p.ID) {
			if m.UserID == sess.UserID {
				return m.Role == "editor" || m.Role == "owner"
			}
		}
		if p.ParentID == "" {
			return false
		}
		parent, ok := page(p.ParentID)
		if !ok {
			return false
		}
		p = parent
	}
	return false
}

func (s *Store) AllMembers() (map[string][]Member, error) {
	rows, err := s.db.Query(`SELECT page_id, user_id, role, display_name, email, accepted FROM page_members ORDER BY page_id, display_name`)
	out := map[string][]Member{}
	err = each(rows, err, func(sc scanner) (bool, error) {
		var m Member
		err := sc.Scan(&m.PageID, &m.UserID, &m.Role, &m.DisplayName, &m.Email, &m.Accepted)
		out[m.PageID] = append(out[m.PageID], m)
		return true, err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type Tree struct {
	sess    Session
	pages   map[string]Page
	members map[string][]Member
}

func (s *Store) Tree() (*Tree, error) {
	all, err := s.AllPages()
	if err != nil {
		return nil, err
	}
	return s.TreeOf(all)
}

func (s *Store) TreeOf(all []Page) (*Tree, error) {
	members, err := s.AllMembers()
	if err != nil {
		return nil, err
	}
	t := &Tree{sess: s.Session(), pages: make(map[string]Page, len(all)), members: members}
	for _, p := range all {
		t.pages[p.ID] = p
	}
	return t, nil
}

func (t *Tree) Members(pageID string) []Member { return t.members[pageID] }

func (t *Tree) Parent(pageID string) string { return t.pages[pageID].ParentID }

func (t *Tree) CanEdit(p Page) bool {
	return canEdit(t.sess, p, t.Members, func(id string) (Page, bool) {
		p, ok := t.pages[id]
		return p, ok
	})
}

func (t *Tree) IsDescendant(candidate, of string) bool {
	seen := map[string]bool{}
	for cursor := candidate; cursor != "" && !seen[cursor]; {
		if cursor == of {
			return true
		}
		seen[cursor] = true
		p, ok := t.pages[cursor]
		if !ok {
			return false
		}
		cursor = p.ParentID
	}
	return false
}
