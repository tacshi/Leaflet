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
	"leaflet/internal/recording"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/internal/templates"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

func (a *App) Update(_ string, fn func()) { a.update(fn) }

func (a *App) Live(pageID string) agents.LiveDoc {
	if a.openEditor(pageID) != nil {
		return a.page.live
	}
	return nil
}

func (a *App) openEditor(pageID string) *editor.Editor {
	if a.page != nil && a.page.id == pageID && !a.page.live.Unreadable() {
		return a.page.editor
	}
	return nil
}

func (a *App) SaveNow(pageID string) {
	if a.page != nil && a.page.id == pageID {
		a.savePage()
	}
}

func (a *App) Online() bool { return a.online }

func (a *App) Settings() store.Settings { return a.settings }

func (a *App) Pages() agents.Pages { return a.pageCore() }

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

type host struct{ a *App }

func (h *host) PageTitle(id string) string { return h.a.pageTitleOf(id) }
func (h *host) OpenPage(id string)         { h.a.openPage(id) }
func (h *host) Image(n *doc.Node) (ui.ImageSource, *ui.VideoPlayer, bool) {
	redraw := func() { h.a.update(func() {}) }
	downloaded := func(ok bool) {
		if ok {
			redraw()
		}
	}
	url := n.Str("url")
	path := ""
	if f, ok := h.a.fileByID(n.Str("file_id")); ok {
		done := downloaded
		if _, busy := h.a.downloads[f.ID]; busy {
			done = nil
		}
		fetching := false
		if path, fetching = h.a.localFile(f, done); path == "" {
			return nil, nil, !fetching
		}
		url = "file://" + path
	}
	if b := h.a.images.get(url, redraw); b != nil {
		return b, h.a.images.player(url, n.ID, redraw), false
	}
	failed := url == "" || h.a.images.hasFailed(url)
	if failed && path != "" {
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
		a.startRecording(ps.id, "")
	}
}

func (h *host) Comment(blockID, quote string) {
	h.a.panelOpen, h.a.panelTab = true, 1
	h.a.dialogs.newComment = &newComment{blockID: blockID, quote: quote}
}

func (h *host) AskAgent(blockID, quote string) {
	ip := &inlinePrompt{blockID: blockID, quote: quote, selection: true}
	ip.from, ip.to = editor.Pos{ID: blockID}, editor.Pos{ID: blockID}
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

func (a *App) addSubpageBlock() {
	ps := a.page
	if ps == nil {
		return
	}
	t := a.defaultTemplate(ps.id, "")
	if !a.flush() {
		return
	}
	p, err := a.st.CreatePage(store.NewPageOptions{ParentID: ps.id})
	if err != nil {
		a.toast(locales.HostErrorCreateSubpage(err.Error()))
		return
	}
	reread(&a.pages, a.st.Pages)
	n := &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": p.ID}}
	ps.editor.InsertBlocks(n)
	a.treeOpen("page", ps.id)
	a.savePage()
	a.openPage(p.ID)
	if a.page != nil && a.page.id == p.ID {
		a.startFrom(t)
	}
}

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
		a.addFiles(paths[:1], "", pageID, func(f store.File) { a.placeUpload(pageID, blockID, f) })
	}()
}

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

func uploadedImage(f store.File) *doc.Node {
	return &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
		"url": store.FileURL(f.ID, f.Name), "align": "center", "alt": f.Name, "file_id": f.ID,
	}}
}

func uploadedFile(f store.File) *doc.Node {
	if doc.ImageExts[strings.ToLower(filepath.Ext(f.Name))] {
		return uploadedImage(f)
	}
	return &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{
		"url": store.FileURL(f.ID, f.Name), "name": f.Name, "file_id": f.ID, "size": f.SizeBytes, "mime": f.MimeType,
	}}
}

// placeUpload puts a file added for a page into it: over the picture of the
// block replaced when there is one, as a new block otherwise. The user may
// have left the page while the file was copied, and it then goes to the end
// of the page as stored; a file with no page left to hold it is removed.
func (a *App) placeUpload(pageID, replaced string, f store.File) {
	swap := func(c *doc.Content) bool {
		n := c.Find(replaced)
		if replaced == "" || n == nil || n.Type != "image" {
			return false
		}
		n.Set("url", store.FileURL(f.ID, f.Name))
		n.Set("file_id", f.ID)
		n.Set("alt", f.Name)
		return true
	}
	if ed := a.openEditor(pageID); ed != nil && !ed.ReadOnly {
		if n := ed.Content.Find(replaced); replaced != "" && n != nil && n.Type == "image" {
			ed.Edit(func(c *doc.Content) { swap(c) })
		} else {
			ed.InsertBlocks(uploadedFile(f))
		}
		return
	}
	placed := false
	err := a.pageCore().EditPage(pageID, func(c *doc.Content) bool {
		placed = swap(c) || c.Insert(doc.Position{At: "end"}, uploadedFile(f)) == nil
		return placed
	})
	if err == nil && placed {
		title := locales.CommonUntitled()
		if p, err := a.st.Page(pageID); err == nil {
			title = p.DisplayTitle()
		}
		a.toast(locales.HostAddedToPage(f.Name, title))
		return
	}
	if err := a.st.DeleteFile(f); err != nil {
		log.Printf("file %s: %v", f.Name, err)
	}
	a.toast(locales.HostFileNotAdded(f.Name))
}

func (a *App) uploadIntoPage() {
	ps := a.page
	if ps == nil {
		return
	}
	pageID := ps.id
	go func() {
		paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.HostUploadTitle()})
		a.addFiles(paths, "", pageID, func(f store.File) { a.placeUpload(pageID, "", f) })
	}()
}

func (a *App) uploadFile(folderID string) {
	go func() {
		paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.HostUploadTitle()})
		if len(paths) == 0 {
			return
		}
		a.addFiles(paths, folderID, "", func(store.File) {})
	}()
}

func (a *App) openFile(f store.File) {
	delete(a.downloadFails, f.ID)
	a.lost(f.LocalPath)
	path, fetching := a.localFile(f, func(ok bool) {
		if here, found := a.fileByID(f.ID); ok && found && here.LocalPath != "" {
			openSafely(here.LocalPath)
		} else {
			a.toast(locales.HostDownloadFailed(f.Name))
		}
	})
	if path == "" {
		a.notHere(f, fetching)
		return
	}
	openSafely(path)
}

// notHere says why a file asked for is not shown: it is on its way, or it
// cannot be had now.
func (a *App) notHere(f store.File, fetching bool) {
	if fetching {
		a.toast(locales.HostDownloading(f.Name))
		return
	}
	a.toast(locales.HostNotOnDevice(f.Name))
}

func openSafely(path string) {
	markDownloaded(path)
	if runsAsProgram(path) {
		mygo.Shell.ShowItemInFolder(path)
		return
	}
	_ = mygo.Shell.OpenPath(path)
}

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

func runsAsProgram(path string) bool {
	if programExts[strings.ToLower(filepath.Ext(path))] {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && runtime.GOOS != "windows" && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

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

type presence struct {
	there bool
	at    time.Time
}

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

func (a *App) lost(path string) { delete(a.present, path) }

// localFile is where a file is on this device. When it is not here, it is
// downloaded if it can be: fetching reports that a download is under way,
// and done is told how it ended.
func (a *App) localFile(f store.File, done func(ok bool)) (path string, fetching bool) {
	if f.LocalPath != "" && a.onDevice(f.LocalPath) {
		return f.LocalPath, false
	}
	if waiting, ok := a.downloads[f.ID]; ok {
		if done != nil {
			a.downloads[f.ID] = append(waiting, done)
		}
		return "", true
	}
	if f.StoragePath == "" || !a.sync.CanFetch() || a.downloadFailed(f.ID) {
		return "", false
	}
	if a.downloads == nil {
		a.downloads = map[string][]func(bool){}
	}
	a.downloads[f.ID] = []func(bool){done}
	var err error
	a.offMain(func() {
		ctx, cancel := context.WithTimeout(context.Background(), recording.DownloadWait(f.SizeBytes))
		defer cancel()
		var r io.ReadCloser
		r, err = a.sync.FetchReader(ctx, "files", f.StoragePath)
		if err == nil {
			_, err = a.st.DownloadedFileFrom(f.ID, r)
			_ = r.Close()
		}
	}, func() {
		waiting := a.downloads[f.ID]
		delete(a.downloads, f.ID)
		if err != nil {
			log.Printf("download %s: %v", f.Name, err)
			if a.downloadFails == nil {
				a.downloadFails = map[string]time.Time{}
			}
			a.downloadFails[f.ID] = time.Now()
			a.later(imageRetry, func() {})
		} else {
			delete(a.downloadFails, f.ID)
			a.reload(store.Change{Table: "files"})
		}
		for _, done := range waiting {
			if done != nil {
				done(err == nil)
			}
		}
	})
	return "", true
}

func (a *App) downloadFailed(id string) bool {
	at, ok := a.downloadFails[id]
	return ok && time.Since(at) < imageRetry
}

func (a *App) exportMarkdown(id string) {
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
		md := doc.PageMarkdown(p.DisplayTitle(), a.withTranscripts(id, c), opts)
		name := safeName(p.DisplayTitle()) + ".md"
		go func() {
			path, _ := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: locales.HostExportTitle(), DefaultPath: name})
			if path == "" {
				return
			}
			err := export.Save(path, strings.NewReader(md))
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

func (a *App) keptHere(p store.Page) bool { return a.keeper().Keeps(p) }

func (a *App) keeper() export.Keeper {
	k := export.Keeper{SyncOn: a.syncOn(), AllOffline: a.settings.AllPagesOffline}
	if a.page != nil {
		k.Pulled = append(k.Pulled, a.page.id)
	}
	if a.pub != nil && a.pulled(a.pub.page) {
		k.Pulled = append(k.Pulled, a.pub.page)
	}
	return k
}

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
		a.sync.Hold(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c, err = a.sync.RefreshContent(ctx, id)
		})
	}, func() {
		if err != nil {
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

func (a *App) loadMembers(pageID string) {
	if p, err := a.st.Page(pageID); err == nil && p.SpaceID == a.st.Session().SpaceID && !a.sync.ICloudOn() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = a.sync.LoadMembers(ctx, pageID)
}

type imageCache struct {
	root     string
	mu       sync.Mutex
	pictures map[string]ui.ImageSource
	used     map[string]time.Time
	swept    time.Time
	loading  map[string]bool
	failed   map[string]time.Time
	gifs     map[string][]byte
	players  map[string]*gifPlayer
	making   map[string]bool
	closed   bool
}

type gifPlayer struct {
	p     *ui.VideoPlayer
	shown time.Time
}

const playerIdle = 30 * time.Second

const imageRetry = time.Minute

func newImageCache(root string) *imageCache {
	return &imageCache{root: root, pictures: map[string]ui.ImageSource{}, used: map[string]time.Time{}, loading: map[string]bool{}, failed: map[string]time.Time{},
		gifs: map[string][]byte{}, players: map[string]*gifPlayer{}, making: map[string]bool{}}
}

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

const pictureIdle = 2 * time.Minute

var pictureBudget int64 = 512 << 20

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

func (ic *imageCache) forget(url string) {
	delete(ic.pictures, url)
	delete(ic.gifs, url)
	delete(ic.used, url)
}

const (
	maxPixels    = doc.MaxPixels
	maxGIFPixels = 200_000_000
)

var errTooLarge = errors.New("picture too large")

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

func animatedGIF(data []byte) bool {
	n, _ := gifFrames(data)
	return n > 1
}

func gifFrames(data []byte) (frames, area int) {
	if !bytes.HasPrefix(data, []byte("GIF8")) || len(data) < 13 {
		return 0, 0
	}
	table := func(packed byte) int {
		if packed&0x80 == 0 {
			return 0
		}
		return 3 << (packed&7 + 1)
	}
	skip := func(i int) int {
		for i < len(data) && data[i] != 0 {
			i += 1 + int(data[i])
		}
		return i + 1
	}
	i := 13 + table(data[10])
	for i < len(data) {
		switch data[i] {
		case 0x21:
			i = skip(i + 2)
		case 0x2C:
			if i+10 > len(data) {
				return frames, area
			}
			w, h := int(data[i+5])|int(data[i+6])<<8, int(data[i+7])|int(data[i+8])<<8
			frames, area = frames+1, area+w*h
			i = skip(i + 10 + table(data[i+9]) + 1)
		default:
			return frames, area
		}
	}
	return frames, area
}

func (ic *imageCache) hasFailed(url string) bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	_, ok := ic.failed[url]
	return ok && !ic.loading[url]
}

func (ic *imageCache) read(url string) ([]byte, error) {
	path, ok := strings.CutPrefix(url, "file://")
	if !ok {
		return web.Fetch(url)
	}
	if !inFolder(ic.root, path) {
		return nil, errNotOwnFile
	}
	return os.ReadFile(path) // #nosec G304
}

var errNotOwnFile = export.ErrNotOwnFile

func inFolder(dir, path string) bool { return export.InFolder(dir, path) }

var embedURL = map[bool]string{true: "https://embed.app.capyvibe.leaflet/", false: "about:blank"}[runtime.GOOS == "windows"]

func hexOf(c ui.Color) string {
	const digits = "0123456789abcdef"
	out := []byte{'#'}
	for _, v := range []uint8{c.R, c.G, c.B} {
		out = append(out, digits[v>>4], digits[v&15])
	}
	return string(out)
}

type embedScroll struct{ a *App }

type embedSize struct {
	h, w float32
	kept bool
}

func (s *embedScroll) Size(id string, h, w float64) {
	s.a.update(func() {
		s.a.embedSizes[id] = embedSize{h: float32(min(max(h, 0), 4000)), w: float32(w)}
	})
}

func (s *embedScroll) Zoomed(id string, zoomed bool) {
	s.a.update(func() {
		if zoomed {
			s.a.embedZoomed[id] = true
		} else {
			delete(s.a.embedZoomed, id)
		}
	})
}

func (s *embedScroll) Exit(id string) {
	s.a.update(func() {
		if ps := s.a.page; ps != nil && ps.fullDiagram != "" && fullView(ps.fullDiagram) == id {
			s.a.leaveFullDiagram()
		}
	})
}

func (a *App) zoomEmbed(key, how string) {
	v := a.embeds[key]
	if v == nil {
		return
	}
	page := v.Page()
	script := `document.getElementById("f").contentWindow.postMessage({leafletZoom: ` + strconv.Quote(how) + `}, "*")`
	go func() { _, _ = page.Eval(script) }()
}

const maxEmbedWheel = 2000

func (s *embedScroll) Scroll(id string, dy float64) {
	dy = max(min(dy, maxEmbedWheel), -maxEmbedWheel)
	s.a.update(func() {
		if a := s.a; a.embedOver == id && a.embeds[id] != nil {
			a.embedWheel += float32(dy)
		}
	})
}

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
	const pad = 14
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
		html := n.Str("html")
		a.sandbox(c, blockView(n.ID), embedInput{src: html, kind: n.Type}, func() string { return html }, width-2-2*pad, func(content float32) float32 {
			return max(height, min(content, 1200))
		}, height)
	})
}

type embedInput struct {
	src, kind string
	bg        ui.Color
	dark      bool
	toPage    bool
}

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
		a.letGoEmbed(key)
		delete(a.embedIn, key)
		v = nil
	}
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

func embedViewOptions(bg ui.Color, toPage bool) mygo.WebViewOptions {
	return mygo.WebViewOptions{BackgroundColor: hexOf(bg), ScrollToContainer: toPage,
		Page: mygo.PageOptions{TrustedOrigins: []string{strings.TrimSuffix(embedURL, "/")},
			PreloadScript: rendered.NoRTC, PreloadAllFrames: true}}
}

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

func blockView(id string) string { return "block:" + id }

func fullView(id string) string { return "full:" + id }

func viewBlock(key string) (id string, full bool) {
	kind, id, _ := strings.Cut(key, ":")
	return id, kind == "full"
}

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
