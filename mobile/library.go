package mobile

import (
	"encoding/json"
	"log"
	"slices"
	"strings"
	"time"

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
	SpaceID  string              `json:"space_id"`
	Pages    []pageRow           `json:"pages"`
	Folders  []folderRow         `json:"folders"`
	Files    []fileRow           `json:"files"`
	Tags     []tagRow            `json:"tags"`
	Top      []string            `json:"top"`
	InFolder map[string][]string `json:"in_folder"`
	Children map[string][]string `json:"children"`
	Shared   []string            `json:"shared"`
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
	name = strings.TrimSpace(name)
	var f store.Folder
	c.q.do(func() { f, err = c.core.Store.CreateFolder(name) })
	if err != nil {
		return "", err
	}
	return encode(folderRow{ID: f.ID, ParentID: f.ParentFolderID, Name: f.Name, SortKey: f.SortKey})
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
}

type todoGroup struct {
	Section string `json:"section"`
	Title   string `json:"title"`
	Todos   []todo `json:"todos"`
}

var todoSections = [...]struct {
	key   string
	title func() string
}{
	doc.TodoOverdue:  {"overdue", locales.TodosSectionOverdue},
	doc.TodoToday:    {"today", locales.CommonToday},
	doc.TodoUpcoming: {"upcoming", locales.TodosSectionUpcoming},
	doc.TodoNoDate:   {"no_date", locales.TodosSectionNoDate},
}

func (c *Core) Todos() (_ string, err error) {
	defer catch(&err)
	var todos []store.PageTodo
	c.q.do(func() { todos, err = c.core.Store.Todos() })
	if err != nil {
		return "", err
	}
	out := []todoGroup{}
	for _, g := range doc.TodoGroups(todos, func(t store.PageTodo) doc.Todo { return t.Todo }, time.Now()) {
		section := todoSections[g.Section]
		group := todoGroup{Section: section.key, Title: section.title(), Todos: make([]todo, 0, len(g.Todos))}
		for _, t := range g.Todos {
			group.Todos = append(group.Todos, todo{
				PageID: t.PageID, PageTitle: t.PageTitle, BlockID: t.BlockID, Text: t.Text, Due: millis(t.Due), Timed: t.Timed, Owner: t.Owner,
			})
		}
		out = append(out, group)
	}
	return encode(out)
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
