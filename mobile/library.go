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

// pageRow is a page as the library lists it.
type pageRow struct {
	ID       string `json:"id"`
	SpaceID  string `json:"space_id"`
	ParentID string `json:"parent_id,omitempty"`
	FolderID string `json:"folder_id,omitempty"`
	Title    string `json:"title"`
	Icon     string `json:"icon,omitempty"`
	SortKey  string `json:"sort_key"`
	// UpdatedAt is in milliseconds since 1970.
	UpdatedAt   int64    `json:"updated_at"`
	KeepOffline bool     `json:"keep_offline"`
	Template    bool     `json:"template"`
	Tags        []string `json:"tags"`
	CanEdit     bool     `json:"can_edit"`
	// CreatedBy is the user who made the page, for its byline.
	CreatedBy string `json:"created_by,omitempty"`
	// DefaultTemplateID is the template pages made under it start from.
	DefaultTemplateID string `json:"default_template_id,omitempty"`
}

// folderRow is a folder as the library lists it.
type folderRow struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Name     string `json:"name"`
	SortKey  string `json:"sort_key"`
	// DefaultTemplateID is the template pages made in it start from.
	DefaultTemplateID string `json:"default_template_id,omitempty"`
}

// TagColor returns a tag's color, as hex, as the desktop colors it: the
// same on every device, whatever the name's case.
func TagColor(name string) string {
	defer catch(nil)
	return doc.TagColor(name)
}

// tagRow is a tag, how many pages have it, and their IDs, by title.
type tagRow struct {
	Tag   string   `json:"tag"`
	Count int      `json:"count"`
	Pages []string `json:"pages"`
}

// library is what the sidebar shows, as Library returns it: the pages,
// templates among them, the folders of the user's space in order, and
// the tags, by name. Pages of another space are those shared with the
// user. The rest is the tree of them, as the desktop's sidebar arranges
// it (library.Arrange), by page ID, each list in order: the user's pages
// at the top level in no folder, those of each folder, those under each
// page, and those shared with the user whose parent the user has not.
// fileRow is a file of the sidebar.
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
	// Files are those uploaded to folders, the sidebar's, not the ones
	// pages hold.
	Files    []fileRow           `json:"files"`
	Tags     []tagRow            `json:"tags"`
	Top      []string            `json:"top"`
	InFolder map[string][]string `json:"in_folder"`
	Children map[string][]string `json:"children"`
	Shared   []string            `json:"shared"`
}

// Library returns the library, as a library in JSON.
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
		// Who may change each page is worked out from one read of the
		// pages and their members, not a read of each page's parents.
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
			// Files of a folder that was deleted are at the top level.
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

// pageIDs are the IDs of pages, in their order.
func pageIDs(pages []store.Page) []string {
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.ID)
	}
	return out
}

// idsBy is pages by a key, as their IDs.
func idsBy(pages map[string][]store.Page) map[string][]string {
	out := make(map[string][]string, len(pages))
	for k, ps := range pages {
		out[k] = pageIDs(ps)
	}
	return out
}

func (c *Core) rowOf(p store.Page) pageRow { return rowWith(p, c.core.Store.CanEdit(p)) }

// rowWith is a page as the library lists it, which the user may change or
// not as canEdit says.
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

// newPage is a page to create, as CreatePage takes it.
type newPage struct {
	ParentID string   `json:"parent_id"`
	FolderID string   `json:"folder_id"`
	AfterID  string   `json:"after_id"`
	Title    string   `json:"title"`
	Tags     []string `json:"tags"`
	// Link puts a card for the page at the end of its parent, as a
	// subpage made from the library gets.
	Link bool `json:"link"`
	// Start fills the page from the template its parent or folder names
	// for new pages, as a page made from the library or a page's Add
	// subpage starts.
	Start bool `json:"start"`
}

// CreatePage creates a page from page, a newPage as JSON, and returns it as a
// pageRow.
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
			// The page is made: a card that can't be saved says so, as on
			// the desktop, and leaves it be.
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
		// The page is made: a template that can't fill it leaves it blank.
		if title, terr := c.ApplyTemplate(row.ID, start); terr != nil {
			log.Printf("page %s: template %s: %v", row.ID, start, terr)
		} else if title != "" {
			row.Title = title
		}
	}
	return encode(row)
}

// RenamePage renames a page.
func (c *Core) RenamePage(id, title string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.Store.RenamePage(id, title) })
	return err
}

// MovePage moves a page under parentID or into folderID (or to the top
// level, with neither), after afterID among its siblings, or last with
// "". As on the desktop, a parent it leaves loses its card, and one it
// goes into gets one at its end (core.Pages.MovePage).
func (c *Core) MovePage(id, parentID, folderID, afterID string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		err = c.pageCore().MovePage(id, parentID, func() error {
			return c.core.Store.MovePage(id, parentID, folderID, afterID)
		})
	})
	return err
}

// MovePageBefore moves page id as MovePage does, before page beforeID
// among its new siblings, or first without one.
func (c *Core) MovePageBefore(id, parentID, folderID, beforeID string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		err = c.pageCore().MovePage(id, parentID, func() error {
			return c.core.Store.MovePageBefore(id, parentID, folderID, beforeID)
		})
	})
	return err
}

// MoveTargets returns the pages page id can move under, as the desktop's
// Move to dialog offers them, the one changed last first, as a JSON array
// of their IDs: those of its space the user may change, but templates,
// the page itself and the pages inside it.
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

// DeletePage deletes a page and its subpages, and the page's card in its
// parent (core.Pages.DeletePage), returning their IDs as a JSON array for
// RestorePages to undo it, the card going back where it was. The edits of
// those open, in any window, are saved first, for Undo to bring them back
// with them; a page one of whose edits can't be saved is not deleted.
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

// RestorePages brings back pages DeletePage deleted, given ids as it
// returned them, and the page's card where it was.
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

// SetPageTags sets a page's tags, given as a JSON array.
func (c *Core) SetPageTags(id, tags string) (err error) {
	defer catch(&err)
	var list []string
	if err := json.Unmarshal([]byte(tags), &list); err != nil {
		return err
	}
	c.q.do(func() { err = c.core.Store.SetPageTags(id, list) })
	return err
}

// RenameTag renames a tag on every page the user may change, templates
// too, merging it into a tag of the new name a page has already, as the
// desktop's Tags tab does. It returns the name as kept, "#" and extra
// spaces gone; a name of nothing changes nothing and returns "".
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

// DeleteTag takes a tag off every page the user may change, templates
// too, and returns what RestoreTags takes to put each page's tags back as
// they were, for Undo.
func (c *Core) DeleteTag(tag string) (_ string, err error) {
	defer catch(&err)
	var before map[string][]string
	c.q.do(func() { before, err = c.core.Store.DeleteTag(tag) })
	if err != nil {
		return "", err
	}
	return encode(before)
}

// RestoreTags puts back the pages' tags as DeleteTag returned them.
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

// SetKeepOffline keeps a page's content on this device, or lets it go
// until the page is opened.
func (c *Core) SetKeepOffline(id string, keep bool) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.SetKeepOffline(id, keep) })
	return err
}

// CreateFolder creates a folder, returning it as a folderRow. Its name
// is kept without the spaces around it.
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

// RenameFolder renames a folder, as the desktop's Rename folder does:
// without the spaces around the name, and a name left empty changes
// nothing.
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

// DeleteFolder deletes a folder.
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

// searchHit is one search result.
type searchHit struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	PageID  string `json:"page_id"`
	BlockID string `json:"block_id,omitempty"`
	Snippet string `json:"snippet"`
}

// Search searches titles, text, transcripts and comments, returning up to
// limit hits as a JSON array of searchHit. #tag words keep to those tags.
func (c *Core) Search(query string, limit int) (_ string, err error) {
	defer catch(&err)
	return c.SearchCancellable(query, limit, 0)
}

// SearchCancellable is Search, which the app may give up on by token, as
// Cancel says, 0 for never: a search typed past, given up on as the next
// begins, stops before it runs, or as it runs, and returns an error, so
// the core's queue is the newer one's at once.
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

// todo is an open to-do of a page.
type todo struct {
	PageID    string `json:"page_id"`
	PageTitle string `json:"page_title"`
	BlockID   string `json:"block_id"`
	Text      string `json:"text"`
	// Due is in milliseconds since 1970, or 0 for none; Timed says
	// whether it has a time of day.
	Due   int64  `json:"due"`
	Timed bool   `json:"timed"`
	Owner string `json:"owner,omitempty"`
}

// todoGroup is a section of the To-dos tab, as the desktop's: its name,
// its key ("overdue", "today", "upcoming" or "no_date") and its to-dos,
// by date.
type todoGroup struct {
	Section string `json:"section"`
	Title   string `json:"title"`
	Todos   []todo `json:"todos"`
}

// todoSections are the To-dos tab's sections, by doc.TodoSection: their
// keys and names.
var todoSections = [...]struct {
	key   string
	title func() string
}{
	doc.TodoOverdue:  {"overdue", locales.TodosSectionOverdue},
	doc.TodoToday:    {"today", locales.CommonToday},
	doc.TodoUpcoming: {"upcoming", locales.TodosSectionUpcoming},
	doc.TodoNoDate:   {"no_date", locales.TodosSectionNoDate},
}

// Todos returns the open to-dos with a due date or an owner, in the
// To-dos tab's sections as the desktop has them now, as a JSON array of
// todoGroup: overdue, today's, the coming ones, each by date, and those
// with no date. A section with none is left out.
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

// Settings returns the settings on this device as JSON.
func (c *Core) Settings() (_ string, err error) {
	defer catch(&err)
	var s store.Settings
	c.q.do(func() { s = c.core.Store.Settings() })
	return encode(s)
}

// SaveSettings keeps settings, as Settings returns them.
func (c *Core) SaveSettings(settings string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		s := c.core.Store.Settings()
		all := s.AllPagesOffline
		if err = json.Unmarshal([]byte(settings), &s); err != nil {
			return
		}
		// Keeping all pages changes through the core, which brings the
		// pages kept from now on.
		keepAll := s.AllPagesOffline
		s.AllPagesOffline = all
		if err = c.core.Store.SaveSettings(s); err == nil && keepAll != all {
			c.core.SetAllPagesOffline(keepAll)
		}
	})
	return err
}
