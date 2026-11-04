package mobile

import (
	"errors"
	"log"
	"slices"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/templates"
	"leaflet/locales"
)

// Templates start pages, as on the desktop (internal/templates): the
// user's own, and the starters that come with Leaflet. A template is
// named by its key: "template:" and its page's ID, or "starter:" and the
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

// choices are the templates to start a page from (templates.Choices),
// and hint what tells the user's apart. Run on the core's queue.
func (c *Core) choices() ([]templates.Choice, func(store.Page) string, error) {
	st := c.core.Store
	all, err := st.Templates()
	if err != nil {
		return nil, nil, err
	}
	subpages := templates.Subpages(all)
	hint := func(t store.Page) string { return templates.Hint(st, t, subpages) }
	return templates.Choices(all, templates.DeletedStarters(st)), hint, nil
}

// choice is the template of key. Run on the core's queue.
func (c *Core) choice(key string) (templates.Choice, error) {
	all, _, err := c.choices()
	if err != nil {
		return templates.Choice{}, err
	}
	if t, ok := templates.Find(all, key); ok {
		return t, nil
	}
	return templates.Choice{}, errors.New(locales.TemplateGone())
}

// Templates returns the templates to start a page from, as a JSON array
// of templateRow.
func (c *Core) Templates() (_ string, err error) {
	defer catch(&err)
	var out []templateRow
	c.q.do(func() {
		var all []templates.Choice
		var hint func(store.Page) string
		if all, hint, err = c.choices(); err != nil {
			return
		}
		out = make([]templateRow, 0, len(all))
		for _, t := range all {
			row := templateRow{Key: t.Key(), Title: t.Title(), Hint: t.Hint(hint), Icon: t.Icon()}
			if t.Page != nil {
				row.PageID = t.Page.ID
			}
			out = append(out, row)
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

// EditTemplate returns the page of the template key to open and change;
// a starter becomes the user's own first.
func (c *Core) EditTemplate(key string) (_ string, err error) {
	defer catch(&err)
	var id string
	c.q.do(func() {
		var t templates.Choice
		if t, err = c.choice(key); err != nil {
			return
		}
		var p store.Page
		p, err = templates.Own(c.core.Store, t)
		id = p.ID
	})
	return id, err
}

// SetStarterDeleted deletes a starter on this device, or brings it back,
// as Undo does. The user's own templates are deleted as pages are.
func (c *Core) SetStarterDeleted(key string, deleted bool) (err error) {
	defer catch(&err)
	c.q.do(func() { err = templates.SetStarterDeleted(c.core.Store, strings.TrimPrefix(key, "starter:"), deleted) })
	return err
}

// SetDefaultTemplate chooses the template that new pages under page
// pageID, or at the top of folder folderID, start from: key, or "" for
// blank pages. A starter becomes the user's own template first. It returns
// the message that says so.
func (c *Core) SetDefaultTemplate(pageID, folderID, key string) (_ string, err error) {
	defer catch(&err)
	var msg string
	c.q.do(func() {
		st := c.core.Store
		var t *templates.Choice
		if key != "" {
			var ch templates.Choice
			if ch, err = c.choice(key); err != nil {
				return
			}
			t = &ch
		}
		if pageID != "" {
			var p store.Page
			if p, err = st.Page(pageID); err != nil {
				return
			}
			msg, err = templates.SetDefault(st, t, p.DisplayTitle(), func(id string) error { return st.SetPageDefaultTemplate(pageID, id) })
			return
		}
		var folders []store.Folder
		if folders, err = st.Folders(); err != nil {
			return
		}
		i := slices.IndexFunc(folders, func(f store.Folder) bool { return f.ID == folderID })
		if i < 0 {
			err = errors.New(locales.DialogFolderGone())
			return
		}
		f := folders[i]
		msg, err = templates.SetDefault(st, t, f.Name, func(id string) error { return st.SetFolderDefaultTemplate(f, id) })
	})
	return msg, err
}

// defaultTemplate is the key of the template new pages under parent, or at
// the top of folder, start from (templates.Default), or "". Run on the
// core's queue.
func (c *Core) defaultTemplate(parent, folder string) string {
	all, _, err := c.choices()
	if err != nil {
		return ""
	}
	if t, ok := templates.Default(c.core.Store, all, parent, folder); ok {
		return t.Key()
	}
	return ""
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
func (c *Core) ApplyTemplate(pageID, key string) (_ string, err error) {
	defer catch(&err)
	return c.useTemplate(pageID, "", key, false)
}

// InsertTemplate puts template key's blocks in page pageID after block
// blockID, or in its place when it is an empty paragraph, as the / menu's
// templates do, with copies of its subpages under the page. An empty page
// is filled as ApplyTemplate fills it, and the title it took returned.
func (c *Core) InsertTemplate(pageID, blockID, key string) (_ string, err error) {
	defer catch(&err)
	return c.useTemplate(pageID, blockID, key, true)
}

// useTemplate fills empty page pageID from template key, as ApplyTemplate
// does, or with insert puts the template's blocks in a page with something
// in it already, at block blockID, as InsertTemplate does. One of the
// user's templates is copied (copyOf); a starter's blocks go in at once.
func (c *Core) useTemplate(pageID, blockID, key string, insert bool) (string, error) {
	var t templates.Choice
	var err error
	c.q.do(func() {
		if t, err = c.choice(key); err != nil {
			return
		}
		if !c.canEdit(pageID) {
			err = errors.New(locales.AppErrorReadOnly())
		}
	})
	if err != nil {
		return "", err
	}
	var title string
	use := func() error {
		empty := c.content(pageID).IsEmpty()
		if !empty && !insert {
			return nil
		}
		blocks, err := templates.Blocks(c.core.Store, t, pageID)
		if err != nil {
			return templates.CopyError(err)
		}
		if len(blocks) > 0 {
			if err := c.editContent(pageID, func(content *doc.Content) bool {
				if content.IsEmpty() {
					content.Children = blocks
					return true
				}
				return insertAt(content, blockID, blocks)
			}); err != nil {
				return err
			}
		}
		if !empty {
			return nil
		}
		p, err := c.core.Store.Page(pageID)
		if err != nil || !t.Retitles(p.Title) {
			return nil
		}
		if err := c.core.Store.RenamePage(pageID, t.Name()); err != nil {
			return err
		}
		title = t.Name()
		return nil
	}
	if t.Page != nil {
		err = c.copyOf(t.Page.ID, use)
	} else {
		c.q.do(func() { err = use() })
	}
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
func (c *Core) DuplicatePage(id string) (_ string, err error) {
	defer catch(&err)
	return c.copyPage(id, func(p store.Page) store.CopyOptions { return templates.Duplicate(c.core.Store, p) })
}

// SaveAsTemplate keeps a copy of a page, with its subpages, as a template,
// and returns it as a pageRow.
func (c *Core) SaveAsTemplate(id string) (_ string, err error) {
	defer catch(&err)
	return c.copyPage(id, func(p store.Page) store.CopyOptions {
		return store.CopyOptions{Title: p.Title, Template: true}
	})
}

// copyPage copies page id, with its subpages, as options say for the page
// as it is once what the copy needs has come (copyOf), and returns the
// copy as a pageRow.
func (c *Core) copyPage(id string, options func(store.Page) store.CopyOptions) (string, error) {
	var row pageRow
	err := c.copyOf(id, func() error {
		st := c.core.Store
		p, err := st.Page(id)
		if err != nil {
			return err
		}
		cp, err := st.CopyPage(id, options(p))
		if err != nil {
			return templates.CopyError(err)
		}
		row = c.rowOf(cp)
		if cp.ParentID != "" && !cp.Template {
			// The copy is made: a card in the parent that can't be saved
			// says so, as on the desktop, and the copy stands, rather than
			// asked for again as if none were made.
			if lerr := c.linkSubpageAfter(cp.ParentID, cp.ID, p.ID); lerr != nil {
				log.Printf("page %s: link in %s: %v", cp.ID, cp.ParentID, lerr)
				c.ev.notice(locales.AppErrorLinkSubpage(lerr.Error()))
			}
		}
		c.core.Sync.KickPush()
		return nil
	})
	if err != nil {
		return "", err
	}
	return encode(row)
}

// copyOf runs fn, which makes a copy of page id with its subpages, on the
// core's queue, once what the copy needs has come to this device
// (fetchNeeds); the audio that came goes once fn has run. One copy is
// made at a time, as on the desktop, from what it fetches to the copy
// itself: another meanwhile fails, saying so, rather than take the audio
// fetched for this one from under it.
func (c *Core) copyOf(id string, fn func() error) error {
	if !c.copying.CompareAndSwap(false, true) {
		return errors.New(locales.TemplateFetchingOther())
	}
	defer c.copying.Store(false)
	var err error
	c.q.do(func() { err = c.readyToCopy(id) })
	if err != nil {
		return err
	}
	audio, err := c.fetchNeeds(id)
	if err != nil {
		return err
	}
	c.q.do(func() {
		defer c.dropFetchedAudio(audio)
		err = fn()
	})
	return err
}

// readyToCopy reports why page id, with its subpages, can't be copied
// now, as the desktop's duplicating, saving as a template and using a
// template refuse to: the edits the app has sent to one of them could not
// be saved, so the copy would go without them (the save said why), or a
// recording in one is still being made or transcribed here, so the copy
// would stay without what the recording gets once it finishes
// (templates.RecordingIn). Run on the core's queue.
func (c *Core) readyToCopy(id string) error {
	saved := true
	for pid, p := range c.pages {
		if c.core.Store.IsDescendant(pid, id) && !p.live.Save() {
			saved = false
		}
	}
	if !saved {
		return errNotSaved()
	}
	if templates.RecordingIn(c.core.Store, c.core.Rec, id, c.core.Store.IsDescendant) {
		return errors.New(locales.TemplateWaitForRecording())
	}
	return nil
}

// fetchNeeds brings to this device what copying page id, with its
// subpages, needs first (templates.Needs), off the core's queue, as
// LocalFile keeps a file. It returns the recordings whose audio came, for
// dropFetchedAudio once the copy is made; failing, it drops what came
// itself, as no copy needs it.
func (c *Core) fetchNeeds(id string) ([]string, error) {
	var needs []store.CopyNeed
	var err error
	c.q.do(func() { needs, err = templates.Needs(c.core.Store, c.core.Sync, id) })
	if err != nil || len(needs) == 0 {
		return nil, err
	}
	audio, err := templates.Fetch(c.core.Store, c.core.Sync, needs)
	if err != nil {
		c.q.do(func() { c.dropFetchedAudio(audio) })
		return nil, templates.CopyError(err)
	}
	return audio, nil
}

// dropFetchedAudio removes from this device the recordings' audio that
// fetchNeeds brought for a copy (templates.DropAudio). Run on the core's
// queue.
func (c *Core) dropFetchedAudio(audio []string) {
	if len(audio) > 0 {
		templates.DropAudio(c.core.Store, c.core.Store.Settings(), audio)
	}
}
