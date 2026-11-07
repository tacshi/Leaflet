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

// MembersOrNil returns a page's members, or nil where they cannot be
// read, for those to whom none is the safe answer, as to CanEdit. Whether
// someone else has a page is read with Members: members unread do not say
// that no one has it.
func (s *Store) MembersOrNil(pageID string) []Member {
	m, _ := s.Members(pageID)
	return m
}

// SharedBeyond returns the pages with a member other than userID, all at
// once, as Members would list them a page at a time.
func (s *Store) SharedBeyond(userID string) (map[string]bool, error) {
	return pageSet(s.db.Query(`SELECT DISTINCT page_id FROM page_members WHERE user_id != ?`, userID))
}

// pageSet is the page IDs rows has, one a row.
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
	return canEdit(s.Session(), p, s.MembersOrNil, func(id string) (Page, bool) {
		p, err := s.Page(id)
		return p, err == nil
	})
}

// canEdit is CanEdit for sess, with each page's members and pages by ID
// from members and page.
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

// AllMembers returns the members kept of every page that has any, by
// page, in one read.
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

// Tree is every page, deleted ones too, with where it is and its members,
// as one read had them: it tells of many pages at once whether the user
// may change them (CanEdit) and which is inside which (IsDescendant), with
// no read of each page's parents and members, which a library of
// thousands of pages would take a read each for.
type Tree struct {
	sess    Session
	pages   map[string]Page
	members map[string][]Member
}

// Tree reads the pages and their members, for questions about many of
// them at once.
func (s *Store) Tree() (*Tree, error) {
	all, err := s.AllPages()
	if err != nil {
		return nil, err
	}
	return s.TreeOf(all)
}

// TreeOf is Tree for every page, deleted ones too, as all, read already:
// it reads their members.
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

// Members are a page's members, as the tree read them.
func (t *Tree) Members(pageID string) []Member { return t.members[pageID] }

// Parent is the page a page is under, "" for one at the top level or not
// there.
func (t *Tree) Parent(pageID string) string { return t.pages[pageID].ParentID }

// CanEdit is Store.CanEdit, as the tree has the pages.
func (t *Tree) CanEdit(p Page) bool {
	return canEdit(t.sess, p, t.Members, func(id string) (Page, bool) {
		p, ok := t.pages[id]
		return p, ok
	})
}

// IsDescendant is Store.IsDescendant, as the tree has the pages.
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
