package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/locales"
)

// Page is a page row.
type Page struct {
	ID        string
	SpaceID   string
	ParentID  string
	FolderID  string
	Title     string
	Icon      string
	SortKey   string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt time.Time
	// KeepOffline keeps the page's content on this device.
	KeepOffline bool
	// Template marks a template, or a subpage of one: a page kept to start
	// others from, out of the page list.
	Template bool
	// Tags are the page's tags, in the order they were added, as
	// NormalizeTags leaves them.
	Tags []string
	// DefaultTemplateID is the template the page's new subpages start
	// from, or "" for blank ones.
	DefaultTemplateID string
}

// DisplayTitle is the title, or "Untitled".
func (p Page) DisplayTitle() string { return TitleOr(p.Title) }

// TitleOr returns a title, or "Untitled" in the app's language for a
// blank one.
func TitleOr(t string) string {
	if strings.TrimSpace(t) == "" {
		return locales.CommonUntitled()
	}
	return t
}

const pageCols = `id, space_id, parent_id, folder_id, title, icon, sort_key, created_by, created_at, updated_at, deleted_at, keep_offline, is_template, tags, default_template_id`

func scanPage(sc scanner) (Page, error) {
	var p Page
	var parent, folder, icon, by, template sql.NullString
	var created, updated int64
	var deleted sql.NullInt64
	var tags string
	err := sc.Scan(&p.ID, &p.SpaceID, &parent, &folder, &p.Title, &icon, &p.SortKey, &by, &created, &updated, &deleted, &p.KeepOffline, &p.Template, &tags, &template)
	p.Tags = decodeTags(tags)
	p.ParentID, p.FolderID, p.Icon, p.CreatedBy = parent.String, folder.String, icon.String, by.String
	p.DefaultTemplateID = template.String
	p.CreatedAt, p.UpdatedAt, p.DeletedAt = time.UnixMilli(created), time.UnixMilli(updated), fromMs(deleted)
	return p, err
}

func (s *Store) queryPages(where string, args ...any) ([]Page, error) {
	return pagesIn(s.db, where, args...)
}

// pagesIn is queryPages in the database or a transaction.
func pagesIn(q querier, where string, args ...any) ([]Page, error) {
	rows, err := q.Query(`SELECT `+pageCols+` FROM pages WHERE `+where, args...) // #nosec G202 -- where is a literal of this package's, its values args.
	return collect(rows, err, scanPage)
}

// Pages returns the pages not deleted, in sort order, without templates.
func (s *Store) Pages() ([]Page, error) {
	return s.queryPages(`deleted_at IS NULL AND is_template = 0 ORDER BY sort_key, id`)
}

// Templates returns the templates not deleted and their subpages, in sort
// order.
func (s *Store) Templates() ([]Page, error) {
	return s.queryPages(`deleted_at IS NULL AND is_template = 1 ORDER BY sort_key, id`)
}

// AllPages returns every page, deleted ones too.
func (s *Store) AllPages() ([]Page, error) { return s.queryPages(`1 = 1 ORDER BY sort_key, id`) }

// Page returns one page, deleted or not.
func (s *Store) Page(id string) (Page, error) { return pageIn(s.db, id) }

func pageIn(q querier, id string) (Page, error) {
	return scanPage(q.QueryRow(`SELECT `+pageCols+` FROM pages WHERE id = ?`, id))
}

// Children returns the pages under a parent ("" for the top level of the
// space), in sort order.
func (s *Store) Children(parentID, spaceID string) ([]Page, error) {
	return childrenIn(s.db, parentID, spaceID)
}

func childrenIn(q querier, parentID, spaceID string) ([]Page, error) {
	if parentID == "" {
		return pagesIn(q, `deleted_at IS NULL AND parent_id IS NULL AND space_id = ? ORDER BY sort_key, id`, spaceID)
	}
	return pagesIn(q, `deleted_at IS NULL AND parent_id = ? ORDER BY sort_key, id`, parentID)
}

func writePage(q querier, p Page) error {
	_, err := q.Exec(`INSERT INTO pages (`+pageCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET space_id = excluded.space_id, parent_id = excluded.parent_id,
		folder_id = excluded.folder_id, title = excluded.title, icon = excluded.icon, sort_key = excluded.sort_key,
		created_by = excluded.created_by, created_at = excluded.created_at, updated_at = excluded.updated_at,
		deleted_at = excluded.deleted_at, keep_offline = excluded.keep_offline, is_template = excluded.is_template,
		tags = excluded.tags, default_template_id = excluded.default_template_id`,
		p.ID, p.SpaceID, ns(p.ParentID), ns(p.FolderID), p.Title, ns(p.Icon), p.SortKey, ns(p.CreatedBy),
		ms(p.CreatedAt), ms(p.UpdatedAt), nt(p.DeletedAt), p.KeepOffline, p.Template, encodeTags(p.Tags), ns(p.DefaultTemplateID))
	return err
}

// savePage stores a page changed here and queues the columns it changed
// to go up, in the caller's transaction: a change without its entry would
// never reach the server, and a retry finding the change stored would not
// queue it either.
func savePage(tx *sql.Tx, p Page, label string) error {
	var old map[string]any
	switch was, err := pageIn(tx, p.ID); {
	case err == nil:
		old = PageToRemote(was)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if err := writePage(tx, p); err != nil {
		return err
	}
	return queueChange(tx, "pages", p.ID, old, PageToRemote(p), label, p.ID)
}

// changePage changes a page as stored, not as the caller last read it,
// in one transaction, as changeFolder does a folder: a change made since
// to another of its fields, here or by a pull, is kept. fn reports
// whether it changed anything.
func (s *Store) changePage(id, label string, fn func(tx *sql.Tx, p *Page) (bool, error)) error {
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		p, err := pageIn(tx, id)
		if err != nil {
			return err
		}
		if changed, err = fn(tx, &p); err != nil || !changed {
			return err
		}
		p.UpdatedAt = time.Now()
		return savePage(tx, p, label)
	})
	if err != nil || !changed {
		return err
	}
	s.Notify("outbox", id)
	s.Notify("pages", id)
	return nil
}

// NewPageOptions describe a page to create.
type NewPageOptions struct {
	ID       string
	ParentID string
	FolderID string
	SpaceID  string
	// AfterID puts the page after that page among its siblings, else last.
	AfterID string
	Title   string
	Icon    string
	Tags    []string
	// Template makes a template; a subpage of one is a template anyway.
	Template bool
	Content  *doc.Content
}

// CreatePage creates a page, last among its siblings unless AfterID says
// where.
func (s *Store) CreatePage(o NewPageOptions) (Page, error) {
	var p Page
	// The page, its content and their entries in the outbox are stored
	// together: a page without its entries would never go to the server.
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		p, err = createPage(tx, s.Session(), o)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	s.Notify("outbox", p.ID)
	s.Notify("pages", p.ID)
	return p, nil
}

// createPage is CreatePage in the caller's transaction, which copying a
// page makes its copies in, all or none.
func createPage(tx *sql.Tx, sess Session, o NewPageOptions) (Page, error) {
	spaceID := o.SpaceID
	template := o.Template
	if o.ParentID != "" {
		if parent, err := pageIn(tx, o.ParentID); err == nil {
			if spaceID == "" {
				spaceID = parent.SpaceID
			}
			// A subpage of a template is part of it.
			template = template || parent.Template
		}
	}
	if spaceID == "" {
		spaceID = sess.SpaceID
	}
	folderID := ""
	if o.ParentID == "" {
		folderID = o.FolderID
	}
	key, err := keyAfter(tx, o.ParentID, spaceID, folderID, o.AfterID, "")
	if err != nil {
		return Page{}, err
	}
	now := time.Now()
	p := Page{
		ID: o.ID, SpaceID: spaceID, ParentID: o.ParentID, FolderID: folderID, Title: o.Title, Icon: o.Icon,
		SortKey: key, CreatedAt: now, UpdatedAt: now, KeepOffline: true, Template: template, Tags: NormalizeTags(o.Tags),
	}
	if p.ID == "" {
		p.ID = doc.NewRowID()
	}
	if sess.UserID != LocalUserID {
		p.CreatedBy = sess.UserID
	}
	content := o.Content
	if content == nil {
		content = doc.Empty()
	}
	body := content.Stamped(nil, now.UnixMilli())
	// The app may have changed accounts since the session was read, as
	// leaflet mcp runs apart from it: the page has nowhere to go then.
	if spaceID != "" && spaceID == sess.SpaceID && !hasSpace(tx, spaceID) {
		return Page{}, ErrNoSpace
	}
	if err := writePage(tx, p); err != nil {
		return Page{}, err
	}
	if err := writeContent(tx, p.ID, body, now, nil); err != nil {
		return Page{}, err
	}
	if err := queue(tx, "pages", p.ID, PageToRemote(p), "new page", p.ID); err != nil {
		return Page{}, err
	}
	return p, queue(tx, "page_content", p.ID, map[string]any{"page_id": p.ID}, "new page", p.ID)
}

// RenamePage changes a page's title.
func (s *Store) RenamePage(id, title string) error {
	return s.changePage(id, "renamed", func(_ *sql.Tx, p *Page) (bool, error) {
		if p.Title == title {
			return false, nil
		}
		p.Title = title
		return true, nil
	})
}

// SetPageDefaultTemplate sets the template a page's new subpages start
// from, "" for blank ones.
func (s *Store) SetPageDefaultTemplate(id, templateID string) error {
	return s.changePage(id, "set default template", func(_ *sql.Tx, p *Page) (bool, error) {
		if p.DefaultTemplateID == templateID {
			return false, nil
		}
		p.DefaultTemplateID = templateID
		return true, nil
	})
}

// DeletePage soft-deletes a page and its subpages, and returns their IDs
// so the delete can be undone.
func (s *Store) DeletePage(id string) ([]string, error) {
	var ids []string
	// The pages are read in the transaction that saves them, so a change
	// made to one meanwhile is kept.
	err := s.inTx(func(tx *sql.Tx) error {
		ids = []string{id}
		seen := map[string]bool{id: true} // a loop of parents is deleted once
		now := time.Now()
		for i := 0; i < len(ids); i++ {
			kids, err := pagesIn(tx, `parent_id = ? AND deleted_at IS NULL`, ids[i])
			if err != nil {
				return err
			}
			for _, k := range kids {
				if !seen[k.ID] {
					seen[k.ID] = true
					ids = append(ids, k.ID)
				}
			}
			p, err := pageIn(tx, ids[i])
			if errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return err
			}
			p.DeletedAt, p.UpdatedAt = now, now
			if err := savePage(tx, p, "deleted"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, pid := range ids {
		s.Notify("outbox", pid)
	}
	s.Notify("pages", id)
	return ids, nil
}

// RestorePages undoes a delete.
func (s *Store) RestorePages(ids []string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		now := time.Now()
		for _, pid := range ids {
			p, err := pageIn(tx, pid)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return err
			}
			p.DeletedAt, p.UpdatedAt = time.Time{}, now
			if err := savePage(tx, p, "restored"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, pid := range ids {
		s.Notify("outbox", pid)
	}
	s.Notify("pages", "")
	return nil
}

// SetKeepOffline keeps a page's content on this device, or not.
func (s *Store) SetKeepOffline(id string, keep bool) error {
	if _, err := s.db.Exec(`UPDATE pages SET keep_offline = ? WHERE id = ?`, keep, id); err != nil {
		return err
	}
	s.Notify("pages", id)
	return nil
}
