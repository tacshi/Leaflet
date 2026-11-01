package mobile

import (
	"context"
	"errors"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/store"
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

// historyLabel says what a version is: its name, or why it was kept.
func historyLabel(h store.HistoryEntry) string {
	if h.Label != "" {
		return h.Label
	}
	switch h.Reason {
	case "agent_run":
		return locales.DialogHistoryAgentRun()
	case "autosave":
		return locales.DialogHistoryAutosave()
	case "restore":
		return locales.DialogHistoryBeforeRestore()
	case "conflict":
		return locales.DialogHistoryConflict()
	}
	return ""
}

// History returns the versions kept of a page, newest first, as a JSON
// array of version. The page's edits are saved first.
func (c *Core) History(pageID string) (string, error) {
	var out []version
	var err error
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
				ID: h.ID, When: locales.DialogHistoryWhen(h.CreatedAt), Label: historyLabel(h),
				Markdown: doc.PageMarkdown("", content, c.titleOf),
			})
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

// NameVersion keeps a page as it is now as a version named name.
func (c *Core) NameVersion(pageID, name string) error {
	name = strings.TrimSpace(name)
	var err error
	c.q.do(func() {
		if p := c.pages[pageID]; p != nil {
			p.live.Save()
		}
		content := c.core.Store.Content(pageID)
		if content == nil || name == "" {
			return
		}
		err = c.core.Store.AddHistory(pageID, content, "manual", "", name)
	})
	return err
}

// RestoreVersion puts version id back as page pageID's content, keeping
// the page as it was as a version first, as the desktop does.
func (c *Core) RestoreVersion(pageID, id string) error {
	var err error
	c.q.do(func() {
		st := c.core.Store
		if !c.canEdit(pageID) {
			err = errors.New("the page can't be changed")
			return
		}
		var entries []store.HistoryEntry
		if entries, err = st.History(pageID); err != nil {
			return
		}
		var old *doc.Content
		for _, h := range entries {
			if h.ID == id {
				old, err = doc.Decode(h.Content)
			}
		}
		if err != nil {
			return
		}
		if old == nil {
			err = errors.New("no such version")
			return
		}
		if p := c.pages[pageID]; p != nil {
			p.live.Save()
		}
		if cur := st.Content(pageID); cur != nil {
			if err = st.AddHistory(pageID, cur, "restore", "", locales.DialogHistoryBeforeRestoring(time.Now())); err != nil {
				return
			}
		}
		err = c.editContent(pageID, func(content *doc.Content) bool {
			content.Children = old.Copy().Children
			return true
		})
	})
	return err
}

func (c *Core) titleOf(id string) string {
	if p, err := c.core.Store.Page(id); err == nil {
		return p.DisplayTitle()
	}
	return ""
}

// exported is a page as Markdown, as ExportMarkdown returns it.
type exported struct {
	// Name is the file's name: the title, safe in a path, and ".md".
	Name     string `json:"name"`
	Markdown string `json:"markdown"`
}

// ExportMarkdown returns a page as Markdown, with its recordings'
// transcripts, as an exported in JSON. The page's edits are saved first.
func (c *Core) ExportMarkdown(pageID string) (string, error) {
	var out exported
	var err error
	c.q.do(func() {
		if p := c.pages[pageID]; p != nil {
			p.live.Save()
		}
		var p store.Page
		if p, err = c.core.Store.Page(pageID); err != nil {
			return
		}
		content := c.content(pageID)
		out = exported{
			Name:     export.SafeName(p.DisplayTitle()) + ".md",
			Markdown: doc.PageMarkdown(p.DisplayTitle(), export.WithTranscripts(c.core.Store, content), c.titleOf),
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

// AddFile keeps the file at path with page pageID, as the desktop's
// Upload does, and returns the block that shows it, as JSON: a picture as
// an image, another file as a file card.
func (c *Core) AddFile(pageID, path string) (string, error) {
	var n *doc.Node
	var err error
	c.q.do(func() {
		var f store.File
		f, err = c.core.Store.AddFile(path, "", pageID, mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
		if err != nil {
			return
		}
		if doc.ImageExts[strings.ToLower(filepath.Ext(f.Name))] {
			n = &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
				"url": "file://" + f.LocalPath, "align": "center", "alt": f.Name, "file_id": f.ID,
			}}
		} else {
			n = &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{
				"url": "file://" + f.LocalPath, "name": f.Name, "file_id": f.ID, "size": f.SizeBytes, "mime": f.MimeType,
			}}
		}
		c.core.Sync.Kick()
	})
	if err != nil {
		return "", err
	}
	return encode(n)
}

// LocalFile returns the path on this device of a file kept with a page,
// by the file_id of the block that shows it, downloading it first when
// another device uploaded it, as the desktop's image blocks do: the
// block's url is a path on the device that added it.
func (c *Core) LocalFile(id string) (string, error) {
	var f store.File
	var err error
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
		return "", errors.New("file not uploaded yet")
	}
	// The network's part runs off the core's queue.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	data, err := c.core.Sync.Fetch(ctx, "files", f.StoragePath)
	if err != nil {
		return "", err
	}
	var path string
	c.q.do(func() { path, err = c.core.Store.DownloadedFile(id, data) })
	return path, err
}
