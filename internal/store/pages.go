package store

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/locales"
)

type Page struct {
	ID                string
	SpaceID           string
	ParentID          string
	FolderID          string
	Title             string
	Icon              string
	SortKey           string
	CreatedBy         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         time.Time
	KeepOffline       bool
	Template          bool
	Tags              []string
	DefaultTemplateID string
}

func (p Page) DisplayTitle() string { return TitleOr(p.Title) }

func TitleOr(t string) string {
	if strings.TrimSpace(t) == "" {
		return locales.CommonUntitled()
	}
	return t
}

const maxTitleLen = 2000

func ClampTitle(title string) string { return clampChars(title, maxTitleLen) }

func clampChars(text string, limit int) string {
	n := 0
	for i := range text {
		if n == limit {
			return text[:i]
		}
		n++
	}
	return text
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

func pagesIn(q querier, where string, args ...any) ([]Page, error) {
	rows, err := q.Query(`SELECT `+pageCols+` FROM pages WHERE `+where, args...) // #nosec G202
	return collect(rows, err, scanPage)
}

func (s *Store) Pages() ([]Page, error) {
	return s.queryPages(`deleted_at IS NULL AND is_template = 0 ORDER BY sort_key, id`)
}

func (s *Store) Templates() ([]Page, error) {
	return s.queryPages(`deleted_at IS NULL AND is_template = 1 ORDER BY sort_key, id`)
}

func (s *Store) AllPages() ([]Page, error) { return s.queryPages(`1 = 1 ORDER BY sort_key, id`) }

func (s *Store) Page(id string) (Page, error) { return pageIn(s.db, id) }

func pageIn(q querier, id string) (Page, error) {
	return scanPage(q.QueryRow(`SELECT `+pageCols+` FROM pages WHERE id = ?`, id))
}

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

type NewPageOptions struct {
	ID       string
	ParentID string
	FolderID string
	SpaceID  string
	AfterID  string
	Title    string
	Icon     string
	Tags     []string
	Template bool
	Content  *doc.Content
}

func (s *Store) CreatePage(o NewPageOptions) (Page, error) {
	var p Page
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

func createPage(tx *sql.Tx, sess Session, o NewPageOptions) (Page, error) {
	spaceID := o.SpaceID
	template := o.Template
	if o.ParentID != "" {
		if parent, err := pageIn(tx, o.ParentID); err == nil {
			if spaceID == "" {
				spaceID = parent.SpaceID
			}
			template = template || parent.Template
		}
	}
	if spaceID == "" {
		spaceID = sess.SpaceID
	}
	folderID := ""
	if o.ParentID == "" && o.FolderID != "" {
		// As when a page is moved: a deleted folder leaves it at the top level, one with no row here is kept.
		switch f, err := folderIn(tx, o.FolderID); {
		case errors.Is(err, sql.ErrNoRows) || err == nil && f.DeletedAt.IsZero():
			folderID = o.FolderID
		case err != nil:
			return Page{}, err
		}
	}
	key, err := keyAfter(tx, o.ParentID, spaceID, folderID, o.AfterID, "")
	if err != nil {
		return Page{}, err
	}
	now := time.Now()
	p := Page{
		ID: o.ID, SpaceID: spaceID, ParentID: o.ParentID, FolderID: folderID, Title: ClampTitle(o.Title), Icon: o.Icon,
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
	if spaceID != "" && spaceID == sess.SpaceID && !hasSpace(tx, spaceID) {
		return Page{}, ErrNoSpace
	}
	if err := writePage(tx, p); err != nil {
		return Page{}, err
	}
	if err := writeContent(tx, p.ID, body, now); err != nil {
		return Page{}, err
	}
	if err := queue(tx, "pages", p.ID, PageToRemote(p), "new page", p.ID); err != nil {
		return Page{}, err
	}
	return p, queue(tx, "page_content", p.ID, map[string]any{"page_id": p.ID}, "new page", p.ID)
}

func (s *Store) RenamePage(id, title string) error {
	title = ClampTitle(title)
	return s.changePage(id, "renamed", func(_ *sql.Tx, p *Page) (bool, error) {
		if p.Title == title {
			return false, nil
		}
		p.Title = title
		return true, nil
	})
}

func (s *Store) SetPageDefaultTemplate(id, templateID string) error {
	return s.changePage(id, "set default template", func(_ *sql.Tx, p *Page) (bool, error) {
		if p.DefaultTemplateID == templateID {
			return false, nil
		}
		p.DefaultTemplateID = templateID
		return true, nil
	})
}

func (s *Store) DeletePage(id string) ([]string, error) {
	var ids []string
	err := s.inTx(func(tx *sql.Tx) error {
		ids = []string{id}
		seen := map[string]bool{id: true}
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

func (s *Store) RestorePages(ids []string) error {
	own := s.Session().SpaceID
	err := s.inTx(func(tx *sql.Tx) error {
		now := time.Now()
		for _, pid := range ids {
			p, err := pageIn(tx, pid)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return err
			}
			if p.SpaceID == own && p.ParentID != "" && !slices.Contains(ids, p.ParentID) {
				switch parent, err := pageIn(tx, p.ParentID); {
				case err == nil && !parent.DeletedAt.IsZero():
					key, err := keyAfter(tx, "", p.SpaceID, "", "", p.ID)
					if err != nil {
						return err
					}
					p.ParentID, p.FolderID, p.SortKey = "", "", key
				case err != nil && !errors.Is(err, sql.ErrNoRows):
					return err
				}
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

func (s *Store) SetKeepOffline(id string, keep bool) error {
	if _, err := s.db.Exec(`UPDATE pages SET keep_offline = ? WHERE id = ?`, keep, id); err != nil {
		return err
	}
	s.Notify("pages", id)
	return nil
}
