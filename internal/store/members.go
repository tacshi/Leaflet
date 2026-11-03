package store

import (
	"database/sql"
)

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
	return collect(rows, err, func(sc scanner) (Member, error) {
		var m Member
		err := sc.Scan(&m.PageID, &m.UserID, &m.Role, &m.DisplayName, &m.Email, &m.Accepted)
		return m, err
	})
}

// MembersOrNil returns a page's members, or nil.
func (s *Store) MembersOrNil(pageID string) []Member {
	m, _ := s.Members(pageID)
	return m
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

// CanEdit reports whether the user may change a page: their own, or one
// shared with them as an editor. The members of the nearest page with the
// user among them say, it or one it is inside: the server gives each page
// the roles of the pages above it too. A shared page is read-only until
// members have loaded for it or a page it is inside.
func (s *Store) CanEdit(p Page) bool {
	sess := s.Session()
	if p.SpaceID == sess.SpaceID {
		return true
	}
	seen := map[string]bool{}
	for !seen[p.ID] {
		seen[p.ID] = true
		for _, m := range s.MembersOrNil(p.ID) {
			if m.UserID == sess.UserID {
				return m.Role == "editor" || m.Role == "owner"
			}
		}
		parent, err := s.Page(p.ParentID)
		if p.ParentID == "" || err != nil {
			return false
		}
		p = parent
	}
	return false
}
