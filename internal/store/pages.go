package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
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

func scanPage(sc interface{ Scan(...any) error }) (Page, error) {
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
	rows, err := q.Query(`SELECT `+pageCols+` FROM pages WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Pages returns the pages not deleted, in sort order, without templates.
func (s *Store) Pages() ([]Page, error) {
	return s.queryPages(`deleted_at IS NULL AND is_template = 0 ORDER BY sort_key`)
}

// Templates returns the templates not deleted and their subpages, in sort
// order.
func (s *Store) Templates() ([]Page, error) {
	return s.queryPages(`deleted_at IS NULL AND is_template = 1 ORDER BY sort_key`)
}

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
		return pagesIn(q, `deleted_at IS NULL AND parent_id IS NULL AND space_id = ? ORDER BY sort_key`, spaceID)
	}
	return pagesIn(q, `deleted_at IS NULL AND parent_id = ? ORDER BY sort_key`, parentID)
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
	switch was, err := scanPage(tx.QueryRow(`SELECT `+pageCols+` FROM pages WHERE id = ?`, p.ID)); {
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
	sess := s.Session()
	spaceID := o.SpaceID
	template := o.Template
	if o.ParentID != "" {
		if parent, err := s.Page(o.ParentID); err == nil {
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
	key, err := keyAfter(s.db, o.ParentID, spaceID, folderID, o.AfterID, "")
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
	// The page, its content and their entries in the outbox are stored
	// together: a page without its entries would never go to the server.
	err = s.inTx(func(tx *sql.Tx) error {
		// The app may have changed accounts since the session was read, as
		// leaflet mcp runs apart from it: the page has nowhere to go then.
		if spaceID != "" && spaceID == sess.SpaceID && !hasSpace(tx, spaceID) {
			return ErrNoSpace
		}
		if err := writePage(tx, p); err != nil {
			return err
		}
		if err := writeContent(tx, p.ID, body, now, nil); err != nil {
			return err
		}
		if err := queue(tx, "pages", p.ID, PageToRemote(p), "new page", p.ID); err != nil {
			return err
		}
		return queue(tx, "page_content", p.ID, map[string]any{"page_id": p.ID}, "new page", p.ID)
	})
	if err != nil {
		return Page{}, err
	}
	s.Notify("outbox", p.ID)
	s.Notify("pages", p.ID)
	return p, nil
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

// Tags are at most maxTags to a page, each at most maxTagLen characters,
// as the server's pages table checks.
const (
	maxTags   = 50
	maxTagLen = 64
)

// NormalizeTags tidies tags as people type them: each trimmed, without a
// leading #, its spaces one, cut to maxTagLen; the blank ones and those
// repeated, in any case, left out, the first spelling kept, in order.
func NormalizeTags(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.Join(strings.Fields(strings.TrimLeft(strings.TrimSpace(t), "#")), " ")
		if r := []rune(t); len(r) > maxTagLen {
			t = strings.TrimSpace(string(r[:maxTagLen]))
		}
		k := strings.ToLower(t)
		if t == "" || seen[k] || len(out) == maxTags {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

func decodeTags(s string) []string {
	var tags []string
	_ = json.Unmarshal([]byte(s), &tags)
	if tags == nil {
		tags = []string{}
	}
	return tags
}

func encodeTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

// HasTag reports whether a page has a tag, in any case.
func (p Page) HasTag(tag string) bool {
	for _, t := range p.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// SetPageTags changes a page's tags.
func (s *Store) SetPageTags(id string, tags []string) error {
	tags = NormalizeTags(tags)
	return s.changePage(id, "tagged", func(_ *sql.Tx, p *Page) (bool, error) {
		if slices.Equal(p.Tags, tags) {
			return false, nil
		}
		p.Tags = tags
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

// TagCount is a tag and how many pages have it.
type TagCount struct {
	Name  string
	Count int
}

// Tags returns the tags of the pages, without templates, by name: a tag
// spelled differently on two pages is one, as the first page spells it.
func (s *Store) Tags() ([]TagCount, error) {
	pages, err := s.Pages()
	if err != nil {
		return nil, err
	}
	return TagsOf(pages), nil
}

// TagsOf counts the tags of pages, by name.
func TagsOf(pages []Page) []TagCount {
	at := map[string]int{}
	var out []TagCount
	for _, p := range pages {
		for _, t := range p.Tags {
			k := strings.ToLower(t)
			if i, ok := at[k]; ok {
				out[i].Count++
				continue
			}
			at[k] = len(out)
			out = append(out, TagCount{Name: t, Count: 1})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// RenameTag renames a tag on every page the user may change, templates
// too, merging it into a tag of the new name a page has already. It
// returns the pages changed, with the tags each had before.
func (s *Store) RenameTag(old, name string) (map[string][]string, error) {
	return s.retag(old, func(t string) string { return name })
}

// DeleteTag takes a tag off every page the user may change, templates
// too, and returns the pages changed, with the tags each had before.
func (s *Store) DeleteTag(tag string) (map[string][]string, error) {
	return s.retag(tag, func(string) string { return "" })
}

// retag changes tag on every page the user may change that has it, to
// what to says ("" takes it off), and returns the pages changed with
// their tags before.
func (s *Store) retag(tag string, to func(string) string) (map[string][]string, error) {
	pages, err := s.queryPages(`deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	// Which pages the user may change is read first, as CanEdit reads the
	// database, which the transaction holds; the pages are read again in
	// it, so a change made to them since is kept.
	var ids []string
	for _, p := range pages {
		if p.HasTag(tag) && s.CanEdit(p) {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	before := map[string][]string{}
	now := time.Now()
	err = s.inTx(func(tx *sql.Tx) error {
		for _, id := range ids {
			p, err := pageIn(tx, id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return err
			}
			if !p.DeletedAt.IsZero() || !p.HasTag(tag) {
				continue
			}
			tags := make([]string, 0, len(p.Tags))
			for _, t := range p.Tags {
				if strings.EqualFold(t, tag) {
					t = to(t)
				}
				tags = append(tags, t)
			}
			if tags = NormalizeTags(tags); slices.Equal(tags, p.Tags) {
				continue
			}
			before[p.ID] = p.Tags
			p.Tags, p.UpdatedAt = tags, now
			if err := savePage(tx, p, "tagged"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(before) == 0 {
		return nil, err
	}
	for id := range before {
		s.Notify("outbox", id)
	}
	s.Notify("pages", "")
	return before, nil
}

// IsDescendant reports whether candidate is page of or one under it. Pages
// in a loop of parents, which sync breaks, are under none outside it.
func (s *Store) IsDescendant(candidate, of string) bool { return isDescendant(s.db, candidate, of) }

func isDescendant(q querier, candidate, of string) bool {
	seen := map[string]bool{}
	for cursor := candidate; cursor != "" && !seen[cursor]; {
		if cursor == of {
			return true
		}
		seen[cursor] = true
		p, err := pageIn(q, cursor)
		if err != nil {
			return false
		}
		cursor = p.ParentID
	}
	return false
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
		if parentID != "" && isDescendant(tx, parentID, id) {
			return false, ErrMoveIntoSelf
		}
		k, err := key(tx, p.SpaceID)
		if err != nil {
			return false, err
		}
		folder := folderID
		if parentID != "" {
			if parent, err := pageIn(tx, parentID); err == nil {
				p.SpaceID = parent.SpaceID
			}
			folder = ""
		}
		p.ParentID, p.FolderID, p.SortKey = parentID, folder, k
		return true, nil
	})
}

// keyAfter is a sort key for a page put after afterID among the pages
// under parentID, or the top-level pages of folderID, or last when afterID
// is "". skip, the page being put, is left out of them.
func keyAfter(q querier, parentID, spaceID, folderID, afterID, skip string) (string, error) {
	siblings, err := siblings(q, parentID, spaceID, folderID, skip)
	if err != nil {
		return "", err
	}
	before, after := "", ""
	if afterID == "" {
		if len(siblings) > 0 {
			before = siblings[len(siblings)-1].SortKey
		}
	} else {
		for i, q := range siblings {
			if q.ID == afterID {
				before = q.SortKey
				if i+1 < len(siblings) {
					after = siblings[i+1].SortKey
				}
			}
		}
	}
	if before != "" && after != "" && before >= after {
		after = ""
	}
	return KeyBetween(before, after), nil
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
	return KeyBetween(before, after), nil
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

// CopyOptions say where a copy of a page goes, as NewPageOptions do.
type CopyOptions struct {
	ParentID string
	FolderID string
	SpaceID  string
	AfterID  string
	Title    string
	// Template makes the copy and its subpages templates; else they are
	// pages.
	Template bool
}

// CopyPage copies a page and its subpages, which keep their titles and
// order, and returns the copy. Links among them go to the copies.
func (s *Store) CopyPage(id string, o CopyOptions) (Page, error) {
	plan, err := s.copyPlan(id, doc.NewRowID())
	if err != nil {
		return Page{}, err
	}
	tree, ids, contents := plan.tree, plan.ids, plan.contents
	p, err := s.CreatePage(NewPageOptions{ID: ids[id], ParentID: o.ParentID, FolderID: o.FolderID, SpaceID: o.SpaceID,
		AfterID: o.AfterID, Title: o.Title, Icon: tree[0].Icon, Tags: tree[0].Tags, Template: o.Template, Content: contents[0]})
	if err != nil {
		return Page{}, err
	}
	if err := s.createCopies(tree[1:], ids, contents[1:], o.Template); err != nil {
		return Page{}, err
	}
	return p, s.putCopies(plan)
}

// CopyInto copies page id's subpages under page into, and returns a copy
// of id's content for into to hold, as when a page starts from a
// template. Links among them go to the copies.
func (s *Store) CopyInto(id, into string) (*doc.Content, error) {
	plan, err := s.copyPlan(id, into)
	if err != nil {
		return nil, err
	}
	tree, ids, contents := plan.tree, plan.ids, plan.contents
	target, err := s.Page(into)
	if err != nil {
		return nil, err
	}
	// The page takes the template's tags too, after its own.
	if tags := NormalizeTags(append(append([]string{}, target.Tags...), tree[0].Tags...)); len(tags) != len(target.Tags) {
		if err := s.SetPageTags(into, tags); err != nil {
			return nil, err
		}
	}
	if err := s.createCopies(tree[1:], ids, contents[1:], target.Template); err != nil {
		return nil, err
	}
	return contents[0], s.putCopies(plan)
}

// pageCopies is what copyPlan plans: the pages, parents before their
// subpages, the IDs of their copies, the copies' contents, and the
// recordings and files the copies get of their own.
type pageCopies struct {
	tree     []Page
	ids      map[string]string
	contents []*doc.Content
	recs     []recordingCopy
	files    []fileCopy
}

// copyPlan reads page id and the pages under it, parents before their
// subpages, and gives each the ID of its copy (copyID for id) and its
// content with links among them going to the copies, recording blocks
// to recordings of their own, and blocks of the page's files to files of
// their own, which putCopies stores once the copies are in.
func (s *Store) copyPlan(id, copyID string) (pageCopies, error) {
	root, err := s.Page(id)
	if err != nil {
		return pageCopies{}, err
	}
	tree := []Page{root}
	seen := map[string]bool{id: true} // a loop of parents is copied once
	for i := 0; i < len(tree); i++ {
		kids, err := s.queryPages(`parent_id = ? AND deleted_at IS NULL ORDER BY sort_key`, tree[i].ID)
		if err != nil {
			return pageCopies{}, err
		}
		for _, k := range kids {
			if !seen[k.ID] {
				seen[k.ID] = true
				tree = append(tree, k)
			}
		}
	}
	plan := pageCopies{tree: tree, ids: map[string]string{id: copyID}, contents: make([]*doc.Content, len(tree))}
	for _, p := range tree[1:] {
		plan.ids[p.ID] = doc.NewRowID()
	}
	for i, p := range tree {
		row, err := s.ContentRow(p.ID)
		if IsNotFound(err) {
			return pageCopies{}, fmt.Errorf("%s is not on this device yet", p.DisplayTitle())
		} else if err != nil {
			return pageCopies{}, err
		}
		// Content that cannot be read is not copied as an empty page.
		c, err := doc.Decode(row.Content)
		if err != nil {
			return pageCopies{}, fmt.Errorf("%s: %w", p.DisplayTitle(), err)
		}
		files := map[string]int{} // the page's files, by where plan.files has their copies
		plan.contents[i] = c.CopyFor(plan.ids)
		plan.contents[i].Walk(func(n, _ *doc.Node) {
			if n.Type == "recording" {
				r, err := s.Recording(n.Str("recording_id"))
				if err != nil {
					return
				}
				from := r.ID
				r.ID, r.PageID, r.BlockID = doc.NewRowID(), plan.ids[p.ID], n.ID
				n.Set("recording_id", r.ID)
				plan.recs = append(plan.recs, recordingCopy{r, from})
				return
			}
			fileID := n.Str("file_id")
			if fileID == "" {
				return
			}
			k, ok := files[fileID]
			if !ok {
				f, err := s.File(fileID)
				if err != nil || !f.DeletedAt.IsZero() {
					return
				}
				k = len(plan.files)
				files[fileID] = k
				plan.files = append(plan.files, fileCopy{f, doc.NewRowID(), plan.ids[p.ID]})
			}
			fc := plan.files[k]
			n.Set("file_id", fc.to)
			n.Set("url", "file://"+filepath.Join(s.Dir, "files", fc.to, fileName(fc.Name)))
		})
	}
	return plan, nil
}

// createCopies makes the copies of subpages that copyPlan planned, each
// last under its parent's copy, so they keep their order.
func (s *Store) createCopies(pages []Page, ids map[string]string, contents []*doc.Content, template bool) error {
	for i, p := range pages {
		if _, err := s.CreatePage(NewPageOptions{ID: ids[p.ID], ParentID: ids[p.ParentID], Title: p.Title,
			Icon: p.Icon, Tags: p.Tags, Template: template, Content: contents[i]}); err != nil {
			return err
		}
	}
	return nil
}

// putCopies stores the recordings and files of copied pages.
func (s *Store) putCopies(plan pageCopies) error {
	return errors.Join(s.putRecordingCopies(plan.recs), s.putFileCopies(plan.files))
}

// recordingCopy is a recording of a copied page, and the ID of the one it
// copies.
type recordingCopy struct {
	Recording
	from string
}

// putRecordingCopies stores and queues the recordings of copied pages.
// Each goes up with audio of its own: only those who can view the page of
// the recording a stored audio is named for can play it. Audio on this
// device is copied, as the original's may go once it is up; audio that is
// only stored comes from the original's as the copy goes up. Until then
// the copy plays the original's.
func (s *Store) putRecordingCopies(recs []recordingCopy) error {
	for _, c := range recs {
		r := c.Recording
		r.LocalPath = ""
		if _, err := os.Stat(c.LocalPath); c.LocalPath != "" && err == nil {
			r.LocalPath = filepath.Join(s.Dir, "recordings", r.ID+filepath.Ext(c.LocalPath))
			if err := os.MkdirAll(filepath.Dir(r.LocalPath), 0o750); err != nil {
				return err
			}
			if err := copyFile(c.LocalPath, r.LocalPath); err != nil {
				return err
			}
		}
		r.UpdatedAt = time.Now()
		// The recording and its entries in the outbox are stored together,
		// as a file's are in putFileCopies.
		err := s.inTx(func(tx *sql.Tx) error {
			if space := s.Session().SpaceID; space != "" && !hasSpace(tx, space) {
				return ErrNoSpace
			}
			if err := putRecording(tx, r); err != nil {
				return err
			}
			if err := queue(tx, "recordings", r.ID, RecordingToRemote(r), "", r.PageID); err != nil {
				return err
			}
			switch {
			case r.LocalPath != "":
				return queue(tx, "storage", "recordings/"+r.ID, uploadOf(r), "audio waiting to upload", r.PageID)
			case r.AudioPath != "":
				return queue(tx, "storage", "recordings/"+r.ID, map[string]any{
					"bucket": "recordings", "from_path": r.AudioPath, "content_type": "audio/wav", "recording_id": r.ID,
				}, "audio waiting to upload", r.PageID)
			}
			return nil
		})
		if err != nil {
			if r.LocalPath != "" {
				_ = os.Remove(r.LocalPath)
			}
			return err
		}
		s.Notify("outbox", r.PageID)
		s.Notify("recordings", r.PageID)
	}
	return nil
}

// fileCopy is a file of a copied page, with the ID of its copy and the
// page the copy is on.
type fileCopy struct {
	File
	to, pageID string
}

// putFileCopies stores and queues the files of copied pages, as AddFile
// adds one: only those who can view the page a file is on can read it, so
// each copy has files of its own. A file on this device is copied; one
// that is only stored comes from the original's as the copy goes up.
func (s *Store) putFileCopies(files []fileCopy) error {
	for _, c := range files {
		now := time.Now()
		f := File{ID: c.to, SpaceID: s.Session().SpaceID, PageID: c.pageID, Name: fileName(c.Name), MimeType: c.MimeType,
			SizeBytes: c.SizeBytes, CreatedAt: now, UpdatedAt: now}
		f.StoragePath = f.SpaceID + "/" + f.ID + "/" + f.Name
		dir := filepath.Join(s.Dir, "files", f.ID)
		if _, err := os.Stat(c.LocalPath); c.LocalPath != "" && err == nil {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return err
			}
			f.LocalPath = filepath.Join(dir, f.Name)
			if err := copyFile(c.LocalPath, f.LocalPath); err != nil {
				return err
			}
		}
		ct := f.MimeType
		if ct == "" {
			ct = "application/octet-stream"
		}
		upload := map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": f.LocalPath, "content_type": ct, "file_id": f.ID}
		if f.LocalPath == "" {
			upload["from_path"] = c.StoragePath
		}
		err := s.inTx(func(tx *sql.Tx) error {
			if f.SpaceID != "" && !hasSpace(tx, f.SpaceID) {
				return ErrNoSpace
			}
			if err := putFile(tx, f); err != nil {
				return err
			}
			if err := queue(tx, "files", f.ID, FileToRemote(f), "new file", f.PageID); err != nil {
				return err
			}
			return queue(tx, "storage", "files/"+f.ID, upload, "file waiting to upload", f.PageID)
		})
		if err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
	}
	if len(files) > 0 {
		s.Notify("outbox", "")
		s.Notify("files", "")
	}
	return nil
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
	_, err := s.db.Exec(`UPDATE pages SET keep_offline = ? WHERE id = ?`, keep, id)
	s.Notify("pages", id)
	return err
}

// ContentRow is a page's stored content.
type ContentRow struct {
	PageID    string
	Content   string
	PlainText string
	UpdatedAt time.Time
	// BaseContent is the server version this device last synced with, the
	// merge base; RemoteUpdatedAt is its time and RemoteRevision its
	// revision.
	BaseContent     string
	RemoteUpdatedAt time.Time
	RemoteRevision  int64
}

// ContentRow returns a page's stored content.
func (s *Store) ContentRow(pageID string) (ContentRow, error) { return contentRow(s.db, pageID) }

func contentRow(q querier, pageID string) (ContentRow, error) {
	var r ContentRow
	var updated int64
	var base sql.NullString
	var remote sql.NullInt64
	err := q.QueryRow(`SELECT page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision FROM page_contents WHERE page_id = ?`, pageID).
		Scan(&r.PageID, &r.Content, &r.PlainText, &updated, &base, &remote, &r.RemoteRevision)
	r.UpdatedAt, r.BaseContent, r.RemoteUpdatedAt = time.UnixMilli(updated), base.String, fromMs(remote)
	return r, err
}

// Content returns a page's content on this device, or nil when it is not
// kept here or cannot be read, as content of a newer app: read as an empty
// page, it would be copied or saved as one.
func (s *Store) Content(pageID string) *doc.Content {
	r, err := s.ContentRow(pageID)
	if err != nil {
		return nil
	}
	c, err := doc.Decode(r.Content)
	if err != nil {
		return nil
	}
	return c
}

func (s *Store) putContent(pageID string, c *doc.Content, at time.Time, keep *ContentRow) error {
	return writeContent(s.db, pageID, c, at, keep)
}

func writeContent(q querier, pageID string, c *doc.Content, at time.Time, keep *ContentRow) error {
	var base any
	var remote any
	var revision int64
	if keep != nil {
		base, remote, revision = ns(keep.BaseContent), nt(keep.RemoteUpdatedAt), keep.RemoteRevision
	}
	_, err := q.Exec(`INSERT INTO page_contents (page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(page_id) DO UPDATE SET content = excluded.content,
		plain_text = excluded.plain_text, updated_at = excluded.updated_at, base_content = excluded.base_content,
		remote_updated_at = excluded.remote_updated_at, remote_revision = excluded.remote_revision`,
		pageID, c.Encode(), c.PlainText(), ms(at), base, remote, revision)
	return err
}

// PutSyncedContent stores content that came from the server, with the
// base it now agrees on.
func (s *Store) PutSyncedContent(pageID string, c *doc.Content, base string, remoteAt time.Time, revision int64) error {
	keep := &ContentRow{BaseContent: base, RemoteUpdatedAt: remoteAt, RemoteRevision: revision}
	if err := s.putContent(pageID, c, time.Now(), keep); err != nil {
		return err
	}
	s.Notify("page_contents", pageID)
	return nil
}

// PutSyncedContentOver is PutSyncedContent for content merged from the
// stored content over ("" when there was none): it stores only while that
// is still what is stored, checked in the statement that writes, and
// returns ErrContentChanged otherwise.
func (s *Store) PutSyncedContentOver(pageID, over string, c *doc.Content, base string, remoteAt time.Time, revision int64) error {
	if err := putSyncedOver(s.db, pageID, over, c, base, remoteAt, revision); err != nil {
		return err
	}
	s.Notify("page_contents", pageID)
	return nil
}

// PutMergedContentOver is PutSyncedContentOver for content merged from
// another device's through iCloud Drive, which has no server base, and,
// unless label is "", queues it to go out in the same transaction: stored
// without its entry, this device's merge would never reach the others.
func (s *Store) PutMergedContentOver(pageID, over string, c *doc.Content, label string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		if err := putSyncedOver(tx, pageID, over, c, "", time.Time{}, 0); err != nil || label == "" {
			return err
		}
		return queue(tx, "page_content", pageID, map[string]any{"page_id": pageID}, label, pageID)
	})
	if err != nil {
		return err
	}
	if label != "" {
		s.Notify("outbox", pageID)
	}
	s.Notify("page_contents", pageID)
	return nil
}

func putSyncedOver(q querier, pageID, over string, c *doc.Content, base string, remoteAt time.Time, revision int64) error {
	var res sql.Result
	var err error
	if over == "" {
		res, err = q.Exec(`INSERT INTO page_contents (page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(page_id) DO NOTHING`,
			pageID, c.Encode(), c.PlainText(), ms(time.Now()), ns(base), nt(remoteAt), revision)
	} else {
		res, err = q.Exec(`UPDATE page_contents SET content = ?, plain_text = ?, updated_at = ?, base_content = ?,
			remote_updated_at = ?, remote_revision = ? WHERE page_id = ? AND content = ?`,
			c.Encode(), c.PlainText(), ms(time.Now()), ns(base), nt(remoteAt), revision, pageID, over)
	}
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrContentChanged
	}
	return nil
}

// SetContentBase records the server version this device agrees with.
func (s *Store) SetContentBase(pageID, base string, remoteAt time.Time) error {
	_, err := s.db.Exec(`UPDATE page_contents SET base_content = ?, remote_updated_at = ? WHERE page_id = ?`, base, ms(remoteAt), pageID)
	return err
}

// ErrContentChanged is SaveContentOver's when the stored content is no
// longer what the caller read.
var ErrContentChanged = errors.New("store: page content changed")

// SaveContent saves an editor's content: stamps the blocks that changed,
// stores it, and queues it for the server. It returns the stamped content
// and whether anything changed.
func (s *Store) SaveContent(pageID string, c *doc.Content) (*doc.Content, bool, error) {
	return s.saveContent(pageID, c, nil)
}

// SaveContentOver is SaveContent for content made from the stored content
// over ("" when there was none): it saves only while that is still what is
// stored, and returns ErrContentChanged otherwise.
func (s *Store) SaveContentOver(pageID, over string, c *doc.Content) (*doc.Content, bool, error) {
	return s.saveContent(pageID, c, &over)
}

// saveContent reads the stored content, stores the new content and queues
// it in one transaction, which holds the write lock throughout: a pull
// cannot store the server's content in between, finding nothing queued,
// and a save that fails leaves nothing half done.
func (s *Store) saveContent(pageID string, c *doc.Content, over *string) (*doc.Content, bool, error) {
	var stamped *doc.Content
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		row, err := contentRow(tx, pageID)
		if over != nil {
			// No content is stored as "", so "" can only mean none.
			none := errors.Is(err, sql.ErrNoRows)
			if *over == "" && !none || *over != "" && (err != nil || row.Content != *over) {
				return ErrContentChanged
			}
		}
		var previous *doc.Content
		switch {
		case err == nil:
			// Content that cannot be read, as a newer app's, is not saved
			// over as if it were an empty page.
			if previous, err = doc.Decode(row.Content); err != nil {
				return fmt.Errorf("stored content: %w", err)
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		now := time.Now()
		stamped = c.Stamped(previous, now.UnixMilli())
		if previous != nil && previous.Encode() == stamped.Encode() {
			return nil
		}
		if err := writeContent(tx, pageID, stamped, now, &row); err != nil {
			return err
		}
		blocks := len(stamped.Blocks)
		if base, err := doc.Decode(row.BaseContent); row.BaseContent != "" && err == nil {
			blocks = len(doc.ChangedBlocks(base, stamped))
		}
		label := fmt.Sprintf("%d blocks edited", blocks)
		if blocks == 1 {
			label = "1 block edited"
		}
		if err := queue(tx, "page_content", pageID, map[string]any{"page_id": pageID}, label, pageID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE pages SET updated_at = ? WHERE id = ?`, ms(now), pageID); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if changed {
		s.Notify("outbox", pageID)
		s.Notify("page_contents", pageID)
	}
	return stamped, changed, nil
}

// HistoryEntry is a snapshot of a page.
type HistoryEntry struct {
	ID      string
	PageID  string
	Content string
	Reason  string // agent_run | autosave | restore | conflict | manual (named)
	// AIJobID is the local agent run that made the snapshot. It stays on
	// this device, as the run's job does.
	AIJobID   string
	Label     string
	CreatedAt time.Time
}

// AddHistory keeps a snapshot of a page.
func (s *Store) AddHistory(pageID string, c *doc.Content, reason, aiJobID, label string) error {
	h := HistoryEntry{ID: doc.NewRowID(), PageID: pageID, Content: c.Encode(), Reason: reason, AIJobID: aiJobID, Label: label, CreatedAt: time.Now()}
	remote := HistoryToRemote(h, s.Session().UserID)
	err := s.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO page_history (id, page_id, content, reason, ai_job_id, label, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			h.ID, h.PageID, h.Content, h.Reason, ns(h.AIJobID), h.Label, ms(h.CreatedAt)); err != nil {
			return err
		}
		if err := queue(tx, "page_history", h.ID, remote, "page history", pageID); err != nil {
			return err
		}
		return pruneHistory(tx, pageID)
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", pageID)
	s.Notify("page_history", pageID)
	return nil
}

// newestHistory selects the ids of a page's newest 50 snapshots, which
// History shows with its named versions. Of two taken in the same
// millisecond, the one stored later is newer.
const newestHistory = `SELECT id FROM page_history WHERE page_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 50`

// pruneHistory removes the snapshots of a page History no longer shows:
// all but its newest 50 and its named versions. One still waiting to go
// up leaves the queue with it, so the server never gets it.
func pruneHistory(tx *sql.Tx, pageID string) error {
	old := `SELECT id FROM page_history WHERE page_id = ? AND reason <> 'manual' AND id NOT IN (` + newestHistory + `)`
	if _, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'page_history' AND entity_id IN (`+old+`)`, pageID, pageID); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM page_history WHERE id IN (`+old+`)`, pageID, pageID)
	return err
}

// History returns a page's newest 50 snapshots, and the versions named
// before them.
func (s *Store) History(pageID string) ([]HistoryEntry, error) {
	rows, err := s.db.Query(`SELECT id, page_id, content, reason, ai_job_id, label, created_at FROM page_history
		WHERE page_id = ? AND (reason = 'manual' OR id IN (`+newestHistory+`))
		ORDER BY created_at DESC, rowid DESC`, pageID, pageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		var job sql.NullString
		var created int64
		if err := rows.Scan(&h.ID, &h.PageID, &h.Content, &h.Reason, &job, &h.Label, &created); err != nil {
			return nil, err
		}
		h.AIJobID, h.CreatedAt = job.String, time.UnixMilli(created)
		out = append(out, h)
	}
	return out, rows.Err()
}

// MaybeAutosave keeps an autosave snapshot at most every ten minutes of
// editing.
func (s *Store) MaybeAutosave(pageID string, c *doc.Content) error {
	var last int64
	err := s.db.QueryRow(`SELECT created_at FROM page_history WHERE page_id = ? ORDER BY created_at DESC LIMIT 1`, pageID).Scan(&last)
	if err == nil && time.Since(time.UnixMilli(last)) < 10*time.Minute {
		return nil
	}
	return s.AddHistory(pageID, c, "autosave", "", "Autosave")
}

// PageTodo is an open to-do of a page, with a due date or an owner.
type PageTodo struct {
	doc.Todo
	PageID    string
	PageTitle string
}

// Todos returns the open to-dos of the pages on this device, but
// templates', that have a due date or an owner, page by page.
func (s *Store) Todos() ([]PageTodo, error) {
	pages, err := s.Pages()
	if err != nil {
		return nil, err
	}
	var out []PageTodo
	for _, p := range pages {
		c := s.Content(p.ID)
		if c == nil {
			continue
		}
		for _, t := range c.Todos() {
			out = append(out, PageTodo{Todo: t, PageID: p.ID, PageTitle: p.DisplayTitle()})
		}
	}
	return out, nil
}

// SearchHit is one search result.
type SearchHit struct {
	Kind    string // page | template | recording | comment
	Title   string
	PageID  string
	Snippet string
	BlockID string
}

// Search finds pages and templates by title, tag or text, recordings by
// transcript, and comments. Words starting with # keep to the pages with
// those tags, which alone lists them, newest first. A blank query returns
// the most recently edited pages.
func (s *Store) Search(query string, limit int) ([]SearchHit, error) {
	filters, rest := SplitTagQuery(query)
	q := strings.ToLower(rest)
	pages, err := s.Pages()
	if err != nil {
		return nil, err
	}
	templates, err := s.Templates()
	if err != nil {
		return nil, err
	}
	// Text, transcripts and comments are read for every page there is,
	// the pages with the tags among them, while tags filter: a limit
	// before the filter would let other pages' matches crowd theirs out.
	textLimit, otherLimit := limit, 10
	if len(filters) > 0 {
		match := tagMatcher(filters, append(append([]Page{}, pages...), templates...))
		pages, templates = slices.DeleteFunc(pages, func(p Page) bool { return !match(p) }),
			slices.DeleteFunc(templates, func(p Page) bool { return !match(p) })
		textLimit, otherLimit = -1, -1
	}
	titles := map[string]string{}
	for _, p := range pages {
		titles[p.ID] = p.Title
	}
	templateTitles := map[string]string{}
	for _, t := range templates {
		templateTitles[t.ID] = t.Title
	}
	var hits []SearchHit
	if q == "" {
		recent := append([]Page{}, pages...)
		sort.Slice(recent, func(i, j int) bool { return recent[i].UpdatedAt.After(recent[j].UpdatedAt) })
		for i, p := range recent {
			if i == 8 && len(filters) == 0 || i == limit {
				break
			}
			hits = append(hits, SearchHit{Kind: "page", Title: p.DisplayTitle(), PageID: p.ID})
		}
		return hits, nil
	}
	seen := map[string]bool{}
	for _, p := range pages {
		if strings.Contains(strings.ToLower(p.Title), q) || tagContains(p, q) {
			hits = append(hits, SearchHit{Kind: "page", Title: p.DisplayTitle(), PageID: p.ID})
			seen[p.ID] = true
		}
	}
	for _, t := range templates {
		if strings.Contains(strings.ToLower(t.Title), q) {
			hits = append(hits, SearchHit{Kind: "template", Title: t.DisplayTitle(), PageID: t.ID})
			seen[t.ID] = true
		}
	}
	// Text matches as the titles do, lowered as Go lowers it, and with %
	// and _ the characters they are: SQLite's lower() and LIKE fold ASCII
	// alone, and LIKE reads those two as wildcards.
	rows, err := s.db.Query(`SELECT page_id, plain_text FROM page_contents WHERE instr(`+lowerFunc+`(plain_text), ?) > 0 LIMIT ?`, q, textLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if seen[id] {
			continue
		}
		if title, ok := templateTitles[id]; ok {
			hits = append(hits, SearchHit{Kind: "template", Title: TitleOr(title), PageID: id, Snippet: snippet(text, q)})
			continue
		}
		if _, ok := titles[id]; !ok {
			continue
		}
		hits = append(hits, SearchHit{Kind: "page", Title: TitleOr(titles[id]), PageID: id, Snippet: snippet(text, q)})
	}
	_ = rows.Close()
	rows, err = s.db.Query(`SELECT page_id, block_id FROM recordings WHERE instr(`+lowerFunc+`(transcript), ?) > 0 LIMIT ?`, q, otherLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, block string
		if err := rows.Scan(&id, &block); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if _, ok := titles[id]; ok {
			hits = append(hits, SearchHit{Kind: "recording", Title: locales.StoreSearchRecording(TitleOr(titles[id])), PageID: id, BlockID: block})
		}
	}
	_ = rows.Close()
	rows, err = s.db.Query(`SELECT page_id, block_id, body FROM comments WHERE instr(`+lowerFunc+`(body), ?) > 0 AND deleted_at IS NULL LIMIT ?`, q, otherLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, block, body string
		if err := rows.Scan(&id, &block, &body); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if _, ok := titles[id]; ok {
			hits = append(hits, SearchHit{Kind: "comment", Title: body, PageID: id, BlockID: block, Snippet: locales.StoreSearchCommentOn(TitleOr(titles[id]))})
		}
	}
	_ = rows.Close()
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// SplitTagQuery splits a search into its tag filters, the words starting
// with #, without it, and the rest of its words.
func SplitTagQuery(query string) (tags []string, rest string) {
	var words []string
	for _, w := range strings.Fields(query) {
		if t, ok := strings.CutPrefix(w, "#"); ok {
			if t = strings.TrimLeft(t, "#"); t != "" {
				tags = append(tags, t)
			}
			continue
		}
		words = append(words, w)
	}
	return tags, strings.Join(words, " ")
}

// tagMatcher reports whether a page has every tag of filters: a tag of
// that name, in any case, or while no page has one, a tag starting with
// it, as one is typed.
func tagMatcher(filters []string, pages []Page) func(Page) bool {
	known := map[string]bool{}
	for _, p := range pages {
		for _, t := range p.Tags {
			known[strings.ToLower(t)] = true
		}
	}
	return func(p Page) bool {
		for _, f := range filters {
			f = strings.ToLower(f)
			if !slices.ContainsFunc(p.Tags, func(t string) bool {
				t = strings.ToLower(t)
				return t == f || !known[f] && strings.HasPrefix(t, f)
			}) {
				return false
			}
		}
		return true
	}
}

// tagContains reports whether a tag of p contains q, lower case.
func tagContains(p Page, q string) bool {
	return slices.ContainsFunc(p.Tags, func(t string) bool { return strings.Contains(strings.ToLower(t), q) })
}

func snippet(text, q string) string {
	// Blocks read as a line of their own, set apart by a dot.
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	text = strings.Join(lines, " · ")
	lower := strings.ToLower(text)
	i, n := strings.Index(lower, q), len(q)
	if i < 0 {
		return ""
	}
	if len(lower) != len(text) {
		// Lowering changed the byte length, so the match is not at the
		// same place in text: show its start.
		i, n = 0, 0
	}
	start, end := max(i-30, 0), min(i+n+50, len(text))
	// Start and end at whole words, where one is near.
	if start > 0 {
		if j := strings.IndexByte(text[start:i], ' '); j >= 0 {
			start += j + 1
		}
	}
	if end < len(text) {
		if j := strings.LastIndexByte(text[i+n:end], ' '); j >= 0 {
			end = i + n + j
		}
	}
	for start > 0 && !utf8Start(text[start]) {
		start--
	}
	for end < len(text) && !utf8Start(text[end]) {
		end++
	}
	out := strings.Trim(text[start:end], " ·")
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
