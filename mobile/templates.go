package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/starters"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Templates start pages, as on the desktop (internal/app/templates.go): the
// user's own, and the starters that come with Leaflet. A template is
// named by a key: "template:" and its page's ID, or "starter:" and the
// starter's.

// templateRow is a template as Templates lists it.
type templateRow struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	// Hint tells it apart: its first line, or a starter's description.
	Hint string `json:"hint"`
	// Icon is a Lucide icon's name; "layout-template" for the user's.
	Icon string `json:"icon"`
	// PageID is the template's page, for the user's own.
	PageID string `json:"page_id,omitempty"`
}

// a choice is a template to start a page from: one of the user's, or a
// starter.
type choice struct {
	page    *store.Page
	starter *starters.Starter
}

func (t choice) key() string {
	if t.page != nil {
		return "template:" + t.page.ID
	}
	return "starter:" + t.starter.Key
}

// name is the title a page takes from the template, "" for none.
func (t choice) name() string {
	if t.page != nil {
		return t.page.Title
	}
	return t.starter.Title()
}

// deletedStartersKey keeps the keys of the starters deleted on this
// device, a JSON array, where the desktop keeps them.
const deletedStartersKey = "templates.deleted_starters"

func (c *Core) deletedStarters() map[string]bool {
	gone := map[string]bool{}
	var keys []string
	if v, ok := c.core.Store.Value(deletedStartersKey); ok {
		_ = json.Unmarshal([]byte(v), &keys)
	}
	for _, k := range keys {
		gone[k] = true
	}
	return gone
}

func (c *Core) setStarterDeleted(key string, deleted bool) error {
	gone := c.deletedStarters()
	if deleted {
		gone[key] = true
	} else {
		delete(gone, key)
	}
	b, _ := json.Marshal(slices.Sorted(maps.Keys(gone)))
	return c.core.Store.SetValue(deletedStartersKey, string(b))
}

// choices are the user's templates, by title, then the starters not
// deleted that they have no template of the same title for. Run on the
// core's queue.
func (c *Core) choices() ([]choice, error) {
	all, err := c.core.Store.Templates()
	if err != nil {
		return nil, err
	}
	var roots []store.Page
	for _, t := range all {
		if t.ParentID == "" {
			roots = append(roots, t)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool {
		return strings.ToLower(roots[i].DisplayTitle()) < strings.ToLower(roots[j].DisplayTitle())
	})
	titles := map[string]bool{}
	var out []choice
	for i := range roots {
		titles[strings.ToLower(strings.TrimSpace(roots[i].Title))] = true
		out = append(out, choice{page: &roots[i]})
	}
	gone := c.deletedStarters()
	ss := starters.All()
	for i := range ss {
		if !gone[ss[i].Key] && !titles[strings.ToLower(ss[i].Title())] {
			out = append(out, choice{starter: &ss[i]})
		}
	}
	return out, nil
}

func (c *Core) choice(key string) (choice, error) {
	all, err := c.choices()
	if err != nil {
		return choice{}, err
	}
	for _, t := range all {
		if t.key() == key {
			return t, nil
		}
	}
	return choice{}, errNoTemplate
}

var (
	errNoTemplate = errors.New("no such template")
	errNoFolder   = errors.New("no such folder")
)

// hint is the first line of a template's text, or how many subpages it
// has when it has no text.
func (c *Core) hint(t store.Page) string {
	h := locales.TemplateHintEmpty()
	all, _ := c.core.Store.Templates()
	kids := 0
	for _, k := range all {
		if k.ParentID == t.ID {
			kids++
		}
	}
	if kids > 0 {
		h = locales.TemplateHintSubpages(kids)
	}
	if row, err := c.core.Store.ContentRow(t.ID); err == nil {
		for _, l := range strings.Split(row.PlainText, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				return l
			}
		}
	}
	return h
}

// Templates returns the templates to start a page from, as a JSON array
// of templateRow.
func (c *Core) Templates() (string, error) {
	var out []templateRow
	var err error
	c.q.do(func() {
		var all []choice
		if all, err = c.choices(); err != nil {
			return
		}
		out = make([]templateRow, 0, len(all))
		for _, t := range all {
			row := templateRow{Key: t.key(), Title: store.TitleOr(t.name())}
			if t.page != nil {
				row.Icon, row.Hint, row.PageID = "layout-template", c.hint(*t.page), t.page.ID
			} else {
				row.Icon, row.Hint = t.starter.Icon, t.starter.Hint()
			}
			out = append(out, row)
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

// ownTemplate is the user's template for t: t itself, or for a starter a
// template made from it, which then takes the starter's place. Run on the
// core's queue.
func (c *Core) ownTemplate(t choice) (store.Page, error) {
	if t.page != nil {
		return *t.page, nil
	}
	p, err := c.core.Store.CreatePage(store.NewPageOptions{
		Title: t.starter.Title(), Template: true, Content: doc.FromBlocks(t.starter.Nodes()),
	})
	if err != nil {
		return store.Page{}, err
	}
	return p, c.setStarterDeleted(t.starter.Key, true)
}

// EditTemplate returns the page of the template key to open and change;
// a starter becomes the user's own first.
func (c *Core) EditTemplate(key string) (string, error) {
	var id string
	var err error
	c.q.do(func() {
		var t choice
		if t, err = c.choice(key); err != nil {
			return
		}
		var p store.Page
		p, err = c.ownTemplate(t)
		id = p.ID
	})
	return id, err
}

// SetStarterDeleted deletes a starter on this device, or brings it back,
// as Undo does. The user's own templates are deleted as pages are.
func (c *Core) SetStarterDeleted(key string, deleted bool) error {
	var err error
	c.q.do(func() { err = c.setStarterDeleted(strings.TrimPrefix(key, "starter:"), deleted) })
	return err
}

// SetDefaultTemplate chooses the template that new pages under page
// pageID, or at the top of folder folderID, start from: key, or "" for
// blank pages. A starter becomes the user's own template first. It returns
// the message that says so.
func (c *Core) SetDefaultTemplate(pageID, folderID, key string) (string, error) {
	var msg string
	var err error
	c.q.do(func() {
		st := c.core.Store
		id, title := "", ""
		if key != "" {
			var t choice
			if t, err = c.choice(key); err != nil {
				return
			}
			var p store.Page
			if p, err = c.ownTemplate(t); err != nil {
				return
			}
			id, title = p.ID, p.DisplayTitle()
		}
		place := ""
		if pageID != "" {
			var p store.Page
			if p, err = st.Page(pageID); err != nil {
				return
			}
			place = p.DisplayTitle()
			err = st.SetPageDefaultTemplate(pageID, id)
		} else {
			var folders []store.Folder
			if folders, err = st.Folders(); err != nil {
				return
			}
			err = errNoFolder
			for _, f := range folders {
				if f.ID == folderID {
					place = f.Name
					err = st.SetFolderDefaultTemplate(f, id)
				}
			}
		}
		if err != nil {
			return
		}
		msg = locales.TemplateDefaultCleared(place)
		if id != "" {
			msg = locales.TemplateDefaultSet(place, title)
		}
	})
	return msg, err
}

// defaultTemplate is the key of the template new pages under parent, or at
// the top of folder, start from, or "". Run on the core's queue.
func (c *Core) defaultTemplate(parent, folder string) string {
	id := ""
	switch {
	case parent != "":
		if p, err := c.core.Store.Page(parent); err == nil {
			id = p.DefaultTemplateID
		}
	case folder != "":
		folders, _ := c.core.Store.Folders()
		for _, f := range folders {
			if f.ID == folder {
				id = f.DefaultTemplateID
			}
		}
	}
	if id == "" {
		return ""
	}
	if _, err := c.choice("template:" + id); err != nil {
		return ""
	}
	return "template:" + id
}

// isEmpty reports whether content is a blank page: one empty paragraph,
// or nothing.
func isEmpty(content *doc.Content) bool {
	ch := content.Children
	return len(ch) == 0 || len(ch) == 1 && ch[0].Type == "paragraph" && ch[0].Delta().Len() == 0 && len(ch[0].Children) == 0
}

// content is page id's content now: the app's, while open, else stored.
// Run on the core's queue.
func (c *Core) content(id string) *doc.Content {
	if p := c.pages[id]; p != nil {
		return p.cur
	}
	if content := c.core.Store.Content(id); content != nil {
		return content
	}
	return doc.Empty()
}

// ApplyTemplate fills page pageID, while it is empty, from template key:
// its blocks, and copies of its subpages under the page. An untitled page
// takes the template's title, which ApplyTemplate returns; "" for none.
// It runs off the core's queue while a template's files come down.
func (c *Core) ApplyTemplate(pageID, key string) (string, error) {
	return c.useTemplate(pageID, "", key, false)
}

// InsertTemplate puts template key's blocks in page pageID after block
// blockID, or in its place when it is an empty paragraph, as the / menu's
// templates do, with copies of its subpages under the page. An empty page
// is filled as ApplyTemplate fills it, and the title it took returned.
func (c *Core) InsertTemplate(pageID, blockID, key string) (string, error) {
	return c.useTemplate(pageID, blockID, key, true)
}

// useTemplate fills empty page pageID from template key, as ApplyTemplate
// does, or with insert puts the template's blocks in a page with something
// in it already, at block blockID, as InsertTemplate does.
func (c *Core) useTemplate(pageID, blockID, key string, insert bool) (string, error) {
	var t choice
	var err error
	c.q.do(func() {
		if t, err = c.choice(key); err == nil && !c.canEdit(pageID) {
			err = errors.New("the page can't be changed")
		}
	})
	if err != nil {
		return "", err
	}
	if t.page != nil {
		if err := c.fetchNeeds(t.page.ID); err != nil {
			return "", err
		}
	}
	var title string
	c.q.do(func() {
		empty := isEmpty(c.content(pageID))
		if !empty && !insert {
			return
		}
		var blocks []*doc.Node
		if t.starter != nil {
			blocks = t.starter.Nodes()
		} else {
			var copied *doc.Content
			if copied, err = c.core.Store.CopyInto(t.page.ID, pageID); err != nil {
				err = copyError(err)
				return
			}
			blocks = copied.Children
		}
		if len(blocks) > 0 {
			if err = c.editContent(pageID, func(content *doc.Content) bool {
				if isEmpty(content) {
					content.Children = blocks
					return true
				}
				return insertAt(content, blockID, blocks)
			}); err != nil {
				return
			}
		}
		if !empty {
			return
		}
		p, perr := c.core.Store.Page(pageID)
		if perr != nil || strings.TrimSpace(p.Title) != "" || t.name() == "" {
			return
		}
		if err = c.core.Store.RenamePage(pageID, t.name()); err == nil {
			title = t.name()
		}
	})
	return title, err
}

// insertAt puts blocks after block id, or in its place when it is an
// empty paragraph, as the desktop editor's InsertBlocks does; at the end
// of the page when it has no such block.
func insertAt(content *doc.Content, id string, blocks []*doc.Node) bool {
	n := content.Find(id)
	if n == nil {
		return content.Insert(doc.Position{At: "end"}, blocks...) == nil
	}
	if content.Insert(doc.Position{After: n.ID}, blocks...) != nil {
		return false
	}
	if n.Type == "paragraph" && n.Delta().Len() == 0 && len(n.Children) == 0 {
		content.Remove(n.ID)
	}
	return true
}

// DuplicatePage copies a page and its subpages next to it, links the copy
// after the page's card in its parent, and returns the copy as a pageRow.
// A page shared with the user that they can't add to the parent of is
// copied into their own space.
func (c *Core) DuplicatePage(id string) (string, error) {
	return c.copyPage(id, func(p store.Page) store.CopyOptions {
		o := store.CopyOptions{ParentID: p.ParentID, FolderID: p.FolderID, AfterID: p.ID, Title: copyTitle(p.Title)}
		if p.SpaceID != c.core.Store.Session().SpaceID && (p.ParentID == "" || !c.canEdit(p.ParentID)) {
			o = store.CopyOptions{Title: o.Title}
		}
		return o
	})
}

// SaveAsTemplate keeps a copy of a page, with its subpages, as a template,
// and returns it as a pageRow.
func (c *Core) SaveAsTemplate(id string) (string, error) {
	return c.copyPage(id, func(p store.Page) store.CopyOptions {
		return store.CopyOptions{Title: p.Title, Template: true}
	})
}

func (c *Core) copyPage(id string, options func(store.Page) store.CopyOptions) (string, error) {
	// A copy goes over the edits the app has sent.
	c.q.do(func() {
		for _, p := range c.pages {
			p.live.Save()
		}
	})
	if err := c.fetchNeeds(id); err != nil {
		return "", err
	}
	var row pageRow
	var err error
	c.q.do(func() {
		st := c.core.Store
		var p store.Page
		if p, err = st.Page(id); err != nil {
			return
		}
		var cp store.Page
		if cp, err = st.CopyPage(id, options(p)); err != nil {
			err = copyError(err)
			return
		}
		row = c.rowOf(cp)
		if cp.ParentID != "" && !cp.Template {
			if lerr := c.linkSubpageAfter(cp.ParentID, cp.ID, p.ID); lerr != nil {
				err = lerr
			}
		}
		c.core.Sync.Kick()
	})
	if err != nil {
		return "", err
	}
	return encode(row)
}

func copyTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	return locales.TemplateCopyTitle(title)
}

// errFilesUploading says a copy waits for files still going up from
// another device.
var errFilesUploading = errors.New(locales.TemplateFilesUploading())

// copyError says why a copy was not made: a file or a recording's audio
// not on this device, as one still uploading, or what else failed.
func copyError(err error) error {
	if errors.Is(err, store.ErrCopyNotHere) || remote.IsNotFound(err) || errors.Is(err, icloud.ErrNotReady) || errors.Is(err, fs.ErrNotExist) {
		return errors.New(locales.TemplateFilesUploading())
	}
	return err
}

// fetchNeeds brings to this device what copying page id, with its
// subpages, needs first (store.CopyNeeds), off the core's queue.
func (c *Core) fetchNeeds(id string) error {
	var needs []store.CopyNeed
	var err error
	canFetch := false
	c.q.do(func() {
		needs, err = c.core.Store.CopyNeeds(id)
		canFetch = c.core.Sync.CanFetch()
	})
	if err != nil {
		return err
	}
	for _, n := range needs {
		if n.Path == "" || !canFetch {
			return errFilesUploading
		}
	}
	for _, n := range needs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		data, ferr := c.core.Sync.Fetch(ctx, n.Bucket, n.Path)
		cancel()
		if ferr != nil {
			return copyError(ferr)
		}
		c.q.do(func() {
			if n.FileID != "" {
				_, err = c.core.Store.DownloadedFile(n.FileID, data)
			} else {
				_, err = c.core.Store.DownloadedAudio(n.RecordingID, data)
			}
		})
		if err != nil {
			return err
		}
	}
	return nil
}
