package app

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/doc"
	"leaflet/internal/filepreview"
	"leaflet/internal/mediaserver"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

type filePreview struct {
	file    store.File
	kind    string
	path    string
	loading bool
	failed  bool
	notice  string

	code *codeeditor.State
	md   string
	mdo  markdown.Options
	rows [][]string
	list ui.ListState

	wv      *mygo.WebView
	address string
}

func (a *App) previewFile(f store.File) {
	kind := doc.FilePreviewKind(f.Name, f.MimeType)
	if kind == "" {
		a.openFile(f)
		return
	}
	a.closePreview()
	a.playAlone("", "")
	p := &filePreview{file: f, kind: kind, loading: true}
	a.preview = p
	delete(a.downloadFails, f.ID)
	a.lost(f.LocalPath)
	path, fetching := a.localFile(f, func(ok bool) {
		if a.preview != p {
			return
		}
		if f, found := a.fileByID(f.ID); ok && found && f.LocalPath != "" {
			a.loadPreview(p, f.LocalPath)
		} else {
			p.loading, p.failed = false, true
		}
	})
	if path != "" {
		a.loadPreview(p, path)
		return
	}
	a.notHere(f, fetching)
	if _, downloading := a.downloads[f.ID]; !downloading {
		p.loading, p.failed = false, true
	}
}

func (a *App) loadPreview(p *filePreview, path string) {
	p.path = path
	switch p.kind {
	case doc.FileImage:
		p.loading = false
	case doc.FilePDF, doc.FileAudio, doc.FileVideo:
		a.previewPage(p)
	default:
		var text filepreview.Text
		var err error
		limit := int64(filepreview.MaxText)
		if p.kind == doc.FileMarkdown {
			limit = filepreview.MaxMarkdown
		}
		var rows [][]string
		var rowsCut bool
		a.offMain(func() {
			text, err = filepreview.Read(path, limit, i18n.Locale())
			if err == nil && p.kind == doc.FileTable {
				rows, rowsCut = filepreview.ParseCSV(text.Text, separator(p.file.Name))
			}
		}, func() {
			if a.preview == p {
				a.showPreview(p, text, rows, rowsCut, limit, err)
			}
		})
	}
}

func separator(name string) rune {
	if strings.EqualFold(filepath.Ext(name), ".tsv") {
		return '\t'
	}
	return ','
}

func (a *App) showPreview(p *filePreview, text filepreview.Text, rows [][]string, rowsCut bool, limit int64, err error) {
	p.loading = false
	if err != nil {
		p.failed = true
		if !errors.Is(err, filepreview.ErrBinary) {
			p.notice = err.Error()
		}
		return
	}
	var words []string
	if text.Cut {
		words = append(words, locales.PreviewTruncated(humanSize(limit)))
	}
	if rowsCut {
		words = append(words, locales.PreviewRowsCut(len(rows)))
	}
	if text.Encoding != "" {
		words = append(words, locales.PreviewEncoding(text.Encoding))
	}
	p.notice = strings.Join(words, " ")
	switch p.kind {
	case doc.FileMarkdown:
		p.md = text.Text
		p.mdo.OnLink = func(url string) {
			if ui.SafeToOpen(url) {
				openLink(url)
			}
		}
	case doc.FileTable:
		p.rows = rows
	default:
		buffer := codeeditor.NewBuffer(text.Text)
		buffer.SetLanguage(codeeditor.LanguageFor(p.file.Name))
		p.code = &codeeditor.State{Buffer: buffer, ReadOnly: true, SoftWrap: true}
	}
}

func (a *App) previewPage(p *filePreview) {
	p.loading = false
	if a.win == nil {
		p.failed = true
		return
	}
	item := mediaserver.Item{Path: p.path, Type: doc.FileMediaType(p.file.Name, p.file.MimeType)}
	if p.kind != doc.FilePDF {
		item.Page, item.Policy = rendered.FilePlayerPage(p.kind == doc.FileVideo, 0), rendered.FilePlayerCSP
		item.Cover, item.CoverType = leafletCover, "image/png"
	}
	address, err := a.media.Register(item)
	if err != nil {
		p.failed, p.notice = true, err.Error()
		return
	}
	wv, err := a.win.NewWebView(mygo.WebViewOptions{BackgroundColor: mediaBackground(p.kind),
		Page: mygo.PageOptions{Session: fileSession(), Untrusted: true, Autoplay: mygo.AutoplayAllow}})
	if err != nil {
		a.media.Revoke(address)
		p.failed, p.notice = true, err.Error()
		return
	}
	page := wv.Page()
	page.OnWillNavigate(func(e *mygo.NavigateEvent) { e.PreventDefault() })
	page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
	if p.kind != doc.FilePDF {
		nameFile(page, p.file.Name)
	}
	if err := page.LoadURL(address); err != nil {
		wv.Destroy()
		a.media.Revoke(address)
		p.failed, p.notice = true, err.Error()
		return
	}
	p.wv, p.address = wv, address
}

func (a *App) closePreview() {
	p := a.preview
	if p == nil {
		return
	}
	a.preview = nil
	if p.wv != nil {
		p.wv.Destroy()
		a.media.Revoke(p.address)
	}
}

func (a *App) previewView(c *ui.Context) {
	pal := theme.Of(c)
	p := a.preview
	bar := c.TitleBar()
	ui.Column(c.Key("preview")).Fill().Background(pal.Surface).Children(func() {
		ui.Row(c.Key("preview-bar")).Height(max(bar.Height, headerHeight)).Padding(0, bar.Right+12, 0, bar.Left+12).Gap(8).AlignItems(ui.Center).
			DragWindow().BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
			ui.Text(c, p.file.Name).FontSize(13.5).FontWeight(600).TextColor(pal.Text).SingleLine().Grow(1)
			tools(c, func() {
				if iconButton(c, "open", "external-link", locales.CardOpenExternally()).Clicked() {
					a.openFile(p.file)
				}
				if iconButton(c, "close", "x", locales.CommonClose()).Clicked() {
					a.closePreview()
				}
			})
		})
		if a.preview == nil {
			return
		}
		if p.notice != "" {
			ui.Text(c, p.notice).FontSize(12.5).TextColor(pal.TextMuted).Padding(8, 16)
		}
		ui.Column(c.Key("preview-body")).Grow(1).FillWidth().Children(func() { a.previewBody(c, p) })
	})
	c.OnShortcut(0, ui.KeyEscape, a.closePreview)
}

func (a *App) previewBody(c *ui.Context, p *filePreview) {
	pal := theme.Of(c)
	message := func(text string) {
		ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
			ui.Icon(c, theme.Icon("file")).FontSize(30).TextColor(pal.ChartMuted)
			ui.Text(c, text).TextColor(pal.TextMuted)
		})
	}
	switch {
	case p.failed:
		message(locales.PreviewCannotShow())
	case p.loading:
		ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).Size(18, 18) })
	case p.wv != nil:
		height := float32(0)
		if p.kind == doc.FileAudio {
			height = audioBarHeight
		}
		a.previewWeb(c, p, height)
	case p.kind == doc.FileImage:
		a.previewImage(c, p, message)
	case p.kind == doc.FileMarkdown:
		ui.Scroll(c).Grow(1).FillWidth().Padding(20, pagePadX).Children(func() {
			markdownView(c, p.md, &p.mdo).Font(theme.PageFamily(p.md)).FontSize(15)
		})
	case p.kind == doc.FileTable:
		a.previewTable(c, p)
	case p.code != nil:
		codeeditor.View(c, p.code).Fill()
	}
}

func (a *App) previewWeb(c *ui.Context, p *filePreview, height float32) {
	if height == 0 {
		ui.WebView(c.Key("preview-web"), p.wv).Fill()
		return
	}
	ui.Column(c).Grow(1).FillWidth().Center().Padding(0, pagePadX).Children(func() {
		ui.WebView(c.Key("preview-web"), p.wv).Height(height).FillWidth().Radius(8)
	})
}

func (a *App) previewImage(c *ui.Context, p *filePreview, message func(string)) {
	link := "file://" + p.path
	bmp := a.images.get(link, func() { a.update(func() {}) })
	switch {
	case bmp != nil:
		ui.Image(c, bmp).Fill().Fit(ui.Contain).Label(p.file.Name)
	case a.images.hasFailed(link):
		message(locales.PreviewCannotShow())
	default:
		ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).Size(18, 18) })
	}
}

func (a *App) previewTable(c *ui.Context, p *filePreview) {
	if len(p.rows) == 0 {
		return
	}
	pal := theme.Of(c)
	head, rows := p.rows[0], p.rows[1:]
	columns := make([]ui.TableColumn, 0, len(head))
	for i, title := range head {
		columns = append(columns, ui.TableColumn{Title: title, ID: strconv.Itoa(i), Width: 180, MinWidth: 60})
	}
	ui.Table(c.Key("preview-table"), &p.list, columns, len(rows), func(row, col int) {
		if col < len(rows[row]) {
			ui.Text(c, rows[row][col]).FontSize(13).TextColor(pal.Text).SingleLine()
		}
	}).Fill().Padding(0, 16)
}

const audioBarHeight = 54
