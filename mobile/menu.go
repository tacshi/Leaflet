package mobile

import (
	"encoding/json"
	"errors"

	"leaflet/internal/doc"
	"leaflet/internal/menu"
	"leaflet/internal/store"
	"leaflet/internal/web"
)

// The editor's / and @ menus, as the desktop's (internal/menu): the core
// says what they offer and filters it as the user types; the app draws
// them and does what an item says.

// MenuItems returns what the menu trigger, "/" or "@", offers in page
// pageID, as a JSON array of menu.Item for FilterMenu to filter.
func (c *Core) MenuItems(pageID, trigger string) (string, error) {
	var items []menu.Item
	var err error
	c.q.do(func() {
		switch trigger {
		case "/":
			items, err = c.slashItems(pageID)
		case "@":
			items, err = c.mentionItems(pageID)
		default:
			err = errors.New("no menu for " + trigger)
		}
	})
	if err != nil {
		return "", err
	}
	return encode(items)
}

// slashItems are the / menu's blocks, and the templates but the page's
// own. Run on the core's queue.
func (c *Core) slashItems(pageID string) ([]menu.Item, error) {
	all, err := c.choices()
	if err != nil {
		return nil, err
	}
	var templates []menu.Item
	for _, t := range all {
		if t.page != nil && t.page.ID == pageID {
			continue
		}
		it := menu.Item{Key: t.key(), Label: store.TitleOr(t.name())}
		if t.page != nil {
			it.Icon, it.Hint = "layout-template", c.hint(*t.page)
		} else {
			it.Icon, it.Hint = t.starter.Icon, t.starter.Hint()
		}
		templates = append(templates, it)
	}
	return menu.Slash(templates), nil
}

// mentionItems are the @ menu's agents, pages and people. Run on the
// core's queue.
func (c *Core) mentionItems(pageID string) ([]menu.Item, error) {
	st := c.core.Store
	pages, err := st.Pages()
	if err != nil {
		return nil, err
	}
	folders, err := st.Folders()
	if err != nil {
		return nil, err
	}
	members, err := st.Members(pageID)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	for _, p := range pages {
		titles[p.ID] = p.DisplayTitle()
	}
	m := menu.Mentions{PageID: pageID, Pages: pages, Members: members, UserID: st.Session().UserID}
	// Where a page is: in its parent page, else its folder.
	m.Place = func(p store.Page) string {
		if t, ok := titles[p.ParentID]; ok {
			return t
		}
		for _, f := range folders {
			if f.ID == p.FolderID {
				return f.Name
			}
		}
		return ""
	}
	for _, o := range c.core.Hub.Options() {
		m.Agents = append(m.Agents, menu.Agent{ProviderID: o.Provider.ID, Name: o.Name(), Model: o.Model})
	}
	return menu.Mention(m), nil
}

// FilterMenu returns the items, a JSON array as MenuItems returns it, that
// query, what is typed after the "/" or "@", finds.
func FilterMenu(items, query string) (string, error) {
	var all []menu.Item
	if err := json.Unmarshal([]byte(items), &all); err != nil {
		return "", err
	}
	return encode(menu.Filter(all, query))
}

// menuBlock is what a / menu item makes of the block at the caret.
type menuBlock struct {
	// Type and Data turn the block into another kind of text block.
	Type string         `json:"type,omitempty"`
	Data map[string]any `json:"data,omitempty"`
	// Node is a block to put at the caret instead.
	Node any `json:"node,omitempty"`
}

// MenuBlock returns what the / menu's item key makes, as JSON: the type
// and data the block at the caret takes, or a block to put there, a chart
// signed by author. It returns "" for an item the app does itself.
func MenuBlock(key, author string) (string, error) {
	if typ, level, ok := menu.Turn(key); ok {
		return encode(menuBlock{Type: typ, Data: doc.TypeData(typ, level)})
	}
	if n, ok := menu.Block(key, author); ok {
		return encode(menuBlock{Node: n})
	}
	return "", nil
}

// LinkBlock returns the block of kind "image", "video" or "link_preview"
// for link, as JSON, or what is wrong with the link, in the app's
// language.
func LinkBlock(kind, link string) (string, error) {
	n, err := menu.LinkBlock(kind, link)
	if err != nil {
		return "", err
	}
	return encode(n)
}

// EmbedStart returns the page a new embed starts from, to change.
func EmbedStart() string { return menu.EmbedStart }

// EmbedBlock returns an embed of html, as tall as height, that author
// made, as JSON.
func EmbedBlock(title, html string, height int, author string) (string, error) {
	n, err := menu.EmbedBlock(title, html, height, author)
	if err != nil {
		return "", err
	}
	return encode(n)
}

// fillLinks asks the pages of page id's link cards without their info
// for it, once a run, and puts what they say in the cards, as the desktop
// does as a card shows: for search, exports and other devices. Run on the
// core's queue.
func (c *Core) fillLinks(id string, content *doc.Content) {
	for _, link := range web.Unfilled(content) {
		if c.linkAsked[link] {
			continue
		}
		c.linkAsked[link] = true
		go func() {
			info, err := web.LookUp(link)
			if err != nil || info.Title == "" {
				return
			}
			c.q.post(func() {
				_ = c.editContent(id, func(content *doc.Content) bool { return info.Fill(content, link) })
			})
		}()
	}
}
