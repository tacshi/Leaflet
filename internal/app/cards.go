package app

import (
	"log"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

// Link cards and file cards.

// webAnswers are what pages on the web said, by what was asked of them:
// asked once each, an answer is kept while the app runs, and a question
// that failed, as offline, is asked again once imageRetry has passed, as
// a picture is. On the main thread only.
type webAnswers[T any] struct {
	answers map[string]T
	asking  map[string]bool
	failed  map[string]time.Time
}

// get returns the answer for key, or the zero value without one. Then it
// asks ask, off the main thread, unless it is asking already or asked in
// vain a moment ago; got has the answer, on the main thread.
func (w *webAnswers[T]) get(a *App, key string, ask func() (T, error), got func(T)) T {
	var zero T
	if v, ok := w.answers[key]; ok {
		return v
	}
	if w.asking[key] || time.Since(w.failed[key]) < imageRetry {
		return zero
	}
	if w.answers == nil {
		w.answers, w.asking, w.failed = map[string]T{}, map[string]bool{}, map[string]time.Time{}
	}
	w.asking[key] = true
	var v T
	var err error
	a.offMain(func() { v, err = ask() }, func() {
		delete(w.asking, key)
		if err != nil {
			w.failed[key] = time.Now()
			// What shows it asks again once the wait is over.
			a.later(imageRetry, func() {})
			return
		}
		delete(w.failed, key)
		w.answers[key] = v
		got(v)
	})
	return zero
}

// settled reports whether the question for key has had its answer, or
// failed the last it was asked.
func (w *webAnswers[T]) settled(key string) bool {
	_, answered := w.answers[key]
	_, failed := w.failed[key]
	return answered || failed
}

// linkInfoOf asks a page for its info the first time a card shows it, and
// keeps it in the page's link cards that lack it, for search, exports and
// other devices, though the page has closed by then. Until the page
// answers, the info is empty.
func (a *App) linkInfoOf(link string) web.LinkInfo {
	pageID := ""
	if a.page != nil {
		pageID = a.page.id
	}
	return a.linkInfo.get(a, link, func() (web.LinkInfo, error) {
		info, err := web.LookUp(link)
		if err != nil {
			log.Printf("link %s: %v", link, err)
		}
		return info, err
	}, func(info web.LinkInfo) { a.keepLinkInfo(pageID, link, info) })
}

// keepLinkInfo puts a link's info in page pageID's cards of it that lack
// it: through its editor while it is open, else in what is stored.
func (a *App) keepLinkInfo(pageID, link string, info web.LinkInfo) {
	if info.Title == "" || !a.canEdit(pageID) {
		return
	}
	if ed := a.openEditor(pageID); ed != nil {
		if ed.ReadOnly {
			return
		}
		var ids []string
		ed.Content.Walk(func(n, _ *doc.Node) {
			if web.Lacking(n, link) {
				ids = append(ids, n.ID)
			}
		})
		for _, id := range ids {
			for k, v := range info.Data() {
				ed.SetData(id, k, v)
			}
		}
		return
	}
	err := a.pageCore().EditPage(pageID, func(c *doc.Content) bool { return info.Fill(c, link) })
	if err != nil {
		log.Printf("link %s: %v", link, err)
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
	info := web.LinkInfo{Title: n.Str("title"), Description: n.Str("description"), Image: n.Str("image"), Site: n.Str("site")}
	if info.Title == "" {
		info = a.linkInfoOf(link)
	}
	site := info.Site
	if u, err := url.Parse(link); err == nil && site == "" {
		site = strings.TrimPrefix(u.Hostname(), "www.")
	}
	title := info.Title
	if title == "" {
		title = link
	}
	// Short without a description or picture to make room for.
	height := float32(64)
	if info.Description != "" || info.Image != "" {
		height = 96
	}
	b := cardButton(c, "link:"+n.ID, title).Height(height)
	b.Children(func() {
		ui.Column(c).Grow(1).FillHeight().Padding(12, 14).Gap(4).Children(func() {
			ui.Text(c, title).FontWeight(600).FontSize(14.5).TextColor(pal.Text).SingleLine()
			if info.Description != "" {
				ui.Text(c, info.Description).FontSize(13).TextColor(pal.TextMuted).MaxLines(2)
			}
			ui.Spacer(c)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, theme.Icon("globe")).FontSize(12).TextColor(pal.TextMuted)
				ui.Text(c, site).FontSize(12).TextColor(pal.TextMuted).SingleLine()
			})
		})
		if info.Image != "" {
			if bmp := a.images.get(info.Image, func() { a.update(func() {}) }); bmp != nil {
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
		return locales.CardSizeGB(math.Round(float64(n)/(1<<30)*10) / 10)
	case n >= 1<<20:
		return locales.CardSizeMB(math.Round(float64(n)/(1<<20)*10) / 10)
	case n >= 1<<10:
		return locales.CardSizeKB(int(math.Round(float64(n) / (1 << 10))))
	}
	return locales.CardSizeBytes(int(n)) // #nosec G115 -- under a kilobyte here.
}

func (a *App) fileBlock(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	for _, file := range a.files {
		if file.ID == n.Str("file_id") {
			b := cardButton(c, "file:"+n.ID, locales.CardOpenFile(file.Name))
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
		name = locales.CardFile()
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
			detail := locales.CardNotOnDevice()
			if size >= 0 {
				detail = humanSize(size)
				if kind != "" {
					detail = locales.CardFileDetail(kind, detail)
				}
			}
			ui.Text(c, detail).FontSize(12).TextColor(pal.TextMuted)
		})
	})
}
