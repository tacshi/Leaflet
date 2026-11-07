package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"log"
	"maps"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/export"
	"leaflet/internal/menu"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/internal/templates"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

// The app as the agents' host.

// Update runs fn on the main thread.
func (a *App) Update(_ string, fn func()) { a.update(fn) }

// Live returns a page as it is open, for agent runs to change.
func (a *App) Live(pageID string) agents.LiveDoc {
	if a.openEditor(pageID) != nil {
		return a.page.live
	}
	return nil
}

// openEditor returns a page's open editor. A page whose content could not
// be read has none to change: what changes it reads the stored content,
// and leaves it.
func (a *App) openEditor(pageID string) *editor.Editor {
	if a.page != nil && a.page.id == pageID && !a.page.live.Unreadable() {
		return a.page.editor
	}
	return nil
}

// SaveNow stores the open page's pending edits.
func (a *App) SaveNow(pageID string) {
	if a.page != nil && a.page.id == pageID {
		a.savePage()
	}
}

// Online reports whether the device has a connection.
func (a *App) Online() bool { return a.online }

// Settings returns the settings.
func (a *App) Settings() store.Settings { return a.settings }

// FlushPage pushes a page's pending changes to the server.
func (a *App) FlushPage(ctx context.Context, pageID string) error {
	return a.sync.FlushPage(ctx, pageID)
}

// SyncNow asks for a sync pass.
func (a *App) SyncNow() { a.sync.Kick() }

// Pages changes pages as the app has them, for agents: the open page
// through its editor, its subpages' cards kept in step.
func (a *App) Pages() agents.Pages { return a.pageCore() }

// Tint marks the blocks an agent run changed.
func (a *App) Tint(pageID string, ids []string) {
	if a.page == nil || a.page.id != pageID {
		return
	}
	for k := range a.page.tinted {
		delete(a.page.tinted, k)
	}
	clear(a.page.lit)
	for _, id := range ids {
		a.page.tinted[id] = true
	}
	a.dialogs.hideRun = ""
}

// host is the app as the editor's host.
type host struct{ a *App }

func (h *host) PageTitle(id string) string { return h.a.pageTitleOf(id) }
func (h *host) OpenPage(id string)         { h.a.openPage(id) }
func (h *host) Image(n *doc.Node) (ui.ImageSource, *ui.VideoPlayer, bool) {
	redraw := func() { h.a.update(func() {}) }
	url := n.Str("url")
	// An uploaded picture is found by its file: its url is a path on the
	// device that uploaded it.
	path := ""
	if f, ok := h.a.fileByID(n.Str("file_id")); ok {
		if path = h.a.localFile(f, redraw); path == "" {
			return nil, nil, h.a.downloadFailed(f.ID)
		}
		url = "file://" + path
	}
	if b := h.a.images.get(url, redraw); b != nil {
		return b, h.a.images.player(url, n.ID, redraw), false
	}
	failed := url == "" || h.a.images.hasFailed(url)
	if failed && path != "" {
		// The file may have gone since it was found.
		h.a.lost(path)
	}
	return nil, nil, failed
}
func (h *host) Embed(c *ui.Context, n *doc.Node, width float32) { h.a.embedBlock(c, n, width) }
func (h *host) Video(c *ui.Context, n *doc.Node, width float32) { h.a.videoBlock(c, n, width) }
func (h *host) VideoMenu(m *ui.Menu, n *doc.Node)               { h.a.videoMenu(m, n) }
func (h *host) Card(c *ui.Context, n *doc.Node)                 { h.a.linkPreviewBlock(c, n) }
func (h *host) File(c *ui.Context, n *doc.Node, width float32)  { h.a.fileBlock(c, n, width) }
func (h *host) FileMenu(c *ui.Context, m *ui.Menu, n *doc.Node) { h.a.fileMenu(c, m, n) }
func (h *host) Recording(c *ui.Context, n *doc.Node)            { h.a.recordingBlock(c, n) }

func (h *host) CommentCount(blockID string) int {
	if h.a.page == nil {
		return 0
	}
	return h.a.page.open[blockID]
}

func (h *host) OpenComments(blockID string) { h.a.showComments(blockID) }

// CommentPreview shows a block's open threads as its badge's hover does:
// each one's quote and comments, to read; a click on the badge opens them
// in the panel to answer.
func (h *host) CommentPreview(c *ui.Context, blockID string) {
	a := h.a
	if a.page == nil {
		return
	}
	pal := theme.Of(c)
	ui.Scroll(c).MaxHeight(316).Children(func() {
		ui.Column(c).Gap(12).Children(func() {
			for _, t := range a.page.threads {
				if t.Root.BlockID != blockID || t.Root.Resolved {
					continue
				}
				ui.Column(c.Key(t.Root.ID)).Gap(8).Children(func() {
					if t.Root.Quote != "" {
						quoteChip(c, t.Root.Quote)
					}
					for _, cm := range t.All() {
						ui.Column(c.Key(cm.ID)).Gap(3).Children(func() {
							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								avatar(c, cm.AuthorName, 18, cm.AuthorKind == "agent")
								ui.Text(c, cm.AuthorName).FontSize(12.5).FontWeight(600).TextColor(pal.Text).SingleLine()
								ui.Text(c, humanTime(cm.CreatedAt)).FontSize(11.5).TextColor(pal.TextMuted).SingleLine()
							})
							if !cm.DeletedAt.IsZero() {
								ui.Text(c, commentBody(cm)).FontSize(13).TextColor(pal.Text).Wrap().PaddingStart(24)
							} else {
								a.commentText(c, cm.Body).FontSize(13).PaddingStart(24)
							}
						})
					}
				})
			}
		})
	})
}

func (h *host) SlashItems(query string) []editor.MenuItem {
	id := ""
	if h.a.page != nil {
		id = h.a.page.id
	}
	return menu.Filter(menu.Slash(templates.Items(h.a.templateChoices(), id, h.a.templateHint)), query)
}

// mentionMenu is the @ menu as last made (menu.Mention), to filter by one
// query after another, and what it was made from: the pages, the folders
// and the open page's members as read, each by its first, which a new read
// makes another; the user, the open page and the agents. The menu asks for
// its items at every frame it shows: they are made again only as one of
// those changes, and filtered again only as the query does.
type mentionMenu struct {
	from     mentionsFrom
	agents   []menu.Agent
	items    *menu.Folded
	query    string
	out      []menu.Item
	filtered bool
}

type mentionsFrom struct {
	pages            *store.Page
	folders          *store.Folder
	members          *store.Member
	nPages, nFolders int
	nMembers         int
	user, page       string
}

func (h *host) MentionItems(query string) []editor.MenuItem {
	a := h.a
	m := menu.Mentions{Pages: a.pages, Folders: a.folders, UserID: a.session.UserID}
	for _, o := range a.hub.Options() {
		m.Agents = append(m.Agents, menu.Agent{ProviderID: o.Provider.ID, Name: o.Name(), Model: o.Model})
	}
	if a.page != nil {
		m.PageID, m.Members = a.page.id, a.page.members
	}
	from := mentionsFrom{pages: firstOf(m.Pages), folders: firstOf(m.Folders), members: firstOf(m.Members),
		nPages: len(m.Pages), nFolders: len(m.Folders), nMembers: len(m.Members), user: m.UserID, page: m.PageID}
	mm := &a.mentions
	if mm.items == nil || from != mm.from || !slices.Equal(m.Agents, mm.agents) {
		*mm = mentionMenu{from: from, agents: m.Agents, items: menu.Fold(menu.Mention(m))}
	}
	if !mm.filtered || query != mm.query {
		mm.out, mm.query, mm.filtered = mm.items.Filter(query), query, true
	}
	return mm.out
}

func (h *host) Pick(t editor.Trigger, item editor.MenuItem) {
	a := h.a
	ps := a.page
	if ps == nil {
		return
	}
	ed := ps.editor
	if t.Kind == '@' {
		switch {
		case item.Key == "connect":
			ed.RemoveTrigger(t)
			a.openSettings("providers")
		case strings.HasPrefix(item.Key, "agent:"):
			ed.RemoveTrigger(t)
			choice := store.ModelChoice{ProviderID: strings.TrimPrefix(item.Key, "agent:")}
			at := editor.Pos{ID: t.ID, Off: t.Off}
			a.dialogs.inline = &inlinePrompt{blockID: t.ID, agent: choice, agentName: item.Label, from: at, to: at}
		case strings.HasPrefix(item.Key, "page:"):
			ed.InsertPageMention(t, strings.TrimPrefix(item.Key, "page:"), item.Label)
		case strings.HasPrefix(item.Key, "user:"):
			ed.InsertMention(t, doc.Mention{Kind: "user", ID: strings.TrimPrefix(item.Key, "user:"), Name: item.Label})
		}
		return
	}
	ed.RemoveTrigger(t)
	if tc, ok := a.templateByKey(item.Key); ok {
		a.insertTemplate(tc)
		return
	}
	if typ, level, ok := menu.Turn(item.Key); ok {
		ed.SetType(typ, level)
		return
	}
	if n, ok := menu.Block(item.Key, a.session.DisplayName); ok {
		ed.InsertBlocks(n)
		switch n.Type {
		case "chart":
			a.dialogs.openChart(n.ID, ed)
		case "math_equation", "diagram":
			a.dialogs.openSource(n)
		}
		return
	}
	switch item.Key {
	case "embed":
		a.dialogs.openEmbed()
	case "subpage":
		a.addSubpageBlock()
	case "video", "link_preview", "image":
		a.dialogs.openLinkDialog(item.Key)
	case "file":
		a.uploadIntoPage()
	case "recording":
		a.startRecording(ps.id)
	}
}

func (h *host) Comment(blockID, quote string) {
	h.a.panelOpen, h.a.panelTab = true, 1
	h.a.dialogs.newComment = &newComment{blockID: blockID, quote: quote}
}

func (h *host) AskAgent(blockID, quote string) {
	ip := &inlinePrompt{blockID: blockID, quote: quote, selection: true}
	ip.from, ip.to = editor.Pos{ID: blockID}, editor.Pos{ID: blockID}
	// Text selected in the page, not in a table's cell, which gives its
	// table. It can span blocks.
	if from, to := h.a.page.editor.Range(); from.ID == blockID {
		ip.from, ip.to = from, to
		ip.blocks = h.a.page.editor.SelectedBlocks()
	}
	h.a.dialogs.inline = ip
}

func (h *host) EditBlock(blockID string) {
	ps := h.a.page
	if ps == nil {
		return
	}
	n := ps.editor.Content.Find(blockID)
	switch {
	case n == nil:
	case n.Type == "chart":
		h.a.dialogs.openChart(blockID, ps.editor)
	case n.Type == "math_equation" || n.Type == "diagram":
		h.a.dialogs.openSource(n)
	case n.Type == "image" && n.Str("file_id") != "":
		h.a.uploadImage(blockID)
	case n.Type == "image" || n.Type == "video":
		h.a.dialogs.editLink(n)
	}
}

// addSubpageBlock creates a subpage, from the template set for the open
// page's new subpages if it has one, and links it from the page.
func (a *App) addSubpageBlock() {
	ps := a.page
	if ps == nil {
		return
	}
	t := a.defaultTemplate(ps.id, "")
	// As in newPageFrom: no subpage is made while the page's edits can't
	// be saved, as it could not open.
	if !a.flush() {
		return
	}
	p, err := a.st.CreatePage(store.NewPageOptions{ParentID: ps.id})
	if err != nil {
		a.toast(locales.HostErrorCreateSubpage(err.Error()))
		return
	}
	a.pages, _ = a.st.Pages()
	n := &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": p.ID}}
	ps.editor.InsertBlocks(n)
	a.treeOpen("page", ps.id)
	a.savePage()
	a.openPage(p.ID)
	if a.page != nil && a.page.id == p.ID {
		a.startFrom(t)
	}
}

// uploadImage picks a picture on the device for an image block: a new one
// at the caret when blockID is "", else in place of the block's picture.
// A file it showed before stays with the page, for its history.
func (a *App) uploadImage(blockID string) {
	ps := a.page
	if ps == nil {
		return
	}
	pageID := ps.id
	var exts []string
	for ext := range doc.ImageExts {
		exts = append(exts, strings.TrimPrefix(ext, "."))
	}
	slices.Sort(exts)
	go func() {
		paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.HostReplaceImageTitle(),
			Filters: []mygo.FileFilter{{Name: locales.HostImageFiles(), Extensions: exts}}})
		if len(paths) == 0 || !doc.ImageExts[strings.ToLower(filepath.Ext(paths[0]))] {
			return
		}
		a.addFiles(paths[:1], "", pageID, func(f store.File) {
			if a.page == nil || a.page.id != pageID {
				return
			}
			if blockID == "" {
				a.page.editor.InsertBlocks(uploadedImage(f))
				return
			}
			a.page.editor.Edit(func(content *doc.Content) {
				if n := content.Find(blockID); n != nil && n.Type == "image" {
					n.Set("url", "file://"+f.LocalPath)
					n.Set("file_id", f.ID)
					n.Set("alt", f.Name)
				}
			})
		})
	}()
}

// addFiles copies files into the app's folder, for a folder or a page;
// it runs off the main thread, as a large file takes a while to copy.
// added runs on the main thread for each, in order, once it is in; one
// that could not be added says so.
func (a *App) addFiles(paths []string, folderID, pageID string, added func(f store.File)) {
	for _, path := range paths {
		f, err := a.st.AddFile(path, folderID, pageID, mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
		a.update(func() {
			if err != nil {
				a.toast(locales.HostErrorAddFile(err.Error()))
				return
			}
			added(f)
		})
	}
}

// uploadedImage is an image block showing a file of the page.
func uploadedImage(f store.File) *doc.Node {
	return &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
		"url": "file://" + f.LocalPath, "align": "center", "alt": f.Name, "file_id": f.ID,
	}}
}

// uploadIntoPage picks files and puts them in the page: a picture as an
// image, others as file cards.
func (a *App) uploadIntoPage() {
	ps := a.page
	if ps == nil {
		return
	}
	pageID := ps.id
	go func() {
		paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.HostUploadTitle()})
		a.addFiles(paths, "", pageID, func(f store.File) {
			if a.page == nil || a.page.id != pageID {
				a.toast(locales.HostAddedToSpace(f.Name))
				return
			}
			n := &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{
				"url": "file://" + f.LocalPath, "name": f.Name, "file_id": f.ID, "size": f.SizeBytes, "mime": f.MimeType,
			}}
			if doc.ImageExts[strings.ToLower(filepath.Ext(f.Name))] {
				n = uploadedImage(f)
			}
			a.page.editor.InsertBlocks(n)
		})
	}()
}

// uploadFile adds files to a folder.
func (a *App) uploadFile(folderID string) {
	go func() {
		paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.HostUploadTitle()})
		if len(paths) == 0 {
			return
		}
		a.update(func() { a.treeOpen("folder", folderID) })
		a.addFiles(paths, folderID, "", func(store.File) {})
	}()
}

func (a *App) openFile(f store.File) {
	// A click tries a download that failed again, and looks for the file
	// on the device again.
	delete(a.downloadFails, f.ID)
	a.lost(f.LocalPath)
	path := a.localFile(f, func() {
		if f, ok := a.fileByID(f.ID); ok && f.LocalPath != "" {
			openSafely(f.LocalPath)
		}
	})
	if path == "" {
		a.toast(locales.HostDownloading(f.Name))
		return
	}
	openSafely(path)
}

// openSafely opens a file in its app. Files come from other devices and
// people, so the system is told first that it came from elsewhere, for
// its checks before a program runs; and a file that would run as one,
// not open in one, is shown in its folder instead.
func openSafely(path string) {
	markDownloaded(path)
	if runsAsProgram(path) {
		mygo.Shell.ShowItemInFolder(path)
		return
	}
	_ = mygo.Shell.OpenPath(path)
}

// programExts are the kinds of file that opening runs, or sends where
// they say: apps, installers, scripts, the shortcuts and links to them,
// documents that run what they hold, and disk images and plug-ins, which
// opening mounts or installs.
var programExts = map[string]bool{
	".app": true, ".command": true, ".tool": true, ".terminal": true, ".sh": true, ".bash": true, ".zsh": true, ".csh": true,
	".ksh": true, ".fish": true, ".pkg": true, ".mpkg": true, ".dmg": true, ".scpt": true, ".scptd": true, ".applescript": true,
	".workflow": true, ".action": true, ".fileloc": true, ".webloc": true, ".inetloc": true, ".afploc": true, ".ftploc": true,
	".mobileconfig": true, ".jar": true, ".jnlp": true, ".py": true, ".pyw": true, ".pl": true, ".rb": true, ".php": true,
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".msi": true, ".msp": true, ".msix": true, ".msixbundle": true,
	".appx": true, ".appxbundle": true, ".appinstaller": true, ".application": true, ".gadget": true, ".ps1": true,
	".psm1": true, ".psd1": true, ".vb": true, ".vbs": true, ".vbe": true, ".js": true, ".jse": true, ".wsf": true,
	".wsh": true, ".wsc": true, ".sct": true, ".hta": true, ".scr": true, ".cpl": true, ".msc": true, ".inf": true,
	".lnk": true, ".pif": true, ".reg": true, ".appref-ms": true, ".url": true, ".website": true, ".scf": true,
	".library-ms": true, ".search-ms": true, ".searchconnector-ms": true, ".settingcontent-ms": true, ".chm": true,
	".hlp": true, ".diagcab": true, ".xbap": true, ".desktop": true, ".appimage": true, ".run": true, ".deb": true,
	".rpm": true, ".flatpakref": true, ".snap": true, ".iso": true, ".img": true, ".cdr": true, ".sparseimage": true,
	".sparsebundle": true, ".vhd": true, ".vhdx": true, ".pyz": true, ".pyzw": true, ".prefpane": true, ".saver": true,
}

// runsAsProgram reports whether opening the file would run it.
func runsAsProgram(path string) bool {
	if programExts[strings.ToLower(filepath.Ext(path))] {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && runtime.GOOS != "windows" && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// fileByID finds a file: in an index of the files, made again once they
// are others than it was made from, as every uploaded picture asks as it
// draws.
func (a *App) fileByID(id string) (store.File, bool) {
	if id == "" {
		return store.File{}, false
	}
	from := listsFrom{files: firstOf(a.files), nFiles: len(a.files)}
	if a.fileIndex == nil || from != a.fileIndexFrom {
		a.fileIndex, a.fileIndexFrom = make(map[string]int, len(a.files)), from
		for i, f := range a.files {
			if _, ok := a.fileIndex[f.ID]; !ok {
				a.fileIndex[f.ID] = i
			}
		}
	}
	i, ok := a.fileIndex[id]
	if !ok {
		return store.File{}, false
	}
	return a.files[i], true
}

// presence is whether a file was on this device, and when that was found.
type presence struct {
	there bool
	at    time.Time
}

// onDevice reports whether the file at path is there: as last looked, not
// at every frame that shows it, until the files are read again, as after
// a download, or showing it failed (lost); one that was not there is
// looked for again once imageRetry has passed.
func (a *App) onDevice(path string) bool {
	if p, ok := a.present[path]; ok && (p.there || time.Since(p.at) < imageRetry) {
		return p.there
	}
	_, err := os.Stat(path)
	if a.present == nil {
		a.present = map[string]presence{}
	}
	a.present[path] = presence{there: err == nil, at: time.Now()}
	return err == nil
}

// lost has the file at path looked for again, as showing it failed.
func (a *App) lost(path string) { delete(a.present, path) }

// localFile returns where a file is on this device, or "" while one
// uploaded from another device downloads; done runs once it has. A
// download that failed is tried again once imageRetry has passed, or
// when the file is opened.
func (a *App) localFile(f store.File, done func()) string {
	if f.LocalPath != "" && a.onDevice(f.LocalPath) {
		return f.LocalPath
	}
	if f.StoragePath == "" || !a.sync.CanFetch() || a.downloads[f.ID] || a.downloadFailed(f.ID) {
		return ""
	}
	if a.downloads == nil {
		a.downloads = map[string]bool{}
	}
	a.downloads[f.ID] = true
	var err error
	a.offMain(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		// Written to the disk as it comes, never all in memory.
		var r io.ReadCloser
		r, err = a.sync.FetchReader(ctx, "files", f.StoragePath)
		if err == nil {
			_, err = a.st.DownloadedFileFrom(f.ID, r)
			_ = r.Close()
		}
	}, func() {
		delete(a.downloads, f.ID)
		if err != nil {
			log.Printf("download %s: %v", f.Name, err)
			if a.downloadFails == nil {
				a.downloadFails = map[string]time.Time{}
			}
			a.downloadFails[f.ID] = time.Now()
			// The next frame after the wait tries again.
			a.later(imageRetry, func() {})
			return
		}
		delete(a.downloadFails, f.ID)
		a.reload(store.Change{Table: "files"})
		done()
	})
	return ""
}

// downloadFailed reports whether a file's download failed, and waits to be
// tried again.
func (a *App) downloadFailed(id string) bool {
	at, ok := a.downloadFails[id]
	return ok && time.Since(at) < imageRetry
}

// exportMarkdown saves a page as Markdown, as the other devices have it.
func (a *App) exportMarkdown(id string) {
	// As exportAll, not without the edits waiting.
	if a.page != nil && a.page.id == id && !a.savePage() {
		return
	}
	a.exportContent(id, func(c *doc.Content) {
		p := a.pageByID(id)
		if p == nil {
			return
		}
		opts := a.markdownOptions()
		opts.Link = export.Moments(c)
		md := doc.PageMarkdown(p.DisplayTitle(), a.withTranscripts(c), opts)
		name := safeName(p.DisplayTitle()) + ".md"
		go func() {
			path, _ := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: locales.HostExportTitle(), DefaultPath: name})
			if path == "" {
				return
			}
			err := os.WriteFile(path, []byte(md), 0o644) // #nosec G306 -- a file the user saves, readable like any other they save.
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

// keptHere reports whether this device has page p's content as the other
// devices have it, kept up to date as it changes: with nothing to sync
// it with, open or served by a public link (pulled), or kept on the
// device, alone or with all the pages.
func (a *App) keptHere(p store.Page) bool {
	return !a.syncOn() || a.pulled(p.ID) || p.KeepOffline || a.settings.AllPagesOffline
}

// exportContent runs use, which exports page id, with its content as the
// other devices have it, on the main thread: at once where this device
// keeps it up to date (keptHere), else once it has come down, as when the
// page opens, or as this device has it when it can't come now. Content
// that is not on this device at all is said to be missing instead, and
// nothing is exported.
func (a *App) exportContent(id string, use func(c *doc.Content)) {
	p := a.pageByID(id)
	if p == nil {
		return
	}
	if a.keptHere(*p) {
		if c := a.st.Content(id); c != nil {
			use(c)
			return
		}
	}
	if a.sync.CanSync() {
		a.toast(locales.HostDownloading(p.DisplayTitle()))
	}
	var c *doc.Content
	var err error
	a.offMain(func() {
		// Between passes, as export.All brings a page down.
		a.sync.Hold(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c, err = a.sync.RefreshContent(ctx, id)
		})
	}, func() {
		if err != nil {
			// The server could not be reached, or failed: the copy on this
			// device is exported as it is, where there is one.
			log.Printf("export %s: %v", id, err)
			c = a.st.Content(id)
		}
		switch {
		case c != nil:
			use(c)
		case err != nil:
			a.toast(locales.HostErrorExport(err.Error()))
		default:
			a.toast(locales.HostErrorExport(locales.CardNotOnDevice()))
		}
	})
}

func safeName(s string) string { return export.SafeName(s) }

// loadMembers reads who a page is shared with, as it opens. Sync reads
// them itself for the user's own pages as they open (sync.Service.SetOpen),
// on the server: here they are read for the pages others share, and for
// all of them while sync goes through iCloud Drive instead.
func (a *App) loadMembers(pageID string) {
	if p, err := a.st.Page(pageID); err == nil && p.SpaceID == a.st.Session().SpaceID && !a.sync.ICloudOn() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = a.sync.LoadMembers(ctx, pageID)
}

// imageCache loads the pictures of image blocks; root is the app's data
// folder, the only one it reads files from.
type imageCache struct {
	root string
	mu   sync.Mutex
	// pictures are bitmaps, or SVGs, which MyGo draws itself; used is
	// when each was last asked for, and swept when releasePictures last
	// looked at them.
	pictures map[string]ui.ImageSource
	used     map[string]time.Time
	swept    time.Time
	loading  map[string]bool
	// failed holds when a picture last failed to load.
	failed map[string]time.Time
	// gifs are the animated GIFs among the pictures, as read. players
	// play them, one for each block showing one, as MyGo shows a player
	// in one place at a time: by url, # and block, none while it is made.
	// releasePlayers closes those no block has shown for a while.
	gifs    map[string][]byte
	players map[string]*gifPlayer
	making  map[string]bool
	// closed is set once the cache is let go, as it is cleared: what it
	// still makes then is closed rather than kept.
	closed bool
}

// gifPlayer is a block's player of an animated GIF, nil for one that did
// not decode, and when a block last asked for it.
type gifPlayer struct {
	p     *ui.VideoPlayer
	shown time.Time
}

// playerIdle is how long a GIF's player outlives the last frame that
// built its block: its decoded frames and its goroutine go then.
const playerIdle = 30 * time.Second

// imageRetry is how long a picture that failed to load waits before it is
// tried again, as the device may have come back online.
const imageRetry = time.Minute

func newImageCache(root string) *imageCache {
	return &imageCache{root: root, pictures: map[string]ui.ImageSource{}, used: map[string]time.Time{}, loading: map[string]bool{}, failed: map[string]time.Time{},
		gifs: map[string][]byte{}, players: map[string]*gifPlayer{}, making: map[string]bool{}}
}

// get returns a picture, or nil while it loads; done runs when it has
// loaded or failed.
func (ic *imageCache) get(url string, done func()) ui.ImageSource {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if b, ok := ic.pictures[url]; ok {
		ic.used[url] = time.Now()
		return b
	}
	if ic.loading[url] || url == "" {
		return nil
	}
	if t, ok := ic.failed[url]; ok && time.Since(t) < imageRetry {
		return nil
	}
	ic.loading[url] = true
	go func() {
		data, err := ic.read(url)
		var b ui.ImageSource
		if err == nil {
			err = checkPixels(data)
		}
		if err == nil {
			b, err = ui.DecodeImage(data)
		}
		if err != nil {
			log.Printf("image %s: %v", url, err)
		}
		ic.mu.Lock()
		delete(ic.loading, url)
		if b != nil {
			ic.pictures[url], ic.used[url] = b, time.Now()
			delete(ic.failed, url)
			if animatedGIF(data) {
				ic.gifs[url] = data
			}
		} else {
			ic.failed[url] = time.Now()
		}
		ic.mu.Unlock()
		done()
	}()
	return nil
}

// player returns the player of an animated GIF in block, looping, or nil
// for another picture, and while it is made, off the main thread, as its
// frames decode; done runs when it is made.
func (ic *imageCache) player(url, block string, done func()) *ui.VideoPlayer {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	data, ok := ic.gifs[url]
	key := url + "#" + block
	if !ok || ic.making[key] {
		return nil
	}
	if gp, ok := ic.players[key]; ok {
		gp.shown = time.Now()
		return gp.p
	}
	ic.making[key] = true
	go func() {
		d, err := ui.NewGIFDecoder(data)
		var p *ui.VideoPlayer
		if err == nil {
			p, err = ui.NewVideoPlayer(d)
		}
		if err != nil {
			log.Printf("gif %s: %v", url, err)
		} else {
			p.SetLoop(true)
		}
		ic.mu.Lock()
		delete(ic.making, key)
		if ic.closed {
			// No one else has this player yet.
			if p != nil {
				p.Close()
			}
		} else {
			ic.players[key] = &gifPlayer{p: p, shown: time.Now()}
		}
		ic.mu.Unlock()
		done()
	}()
	return nil
}

// releasePlayers closes and forgets the players no block has asked for
// within playerIdle of now: those of blocks gone, scrolled away, or on a
// page no longer open, and those made after their block went. Call it on
// the main thread, as a player's methods are, after a frame's blocks are
// built, so that a player on screen has just been asked for.
func (ic *imageCache) releasePlayers(now time.Time) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	for key, gp := range ic.players {
		if now.Sub(gp.shown) < playerIdle {
			continue
		}
		if gp.p != nil {
			gp.p.Close()
		}
		delete(ic.players, key)
	}
}

// pictureIdle is how long a picture stays decoded after the last frame
// that asked for it, and pictureBudget about how many bytes of pixels the
// pictures may hold: each of a page of large photos scrolled through
// would stay otherwise, up to 200 MB each. The budget is a variable for
// tests.
const pictureIdle = 2 * time.Minute

var pictureBudget int64 = 512 << 20

// releasePictures forgets, once a second at most, the pictures no frame
// has asked for within pictureIdle of now, and the least recently asked
// for while the rest hold more than pictureBudget, but for those asked for
// within the last second, which are on screen; a picture forgotten loads
// again as a block asks for it. Call it as releasePlayers.
func (ic *imageCache) releasePictures(now time.Time) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if now.Sub(ic.swept) < time.Second {
		return
	}
	ic.swept = now
	type held struct {
		url   string
		at    time.Time
		bytes int64
	}
	var kept []held
	var total int64
	for url, b := range ic.pictures {
		at := ic.used[url]
		if now.Sub(at) >= pictureIdle {
			ic.forget(url)
			continue
		}
		w, h := ui.ImageSize(b)
		n := int64(w) * int64(h) * 4
		kept = append(kept, held{url, at, n})
		total += n
	}
	if total <= pictureBudget {
		return
	}
	slices.SortFunc(kept, func(x, y held) int { return x.at.Compare(y.at) })
	for _, p := range kept {
		if total <= pictureBudget || now.Sub(p.at) < time.Second {
			break
		}
		ic.forget(p.url)
		total -= p.bytes
	}
}

// forget lets a picture go; under mu.
func (ic *imageCache) forget(url string) {
	delete(ic.pictures, url)
	delete(ic.gifs, url)
	delete(ic.used, url)
}

// maxPixels is the most pixels a picture may have to be shown, and
// maxGIFPixels those of all an animated GIF's frames: a small file can
// say it is far larger, and decoding it would take more memory than the
// device has.
const (
	maxPixels    = doc.MaxPixels
	maxGIFPixels = 200_000_000
)

// errTooLarge is for a picture of more pixels than maxPixels.
var errTooLarge = errors.New("picture too large")

// checkPixels refuses a picture whose header says it has more pixels than
// it may, before it is decoded. What is no bitmap, as an SVG, passes.
func checkPixels(data []byte) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	if cfg.Width*cfg.Height > maxPixels {
		return errTooLarge
	}
	if _, area := gifFrames(data); area > maxGIFPixels {
		return errTooLarge
	}
	return nil
}

// close closes the players, for a cache let go. Call it on the main
// thread, as releasePlayers.
func (ic *imageCache) close() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.closed = true
	for key, gp := range ic.players {
		if gp.p != nil {
			gp.p.Close()
		}
		delete(ic.players, key)
	}
}

// animatedGIF reports whether data is a GIF of more than one frame.
func animatedGIF(data []byte) bool {
	n, _ := gifFrames(data)
	return n > 1
}

// gifFrames counts a GIF's frames, and their pixels, from its blocks,
// without decoding them; 0 for what is no GIF. What follows a block cut
// short is left out.
func gifFrames(data []byte) (frames, area int) {
	if !bytes.HasPrefix(data, []byte("GIF8")) || len(data) < 13 {
		return 0, 0
	}
	// A color table, after a packed byte saying it is there, of 2^(n+1)
	// colors of 3 bytes.
	table := func(packed byte) int {
		if packed&0x80 == 0 {
			return 0
		}
		return 3 << (packed&7 + 1)
	}
	// skip passes over sub-blocks: each its size, then its bytes, till
	// one of size 0.
	skip := func(i int) int {
		for i < len(data) && data[i] != 0 {
			i += 1 + int(data[i])
		}
		return i + 1
	}
	i := 13 + table(data[10])
	for i < len(data) {
		switch data[i] {
		case 0x21: // an extension: its label, then sub-blocks
			i = skip(i + 2)
		case 0x2C: // a frame: where it is, its size, then its pixels
			if i+10 > len(data) {
				return frames, area
			}
			w, h := int(data[i+5])|int(data[i+6])<<8, int(data[i+7])|int(data[i+8])<<8
			frames, area = frames+1, area+w*h
			i = skip(i + 10 + table(data[i+9]) + 1)
		default: // the trailer, or what is no block
			return frames, area
		}
	}
	return frames, area
}

// hasFailed reports whether a picture failed to load, and is not being
// tried again.
func (ic *imageCache) hasFailed(url string) bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	_, ok := ic.failed[url]
	return ok && !ic.loading[url]
}

// read reads a picture: from the web, or a file of the app's own, in its
// data folder. A file elsewhere on the device is not shown, as a block's
// link to one can come from another device or person.
func (ic *imageCache) read(url string) ([]byte, error) {
	path, ok := strings.CutPrefix(url, "file://")
	if !ok {
		return web.Fetch(url)
	}
	if !inFolder(ic.root, path) {
		return nil, errNotOwnFile
	}
	return os.ReadFile(path) // #nosec G304 -- a file in the app's data folder.
}

// errNotOwnFile is for a file outside the app's data folder.
var errNotOwnFile = export.ErrNotOwnFile

// inFolder reports whether path is in folder dir, links followed.
func inFolder(dir, path string) bool { return export.InFolder(dir, path) }

// embedURL is where an embed's wrapper page loads. WebView2 serves a
// loaded document only to a URL it requests over the network, and never
// requests about:blank, so there the page loads at an https URL of the
// app's bundle ID instead, as a video's does, trusted to tell Go its
// size. Its policy still keeps it off the network, and the embed's own
// sandboxed frame has an opaque origin either way.
var embedURL = map[bool]string{true: "https://embed.app.capyvibe.leaflet/", false: "about:blank"}[runtime.GOOS == "windows"]

// hexOf writes a color as CSS's "#rrggbb".
func hexOf(c ui.Color) string {
	const digits = "0123456789abcdef"
	out := []byte{'#'}
	for _, v := range []uint8{c.R, c.G, c.B} {
		out = append(out, digits[v>>4], digits[v&15])
	}
	return string(out)
}

// embedScroll is the service an embed's wrapper calls: to scroll the page,
// and to say how tall the embed's content is and whether it is zoomed in.
type embedScroll struct{ a *App }

// embedSize is how tall an embed's content is at a width of its frame.
// kept is set while it is what a view let go said: the block keeps that
// height, its view made again hidden till it says its own.
type embedSize struct {
	h, w float32
	kept bool
}

// Size keeps the height of embed id's content at its frame's width w. The
// first call also tells that the embed has drawn, so it shows.
func (s *embedScroll) Size(id string, h, w float64) {
	s.a.update(func() {
		s.a.embedSizes[id] = embedSize{h: float32(min(max(h, 0), 4000)), w: float32(w)}
	})
}

// Zoomed keeps whether diagram id is zoomed in, for its bar to offer to
// fit it again.
func (s *embedScroll) Zoomed(id string, zoomed bool) {
	s.a.update(func() {
		if zoomed {
			s.a.embedZoomed[id] = true
		} else {
			delete(s.a.embedZoomed, id)
		}
	})
}

// Exit leaves the full screen view of the diagram whose frame is id.
func (s *embedScroll) Exit(id string) {
	s.a.update(func() {
		if ps := s.a.page; ps != nil && ps.fullDiagram != "" && fullView(ps.fullDiagram) == id {
			s.a.leaveFullDiagram()
		}
	})
}

// zoomEmbed asks embed key's frame to zoom: "in", "out", or "fit" to show
// all of it again.
func (a *App) zoomEmbed(key, how string) {
	v := a.embeds[key]
	if v == nil {
		return
	}
	page := v.Page()
	script := `document.getElementById("f").contentWindow.postMessage({leafletZoom: ` + strconv.Quote(how) + `}, "*")`
	// Eval waits for the page, so not on the UI thread.
	go func() { _, _ = page.Eval(script) }()
}

// maxEmbedWheel is the most DIPs an embed's leftover wheel scrolls the
// page in a frame.
const maxEmbedWheel = 2000

// Scroll scrolls the open page by dy DIPs, for wheel movement over embed
// id that the embed did not use: only while the pointer is over its view,
// as an embed could otherwise scroll the page at will, and once a frame
// (embedWheel), however often it asks.
func (s *embedScroll) Scroll(id string, dy float64) {
	dy = max(min(dy, maxEmbedWheel), -maxEmbedWheel)
	s.a.update(func() {
		if a := s.a; a.embedOver == id && a.embeds[id] != nil {
			a.embedWheel += float32(dy)
		}
	})
}

// scrollByEmbeds scrolls the open page by the wheel the embed under the
// pointer passed on since the frame before, up to maxEmbedWheel.
func (a *App) scrollByEmbeds(ps *pageState) {
	if dy := a.embedWheel; dy != 0 {
		a.embedWheel = 0
		ps.scroll.Y = max(min(ps.scroll.Y+max(min(dy, maxEmbedWheel), -maxEmbedWheel), ps.scroll.MaxY), 0)
	}
}

func (a *App) embedBlock(c *ui.Context, n *doc.Node, width float32) {
	switch n.Type {
	case "math_equation", "diagram":
		a.renderedBlock(c, n, width)
		return
	}
	pal := theme.Of(c)
	height := float32(min(max(n.Int("height", 320), 120), 1200))
	// The frame is inside the card's border and padding.
	const pad = 14
	// A card as a chart is, with the title inside.
	box := ui.Column(c.Key("embed:"+n.ID)).Padding(12, pad, pad).Gap(10).Radius(10).Border(1, pal.Divider).Background(pal.Surface)
	hovered := box.Hovered()
	box.Children(func() {
		ui.Row(c).Height(24).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, n.Str("title")).FontWeight(650).FontSize(15).TextColor(pal.Text).Grow(1).SingleLine()
			if a.page != nil && !a.page.editor.ReadOnly {
				b := ui.ButtonBase(c.Key("edit")).Height(24).Padding(0, 8).Gap(4).Radius(6).TextColor(pal.TextMuted).
					Opacity(map[bool]float32{true: 1, false: 0}[hovered]).IgnoreInput(!hovered).AlignItems(ui.Center).Label(locales.HostEditEmbed())
				if b.Hovered() {
					b.Background(pal.Hover)
				}
				b.Children(func() {
					ui.Icon(c, theme.Icon("pencil")).FontSize(12)
					ui.Text(c, locales.CommonEdit()).FontSize(12)
				})
				if b.Clicked() {
					a.dialogs.editEmbed(n)
				}
			}
			ui.Row(c).Gap(4).AlignItems(ui.Center).Tooltip(locales.HostSandboxTooltip()).Children(func() {
				ui.Icon(c, theme.Icon("shield-check")).FontSize(12).TextColor(pal.TextMuted)
				ui.Text(c, locales.HostSandboxed()).FontSize(11).TextColor(pal.TextMuted)
			})
		})
		// The block grows when the content is taller at this width, as a
		// narrow page stacks its layout.
		html := n.Str("html")
		a.sandbox(c, blockView(n.ID), embedInput{src: html, kind: n.Type}, func() string { return html }, width-2-2*pad, func(content float32) float32 {
			return max(height, min(content, 1200))
		}, height)
	})
}

// embedInput is what an embed's web view loads its page from: the embed's
// HTML, or a formula's or a diagram's source, of block kind kind ("full"
// for a diagram's full screen view), on the background bg, dark or not.
// toPage gives the page all of the wheel (sandbox). The page, as large as
// the scripts that draw math and diagrams, is made again only as one of
// them changes, not at every frame.
type embedInput struct {
	src, kind string
	bg        ui.Color
	dark      bool
	toPage    bool
}

// sandbox shows a block's HTML in its sandboxed web view, key naming it,
// at width w inside the page: page makes the HTML of in, which sandbox
// fills in with the theme's colors. fit gives the frame's height from its
// content's, once that has drawn; until then it is height, and the box
// shows rather than a blank page in the frame's color. A nil fit has the
// frame fill the rest of its parent instead. The view loads again when in
// changes: by hand, by an agent, by undo or a restored version, or as the
// theme does.
//
// A block's view is made only once its frame comes within a window's
// height of the window, and let go once it is three away: a page of many
// formulas runs those near the part of it shown, each with its own copy
// of KaTeX, not all of them. One let go keeps its height for its block.
//
// in.toPage gives all of the wheel over the view to the page around it, as
// MyGo does natively, for HTML that neither scrolls nor takes the wheel
// itself, as math; otherwise the frame keeps the wheel and passes the
// page what it leaves.
func (a *App) sandbox(c *ui.Context, key string, in embedInput, page func() string, w float32, fit func(float32) float32, height float32) {
	pal := theme.Of(c)
	in.bg, in.dark = pal.Surface, pal.Dark
	if a.embedBuilt == nil {
		a.embedBuilt = map[string]bool{}
	}
	a.embedBuilt[key] = true
	size, sized := a.embedSizes[key]
	if sized && fit != nil && abs32(size.w-w) < 4 {
		height = fit(size.h)
	}
	ready := sized && !size.kept
	v := a.embeds[key]
	if v != nil && a.embedToPage[key] != in.toPage {
		// The block was replaced by another kind, keeping its ID, as an
		// agent's replace_block does: a view takes the wheel as it was
		// made, so a new one takes it as the new kind needs.
		a.letGoEmbed(key)
		delete(a.embedIn, key)
		v = nil
	}
	// A view that could not be made is not tried again at every frame,
	// while the block stays in the page: what failed would fail again.
	if why, failed := a.embedFailed[key]; v == nil && failed {
		ui.Text(c, locales.HostEmbedError(why)).TextColor(pal.TextMuted)
		return
	}
	var holder ui.Element
	near := true
	if fit != nil {
		holder = ui.Column(c.Key("frame:" + key)).FillWidth()
		var far bool
		near, far = embedNear(c, holder.Bounds())
		if v != nil && far {
			a.letGoEmbed(key)
			v = nil
		}
	}
	made := false
	if v == nil && near && a.win != nil {
		wv, err := a.win.NewWebView(embedViewOptions(pal.Surface, in.toPage))
		if err != nil {
			log.Printf("embed %s: %v", key, err)
			a.embedFailed[key] = err.Error()
			ui.Text(c, locales.HostEmbedError(err.Error())).TextColor(pal.TextMuted)
			return
		}
		p := wv.Page()
		p.OnWillNavigate(func(e *mygo.NavigateEvent) {
			if !strings.HasPrefix(e.URL, "about:") && !strings.HasPrefix(e.URL, "data:") {
				e.PreventDefault()
			}
		})
		p.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
		a.embeds[key] = wv
		if in.toPage {
			a.embedToPage[key] = true
		} else {
			delete(a.embedToPage, key)
		}
		v, made = wv, true
	}
	if v != nil && (made || a.embedIn[key] != in) {
		if was, ok := a.embedIn[key]; ok && was == in && sized {
			// Made again for the same page: it keeps its height, hidden
			// till it has drawn.
			size.kept = true
			a.embedSizes[key] = size
		} else {
			delete(a.embedSizes, key)
		}
		v.Page().LoadHTML(rendered.EmbedPage(key, page(), hexOf(in.bg), in.dark, !in.toPage), embedURL)
		a.embedIn[key] = in
		delete(a.embedZoomed, key)
		ready = false
	}
	show := func() {
		if v == nil {
			// Away from what the window shows: the block keeps its room.
			ui.Box(c).Height(height).FillWidth().Radius(6).Background(pal.Ground)
			return
		}
		wv := ui.WebView(c, v).FillWidth().Radius(6)
		if wv.Hovered() {
			a.embedOver = key
		} else if a.embedOver == key {
			a.embedOver = ""
		}
		if fit == nil {
			wv.Grow(1)
			if !ready {
				wv.Opacity(0)
			}
			return
		}
		wv.Height(height)
		if !ready {
			wv.Opacity(0)
			ui.Box(c).Height(height).FillWidth().Margin(-height, 0, 0, 0).Radius(6).Background(pal.Ground)
		}
	}
	if fit == nil {
		show()
		return
	}
	holder.Children(show)
}

// embedViewOptions are those of an embed's web view, on a background of
// bg: every frame of its page starts without WebRTC (rendered.NoRTC), the
// frames the embed makes itself too, before their own scripts run.
func embedViewOptions(bg ui.Color, toPage bool) mygo.WebViewOptions {
	return mygo.WebViewOptions{BackgroundColor: hexOf(bg), ScrollToContainer: toPage,
		Page: mygo.PageOptions{TrustedOrigins: []string{strings.TrimSuffix(embedURL, "/")},
			PreloadScript: rendered.NoRTC, PreloadAllFrames: true}}
}

// embedNear tells where a block's frame, r as last laid out, is from the
// window: near within a window's height of it, where its web view is
// made, or far past three, where it is let go; in between, neither, and a
// view keeps as it is. A frame not laid out yet is neither, and the next
// frame tells; in a window of no size, all are near.
func embedNear(c *ui.Context, r ui.Rect) (near, far bool) {
	_, wh := c.Size()
	if wh <= 0 {
		return true, false
	}
	if r.W <= 0 && r.H <= 0 {
		c.Invalidate()
		return false, false
	}
	var away float32
	switch {
	case r.Y+r.H < 0:
		away = -(r.Y + r.H)
	case r.Y > wh:
		away = r.Y - wh
	}
	return away <= wh, away > 3*wh
}

// blockView and fullView are the keys of a block's web view: in its place
// in the page, and filling the page's body, as a diagram's full screen
// view. Each is its kind and the block's ID after it, so that no ID a
// block has, whatever it holds, names another block's view.
func blockView(id string) string { return "block:" + id }

func fullView(id string) string { return "full:" + id }

// viewBlock is the block a web view's key is of, and whether the view is
// the one filling the page's body (fullView).
func viewBlock(key string) (id string, full bool) {
	kind, id, _ := strings.Cut(key, ":")
	return id, kind == "full"
}

// letGoEmbed destroys embed key's web view, keeping what its block shows
// at: its height, and the page it had, to tell its next view, made for
// the same page, to keep that height.
func (a *App) letGoEmbed(key string) {
	if v := a.embeds[key]; v != nil {
		v.Destroy()
	}
	delete(a.embeds, key)
	delete(a.embedZoomed, key)
	if a.embedOver == key {
		a.embedOver = ""
	}
}

// dropEmbeds lets go of the web views of embeds, math and diagrams no
// longer in the open page, and what they said about themselves, as
// deleting one or undoing its insert leaves it running otherwise; and,
// called as the page has built, of the views of blocks it did not build,
// as those folded away, which keep what they said, to show again.
func (a *App) dropEmbeds() {
	gone := func(key string) bool {
		id, _ := viewBlock(key)
		return a.page == nil || !a.page.editor.HasBlock(id)
	}
	for key := range a.embeds {
		if _, full := viewBlock(key); gone(key) || !a.embedBuilt[key] && !full {
			a.letGoEmbed(key)
		}
	}
	clear(a.embedBuilt)
	maps.DeleteFunc(a.embedIn, func(key string, _ embedInput) bool { return gone(key) })
	maps.DeleteFunc(a.embedSizes, func(key string, _ embedSize) bool { return gone(key) })
	maps.DeleteFunc(a.embedZoomed, func(key string, _ bool) bool { return gone(key) })
	maps.DeleteFunc(a.embedToPage, func(key string, _ bool) bool { return gone(key) })
	maps.DeleteFunc(a.embedFailed, func(key, _ string) bool { return gone(key) })
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

var _ agents.Host = (*App)(nil)
