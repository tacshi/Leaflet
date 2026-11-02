package mobile

import (
	"encoding/json"
	"log"
	"time"

	"leaflet/internal/store"
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

// tagRow is a tag and how many pages have it.
type tagRow struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// library is what the sidebar shows, as Library returns it: the
// pages, templates among them, the folders and the tags. Pages of another
// space are those shared with the user.
type library struct {
	SpaceID string      `json:"space_id"`
	Pages   []pageRow   `json:"pages"`
	Folders []folderRow `json:"folders"`
	Tags    []tagRow    `json:"tags"`
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
		lib.Pages = make([]pageRow, 0, len(pages)+len(templates))
		for _, p := range append(pages, templates...) {
			lib.Pages = append(lib.Pages, c.rowOf(p))
		}
		var folders []store.Folder
		if folders, err = st.Folders(); err != nil {
			return
		}
		lib.Folders = make([]folderRow, 0, len(folders))
		for _, f := range folders {
			lib.Folders = append(lib.Folders, folderRow{
				ID: f.ID, ParentID: f.ParentFolderID, Name: f.Name, SortKey: f.SortKey, DefaultTemplateID: f.DefaultTemplateID,
			})
		}
		var tags []store.TagCount
		if tags, err = st.Tags(); err != nil {
			return
		}
		lib.Tags = make([]tagRow, 0, len(tags))
		for _, t := range tags {
			lib.Tags = append(lib.Tags, tagRow{Tag: t.Name, Count: t.Count})
		}
	})
	if err != nil {
		return "", err
	}
	return encode(lib)
}

func (c *Core) rowOf(p store.Page) pageRow {
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	return pageRow{
		ID: p.ID, SpaceID: p.SpaceID, ParentID: p.ParentID, FolderID: p.FolderID,
		Title: p.Title, Icon: p.Icon, SortKey: p.SortKey, UpdatedAt: millis(p.UpdatedAt),
		KeepOffline: p.KeepOffline, Template: p.Template, Tags: tags, CanEdit: c.core.Store.CanEdit(p),
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
			// The page is made: a card that can't be saved leaves it be.
			if lerr := c.linkSubpage(n.ParentID, p.ID); lerr != nil {
				log.Printf("page %s: link in %s: %v", p.ID, n.ParentID, lerr)
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
// level, with neither), after afterID among its siblings, or first with
// "". As on the desktop, a parent it leaves loses its card, and one it
// goes into gets one at its end.
func (c *Core) MovePage(id, parentID, folderID, afterID string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		old := ""
		if p, perr := c.core.Store.Page(id); perr == nil {
			old = p.ParentID
		}
		if err = c.core.Store.MovePage(id, parentID, folderID, afterID); err != nil || old == parentID {
			return
		}
		if old != "" {
			if lerr := c.unlinkSubpage(old, id); lerr != nil {
				log.Printf("page %s: unlink from %s: %v", id, old, lerr)
			}
		}
		if parentID != "" {
			if lerr := c.linkSubpage(parentID, id); lerr != nil {
				log.Printf("page %s: link in %s: %v", id, parentID, lerr)
			}
		}
	})
	return err
}

// DeletePage deletes a page and its subpages, and the page's card in its
// parent, returning their IDs as a JSON array for RestorePages to undo it,
// the card going back where it was. The edits of those open, in any
// window, are saved first, for Undo to bring them back with them; a page
// one of whose edits can't be saved is not deleted.
func (c *Core) DeletePage(id string) (_ string, err error) {
	defer catch(&err)
	var ids []string
	c.q.do(func() {
		for pid, p := range c.pages {
			if c.within(pid, id) && !p.live.Save() {
				err = errNotSaved()
				return
			}
		}
		// Its card leaves its parent, rather than staying to say the page
		// is gone.
		c.unlinkDeleted(id)
		if ids, err = c.core.Store.DeletePage(id); err != nil {
			// The page stays, and so does its card.
			c.relinkRestored([]string{id})
			return
		}
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

// within reports whether page id is root or one of its subpages, however
// deep.
func (c *Core) within(id, root string) bool {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		if id == root {
			return true
		}
		seen[id] = true
		p, err := c.core.Store.Page(id)
		if err != nil {
			return false
		}
		id = p.ParentID
	}
	return false
}

// RestorePages brings back pages DeletePage deleted, given ids as it
// returned them.
func (c *Core) RestorePages(ids string) (err error) {
	defer catch(&err)
	var list []string
	if err := json.Unmarshal([]byte(ids), &list); err != nil {
		return err
	}
	c.q.do(func() {
		if err = c.core.Store.RestorePages(list); err == nil {
			c.relinkRestored(list)
		}
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
	c.q.do(func() { err = c.core.Store.SetKeepOffline(id, keep) })
	return err
}

// CreateFolder creates a folder, returning it as a folderRow.
func (c *Core) CreateFolder(name string) (_ string, err error) {
	defer catch(&err)
	var f store.Folder
	c.q.do(func() { f, err = c.core.Store.CreateFolder(name) })
	if err != nil {
		return "", err
	}
	return encode(folderRow{ID: f.ID, ParentID: f.ParentFolderID, Name: f.Name, SortKey: f.SortKey})
}

// RenameFolder renames a folder.
func (c *Core) RenameFolder(id, name string) (err error) {
	defer catch(&err)
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
	var hits []store.SearchHit
	c.q.do(func() { hits, err = c.core.Store.Search(query, limit) })
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

// Todos returns the open to-dos with a due date or an owner, as a JSON
// array of todo.
func (c *Core) Todos() (_ string, err error) {
	defer catch(&err)
	var todos []store.PageTodo
	c.q.do(func() { todos, err = c.core.Store.Todos() })
	if err != nil {
		return "", err
	}
	out := make([]todo, 0, len(todos))
	for _, t := range todos {
		out = append(out, todo{PageID: t.PageID, PageTitle: t.PageTitle, BlockID: t.BlockID, Text: t.Text, Due: millis(t.Due), Timed: t.Timed, Owner: t.Owner})
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
		if err = json.Unmarshal([]byte(settings), &s); err == nil {
			err = c.core.Store.SaveSettings(s)
		}
	})
	return err
}
