package app

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/guard"
	"leaflet/internal/theme"
	"leaflet/internal/webpage"
	"leaflet/locales"
)

const pdfLoadWait = 20 * time.Second

const pdfDownloadWait = 2 * time.Minute

const pdfMargin = 0.6

func (a *App) exportPDF(id string) {
	if a.page != nil && a.page.id == id && !a.savePage() {
		return
	}
	a.exportContent(id, func(c *doc.Content) {
		p := a.pageByID(id)
		if p == nil {
			return
		}
		o := webpage.Options{Store: a.st, Page: id, Title: p.DisplayTitle(), Titles: a.pageTitles(), Files: slices.Clone(a.files),
			Lang: i18n.Locale(), Head: "<style>" + printLook() + "</style>", Serve: webpage.Inline, Code: codeHTML, Chart: chartHTML, Print: true,
			Region: webpage.RegionOf(mygo.App.Region().Locale)}
		name := safeName(p.DisplayTitle()) + ".pdf"
		cr := a.core()
		go func() {
			defer guard.Recover()
			path, _ := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: locales.HostExportPDFTitle(), DefaultPath: name})
			if path == "" {
				return
			}
			a.update(func() { a.toast(locales.HostExportingPDF(filepath.Base(path))) })
			ctx, cancel := context.WithTimeout(context.Background(), pdfDownloadWait)
			if cr.DownloadPictures(ctx, c, id) {
				if files, err := a.st.Files(); err == nil {
					o.Files = files
				}
			}
			cancel()
			pdf, err := printPDF(webpage.HTML(c, o))
			if err == nil {
				err = export.Save(path, bytes.NewReader(pdf))
			}
			a.update(func() {
				if err != nil {
					a.toast(locales.HostErrorExport(err.Error()))
				} else {
					a.toast(locales.HostExported(filepath.Base(path)))
				}
			})
		}()
	})
}

var printLook = sync.OnceValue(func() string { return pageLook(theme.For(false)) })

func printPDF(page string) ([]byte, error) {
	win := mygo.NewWindow(mygo.WindowOptions{Hidden: true, Width: 816, Height: 1056, SkipTaskbar: true, BackgroundColor: "#ffffff",
		Page: mygo.PageOptions{BackgroundThrottling: mygo.ThrottlingOff}})
	defer win.Destroy()
	pg := win.Page()
	pg.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if !strings.HasPrefix(e.URL, "about:") && !strings.HasPrefix(e.URL, "data:") {
			e.PreventDefault()
		}
	})
	pg.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
	loaded := make(chan struct{}, 1)
	done := func() {
		select {
		case loaded <- struct{}{}:
		default:
		}
	}
	pg.OnDidFinishLoad(done)
	pg.OnDidFailLoad(func(*mygo.LoadError) { done() })
	pg.LoadHTML(page, "")
	select {
	case <-loaded:
	case <-time.After(pdfLoadWait):
		pg.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = pg.EvalContext(ctx, "await document.fonts.ready")
	cancel()
	w, h := webpage.Paper(webpage.RegionOf(mygo.App.Region().Locale))
	return pg.PrintToPDF(mygo.PDFOptions{PageSize: mygo.PageSize{Width: w, Height: h},
		Margins: &mygo.Margins{Top: pdfMargin, Right: pdfMargin, Bottom: pdfMargin, Left: pdfMargin}, Background: true})
}
