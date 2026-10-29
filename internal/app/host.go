package app

import (
	"bytes"
	"context"
	"errors"
	"html"
	"image/gif"
	"log"
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
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// The app as the agents' host.

// Update runs fn on the main thread.
func (a *App) Update(fn func()) { a.update(fn) }

// Editor returns a page's open editor. A page whose content could not be
// read has none to change: what changes it reads the stored content,
// and leaves it.
func (a *App) Editor(pageID string) *editor.Editor {
	if a.page != nil && a.page.id == pageID && !a.page.unreadable {
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

// Tint marks the blocks an agent run changed.
func (a *App) Tint(pageID string, ids []string) {
	if a.page == nil || a.page.id != pageID {
		return
	}
	for k := range a.page.tinted {
		delete(a.page.tinted, k)
	}
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
	if f, ok := h.a.fileByID(n.Str("file_id")); ok {
		path := h.a.localFile(f, redraw)
		if path == "" {
			return nil, nil, h.a.downloadFailed(f.ID)
		}
		url = "file://" + path
	}
	if b := h.a.images.get(url, redraw); b != nil {
		return b, h.a.images.player(url, n.ID, redraw), false
	}
	return nil, nil, url == "" || h.a.images.hasFailed(url)
}
func (h *host) Embed(c *ui.Context, n *doc.Node, width float32) { h.a.embedBlock(c, n, width) }
func (h *host) Video(c *ui.Context, n *doc.Node, width float32) { h.a.videoBlock(c, n, width) }
func (h *host) Card(c *ui.Context, n *doc.Node) {
	if n.Type == "file" {
		h.a.fileBlock(c, n)
	} else {
		h.a.linkPreviewBlock(c, n)
	}
}
func (h *host) Recording(c *ui.Context, n *doc.Node) { h.a.recordingBlock(c, n) }

func (h *host) CommentCount(blockID string) int {
	if h.a.page == nil {
		return 0
	}
	n := 0
	for _, t := range h.a.page.threads {
		if t.Root.BlockID == blockID && !t.Root.Resolved {
			n++
		}
	}
	return n
}

func (h *host) OpenComments(blockID string) {
	h.a.panelOpen, h.a.panelTab = true, 1
	h.a.dialogs.commentFocus = blockID
}

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

type slashItem struct {
	editor.MenuItem
	keywords string
}

// slashItems are the / menu's choices, said in the app's language; the
// keywords stay English, to find a block by its English name too.
func slashItems() []slashItem {
	return []slashItem{
		{editor.MenuItem{Key: "paragraph", Label: locales.HostSlashText(), Hint: locales.HostSlashTextHint(), Icon: "type", Group: locales.HostGroupBasic()}, "text paragraph plain"},
		{editor.MenuItem{Key: "h1", Label: locales.HostSlashHeading1(), Hint: locales.HostSlashHeading1Hint(), Icon: "heading-1", Group: locales.HostGroupBasic()}, "h1 title heading"},
		{editor.MenuItem{Key: "h2", Label: locales.HostSlashHeading2(), Hint: locales.HostSlashHeading2Hint(), Icon: "heading-2", Group: locales.HostGroupBasic()}, "h2 heading subtitle"},
		{editor.MenuItem{Key: "h3", Label: locales.HostSlashHeading3(), Hint: locales.HostSlashHeading3Hint(), Icon: "heading-3", Group: locales.HostGroupBasic()}, "h3 heading"},
		{editor.MenuItem{Key: "bulleted_list", Label: locales.HostSlashBulleted(), Hint: locales.HostSlashBulletedHint(), Icon: "list", Group: locales.HostGroupBasic()}, "bullet list ul"},
		{editor.MenuItem{Key: "numbered_list", Label: locales.HostSlashNumbered(), Hint: locales.HostSlashNumberedHint(), Icon: "list-ordered", Group: locales.HostGroupBasic()}, "number ordered ol"},
		{editor.MenuItem{Key: "todo_list", Label: locales.HostSlashTodo(), Hint: locales.HostSlashTodoHint(), Icon: "square-check", Group: locales.HostGroupBasic()}, "todo task check"},
		{editor.MenuItem{Key: "quote", Label: locales.HostSlashQuote(), Hint: locales.HostSlashQuoteHint(), Icon: "quote", Group: locales.HostGroupBasic()}, "quote blockquote"},
		{editor.MenuItem{Key: "callout", Label: locales.HostSlashCallout(), Hint: locales.HostSlashCalloutHint(), Icon: "info", Group: locales.HostGroupBasic()}, "callout note tip warning alert admonition"},
		{editor.MenuItem{Key: "toggle_list", Label: locales.HostSlashToggle(), Hint: locales.HostSlashToggleHint(), Icon: "chevron-right", Group: locales.HostGroupBasic()}, "toggle collapse fold details disclosure"},
		{editor.MenuItem{Key: "code", Label: locales.HostSlashCode(), Hint: locales.HostSlashCodeHint(), Icon: "code", Group: locales.HostGroupBasic()}, "code snippet program ```"},
		{editor.MenuItem{Key: "divider", Label: locales.HostSlashDivider(), Hint: locales.HostSlashDividerHint(), Icon: "minus", Group: locales.HostGroupBasic()}, "divider line hr rule"},
		{editor.MenuItem{Key: "table", Label: locales.HostSlashTable(), Hint: locales.HostSlashTableHint(), Icon: "table", Group: locales.HostGroupBlocks()}, "table grid"},
		{editor.MenuItem{Key: "chart", Label: locales.HostSlashChart(), Hint: locales.HostSlashChartHint(), Icon: "chart-column", Group: locales.HostGroupBlocks()}, "chart graph bar line pie"},
		{editor.MenuItem{Key: "math_equation", Label: locales.HostSlashMath(), Hint: locales.HostSlashMathHint(), Icon: "sigma", Group: locales.HostGroupBlocks()}, "math equation formula latex tex katex"},
		{editor.MenuItem{Key: "diagram", Label: locales.HostSlashDiagram(), Hint: locales.HostSlashDiagramHint(), Icon: "workflow", Group: locales.HostGroupBlocks()}, "diagram mermaid flowchart sequence chart graph"},
		{editor.MenuItem{Key: "embed", Label: locales.HostSlashEmbed(), Hint: locales.HostSlashEmbedHint(), Icon: "layout-grid", Group: locales.HostGroupBlocks()}, "embed html widget"},
		{editor.MenuItem{Key: "subpage", Label: locales.HostSlashSubpage(), Hint: locales.HostSlashSubpageHint(), Icon: "file-plus", Group: locales.HostGroupBlocks()}, "page subpage child"},
		{editor.MenuItem{Key: "image", Label: locales.HostSlashImage(), Hint: locales.HostSlashImageHint(), Icon: "image", Group: locales.HostGroupBlocks()}, "image picture photo"},
		{editor.MenuItem{Key: "video", Label: locales.HostSlashVideo(), Hint: locales.HostSlashVideoHint(), Icon: "play", Group: locales.HostGroupBlocks()}, "video youtube vimeo movie film mp4"},
		{editor.MenuItem{Key: "link_preview", Label: locales.HostSlashLinkCard(), Hint: locales.HostSlashLinkCardHint(), Icon: "globe", Group: locales.HostGroupBlocks()}, "link card bookmark preview url web"},
		{editor.MenuItem{Key: "file", Label: locales.HostSlashFile(), Hint: locales.HostSlashFileHint(), Icon: "paperclip", Group: locales.HostGroupBlocks()}, "file attach attachment upload document pdf"},
		{editor.MenuItem{Key: "recording", Label: locales.CommonRecording(), Hint: locales.HostSlashRecordingHint(), Icon: "mic", Group: locales.HostGroupBlocks()}, "record audio voice transcript"},
	}
}

func (h *host) SlashItems(query string) []editor.MenuItem {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []editor.MenuItem
	for _, it := range slashItems() {
		if q == "" || strings.Contains(strings.ToLower(it.Label), q) || strings.Contains(it.keywords, q) {
			out = append(out, it.MenuItem)
		}
	}
	return out
}

func (h *host) MentionItems(query string) []editor.MenuItem {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []editor.MenuItem
	for _, o := range h.a.hub.Options() {
		if q == "" || strings.Contains(strings.ToLower(o.Name()), q) {
			out = append(out, editor.MenuItem{Key: "agent:" + o.Provider.ID, Label: o.Name(), Hint: o.Model, Icon: "sparkles", Group: locales.CommonAgents()})
		}
	}
	if len(h.a.hub.Options()) == 0 && q == "" {
		out = append(out, editor.MenuItem{Key: "connect", Label: locales.HostAddAgentProvider(), Hint: locales.HostAddAgentProviderHint(), Icon: "sparkles", Group: locales.CommonAgents()})
	}
	// The user's pages, to mention one: most recently edited first.
	pages := slices.Clone(h.a.pages)
	slices.SortStableFunc(pages, func(x, y store.Page) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
	n := 0
	for _, p := range pages {
		if n == 8 {
			break
		}
		if h.a.page != nil && p.ID == h.a.page.id {
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(p.DisplayTitle()), q) {
			out = append(out, editor.MenuItem{Key: "page:" + p.ID, Label: p.DisplayTitle(), Hint: h.a.pagePlace(p), Icon: "file-text", Group: locales.HostGroupPages()})
			n++
		}
	}
	if h.a.page != nil {
		for _, m := range h.a.page.members {
			// Someone invited who has not accepted is no account yet: their
			// ID is the invite's.
			if m.UserID == h.a.session.UserID || !m.Accepted && m.Role != "owner" {
				continue
			}
			if q == "" || strings.Contains(strings.ToLower(m.Name()), q) {
				out = append(out, editor.MenuItem{Key: "user:" + m.UserID, Label: m.Name(), Hint: m.Role, Icon: "user", Group: locales.HostGroupPeople()})
			}
		}
	}
	return out
}

// pagePlace says where a page is, for a list of pages: in its parent
// page, else its folder, else nothing at the top of the space.
func (a *App) pagePlace(p store.Page) string {
	if parent := a.pageByID(p.ParentID); parent != nil {
		return parent.DisplayTitle()
	}
	for _, f := range a.folders {
		if f.ID == p.FolderID {
			return f.Name
		}
	}
	return ""
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
	switch item.Key {
	case "paragraph", "bulleted_list", "numbered_list", "todo_list", "quote", "code", "callout", "toggle_list":
		ed.SetType(item.Key, 0)
	case "h1", "h2", "h3":
		ed.SetType("heading", int(item.Key[1]-'0'))
	case "divider":
		ed.InsertBlocks(&doc.Node{ID: doc.NewBlockID(), Type: "divider"})
	case "table":
		n, _ := doc.TableNode([][]string{{locales.HostTableColumn(1), locales.HostTableColumn(2), locales.HostTableColumn(3)}, {"", "", ""}, {"", "", ""}}, "")
		ed.InsertBlocks(n)
	case "chart":
		data, _ := doc.ChartData(map[string]any{
			"chart_type": "bar", "title": locales.HostChartTitle(), "labels": []any{"A", "B", "C"},
			"series": []any{map[string]any{"name": locales.HostChartSeries(), "values": []any{3.0, 5.0, 2.0}}},
		})
		data["author"] = a.session.DisplayName
		n := &doc.Node{ID: doc.NewBlockID(), Type: "chart", Data: data}
		ed.InsertBlocks(n)
		a.dialogs.openChart(n.ID, ed)
	case "embed":
		a.dialogs.openEmbed()
	case "subpage":
		a.addSubpageBlock()
	case "video", "link_preview", "image":
		a.dialogs.openLinkDialog(item.Key)
	case "file":
		a.uploadIntoPage()
	case "math_equation", "diagram":
		n := doc.MathNode("")
		if item.Key == "diagram" {
			n = doc.DiagramNode("")
		}
		ed.InsertBlocks(n)
		a.dialogs.openSource(n)
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

// addSubpageBlock creates a subpage and links it from the open page.
func (a *App) addSubpageBlock() {
	ps := a.page
	if ps == nil {
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
	a.focusTitle(false)
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
	// A click tries a download that failed again.
	delete(a.downloadFails, f.ID)
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
// and documents that run what they hold.
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
	".rpm": true, ".flatpakref": true, ".snap": true,
}

// runsAsProgram reports whether opening the file would run it.
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
	for _, f := range a.files {
		if f.ID == id {
			return f, true
		}
	}
	return store.File{}, false
}

// localFile returns where a file is on this device, or "" while one
// uploaded from another device downloads; done runs once it has. A
// download that failed is tried again once imageRetry has passed, or
// when the file is opened.
func (a *App) localFile(f store.File, done func()) string {
	if f.LocalPath != "" {
		if _, err := os.Stat(f.LocalPath); err == nil {
			return f.LocalPath
		}
	}
	if f.StoragePath == "" || !a.sync.CanFetch() || a.downloads[f.ID] || a.downloadFailed(f.ID) {
		return ""
	}
	if a.downloads == nil {
		a.downloads = map[string]bool{}
	}
	a.downloads[f.ID] = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		data, err := a.sync.Fetch(ctx, "files", f.StoragePath)
		if err == nil {
			_, err = a.st.DownloadedFile(f.ID, data)
		}
		a.update(func() {
			delete(a.downloads, f.ID)
			if err != nil {
				log.Printf("download %s: %v", f.Name, err)
				if a.downloadFails == nil {
					a.downloadFails = map[string]time.Time{}
				}
				a.downloadFails[f.ID] = time.Now()
				// The next frame after the wait tries again.
				time.AfterFunc(imageRetry, func() { a.update(func() {}) })
				return
			}
			delete(a.downloadFails, f.ID)
			a.reload(store.Change{Table: "files"})
			done()
		})
	}()
	return ""
}

// downloadFailed reports whether a file's download failed, and waits to be
// tried again.
func (a *App) downloadFailed(id string) bool {
	at, ok := a.downloadFails[id]
	return ok && time.Since(at) < imageRetry
}

// exportPage saves a page as Markdown.
func (a *App) exportPage(id string) {
	if a.page != nil && a.page.id == id {
		a.savePage()
	}
	p := a.pageByID(id)
	c := a.st.Content(id)
	if p == nil || c == nil {
		return
	}
	md := doc.PageMarkdown(p.DisplayTitle(), c, a.pageTitleOf)
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
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, s)
	if strings.TrimSpace(s) == "" {
		return locales.CommonUntitled()
	}
	// "." and ".." name the folder itself and the one above it.
	if strings.Trim(s, ".") == "" {
		return strings.ReplaceAll(s, ".", "-")
	}
	return s
}

// loadMembers reads who a page is shared with.
func (a *App) loadMembers(pageID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = a.sync.LoadMembers(ctx, pageID)
}

// imageCache loads the pictures of image blocks; root is the app's data
// folder, the only one it reads files from.
type imageCache struct {
	root string
	dir  string
	mu   sync.Mutex
	// pictures are bitmaps, or SVGs, which MyGo draws itself.
	pictures map[string]ui.ImageSource
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

func newImageCache(root, dir string) *imageCache {
	return &imageCache{root: root, dir: dir, pictures: map[string]ui.ImageSource{}, loading: map[string]bool{}, failed: map[string]time.Time{},
		gifs: map[string][]byte{}, players: map[string]*gifPlayer{}, making: map[string]bool{}}
}

// get returns a picture, or nil while it loads; done runs when it has
// loaded or failed.
func (ic *imageCache) get(url string, done func()) ui.ImageSource {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if b, ok := ic.pictures[url]; ok {
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
			b, err = ui.DecodeImage(data)
		}
		if err != nil {
			log.Printf("image %s: %v", url, err)
		}
		ic.mu.Lock()
		delete(ic.loading, url)
		if b != nil {
			ic.pictures[url] = b
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
		ic.players[key] = &gifPlayer{p: p, shown: time.Now()}
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

// animatedGIF reports whether data is a GIF of more than one frame.
func animatedGIF(data []byte) bool {
	if !bytes.HasPrefix(data, []byte("GIF8")) {
		return false
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	return err == nil && len(g.Image) > 1
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
		return fetch(url)
	}
	if !inFolder(ic.root, path) {
		return nil, errNotOwnFile
	}
	return os.ReadFile(path) // #nosec G304 -- a file in the app's data folder.
}

// errNotOwnFile is for a file outside the app's data folder.
var errNotOwnFile = errors.New("not a file of the app's")

// inFolder reports whether path is in folder dir, links followed.
func inFolder(dir, path string) bool {
	if dir == "" || !filepath.IsAbs(path) {
		return false
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// embedURL is where an embed's wrapper page loads. WebView2 serves a
// loaded document only to a URL it requests over the network, and never
// requests about:blank, so there the page loads at an https URL of the
// app's bundle ID instead, as a video's does, trusted to tell Go its
// size. Its policy still keeps it off the network, and the embed's own
// sandboxed frame has an opaque origin either way.
var embedURL = map[bool]string{true: "https://embed.app.capyvibe.leaflet/", false: "about:blank"}[runtime.GOOS == "windows"]

// embedCSP is the policy of an embed's frame: inline scripts and styles,
// images, fonts and media from data and blob URLs, and nothing from the
// network.
const embedCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:"

// embedWheel runs in an embed's frame: wheel movement the embed cannot
// use itself, as most embeds do not scroll, goes to the page around it.
const embedWheel = `<script>(function () {
  function scrolls(el, dy) {
    for (; el && el.nodeType === 1; el = el.parentElement) {
      var s = getComputedStyle(el), o = s.overflowY;
      var root = el === document.scrollingElement;
      if ((root || o === "auto" || o === "scroll") && el.scrollHeight > el.clientHeight + 1) {
        if (dy < 0 && el.scrollTop > 0) return true;
        if (dy > 0 && el.scrollTop + el.clientHeight < el.scrollHeight - 1) return true;
      }
    }
    return false;
  }
  addEventListener("wheel", function (e) {
    if (e.ctrlKey) return;
    var k = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1;
    var dy = e.deltaY * k;
    if (dy && !scrolls(e.target, dy)) parent.postMessage({leafletWheel: dy}, "*");
  }, {passive: true});
})();</script>`

// embedFit runs in an embed's frame: it tells the page how tall the
// embed's content is at the frame's width, so a block too short for it,
// as a page narrows and its layout stacks, grows to show it all.
const embedFit = `<script>(function () {
  var last = "";
  function fit() {
    var b = document.body;
    if (!b) return;
    var s = getComputedStyle(b);
    var h = Math.ceil(b.scrollHeight + (parseFloat(s.marginTop) || 0) + (parseFloat(s.marginBottom) || 0));
    var key = h + ":" + innerWidth;
    if (key === last) return;
    last = key;
    parent.postMessage({leafletSize: [h, innerWidth]}, "*");
  }
  addEventListener("load", function () {
    fit();
    new ResizeObserver(fit).observe(document.body);
  });
  addEventListener("resize", fit);
})();</script>`

// embedPage wraps an embed's HTML in a sandboxed frame: frames never call
// Go, and the policy keeps it off the network. A srcdoc frame inherits the
// wrapper's policy, so the wrapper has the frame's. The wrapper's own
// script only passes the frame's leftover wheel movement and its content's
// size, whether it is zoomed in and its asking to leave full screen to the
// page, for the embed id.
//
// Its background is the page's. In the dark theme the frame's root gets
// data-theme="dark" and a dark color scheme, so an embed that styles
// nothing follows the page, and one that does can match it with
// :root[data-theme="dark"].
func embedPage(id, src string, pal *theme.Palette) string {
	bg, scheme := hexOf(pal.Surface), "light"
	if pal.Dark {
		scheme = "dark"
	}
	meta := `<meta http-equiv="Content-Security-Policy" content="` + embedCSP + `">` +
		`<meta name="color-scheme" content="` + scheme + `">` +
		`<script>document.documentElement.dataset.theme = "` + scheme + `";</script>` + embedWheel + embedFit
	inner := src
	if i := strings.Index(strings.ToLower(inner), "<head>"); i >= 0 {
		inner = inner[:i+6] + meta + inner[i+6:]
	} else {
		inner = meta + inner
	}
	return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="` + embedCSP + `; frame-src about: data:">` +
		`<meta name="color-scheme" content="` + scheme + `">` +
		`<style>html,body{margin:0;height:100%;background:` + bg + `}iframe{border:0;width:100%;height:100%;display:block;background:` + bg + `}</style></head><body>` +
		`<iframe id="f" sandbox="allow-scripts" srcdoc="` + html.EscapeString(inner) + `"></iframe>` +
		// Without a wheel listener of its own, WebKit scrolls the wrapper
		// without telling a frame that could scroll, so it never forwards.
		`<script>addEventListener("wheel", function () {}, {passive: true});` +
		`addEventListener("message", function (e) {` +
		`if (e.source !== document.getElementById("f").contentWindow || !e.data || !window.mygo) return;` +
		`if (typeof e.data.leafletWheel === "number") window.mygo.call("Embeds.Scroll", e.data.leafletWheel);` +
		`var z = e.data.leafletSize;` +
		`if (z && typeof z[0] === "number" && typeof z[1] === "number") window.mygo.call("Embeds.Size", ` + strconv.Quote(id) + `, z[0], z[1]);` +
		`if (typeof e.data.leafletZoomed === "boolean") window.mygo.call("Embeds.Zoomed", ` + strconv.Quote(id) + `, e.data.leafletZoomed);` +
		`if (e.data.leafletExit === true) window.mygo.call("Embeds.Exit", ` + strconv.Quote(id) + `);` +
		`});</script></body></html>`
}

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
type embedSize struct{ h, w float32 }

// Size keeps the height of embed id's content at its frame's width w. The
// first call also tells that the embed has drawn, so it shows.
func (s *embedScroll) Size(id string, h, w float64) {
	s.a.update(func() {
		s.a.embedSizes[id] = embedSize{float32(min(max(h, 0), 4000)), float32(w)}
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
		if ps := s.a.page; ps != nil && "full:"+ps.fullDiagram == id {
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

// Scroll scrolls the open page by dy DIPs, for wheel movement over an
// embed that the embed did not use.
func (s *embedScroll) Scroll(dy float64) {
	dy = max(min(dy, 2000), -2000)
	s.a.update(func() {
		if ps := s.a.page; ps != nil {
			ps.scroll.Y = max(min(ps.scroll.Y+float32(dy), ps.scroll.MaxY), 0)
		}
	})
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
					Opacity(map[bool]float32{true: 1, false: 0}[hovered]).AlignItems(ui.Center).Label(locales.HostEditEmbed())
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
		a.sandbox(c, n.ID, n.Str("html"), width-2-2*pad, func(content float32) float32 {
			return max(height, min(content, 1200))
		}, height, 10)
	})
}

// sandbox shows HTML in a block's sandboxed web view, key naming it, at
// width w inside the page. fit gives the frame's height from its
// content's, once that has drawn; until then it is height, and the box
// shows, gap above it, rather than a blank page in the frame's color. A
// nil fit has the frame fill the rest of its parent instead.
// The view loads again when the HTML or the theme changes: by hand, by an
// agent, by undo or a restored version.
func (a *App) sandbox(c *ui.Context, key, html string, w float32, fit func(float32) float32, height, gap float32) {
	pal := theme.Of(c)
	src := embedPage(key, html, pal)
	size, ready := a.embedSizes[key]
	if ready && fit != nil && abs32(size.w-w) < 4 {
		height = fit(size.h)
	}
	v := a.embeds[key]
	if v == nil && a.win != nil {
		wv, err := a.win.NewWebView(mygo.WebViewOptions{BackgroundColor: hexOf(pal.Surface), Page: mygo.PageOptions{TrustedOrigins: []string{strings.TrimSuffix(embedURL, "/")}}})
		if err != nil {
			ui.Text(c, locales.HostEmbedError(err.Error())).TextColor(pal.TextMuted)
			return
		}
		page := wv.Page()
		page.OnWillNavigate(func(e *mygo.NavigateEvent) {
			if !strings.HasPrefix(e.URL, "about:") && !strings.HasPrefix(e.URL, "data:") {
				e.PreventDefault()
			}
		})
		page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
		a.embeds[key] = wv
		v = wv
	}
	if v == nil {
		return
	}
	if a.embedSrc[key] != src {
		v.Page().LoadHTML(src, embedURL)
		a.embedSrc[key] = src
		delete(a.embedSizes, key)
		delete(a.embedZoomed, key)
		ready = false
	}
	wv := ui.WebView(c, v).FillWidth().Radius(6)
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
		ui.Box(c).Height(height).FillWidth().Margin(-height-gap, 0, 0, 0).Radius(6).Background(pal.Ground)
	}
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

var _ agents.Host = (*App)(nil)
