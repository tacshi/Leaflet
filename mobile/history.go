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
	"leaflet/internal/store"
	"leaflet/internal/web"
	"leaflet/internal/webpage"
	"leaflet/locales"
)

// A page's versions, its Markdown and the files added to it, as the
// desktop's History dialog, Export as Markdown and Upload have them.

// version is a kept version of a page, as History lists it.
type version struct {
	ID string `json:"id"`
	// When and Label say when it was kept, and its name or why.
	When  string `json:"when"`
	Label string `json:"label"`
	// Markdown is the version's text, to look at before restoring it.
	Markdown string `json:"markdown"`
}

// History returns the versions kept of a page, newest first, as a JSON
// array of version. The page's edits are saved first.
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

// CurrentMarkdown returns a page's text as it is now, as History has its
// versions', to compare them with. The page's edits are saved first.
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

// NameVersion keeps a page as it is now as a version named name.
func (c *Core) NameVersion(pageID, name string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.pageCore().NameVersion(pageID, name) })
	return err
}

// RestoreVersion puts version id back as page pageID's content, keeping
// the page as it was as a version first, as the desktop does, and returns
// that version's ID, for Undo to restore it, or "" when the page had no
// content to keep (core.Pages.RestoreVersion). The page's last agent run
// no longer offers Undo: its edits are gone or back with the version, and
// undoing it would do nothing, or something else.
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

// markdownOptions are how the app writes a page as Markdown: in its
// language, its subpages by their titles.
func (c *Core) markdownOptions() doc.MarkdownOptions {
	return doc.MarkdownOptions{Lang: i18n.Locale(), PageTitle: c.titleOf}
}

// exported is a page as Markdown, as ExportMarkdown returns it.
type exported struct {
	// Name is the file's name: the title, safe in a path, and ".md".
	Name     string `json:"name"`
	Markdown string `json:"markdown"`
}

// ExportMarkdown returns a page as Markdown, with its recordings'
// transcripts, as an exported in JSON. The page's edits are saved first.
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

// exportedPDF is a page to print as a PDF, as ExportPDFPage returns it.
type exportedPDF struct {
	// Name is the file's name: the title, safe in a path, and ".pdf".
	Name string `json:"name"`
	// Path is the file that holds the page as its public link shows it,
	// laid out for paper, with its pictures and scripts in it, for the
	// app to load and remove once it is printed: a page of many pictures
	// is many megabytes, which would go through the app's memory several
	// times as JSON and strings.
	Path string `json:"path"`
	// Width and Height are the paper's, in inches.
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// pdfDownloadWait is how long a page to print waits for its own pictures
// that another device uploaded to download, as the desktop's waits.
const pdfDownloadWait = 2 * time.Minute

// ExportPDFPage writes a page as a web page to print as a PDF, as the
// desktop's Export › PDF prints it, on the paper of region, a code such as
// "US", in a file of its own in the folder dir, and returns an exportedPDF
// in JSON. The page's edits are saved first.
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
	// Pictures another device added, which this one has not shown yet,
	// come first, off the queue: paper has no other way to them.
	ctx, cancel := context.WithTimeout(context.Background(), pdfDownloadWait)
	defer cancel()
	if c.core.DownloadPictures(ctx, content, pageID) {
		c.q.do(func() {
			if files, err := c.core.Store.Files(); err == nil {
				o.Files = files
			}
		})
	}
	// Off the queue: its pictures are read into it.
	out := exportedPDF{Name: export.SafeName(o.Title) + ".pdf"}
	if out.Path, err = writePage(dir, webpage.HTML(content, o)); err != nil {
		return "", err
	}
	out.Width, out.Height = webpage.Paper(region)
	return encode(out)
}

// writePage writes html in a file of its own in the folder dir, and
// returns the file's path.
func writePage(dir, html string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "page-*.html")
	if err != nil {
		return "", err
	}
	// Written from the string itself, which a file takes without a copy.
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

// PlainCopy returns blocks as Copy as Plain Text puts them on the
// clipboard, as the desktop's: text alone, a line per block, a subpage
// as its page's title. nodes is a JSON array of the blocks.
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

// CopyText returns blocks, a JSON array of nodes, as Copy puts them on the
// clipboard: their Markdown, as the desktop's Copy writes it, which pastes
// back as the blocks, in Leaflet or another app that reads Markdown.
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

// AppendBlock puts a block, as JSON, at the end of page pageID, open in
// the app or not: AddFile's, when the page left the window as the file was
// copied, and no editor of it has a caret to put the block at.
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

// AddFolderFile keeps the file at path in folder folderID, or at the top
// level without one, as the sidebar's Upload does.
func (c *Core) AddFolderFile(folderID, path string) (err error) {
	defer catch(&err)
	// Off the core's queue, as AddFile: the store keeps itself safe.
	if _, err = c.core.Store.AddFile(path, folderID, "", mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))); err != nil {
		return err
	}
	c.core.Sync.KickPush()
	return nil
}

// DeleteFile deletes a file of the sidebar, from every device it syncs to.
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

// AddFile keeps the file at path with page pageID, as the desktop's
// Upload does, and returns the block that shows it, as JSON: a picture as
// an image, another file as a file card.
func (c *Core) AddFile(pageID, path string) (_ string, err error) {
	defer catch(&err)
	// Off the core's queue, as the desktop adds one off its main thread:
	// a video takes a while to copy, and every call of the app's would
	// wait on it. The store keeps itself safe.
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
	// The file goes up soon; it brings nothing from elsewhere.
	c.core.Sync.KickPush()
	return encode(n)
}

// Picture reads the picture at link, on the web, for an image block, as
// the desktop's image blocks read one (web.Fetch): from a public address
// only, never this device's network, and http too, which the app's own
// loading refuses. It waits on the network.
func (c *Core) Picture(link string) (_ []byte, err error) {
	defer catch(&err)
	return c.PictureCancellable(link, 0)
}

// PictureCancellable is Picture, which the app may give up on by token,
// as Cancel says, 0 for never: a picture whose page closed, or that left
// the window, stops waiting on a slow site, and leaves its turn to the
// pictures still to show.
func (c *Core) PictureCancellable(link string, token int64) (_ []byte, err error) {
	defer catch(&err)
	ctx, done := c.cancellable(token, time.Minute)
	defer done()
	return fetchPicture(ctx, link)
}

// fetchPicture reads a picture from the web, as web.FetchContext does.
// Tests replace it.
var fetchPicture = web.FetchContext

// LocalFile returns the path on this device of a file kept with a page,
// by the file_id of the block that shows it, downloading it first when
// another device uploaded it, as the desktop's image blocks do: the
// block's url is a path on the device that added it.
//
// The app may give up on it by token, as Cancel says, 0 for never.
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
	// The network's part runs off the core's queue, and so does the
	// writing of what came, as AddFile's copy does: to the disk as it
	// comes, as a video of hundreds of megabytes is never all in memory.
	ctx, done := c.cancellable(token, 5*time.Minute)
	defer done()
	data, err := c.core.Sync.FetchReader(ctx, "files", f.StoragePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = data.Close() }()
	return c.core.Store.DownloadedFileFrom(id, data)
}
