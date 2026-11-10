package mobile

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/menu"
	"leaflet/internal/templates"
	"leaflet/internal/web"
)

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

func (c *Core) slashItems(pageID string) ([]menu.Item, error) {
	all, hint, err := c.choices()
	if err != nil {
		return nil, err
	}
	return menu.Slash(templates.Items(all, pageID, hint)), nil
}

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
	m := menu.Mentions{PageID: pageID, Pages: pages, Folders: folders, Members: members, UserID: st.Session().UserID}
	for _, o := range c.core.Hub.Options() {
		m.Agents = append(m.Agents, menu.Agent{ProviderID: o.Provider.ID, Name: o.Name(), Model: o.Model})
	}
	return menu.Mention(m), nil
}

func FilterMenu(items, query string) (_ string, err error) {
	defer catch(&err)
	var all []menu.Item
	if err := json.Unmarshal([]byte(items), &all); err != nil {
		return "", err
	}
	return encode(menu.Filter(all, query))
}

type menuBlock struct {
	Type string         `json:"type,omitempty"`
	Data map[string]any `json:"data,omitempty"`
	Node any            `json:"node,omitempty"`
}

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

func LinkBlock(kind, link string) (_ string, err error) {
	defer catch(&err)
	if kind == "video" {
		link = web.VideoLink(link)
	}
	n, err := menu.LinkBlock(kind, link)
	if err != nil {
		return "", err
	}
	return encode(n)
}

func ShortVideoLink(link string) bool {
	defer catch(nil)
	return doc.ShortVideoLink(link)
}

func EmbedStart() string {
	defer catch(nil)
	return menu.EmbedStart
}

func EmbedBlock(title, html string, height int, author string) (_ string, err error) {
	defer catch(&err)
	n, err := menu.EmbedBlock(title, html, height, author)
	if err != nil {
		return "", err
	}
	return encode(n)
}

var linkRetry = time.Minute

func (c *Core) fillLinks(id string, content *doc.Content) {
	for _, link := range web.Unfilled(content) {
		c.askOnce(link, func() bool {
			info, err := web.LookUp(link)
			if err != nil {
				return false
			}
			if info.Title != "" {
				c.q.post(func() {
					_ = c.editContent(id, func(content *doc.Content) bool { return info.Fill(content, link) })
				})
			}
			return true
		})
	}
	for _, v := range untitledVideos(content) {
		c.askOnce("video "+v.Provider+":"+v.ID, func() bool {
			title, err := videoTitle(v)
			if err != nil {
				return false
			}
			if title != "" {
				c.q.post(func() {
					_ = c.editContent(id, func(content *doc.Content) bool { return nameVideo(content, v, title) })
				})
			}
			return true
		})
	}
}

func (c *Core) askOnce(key string, ask func() bool) {
	if c.linkAsked[key] {
		return
	}
	c.linkAsked[key] = true
	guard.Go(func() {
		if !ask() {
			time.AfterFunc(linkRetry, func() { c.q.post(func() { delete(c.linkAsked, key) }) })
		}
	})
}

var videoTitle = func(v doc.Video) (string, error) {
	data, err := web.Fetch(v.InfoURL())
	if err != nil {
		return "", err
	}
	title, _, err := v.ReadInfo(data)
	return title, err
}

func untitledVideos(content *doc.Content) []doc.Video {
	var out []doc.Video
	content.Walk(func(n, _ *doc.Node) {
		if n.Type != "video" || n.Str("title") != "" {
			return
		}
		if v, ok := doc.ParseVideoURL(n.Str("url")); ok && v.Provider != "file" &&
			!slices.ContainsFunc(out, func(o doc.Video) bool { return o.Provider == v.Provider && o.ID == v.ID }) {
			out = append(out, v)
		}
	})
	return out
}

func nameVideo(content *doc.Content, v doc.Video, title string) bool {
	found := false
	content.Walk(func(n, _ *doc.Node) {
		if n.Type != "video" || n.Str("title") != "" {
			return
		}
		if o, ok := doc.ParseVideoURL(n.Str("url")); ok && o.Provider == v.Provider && o.ID == v.ID {
			n.Set("title", title)
			found = true
		}
	})
	return found
}

type pasted struct {
	Blocks []*doc.Node `json:"blocks,omitempty"`
	Media  *doc.Node   `json:"media,omitempty"`
	Link   string      `json:"link,omitempty"`
}

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

type fence struct {
	Code     bool      `json:"code,omitempty"`
	Language string    `json:"language,omitempty"`
	Block    *doc.Node `json:"block,omitempty"`
}

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
