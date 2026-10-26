package app

import (
	"fmt"
	"html"
	"log"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

// Link cards and file cards.

// linkInfo is what a web page says about itself in its head.
type linkInfo struct{ title, description, image, site string }

var (
	metaTag   = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	titleTag  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	attribute = regexp.MustCompile(`(?is)([a-z:_-]+)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
)

// readLinkInfo reads a page's title, description, picture and site name
// from its Open Graph tags, or its title and description tags. A picture
// given relative to the page is made whole.
func readLinkInfo(page, src string) linkInfo {
	if i := strings.Index(strings.ToLower(src), "</head>"); i >= 0 {
		src = src[:i]
	}
	meta := map[string]string{}
	for _, tag := range metaTag.FindAllString(src, -1) {
		attrs := map[string]string{}
		for _, m := range attribute.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(m[1])] = html.UnescapeString(strings.Trim(m[2], `"'`))
		}
		key := strings.ToLower(attrs["property"])
		if key == "" {
			key = strings.ToLower(attrs["name"])
		}
		if key != "" && meta[key] == "" {
			meta[key] = strings.TrimSpace(attrs["content"])
		}
	}
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := meta[k]; v != "" {
				return strings.Join(strings.Fields(v), " ")
			}
		}
		return ""
	}
	info := linkInfo{
		title:       first("og:title", "twitter:title"),
		description: first("og:description", "twitter:description", "description"),
		image:       first("og:image", "og:image:url", "twitter:image"),
		site:        first("og:site_name"),
	}
	if m := titleTag.FindStringSubmatch(src); info.title == "" && m != nil {
		info.title = strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
	}
	if info.image != "" {
		base, err1 := url.Parse(page)
		ref, err2 := url.Parse(info.image)
		if err1 != nil || err2 != nil {
			info.image = ""
		} else if abs := base.ResolveReference(ref); abs.Scheme == "http" || abs.Scheme == "https" {
			info.image = abs.String()
		} else {
			info.image = ""
		}
	}
	return info
}

// linkInfoOf asks a page for its info the first time a card shows it, and
// keeps it in the page's link cards that lack it, for search, exports and
// other devices. Until the page answers, the info is empty.
func (a *App) linkInfoOf(link string) linkInfo {
	if info, ok := a.linkInfo[link]; ok {
		return info
	}
	if !a.linkAsked[link] {
		a.linkAsked[link] = true
		go func() {
			var info linkInfo
			data, err := fetch(link)
			if err == nil {
				info = readLinkInfo(link, string(data[:min(len(data), 1<<20)]))
			} else {
				log.Printf("link %s: %v", link, err)
			}
			a.update(func() {
				a.linkInfo[link] = info
				a.keepLinkInfo(link, info)
			})
		}()
	}
	return linkInfo{}
}

func (a *App) keepLinkInfo(link string, info linkInfo) {
	if info.title == "" || a.page == nil || a.page.editor.ReadOnly {
		return
	}
	var ids []string
	a.page.editor.Content.Walk(func(n, _ *doc.Node) {
		if n.Type == "link_preview" && n.Str("url") == link && n.Str("title") == "" {
			ids = append(ids, n.ID)
		}
	})
	for _, id := range ids {
		ed := a.page.editor
		ed.SetData(id, "title", info.title)
		ed.SetData(id, "description", info.description)
		ed.SetData(id, "image", info.image)
		ed.SetData(id, "site", info.site)
	}
}

// cardButton is the box of a link or file card, which opens what it stands for.
func cardButton(c *ui.Context, key, label string) ui.Element {
	pal := theme.Of(c)
	b := ui.ButtonBase(c.Key(key)).FillWidth().Radius(8).Border(1, pal.Divider).Background(pal.Surface).
		Cursor(ui.CursorPointer).AlignItems(ui.Center).Clip().Label(label)
	if b.Hovered() {
		b.Background(pal.Hover.Over(pal.Surface))
	}
	return b
}

func (a *App) linkPreviewBlock(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	link := n.Str("url")
	info := linkInfo{n.Str("title"), n.Str("description"), n.Str("image"), n.Str("site")}
	if info.title == "" {
		info = a.linkInfoOf(link)
	}
	site := info.site
	if u, err := url.Parse(link); err == nil && site == "" {
		site = strings.TrimPrefix(u.Hostname(), "www.")
	}
	title := info.title
	if title == "" {
		title = link
	}
	// Short without a description or picture to make room for.
	height := float32(64)
	if info.description != "" || info.image != "" {
		height = 96
	}
	b := cardButton(c, "link:"+n.ID, title).Height(height)
	b.Children(func() {
		ui.Column(c).Grow(1).FillHeight().Padding(12, 14).Gap(4).Children(func() {
			ui.Text(c, title).FontWeight(600).FontSize(14.5).TextColor(pal.Text).SingleLine()
			if info.description != "" {
				ui.Text(c, info.description).FontSize(13).TextColor(pal.TextMuted).MaxLines(2)
			}
			ui.Spacer(c)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, theme.Icon("globe")).FontSize(12).TextColor(pal.TextMuted)
				ui.Text(c, site).FontSize(12).TextColor(pal.TextMuted).SingleLine()
			})
		})
		if info.image != "" {
			if bmp := a.images.get(info.image, func() { a.update(func() {}) }); bmp != nil {
				ui.Image(c, bmp).Size(150, 96).Fit(ui.Cover)
			}
		}
	})
	if b.Clicked() {
		openLink(link)
	}
}

// humanSize is a file's size as people read it.
func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func (a *App) fileBlock(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	for _, file := range a.files {
		if file.ID == n.Str("file_id") {
			b := cardButton(c, "file:"+n.ID, "Open "+file.Name)
			a.fileCard(c, b, file.Name, file.SizeBytes, pal)
			if b.Clicked() {
				a.openFile(file)
			}
			return
		}
	}
	// Not on this device yet, or gone: the card says so.
	name := n.Str("name")
	if name == "" {
		name = "File"
	}
	a.fileCard(c, cardButton(c, "file:"+n.ID, name).Opacity(0.7), name, -1, pal)
}

func (a *App) fileCard(c *ui.Context, b ui.Element, name string, size int64, pal *theme.Palette) {
	b.Padding(10, 14).Gap(12).Children(func() {
		ui.Row(c).Size(36, 36).Center().Radius(8).Background(pal.Ground).Children(func() {
			ui.Icon(c, theme.Icon("paperclip")).FontSize(17).TextColor(pal.Icon)
		})
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, name).FontWeight(600).FontSize(14).TextColor(pal.Text).SingleLine()
			kind := strings.ToUpper(strings.TrimPrefix(filepath.Ext(name), "."))
			detail := "Not on this device yet"
			if size >= 0 {
				detail = humanSize(size)
				if kind != "" {
					detail = kind + " · " + detail
				}
			}
			ui.Text(c, detail).FontSize(12).TextColor(pal.TextMuted)
		})
	})
}
