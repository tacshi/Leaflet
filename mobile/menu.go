package mobile

import (
	"encoding/json"
	"errors"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/menu"
	"leaflet/internal/store"
	"leaflet/internal/web"
)

// The editor's / and @ menus, as the desktop's (internal/menu): the core
// says what they offer and filters it as the user types; the app draws
// them and does what an item says.

// MenuItems returns what the menu trigger, "/" or "@", offers in page
// pageID, as a JSON array of menu.Item for FilterMenu to filter.
func (c *Core) MenuItems(pageID, trigger string) (_ string, err error) {
	defer catch(&err)
	var items []menu.Item
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
func FilterMenu(items, query string) (_ string, err error) {
	defer catch(&err)
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
func MenuBlock(key, author string) (_ string, err error) {
	defer catch(&err)
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
func LinkBlock(kind, link string) (_ string, err error) {
	defer catch(&err)
	n, err := menu.LinkBlock(kind, link)
	if err != nil {
		return "", err
	}
	return encode(n)
}

// EmbedStart returns the page a new embed starts from, to change.
func EmbedStart() string {
	defer catch(nil)
	return menu.EmbedStart
}

// EmbedBlock returns an embed of html, as tall as height, that author
// made, as JSON.
func EmbedBlock(title, html string, height int, author string) (_ string, err error) {
	defer catch(&err)
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
		guard.Go(func() {
			info, err := web.LookUp(link)
			if err != nil || info.Title == "" {
				return
			}
			c.q.post(func() {
				_ = c.editContent(id, func(content *doc.Content) bool { return info.Fill(content, link) })
			})
		})
	}
}

// pasted is what text pasted makes, as PasteText gives it: the blocks of
// lines, a video or a picture for a link pasted on an empty line, or the
// web link the text is.
type pasted struct {
	Blocks []*doc.Node `json:"blocks,omitempty"`
	Media  *doc.Node   `json:"media,omitempty"`
	Link   string      `json:"link,omitempty"`
}

// PasteText reads text pasted, as the desktop's editor pastes text: lines
// as blocks, read as Markdown (doc.ParseLines), and one line that is a web
// link as that link; on an empty line, a YouTube, Vimeo or video file's
// link as a video, and a picture's as an image. empty says the caret is on
// an empty line. A line of other text makes nothing: it goes in as typed.
func PasteText(text string, empty bool) (_ string, err error) {
	defer catch(&err)
	var out pasted
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", " ", "\n", "\u0085", "\n").Replace(text)
	line := strings.TrimSpace(text)
	switch {
	case strings.Contains(strings.TrimRight(text, "\n"), "\n"):
		out.Blocks = doc.ParseLines(strings.TrimRight(text, "\n"))
	case empty && line != "":
		if v, err := doc.VideoNode(line, ""); err == nil {
			out.Media = v
		} else if doc.ImageLink(line) {
			out.Media, _ = doc.ImageNode(line)
		}
	}
	if out.Blocks == nil && out.Media == nil && doc.WebURL(text) {
		out.Link = line
	}
	return encode(out)
}

// fence is what Enter after a line makes, as FenceEnter gives it.
type fence struct {
	Code     bool      `json:"code,omitempty"`
	Language string    `json:"language,omitempty"`
	Block    *doc.Node `json:"block,omitempty"`
}

// FenceEnter says what Enter at the end of a paragraph of line makes, as
// the desktop's editor has it (fenceEnter): a Markdown fence, as "```go",
// a code block in its language; "```mermaid" a diagram, and "$$" math, to
// be written; nothing for another line.
func FenceEnter(line string) (_ string, err error) {
	defer catch(&err)
	var out fence
	language, ok := doc.CodeFence(line)
	switch {
	case ok && language == "mermaid":
		out.Block = doc.DiagramNode("")
	case ok:
		out.Code, out.Language = true, language
	case strings.TrimSpace(line) == "$$":
		out.Block = doc.MathNode("")
	}
	return encode(out)
}
