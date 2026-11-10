package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/internal/web"
	"leaflet/internal/webpage"
	"leaflet/locales"
)

type version struct {
	ID       string `json:"id"`
	When     string `json:"when"`
	Label    string `json:"label"`
	Markdown string `json:"markdown"`
}

func (c *Core) History(pageID string) (_ string, err error) {
	defer catch(&err)
	var out []version
	c.q.do(func() {
		if p := c.pages[pageID]; p != nil {
			p.live.Save()
		}
		var entries []store.HistoryEntry
		if entries, err = c.core.Store.History(pageID); err != nil {
			return
		}
		out = make([]version, 0, len(entries))
		for _, h := range entries {
			content, derr := doc.Decode(h.Content)
			if derr != nil {
				continue
			}
			out = append(out, version{
				ID: h.ID, When: locales.DialogHistoryWhen(h.CreatedAt), Label: core.HistoryLabel(h),
				Markdown: doc.PageMarkdown("", content, c.markdownOptions()),
			})
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

func (c *Core) CurrentMarkdown(pageID string) (_ string, err error) {
	defer catch(&err)
	var out string
	c.q.do(func() {
		if p := c.pages[pageID]; p != nil {
			p.live.Save()
		}
		out = doc.PageMarkdown("", c.core.Store.Content(pageID), c.markdownOptions())
	})
	return out, nil
}

func (c *Core) NameVersion(pageID, name string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.pageCore().NameVersion(pageID, name) })
	return err
}

func (c *Core) RestoreVersion(pageID, id string) (_ string, err error) {
	defer catch(&err)
	kept := ""
	c.q.do(func() {
		var run string
		if kept, run, err = c.pageCore().RestoreVersion(pageID, id); err != nil || run == "" {
			return
		}
		c.settled[pageID] = run
		c.ev.agentsChanged(pageID)
	})
	return kept, err
}

func (c *Core) titleOf(id string) string {
	if p, err := c.core.Store.Page(id); err == nil {
		return p.DisplayTitle()
	}
	return ""
}

func (c *Core) markdownOptions() doc.MarkdownOptions {
	return doc.MarkdownOptions{Lang: i18n.Locale(), PageTitle: c.titleOf}
}

type exported struct {
	Name     string `json:"name"`
	Markdown string `json:"markdown"`
}

func (c *Core) ExportMarkdown(pageID string) (_ string, err error) {
	defer catch(&err)
	var out exported
	c.q.do(func() {
		if p := c.pages[pageID]; p != nil {
			p.live.Save()
		}
		var p store.Page
		if p, err = c.core.Store.Page(pageID); err != nil {
			return
		}
		content := c.content(pageID)
		opts := c.markdownOptions()
		opts.Link = export.Moments(content)
		out = exported{
			Name:     export.SafeName(p.DisplayTitle()) + ".md",
			Markdown: doc.PageMarkdown(p.DisplayTitle(), export.WithTranscripts(c.core.Store, pageID, content), opts),
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

type exportedPDF struct {
	Name   string  `json:"name"`
	Path   string  `json:"path"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

const pdfDownloadWait = 2 * time.Minute

func (c *Core) ExportPDFPage(pageID, region, dir string) (_ string, err error) {
	defer catch(&err)
	var o webpage.Options
	var content *doc.Content
	c.q.do(func() {
		if p := c.pages[pageID]; p != nil {
			p.live.Save()
		}
		var p store.Page
		if p, err = c.core.Store.Page(pageID); err != nil {
			return
		}
		var pages []store.Page
		if pages, err = c.core.Store.Pages(); err != nil {
			return
		}
		titles := map[string]string{}
		for _, pg := range pages {
			titles[pg.ID] = pg.DisplayTitle()
		}
		var files []store.File
		if files, err = c.core.Store.Files(); err != nil {
			return
		}
		content = c.content(pageID).Copy()
		o = webpage.Options{Store: c.core.Store, Page: pageID, Title: p.DisplayTitle(), Titles: titles, Files: files,
			Lang: i18n.Locale(), Serve: webpage.Inline, Print: true, Region: region}
	})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pdfDownloadWait)
	defer cancel()
	if c.core.DownloadPictures(ctx, content, pageID) {
		c.q.do(func() {
			if files, err := c.core.Store.Files(); err == nil {
				o.Files = files
			}
		})
	}
	out := exportedPDF{Name: export.SafeName(o.Title) + ".pdf"}
	if out.Path, err = writePage(dir, webpage.HTML(content, o)); err != nil {
		return "", err
	}
	out.Width, out.Height = webpage.Paper(region)
	return encode(out)
}

func writePage(dir, html string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "page-*.html")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(html)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (c *Core) PlainCopy(nodes string) (_ string, err error) {
	defer catch(&err)
	var ns []*doc.Node
	if err := json.Unmarshal([]byte(nodes), &ns); err != nil {
		return "", err
	}
	var out string
	c.q.do(func() { out = doc.PlainCopy(ns, c.titleOf) })
	return out, nil
}

func (c *Core) CopyText(nodes string) (_ string, err error) {
	defer catch(&err)
	var ns []*doc.Node
	if err := json.Unmarshal([]byte(nodes), &ns); err != nil {
		return "", err
	}
	var out string
	c.q.do(func() {
		out = strings.TrimRight(doc.PageMarkdown("", &doc.Content{Children: ns}, c.markdownOptions()), "\n")
	})
	return out, nil
}

func (c *Core) AppendBlock(pageID, node string) (err error) {
	defer catch(&err)
	var n doc.Node
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return err
	}
	c.q.do(func() {
		err = c.editContent(pageID, func(content *doc.Content) bool {
			return content.Insert(doc.Position{At: "end"}, &n) == nil
		})
	})
	return err
}

func (c *Core) AddFolderFile(folderID, path string) (err error) {
	defer catch(&err)
	if _, err = c.core.Store.AddFile(path, folderID, "", mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))); err != nil {
		return err
	}
	c.core.Sync.KickPush()
	return nil
}

func (c *Core) DeleteFile(id string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		var f store.File
		if f, err = c.core.Store.File(id); err != nil {
			return
		}
		err = c.core.Store.DeleteFile(f)
	})
	return err
}

func (c *Core) AddFile(pageID, path string) (_ string, err error) {
	defer catch(&err)
	f, err := c.core.Store.AddFile(path, "", pageID, mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
	if err != nil {
		return "", err
	}
	var n *doc.Node
	if doc.ImageExts[strings.ToLower(filepath.Ext(f.Name))] {
		n = &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
			"url": "file://" + f.LocalPath, "align": "center", "alt": f.Name, "file_id": f.ID,
		}}
	} else {
		n = &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{
			"url": "file://" + f.LocalPath, "name": f.Name, "file_id": f.ID, "size": f.SizeBytes, "mime": f.MimeType,
		}}
	}
	c.core.Sync.KickPush()
	return encode(n)
}

func (c *Core) Picture(link string) (_ []byte, err error) {
	defer catch(&err)
	return c.PictureCancellable(link, 0)
}

func (c *Core) PictureCancellable(link string, token int64) (_ []byte, err error) {
	defer catch(&err)
	ctx, done := c.cancellable(token, time.Minute)
	defer done()
	return fetchPicture(ctx, link)
}

var fetchPicture = web.FetchContext

func (c *Core) LocalFile(id string, token int64) (_ string, err error) {
	defer catch(&err)
	var f store.File
	c.q.do(func() { f, err = c.core.Store.File(id) })
	if err != nil {
		return "", err
	}
	if f.LocalPath != "" {
		if _, err := os.Stat(f.LocalPath); err == nil {
			return f.LocalPath, nil
		}
	}
	if f.StoragePath == "" {
		return "", errors.New(locales.CardNotOnDevice())
	}
	ctx, done := c.cancellable(token, recording.DownloadWait(f.SizeBytes))
	defer done()
	data, err := c.core.Sync.FetchReader(ctx, "files", f.StoragePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = data.Close() }()
	return c.core.Store.DownloadedFileFrom(id, data)
}
