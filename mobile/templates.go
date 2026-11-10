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

type templateRow struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Hint   string `json:"hint"`
	Icon   string `json:"icon"`
	PageID string `json:"page_id,omitempty"`
}

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

func (c *Core) SetStarterDeleted(key string, deleted bool) (err error) {
	defer catch(&err)
	c.q.do(func() { err = templates.SetStarterDeleted(c.core.Store, strings.TrimPrefix(key, "starter:"), deleted) })
	return err
}

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

func (c *Core) content(id string) *doc.Content {
	if p := c.pages[id]; p != nil {
		return p.cur
	}
	if content := c.core.Store.Content(id); content != nil {
		return content
	}
	return doc.Empty()
}

func (c *Core) ApplyTemplate(pageID, key string) (_ string, err error) {
	defer catch(&err)
	return c.useTemplate(pageID, "", key, false)
}

func (c *Core) InsertTemplate(pageID, blockID, key string) (_ string, err error) {
	defer catch(&err)
	return c.useTemplate(pageID, blockID, key, true)
}

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

func (c *Core) DuplicatePage(id string) (_ string, err error) {
	defer catch(&err)
	return c.copyPage(id, func(p store.Page) store.CopyOptions { return templates.Duplicate(c.core.Store, p) })
}

func (c *Core) SaveAsTemplate(id string) (_ string, err error) {
	defer catch(&err)
	return c.copyPage(id, func(p store.Page) store.CopyOptions {
		return store.CopyOptions{Title: p.Title, Template: true}
	})
}

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

func (c *Core) dropFetchedAudio(audio []string) {
	if len(audio) > 0 {
		templates.DropAudio(c.core.Store, c.core.Store.Settings(), audio)
	}
}
