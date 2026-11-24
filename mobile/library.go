package mobile

import (
	"encoding/json"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/core"
	"leaflet/internal/doc"
	libtree "leaflet/internal/library"
	"leaflet/internal/store"
	"leaflet/locales"
)

type pageRow struct {
	ID                string   `json:"id"`
	SpaceID           string   `json:"space_id"`
	ParentID          string   `json:"parent_id,omitempty"`
	FolderID          string   `json:"folder_id,omitempty"`
	Title             string   `json:"title"`
	Icon              string   `json:"icon,omitempty"`
	SortKey           string   `json:"sort_key"`
	UpdatedAt         int64    `json:"updated_at"`
	KeepOffline       bool     `json:"keep_offline"`
	Template          bool     `json:"template"`
	Tags              []string `json:"tags"`
	CanEdit           bool     `json:"can_edit"`
	CreatedBy         string   `json:"created_by,omitempty"`
	DefaultTemplateID string   `json:"default_template_id,omitempty"`
}

type folderRow struct {
	ID                string `json:"id"`
	ParentID          string `json:"parent_id,omitempty"`
	Name              string `json:"name"`
	SortKey           string `json:"sort_key"`
	DefaultTemplateID string `json:"default_template_id,omitempty"`
}

func TagColor(name string) string {
	defer catch(nil)
	return doc.TagColor(name)
}

type tagRow struct {
	Tag   string   `json:"tag"`
	Count int      `json:"count"`
	Pages []string `json:"pages"`
}

type fileRow struct {
	ID       string `json:"id"`
	FolderID string `json:"folder_id,omitempty"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
}

type library struct {
	SpaceID string      `json:"space_id"`
	Pages   []pageRow   `json:"pages"`
	Folders []folderRow `json:"folders"`
	// RootFolders and SubFolders arrange the folders as the sidebar shows them.
	RootFolders []string            `json:"root_folders"`
	SubFolders  map[string][]string `json:"sub_folders"`
	Files       []fileRow           `json:"files"`
	Tags        []tagRow            `json:"tags"`
	Top         []string            `json:"top"`
	InFolder    map[string][]string `json:"in_folder"`
	Children    map[string][]string `json:"children"`
	Shared      []string            `json:"shared"`
}

func (c *Core) Library() (_ string, err error) {
	defer catch(&err)
	var lib library
	c.q.do(func() {
		st := c.core.Store
		lib.SpaceID = st.Session().SpaceID
		var pages, templates []store.Page
		if pages, err = st.Pages(); err != nil {
			return
		}
		if templates, err = st.Templates(); err != nil {
			return
		}
		var folders []store.Folder
		if folders, err = st.Folders(); err != nil {
			return
		}
		var access *store.Tree
		if access, err = st.Tree(); err != nil {
			return
		}
		all := slices.Concat(pages, templates)
		lib.Pages = make([]pageRow, 0, len(all))
		for _, p := range all {
			lib.Pages = append(lib.Pages, rowWith(p, access.CanEdit(p)))
		}
		tree := libtree.Arrange(lib.SpaceID, all, folders)
		lib.Folders = make([]folderRow, 0, len(tree.Folders))
		for _, f := range tree.Folders {
			lib.Folders = append(lib.Folders, folderRow{
				ID: f.ID, ParentID: f.ParentFolderID, Name: f.Name, SortKey: f.SortKey, DefaultTemplateID: f.DefaultTemplateID,
			})
		}
		lib.RootFolders = folderIDs(tree.RootFolders)
		lib.SubFolders = make(map[string][]string, len(tree.SubFolders))
		for id, in := range tree.SubFolders {
			lib.SubFolders[id] = folderIDs(in)
		}
		lib.Tags = make([]tagRow, 0, len(tree.Tags))
		for _, t := range tree.Tags {
			lib.Tags = append(lib.Tags, tagRow{Tag: t.Name, Count: t.Count, Pages: pageIDs(tree.Tagged(t.Name))})
		}
		var files []store.File
		if files, err = st.Files(); err != nil {
			return
		}
		lib.Files = make([]fileRow, 0, len(files))
		for _, f := range files {
			if f.FolderID != "" || f.PageID == "" {
				lib.Files = append(lib.Files, fileRow{ID: f.ID, FolderID: f.FolderID, Name: f.Name, Size: f.SizeBytes})
			}
		}
		lib.Top, lib.Shared = pageIDs(tree.Top), pageIDs(tree.Shared)
		lib.InFolder, lib.Children = idsBy(tree.InFolder), idsBy(tree.Children)
	})
	if err != nil {
		return "", err
	}
	return encode(lib)
}

func folderIDs(folders []store.Folder) []string {
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		out = append(out, f.ID)
	}
	return out
}

func pageIDs(pages []store.Page) []string {
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.ID)
	}
	return out
}

func idsBy(pages map[string][]store.Page) map[string][]string {
	out := make(map[string][]string, len(pages))
	for k, ps := range pages {
		out[k] = pageIDs(ps)
	}
	return out
}

func (c *Core) rowOf(p store.Page) pageRow { return rowWith(p, c.core.Store.CanEdit(p)) }

func rowWith(p store.Page, canEdit bool) pageRow {
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	return pageRow{
		ID: p.ID, SpaceID: p.SpaceID, ParentID: p.ParentID, FolderID: p.FolderID,
		Title: p.Title, Icon: p.Icon, SortKey: p.SortKey, UpdatedAt: millis(p.UpdatedAt),
		KeepOffline: p.KeepOffline, Template: p.Template, Tags: tags, CanEdit: canEdit,
		CreatedBy: p.CreatedBy, DefaultTemplateID: p.DefaultTemplateID,
	}
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

type newPage struct {
	ParentID string   `json:"parent_id"`
	FolderID string   `json:"folder_id"`
	AfterID  string   `json:"after_id"`
	Title    string   `json:"title"`
	Tags     []string `json:"tags"`
	Link     bool     `json:"link"`
	Start    bool     `json:"start"`
}

func (c *Core) CreatePage(page string) (_ string, err error) {
	defer catch(&err)
	var n newPage
	if err := json.Unmarshal([]byte(page), &n); err != nil {
		return "", err
	}
	var row pageRow
	start := ""
	c.q.do(func() {
		var p store.Page
		p, err = c.core.Store.CreatePage(store.NewPageOptions{
			ParentID: n.ParentID, FolderID: n.FolderID, AfterID: n.AfterID, Title: n.Title, Tags: n.Tags,
		})
		if err != nil {
			return
		}
		row = c.rowOf(p)
		if n.Link && n.ParentID != "" {
			if lerr := c.pageCore().LinkSubpage(n.ParentID, p.ID, ""); lerr != nil {
				log.Printf("page %s: link in %s: %v", p.ID, n.ParentID, lerr)
				c.ev.notice(locales.AppErrorLinkSubpage(lerr.Error()))
			}
		}
		if n.Start {
			start = c.defaultTemplate(n.ParentID, n.FolderID)
		}
	})
	if err != nil {
		return "", err
	}
	if start != "" {
		if title, terr := c.ApplyTemplate(row.ID, start); terr != nil {
			log.Printf("page %s: template %s: %v", row.ID, start, terr)
		} else if title != "" {
			row.Title = title
		}
	}
	return encode(row)
}

func (c *Core) RenamePage(id, title string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.Store.RenamePage(id, title) })
	return err
}

func (c *Core) MovePage(id, parentID, folderID, afterID string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		err = c.pageCore().MovePage(id, parentID, func() error {
			return c.core.Store.MovePage(id, parentID, folderID, afterID)
		})
	})
	return err
}

func (c *Core) MovePageBefore(id, parentID, folderID, beforeID string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		err = c.pageCore().MovePage(id, parentID, func() error {
			return c.core.Store.MovePageBefore(id, parentID, folderID, beforeID)
		})
	})
	return err
}

func (c *Core) MoveTargets(id string) (_ string, err error) {
	defer catch(&err)
	var out []string
	c.q.do(func() {
		var p store.Page
		if p, err = c.core.Store.Page(id); err != nil {
			return
		}
		var pages []store.Page
		if pages, err = c.core.Store.Pages(); err != nil {
			return
		}
		var access *store.Tree
		if access, err = c.core.Store.Tree(); err != nil {
			return
		}
		slices.SortStableFunc(pages, func(a, b store.Page) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
		out = []string{}
		for _, t := range pages {
			if t.SpaceID == p.SpaceID && !t.Template && access.CanEdit(t) && !access.IsDescendant(t.ID, id) {
				out = append(out, t.ID)
			}
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

func (c *Core) DeletePage(id string) (_ string, err error) {
	defer catch(&err)
	var ids []string
	c.q.do(func() {
		for pid, p := range c.pages {
			if c.core.Store.IsDescendant(pid, id) && !p.live.Save() {
				err = errNotSaved()
				return
			}
		}
		var card doc.Position
		if ids, card, err = c.pageCore().DeletePage(id); err != nil {
			return
		}
		c.cards[id] = card
		for _, pid := range ids {
			if p := c.pages[pid]; p != nil {
				p.live.Close()
				delete(c.pages, pid)
				c.core.Sync.SetOpen(pid, false)
			}
		}
	})
	if err != nil {
		return "", err
	}
	return encode(ids)
}

func (c *Core) RestorePages(ids string) (err error) {
	defer catch(&err)
	var list []string
	if err := json.Unmarshal([]byte(ids), &list); err != nil {
		return err
	}
	c.q.do(func() {
		card := doc.Position{At: "end"}
		if len(list) > 0 {
			if at, ok := c.cards[list[0]]; ok {
				card = at
			}
			delete(c.cards, list[0])
		}
		err = c.pageCore().RestorePages(list, card)
	})
	return err
}

func (c *Core) SetPageTags(id, tags string) (err error) {
	defer catch(&err)
	var list []string
	if err := json.Unmarshal([]byte(tags), &list); err != nil {
		return err
	}
	c.q.do(func() { err = c.core.Store.SetPageTags(id, list) })
	return err
}

func (c *Core) RenameTag(old, name string) (_ string, err error) {
	defer catch(&err)
	tags := store.NormalizeTags([]string{name})
	if len(tags) == 0 {
		return "", nil
	}
	c.q.do(func() { _, err = c.core.Store.RenameTag(old, tags[0]) })
	if err != nil {
		return "", err
	}
	return tags[0], nil
}

func (c *Core) DeleteTag(tag string) (_ string, err error) {
	defer catch(&err)
	var before map[string][]string
	c.q.do(func() { before, err = c.core.Store.DeleteTag(tag) })
	if err != nil {
		return "", err
	}
	return encode(before)
}

func (c *Core) RestoreTags(before string) (err error) {
	defer catch(&err)
	var tags map[string][]string
	if err := json.Unmarshal([]byte(before), &tags); err != nil {
		return err
	}
	c.q.do(func() {
		for id, list := range tags {
			if err = c.core.Store.SetPageTags(id, list); err != nil {
				return
			}
		}
	})
	return err
}

func (c *Core) SetKeepOffline(id string, keep bool) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.SetKeepOffline(id, keep) })
	return err
}

func (c *Core) CreateFolder(name string) (_ string, err error) {
	defer catch(&err)
	return c.CreateFolderIn(name, "")
}

// CreateFolderIn makes a folder last in the folder parentID, or at the top level when it is "".
func (c *Core) CreateFolderIn(name, parentID string) (_ string, err error) {
	defer catch(&err)
	name = strings.TrimSpace(name)
	var f store.Folder
	c.q.do(func() { f, err = c.core.Store.CreateFolderIn(name, parentID) })
	if err != nil {
		return "", err
	}
	return encode(folderRow{ID: f.ID, ParentID: f.ParentFolderID, Name: f.Name, SortKey: f.SortKey})
}

// MoveFolder puts the folder in the folder parentID, or at the top level when it is "", after the folder afterID or last.
func (c *Core) MoveFolder(id, parentID, afterID string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.Store.MoveFolder(id, parentID, afterID) })
	return err
}

// MoveFolderBefore puts the folder in the folder parentID before the folder beforeID, or first.
func (c *Core) MoveFolderBefore(id, parentID, beforeID string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.Store.MoveFolderBefore(id, parentID, beforeID) })
	return err
}

func (c *Core) RenameFolder(id, name string) (err error) {
	defer catch(&err)
	if name = strings.TrimSpace(name); name == "" {
		return nil
	}
	c.q.do(func() {
		var f store.Folder
		if f, err = c.core.Store.Folder(id); err == nil {
			err = c.core.Store.RenameFolder(f, name)
		}
	})
	return err
}

func (c *Core) DeleteFolder(id string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		var f store.Folder
		if f, err = c.core.Store.Folder(id); err == nil {
			err = c.core.Store.DeleteFolder(f)
		}
	})
	return err
}

type searchHit struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	PageID  string `json:"page_id"`
	BlockID string `json:"block_id,omitempty"`
	Snippet string `json:"snippet"`
}

func (c *Core) Search(query string, limit int) (_ string, err error) {
	defer catch(&err)
	return c.SearchCancellable(query, limit, 0)
}

func (c *Core) SearchCancellable(query string, limit int, token int64) (_ string, err error) {
	defer catch(&err)
	ctx, done := c.cancellable(token, time.Minute)
	defer done()
	var hits []store.SearchHit
	c.q.do(func() {
		if err = ctx.Err(); err == nil {
			hits, err = c.core.Store.SearchContext(ctx, query, limit)
		}
	})
	if err != nil {
		return "", err
	}
	out := make([]searchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, searchHit{Kind: h.Kind, Title: h.Title, PageID: h.PageID, BlockID: h.BlockID, Snippet: h.Snippet})
	}
	return encode(out)
}

type todo struct {
	PageID    string `json:"page_id"`
	PageTitle string `json:"page_title"`
	BlockID   string `json:"block_id"`
	Text      string `json:"text"`
	Due       int64  `json:"due"`
	Timed     bool   `json:"timed"`
	Owner     string `json:"owner,omitempty"`
	Overdue   bool   `json:"overdue,omitempty"`
}

// todoGroup is a day's to-dos, or those with no date, under the pages they are on.
type todoGroup struct {
	// Key names the day, as 2026-11-20, or doc.TodoNoDateKey: the app shows a day's to-dos by it.
	Key string `json:"key"`
	// Section is where the day falls: "overdue" for a day gone by, "today", "upcoming", or
	// "no_date".
	Section string `json:"section"`
	// Title names the day, as Today, Tomorrow or Nov 20.
	Title string `json:"title"`
	// Date is the day's full date, as Monday, November 23, 2026, or empty for the to-dos with no
	// date.
	Date string `json:"date,omitempty"`
	// Count is how many to-dos are left on the day.
	Count int `json:"count"`
	// Pages are the day's to-dos under their pages, each page where its first to-do is and its
	// to-dos in the day's order.
	Pages []pageTodos `json:"pages"`
}

// pageTodos is a page's to-dos on a day.
type pageTodos struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Todos []todo `json:"todos"`
}

var todoSections = [...]string{doc.TodoOverdue: "overdue", doc.TodoToday: "today", doc.TodoUpcoming: "upcoming", doc.TodoNoDate: "no_date"}

// Todos lists the to-dos a day at a time, under the pages they are on, each due at its time in
// zone, the zone the device is in now, or one offset seconds east of UTC: a due is a time on the
// clock wherever the device is, and the core keeps the zone it opened in.
func (c *Core) Todos(zone string, offset int) (_ string, err error) {
	defer catch(&err)
	return c.todosAt(time.Now(), location(zone, offset))
}

func (c *Core) todosAt(now time.Time, loc *time.Location) (string, error) {
	days, clock, err := c.todoDays(now, loc)
	if err != nil {
		return "", err
	}
	m := locales.In(i18n.Locale())
	out := make([]todoGroup, 0, len(days))
	for _, d := range days {
		out = append(out, todoGroupOf(m, d, clock, loc))
	}
	return encode(out)
}

// TodoDay is the day key names as Todos lists it, in zone or one offset seconds east of UTC as
// for Todos, or that day with no to-dos once none is left on it, so its title and date still
// show.
func (c *Core) TodoDay(key, zone string, offset int) (_ string, err error) {
	defer catch(&err)
	return c.todoDayAt(key, time.Now(), location(zone, offset))
}

func (c *Core) todoDayAt(key string, now time.Time, loc *time.Location) (string, error) {
	days, clock, err := c.todoDays(now, loc)
	if err != nil {
		return "", err
	}
	return encode(todoGroupOf(locales.In(i18n.Locale()), doc.TodoDayAt(days, key, clock), clock, loc))
}

// todoDays is the to-dos a day at a time, and the time it is on the clock of the zone the core
// opened in: the dues are read in that zone, so the days are made at the time there that the
// device's clock in loc shows.
func (c *Core) todoDays(now time.Time, loc *time.Location) ([]doc.TodoDay[store.PageTodo], time.Time, error) {
	var todos []store.PageTodo
	var err error
	c.q.do(func() { todos, err = c.core.Store.Todos() })
	if err != nil {
		return nil, time.Time{}, err
	}
	clock := core.OnClock(now.In(loc), time.Local)
	return doc.TodoDays(todos, func(t store.PageTodo) doc.Todo { return t.Todo }, clock), clock, nil
}

// todoGroupOf is a day of to-dos as the app shows it, at clock, each due at its time in loc.
func todoGroupOf(m locales.Messages, d doc.TodoDay[store.PageTodo], clock time.Time, loc *time.Location) todoGroup {
	group := todoGroup{Key: d.Key(), Section: todoSections[d.Section], Title: m.TodosSectionNoDate(), Count: len(d.Todos), Pages: []pageTodos{}}
	if !d.Day.IsZero() {
		group.Title = doc.DueLabel(m, d.Day, false, clock)
		group.Date = m.TodosDayDate(d.Day)
	}
	for _, todos := range d.ByPage(func(t store.PageTodo) string { return t.PageID }) {
		page := pageTodos{ID: todos[0].PageID, Title: todos[0].PageTitle, Todos: make([]todo, 0, len(todos))}
		for _, t := range todos {
			due := t.Due
			if !due.IsZero() {
				due = core.OnClock(due, loc)
			}
			page.Todos = append(page.Todos, todo{
				PageID: t.PageID, PageTitle: t.PageTitle, BlockID: t.BlockID, Text: t.Text, Due: millis(due), Timed: t.Timed, Owner: t.Owner,
				Overdue: !t.Due.IsZero() && doc.Overdue(t.Due, t.Timed, clock),
			})
		}
		group.Pages = append(group.Pages, page)
	}
	return group
}

// CheckTodo checks off the to-do blockID of a page, or back on with checked false, as its
// checkbox on its day's list of to-dos does: the page open in an editor shows it at once.
func (c *Core) CheckTodo(pageID, blockID string, checked bool) (err error) {
	defer catch(&err)
	c.q.do(func() {
		err = c.editContent(pageID, func(content *doc.Content) bool {
			n := content.Find(blockID)
			if n == nil || n.Type != "todo_list" || n.Bool("checked") == checked {
				return false
			}
			n.Set("checked", checked)
			return true
		})
	})
	return err
}

func (c *Core) Settings() (_ string, err error) {
	defer catch(&err)
	var s store.Settings
	c.q.do(func() { s = c.core.Store.Settings() })
	return encode(s)
}

func (c *Core) SaveSettings(settings string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		s := c.core.Store.Settings()
		all := s.AllPagesOffline
		if err = json.Unmarshal([]byte(settings), &s); err != nil {
			return
		}
		keepAll := s.AllPagesOffline
		s.AllPagesOffline = all
		if err = c.core.Store.SaveSettings(s); err == nil && keepAll != all {
			c.core.SetAllPagesOffline(keepAll)
		}
	})
	return err
}
