package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"leaflet/internal/doc"
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
}

// DisplayTitle is the title, or "Untitled".
func (p Page) DisplayTitle() string { return TitleOr(p.Title) }

// TitleOr returns a title, or "Untitled" for a blank one.
func TitleOr(t string) string {
	if strings.TrimSpace(t) == "" {
		return "Untitled"
	}
	return t
}

const pageCols = `id, space_id, parent_id, folder_id, title, icon, sort_key, created_by, created_at, updated_at, deleted_at, keep_offline`

func scanPage(sc interface{ Scan(...any) error }) (Page, error) {
	var p Page
	var parent, folder, icon, by sql.NullString
	var created, updated int64
	var deleted sql.NullInt64
	err := sc.Scan(&p.ID, &p.SpaceID, &parent, &folder, &p.Title, &icon, &p.SortKey, &by, &created, &updated, &deleted, &p.KeepOffline)
	p.ParentID, p.FolderID, p.Icon, p.CreatedBy = parent.String, folder.String, icon.String, by.String
	p.CreatedAt, p.UpdatedAt, p.DeletedAt = time.UnixMilli(created), time.UnixMilli(updated), fromMs(deleted)
	return p, err
}

func (s *Store) queryPages(where string, args ...any) ([]Page, error) {
	rows, err := s.db.Query(`SELECT `+pageCols+` FROM pages WHERE `+where, args...)
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

// Pages returns the pages not deleted, in sort order.
func (s *Store) Pages() ([]Page, error) {
	return s.queryPages(`deleted_at IS NULL ORDER BY sort_key`)
}

// Page returns one page, deleted or not.
func (s *Store) Page(id string) (Page, error) {
	return scanPage(s.db.QueryRow(`SELECT `+pageCols+` FROM pages WHERE id = ?`, id))
}

// Children returns the pages under a parent ("" for the top level of the
// space), in sort order.
func (s *Store) Children(parentID, spaceID string) ([]Page, error) {
	if parentID == "" {
		return s.queryPages(`deleted_at IS NULL AND parent_id IS NULL AND space_id = ? ORDER BY sort_key`, spaceID)
	}
	return s.queryPages(`deleted_at IS NULL AND parent_id = ? ORDER BY sort_key`, parentID)
}

func (s *Store) putPage(p Page) error {
	_, err := s.db.Exec(`INSERT INTO pages (`+pageCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET space_id = excluded.space_id, parent_id = excluded.parent_id,
		folder_id = excluded.folder_id, title = excluded.title, icon = excluded.icon, sort_key = excluded.sort_key,
		created_by = excluded.created_by, created_at = excluded.created_at, updated_at = excluded.updated_at,
		deleted_at = excluded.deleted_at, keep_offline = excluded.keep_offline`,
		p.ID, p.SpaceID, ns(p.ParentID), ns(p.FolderID), p.Title, ns(p.Icon), p.SortKey, ns(p.CreatedBy),
		ms(p.CreatedAt), ms(p.UpdatedAt), nt(p.DeletedAt), p.KeepOffline)
	return err
}

func (s *Store) queuePage(p Page, label string) error {
	return s.Queue("pages", p.ID, PageToRemote(p), label, p.ID)
}

// NewPageOptions describe a page to create.
type NewPageOptions struct {
	ID       string
	ParentID string
	FolderID string
	SpaceID  string
	Title    string
	Content  *doc.Content
}

// CreatePage creates a page last among its siblings.
func (s *Store) CreatePage(o NewPageOptions) (Page, error) {
	sess := s.Session()
	spaceID := o.SpaceID
	if o.ParentID != "" {
		if parent, err := s.Page(o.ParentID); err == nil && spaceID == "" {
			spaceID = parent.SpaceID
		}
	}
	if spaceID == "" {
		spaceID = sess.SpaceID
	}
	siblings, err := s.Children(o.ParentID, spaceID)
	if err != nil {
		return Page{}, err
	}
	last := ""
	if len(siblings) > 0 {
		last = siblings[len(siblings)-1].SortKey
	}
	now := time.Now()
	p := Page{
		ID: o.ID, SpaceID: spaceID, ParentID: o.ParentID, Title: o.Title,
		SortKey: KeyBetween(last, ""), CreatedAt: now, UpdatedAt: now, KeepOffline: true,
	}
	if p.ID == "" {
		p.ID = doc.NewRowID()
	}
	if o.ParentID == "" {
		p.FolderID = o.FolderID
	}
	if sess.UserID != LocalUserID {
		p.CreatedBy = sess.UserID
	}
	content := o.Content
	if content == nil {
		content = doc.Empty()
	}
	body := content.Stamped(nil, now.UnixMilli())
	if err := s.putPage(p); err != nil {
		return Page{}, err
	}
	if err := s.putContent(p.ID, body, now, nil); err != nil {
		return Page{}, err
	}
	if err := s.queuePage(p, "new page"); err != nil {
		return Page{}, err
	}
	if err := s.Queue("page_content", p.ID, map[string]any{"page_id": p.ID}, "new page", p.ID); err != nil {
		return Page{}, err
	}
	s.Notify("pages", p.ID)
	return p, nil
}

// RenamePage changes a page's title.
func (s *Store) RenamePage(id, title string) error {
	p, err := s.Page(id)
	if err != nil || p.Title == title {
		return err
	}
	p.Title, p.UpdatedAt = title, time.Now()
	if err := s.putPage(p); err != nil {
		return err
	}
	if err := s.queuePage(p, "renamed"); err != nil {
		return err
	}
	s.Notify("pages", id)
	return nil
}

// IsDescendant reports whether candidate is page of or one under it.
func (s *Store) IsDescendant(candidate, of string) bool {
	for cursor := candidate; cursor != ""; {
		if cursor == of {
			return true
		}
		p, err := s.Page(cursor)
		if err != nil {
			return false
		}
		cursor = p.ParentID
	}
	return false
}

// ErrMoveIntoSelf is returned for a move under the page itself.
var ErrMoveIntoSelf = errors.New("a page cannot move inside itself")

// MovePage moves a page under parentID (or to the top level, into
// folderID), after afterID among its new siblings, or last.
func (s *Store) MovePage(id, parentID, folderID, afterID string) error {
	p, err := s.Page(id)
	if err != nil {
		return err
	}
	if parentID != "" && s.IsDescendant(parentID, id) {
		return ErrMoveIntoSelf
	}
	all, err := s.Children(parentID, p.SpaceID)
	if err != nil {
		return err
	}
	var siblings []Page
	for _, q := range all {
		if q.ID != id && (parentID != "" || q.FolderID == folderID) {
			siblings = append(siblings, q)
		}
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
	if parentID != "" {
		if parent, err := s.Page(parentID); err == nil {
			p.SpaceID = parent.SpaceID
		}
		folderID = ""
	}
	p.ParentID, p.FolderID = parentID, folderID
	if before != "" && after != "" && before >= after {
		after = ""
	}
	p.SortKey, p.UpdatedAt = KeyBetween(before, after), time.Now()
	if err := s.putPage(p); err != nil {
		return err
	}
	if err := s.queuePage(p, "moved"); err != nil {
		return err
	}
	s.Notify("pages", id)
	return nil
}

// DeletePage soft-deletes a page and its subpages, and returns their IDs
// so the delete can be undone.
func (s *Store) DeletePage(id string) ([]string, error) {
	ids := []string{id}
	for i := 0; i < len(ids); i++ {
		kids, err := s.queryPages(`parent_id = ? AND deleted_at IS NULL`, ids[i])
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			ids = append(ids, k.ID)
		}
	}
	now := time.Now()
	for _, pid := range ids {
		p, err := s.Page(pid)
		if err != nil {
			continue
		}
		p.DeletedAt, p.UpdatedAt = now, now
		if err := s.putPage(p); err != nil {
			return nil, err
		}
		if err := s.queuePage(p, "deleted"); err != nil {
			return nil, err
		}
	}
	s.Notify("pages", id)
	return ids, nil
}

// RestorePages undoes a delete.
func (s *Store) RestorePages(ids []string) error {
	for _, pid := range ids {
		p, err := s.Page(pid)
		if err != nil {
			continue
		}
		p.DeletedAt, p.UpdatedAt = time.Time{}, time.Now()
		if err := s.putPage(p); err != nil {
			return err
		}
		if err := s.queuePage(p, "restored"); err != nil {
			return err
		}
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
func (s *Store) ContentRow(pageID string) (ContentRow, error) {
	var r ContentRow
	var updated int64
	var base sql.NullString
	var remote sql.NullInt64
	err := s.db.QueryRow(`SELECT page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision FROM page_contents WHERE page_id = ?`, pageID).
		Scan(&r.PageID, &r.Content, &r.PlainText, &updated, &base, &remote, &r.RemoteRevision)
	r.UpdatedAt, r.BaseContent, r.RemoteUpdatedAt = time.UnixMilli(updated), base.String, fromMs(remote)
	return r, err
}

// Content returns a page's content on this device, or nil when it is not
// kept here.
func (s *Store) Content(pageID string) *doc.Content {
	r, err := s.ContentRow(pageID)
	if err != nil {
		return nil
	}
	return doc.MustDecode(r.Content)
}

func (s *Store) putContent(pageID string, c *doc.Content, at time.Time, keep *ContentRow) error {
	var base any
	var remote any
	var revision int64
	if keep != nil {
		base, remote, revision = ns(keep.BaseContent), nt(keep.RemoteUpdatedAt), keep.RemoteRevision
	}
	_, err := s.db.Exec(`INSERT INTO page_contents (page_id, content, plain_text, updated_at, base_content, remote_updated_at, remote_revision)
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

// SetContentBase records the server version this device agrees with.
func (s *Store) SetContentBase(pageID, base string, remoteAt time.Time) error {
	_, err := s.db.Exec(`UPDATE page_contents SET base_content = ?, remote_updated_at = ? WHERE page_id = ?`, base, ms(remoteAt), pageID)
	return err
}

// SaveContent saves an editor's content: stamps the blocks that changed,
// stores it, and queues it for the server. It returns the stamped content
// and whether anything changed.
func (s *Store) SaveContent(pageID string, c *doc.Content) (*doc.Content, bool, error) {
	row, err := s.ContentRow(pageID)
	var previous *doc.Content
	if err == nil {
		previous = doc.MustDecode(row.Content)
	}
	now := time.Now()
	stamped := c.Stamped(previous, now.UnixMilli())
	if previous != nil && previous.Encode() == stamped.Encode() {
		return stamped, false, nil
	}
	if err := s.putContent(pageID, stamped, now, &row); err != nil {
		return nil, false, err
	}
	changed := len(stamped.Blocks)
	if row.BaseContent != "" {
		changed = len(doc.ChangedBlocks(doc.MustDecode(row.BaseContent), stamped))
	}
	label := fmt.Sprintf("%d blocks edited", changed)
	if changed == 1 {
		label = "1 block edited"
	}
	if err := s.Queue("page_content", pageID, map[string]any{"page_id": pageID}, label, pageID); err != nil {
		return nil, false, err
	}
	if _, err := s.db.Exec(`UPDATE pages SET updated_at = ? WHERE id = ?`, ms(now), pageID); err != nil {
		return nil, false, err
	}
	s.Notify("page_contents", pageID)
	return stamped, true, nil
}

// HistoryEntry is a snapshot of a page.
type HistoryEntry struct {
	ID        string
	PageID    string
	Content   string
	Reason    string // agent_run | autosave | restore | conflict
	AIJobID   string
	Label     string
	CreatedAt time.Time
}

// AddHistory keeps a snapshot of a page.
func (s *Store) AddHistory(pageID string, c *doc.Content, reason, aiJobID, label string) error {
	h := HistoryEntry{ID: doc.NewRowID(), PageID: pageID, Content: c.Encode(), Reason: reason, AIJobID: aiJobID, Label: label, CreatedAt: time.Now()}
	if _, err := s.db.Exec(`INSERT INTO page_history (id, page_id, content, reason, ai_job_id, label, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.PageID, h.Content, h.Reason, ns(h.AIJobID), h.Label, ms(h.CreatedAt)); err != nil {
		return err
	}
	if err := s.Queue("page_history", h.ID, HistoryToRemote(h, s.Session().UserID), "page history", pageID); err != nil {
		return err
	}
	s.Notify("page_history", pageID)
	return nil
}

// History returns a page's newest 50 snapshots.
func (s *Store) History(pageID string) ([]HistoryEntry, error) {
	rows, err := s.db.Query(`SELECT id, page_id, content, reason, ai_job_id, label, created_at FROM page_history WHERE page_id = ? ORDER BY created_at DESC LIMIT 50`, pageID)
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

// SearchHit is one search result.
type SearchHit struct {
	Kind    string // page | recording | comment
	Title   string
	PageID  string
	Snippet string
	BlockID string
}

// Search finds pages by title or text, recordings by transcript, and
// comments. A blank query returns the most recently edited pages.
func (s *Store) Search(query string, limit int) ([]SearchHit, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	pages, err := s.Pages()
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	for _, p := range pages {
		titles[p.ID] = p.Title
	}
	var hits []SearchHit
	if q == "" {
		recent := append([]Page{}, pages...)
		sort.Slice(recent, func(i, j int) bool { return recent[i].UpdatedAt.After(recent[j].UpdatedAt) })
		for i, p := range recent {
			if i == 8 {
				break
			}
			hits = append(hits, SearchHit{Kind: "page", Title: p.DisplayTitle(), PageID: p.ID})
		}
		return hits, nil
	}
	seen := map[string]bool{}
	for _, p := range pages {
		if strings.Contains(strings.ToLower(p.Title), q) {
			hits = append(hits, SearchHit{Kind: "page", Title: p.DisplayTitle(), PageID: p.ID})
			seen[p.ID] = true
		}
	}
	like := "%" + q + "%"
	rows, err := s.db.Query(`SELECT page_id, plain_text FROM page_contents WHERE lower(plain_text) LIKE ? LIMIT ?`, like, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if _, ok := titles[id]; !ok || seen[id] {
			continue
		}
		hits = append(hits, SearchHit{Kind: "page", Title: TitleOr(titles[id]), PageID: id, Snippet: snippet(text, q)})
	}
	_ = rows.Close()
	rows, err = s.db.Query(`SELECT page_id, block_id FROM recordings WHERE lower(transcript) LIKE ? LIMIT 10`, like)
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
			hits = append(hits, SearchHit{Kind: "recording", Title: "Recording · " + TitleOr(titles[id]), PageID: id, BlockID: block})
		}
	}
	_ = rows.Close()
	rows, err = s.db.Query(`SELECT page_id, block_id, body FROM comments WHERE lower(body) LIKE ? AND deleted_at IS NULL LIMIT 10`, like)
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
			hits = append(hits, SearchHit{Kind: "comment", Title: body, PageID: id, BlockID: block, Snippet: "Comment on " + TitleOr(titles[id])})
		}
	}
	_ = rows.Close()
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
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
