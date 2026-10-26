package app

import (
	"context"
	"html"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
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
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/theme"
)

// The app as the agents' host.

// Update runs fn on the main thread.
func (a *App) Update(fn func()) { a.update(fn) }

// Editor returns a page's open editor.
func (a *App) Editor(pageID string) *editor.Editor {
	if a.page != nil && a.page.id == pageID {
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
func (h *host) Image(url string) *ui.Bitmap {
	return h.a.images.get(url, func() { h.a.update(func() {}) })
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

type slashItem struct {
	editor.MenuItem
	keywords string
}

var slashItems = []slashItem{
	{editor.MenuItem{Key: "paragraph", Label: "Text", Hint: "Plain writing", Icon: "type", Group: "Basic"}, "text paragraph plain"},
	{editor.MenuItem{Key: "h1", Label: "Heading 1", Hint: "Big section heading", Icon: "heading-1", Group: "Basic"}, "h1 title heading"},
	{editor.MenuItem{Key: "h2", Label: "Heading 2", Hint: "Medium section heading", Icon: "heading-2", Group: "Basic"}, "h2 heading subtitle"},
	{editor.MenuItem{Key: "h3", Label: "Heading 3", Hint: "Small section heading", Icon: "heading-3", Group: "Basic"}, "h3 heading"},
	{editor.MenuItem{Key: "bulleted_list", Label: "Bulleted list", Hint: "A simple list", Icon: "list", Group: "Basic"}, "bullet list ul"},
	{editor.MenuItem{Key: "numbered_list", Label: "Numbered list", Hint: "A list with numbers", Icon: "list-ordered", Group: "Basic"}, "number ordered ol"},
	{editor.MenuItem{Key: "todo_list", Label: "To-do list", Hint: "Track tasks", Icon: "square-check", Group: "Basic"}, "todo task check"},
	{editor.MenuItem{Key: "quote", Label: "Quote", Hint: "Set off a quotation", Icon: "quote", Group: "Basic"}, "quote blockquote"},
	{editor.MenuItem{Key: "callout", Label: "Callout", Hint: "Set off a note, tip or warning", Icon: "info", Group: "Basic"}, "callout note tip warning alert admonition"},
	{editor.MenuItem{Key: "toggle_list", Label: "Toggle", Hint: "Fold away what is inside", Icon: "chevron-right", Group: "Basic"}, "toggle collapse fold details disclosure"},
	{editor.MenuItem{Key: "code", Label: "Code", Hint: "Code with highlighting", Icon: "code", Group: "Basic"}, "code snippet program ```"},
	{editor.MenuItem{Key: "divider", Label: "Divider", Hint: "A line between sections", Icon: "minus", Group: "Basic"}, "divider line hr rule"},
	{editor.MenuItem{Key: "table", Label: "Table", Hint: "Rows and columns", Icon: "table", Group: "Blocks"}, "table grid"},
	{editor.MenuItem{Key: "chart", Label: "Chart", Hint: "Bar, line or pie", Icon: "chart-column", Group: "Blocks"}, "chart graph bar line pie"},
	{editor.MenuItem{Key: "math_equation", Label: "Math", Hint: "A formula in TeX", Icon: "sigma", Group: "Blocks"}, "math equation formula latex tex katex"},
	{editor.MenuItem{Key: "diagram", Label: "Diagram", Hint: "A Mermaid flowchart or other diagram", Icon: "workflow", Group: "Blocks"}, "diagram mermaid flowchart sequence chart graph"},
	{editor.MenuItem{Key: "embed", Label: "Interactive embed", Hint: "HTML that runs sandboxed", Icon: "layout-grid", Group: "Blocks"}, "embed html widget"},
	{editor.MenuItem{Key: "subpage", Label: "Subpage", Hint: "A page inside this page", Icon: "file-plus", Group: "Blocks"}, "page subpage child"},
	{editor.MenuItem{Key: "image", Label: "Image", Hint: "Upload a picture", Icon: "image", Group: "Blocks"}, "image picture photo"},
	{editor.MenuItem{Key: "video", Label: "Video", Hint: "A YouTube or Vimeo link", Icon: "play", Group: "Blocks"}, "video youtube vimeo movie film"},
	{editor.MenuItem{Key: "link_preview", Label: "Link card", Hint: "A web page with its title and picture", Icon: "globe", Group: "Blocks"}, "link card bookmark preview url web"},
	{editor.MenuItem{Key: "file", Label: "File", Hint: "Attach a file to the page", Icon: "paperclip", Group: "Blocks"}, "file attach attachment upload document pdf"},
	{editor.MenuItem{Key: "recording", Label: "Recording", Hint: "Audio with a transcript", Icon: "mic", Group: "Blocks"}, "record audio voice transcript"},
}

func (h *host) SlashItems(query string) []editor.MenuItem {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []editor.MenuItem
	for _, it := range slashItems {
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
			out = append(out, editor.MenuItem{Key: "agent:" + o.Provider.ID, Label: o.Name(), Hint: o.Model, Icon: "sparkles", Group: "Agents"})
		}
	}
	if len(h.a.hub.Options()) == 0 && q == "" {
		out = append(out, editor.MenuItem{Key: "connect", Label: "Add an agent provider", Hint: "Settings › Agent providers", Icon: "sparkles", Group: "Agents"})
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
			out = append(out, editor.MenuItem{Key: "page:" + p.ID, Label: p.DisplayTitle(), Hint: h.a.pagePlace(p), Icon: "file-text", Group: "Pages"})
			n++
		}
	}
	if h.a.page != nil {
		for _, m := range h.a.page.members {
			if m.UserID == h.a.session.UserID {
				continue
			}
			if q == "" || strings.Contains(strings.ToLower(m.Name()), q) {
				out = append(out, editor.MenuItem{Key: "user:" + m.UserID, Label: m.Name(), Hint: m.Role, Icon: "user", Group: "People"})
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
			a.navigate(Route{Kind: "settings", Section: "providers"})
		case strings.HasPrefix(item.Key, "agent:"):
			ed.RemoveTrigger(t)
			choice := store.ModelChoice{ProviderID: strings.TrimPrefix(item.Key, "agent:")}
			a.dialogs.inline = &inlinePrompt{blockID: t.ID, agent: choice, agentName: item.Label}
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
		n, _ := doc.TableNode([][]string{{"Column 1", "Column 2", "Column 3"}, {"", "", ""}, {"", "", ""}}, "")
		ed.InsertBlocks(n)
	case "chart":
		data, _ := doc.ChartData(map[string]any{
			"chart_type": "bar", "title": "New chart", "labels": []any{"A", "B", "C"},
			"series": []any{map[string]any{"name": "Value", "values": []any{3.0, 5.0, 2.0}}},
		})
		data["author"] = a.session.DisplayName
		n := &doc.Node{ID: doc.NewBlockID(), Type: "chart", Data: data}
		ed.InsertBlocks(n)
		a.dialogs.openChart(n.ID, ed)
	case "embed":
		a.dialogs.openEmbed()
	case "subpage":
		a.addSubpageBlock()
	case "image":
		a.uploadIntoPage()
	case "video", "link_preview":
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
	h.a.dialogs.inline = &inlinePrompt{blockID: blockID, quote: quote, selection: true}
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
		a.toast("Could not create the subpage: " + err.Error())
		return
	}
	a.pages, _ = a.st.Pages()
	n := &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": p.ID}}
	ps.editor.InsertBlocks(n)
	a.expanded[ps.id] = true
	a.savePage()
	a.openPage(p.ID)
	a.focusTitle(false)
}

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true}

// uploadIntoPage picks files and puts them in the page: a picture as an
// image, others as file cards.
func (a *App) uploadIntoPage() {
	ps := a.page
	if ps == nil {
		return
	}
	pageID := ps.id
	go func() {
		paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Upload a file"})
		if len(paths) == 0 {
			return
		}
		a.update(func() {
			for _, path := range paths {
				ext := strings.ToLower(filepath.Ext(path))
				f, err := a.st.AddFile(path, "", pageID, mime.TypeByExtension(ext))
				if err != nil {
					a.toast("Could not add the file: " + err.Error())
					continue
				}
				if a.page == nil || a.page.id != pageID {
					a.toast("Added " + f.Name + " to the space")
					continue
				}
				n := &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{
					"url": "file://" + f.LocalPath, "name": f.Name, "file_id": f.ID, "size": f.SizeBytes, "mime": f.MimeType,
				}}
				if imageExts[ext] {
					n = &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
						"url": "file://" + f.LocalPath, "align": "center", "alt": f.Name, "file_id": f.ID,
					}}
				}
				a.page.editor.InsertBlocks(n)
			}
		})
	}()
}

// uploadFile adds files to a folder.
func (a *App) uploadFile(folderID string) {
	go func() {
		paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Upload a file"})
		a.update(func() {
			for _, path := range paths {
				if _, err := a.st.AddFile(path, folderID, "", mime.TypeByExtension(filepath.Ext(path))); err != nil {
					a.toast("Could not add the file: " + err.Error())
				}
			}
			a.folderOpen[folderID] = true
		})
	}()
}

func (a *App) openFile(f store.File) {
	if f.LocalPath == "" {
		a.toast("This file is on the server; it downloads when you sync.")
		return
	}
	_ = mygo.Shell.OpenPath(f.LocalPath)
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
		path, _ := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Export as Markdown", DefaultPath: name})
		if path == "" {
			return
		}
		err := os.WriteFile(path, []byte(md), 0o644) // #nosec G306 -- a file the user saves, readable like any other they save.
		a.update(func() {
			if err != nil {
				a.toast("Could not export: " + err.Error())
			} else {
				a.toast("Exported " + filepath.Base(path))
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
		return "Untitled"
	}
	return s
}

// loadMembers reads who a page is shared with.
func (a *App) loadMembers(pageID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := a.backend.RPC(ctx, "page_members", map[string]any{"p_page_id": pageID})
	if err != nil {
		return
	}
	var members []store.Member
	list, _ := res.([]any)
	for _, r := range list {
		m, _ := r.(map[string]any)
		members = append(members, store.Member{PageID: pageID, UserID: store.S(m, "user_id"), Role: store.S(m, "role"),
			DisplayName: store.S(m, "display_name"), Email: store.S(m, "email")})
	}
	_ = a.st.SetMembers(pageID, members)
}

// imageCache loads the pictures of image blocks.
type imageCache struct {
	dir     string
	mu      sync.Mutex
	bitmaps map[string]*ui.Bitmap
	loading map[string]bool
}

func newImageCache(dir string) *imageCache {
	return &imageCache{dir: dir, bitmaps: map[string]*ui.Bitmap{}, loading: map[string]bool{}}
}

func (ic *imageCache) get(url string, loaded func()) *ui.Bitmap {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if b, ok := ic.bitmaps[url]; ok {
		return b
	}
	if ic.loading[url] || url == "" {
		return nil
	}
	ic.loading[url] = true
	go func() {
		data, err := fetch(url)
		var b *ui.Bitmap
		if err == nil {
			b, err = ui.DecodeBitmap(data)
		}
		if err != nil {
			log.Printf("image %s: %v", url, err)
		}
		ic.mu.Lock()
		if b != nil {
			ic.bitmaps[url] = b
		}
		ic.mu.Unlock()
		if b != nil {
			loaded()
		}
	}()
	return nil
}

func fetch(url string) ([]byte, error) {
	if strings.HasPrefix(url, "file://") {
		return os.ReadFile(strings.TrimPrefix(url, "file://"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 400 {
		return nil, &remote.Error{Status: res.StatusCode, Message: res.Status}
	}
	return io.ReadAll(io.LimitReader(res.Body, 20<<20))
}

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
// size to the page, for the embed id.
//
// In the dark theme the frame's root gets data-theme="dark" and a dark
// color scheme, so an embed that styles nothing follows the page, and one
// that does can match it with :root[data-theme="dark"].
func embedPage(id, src string, dark bool) string {
	bg, scheme := "#fff", "light"
	if dark {
		bg, scheme = "#1b1e24", "dark"
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
		`});</script></body></html>`
}

// embedScroll is the service an embed's wrapper calls: to scroll the page,
// and to say how tall the embed's content is.
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
					Opacity(map[bool]float32{true: 1, false: 0}[hovered]).AlignItems(ui.Center).Label("Edit embed")
				if b.Hovered() {
					b.Background(pal.Hover)
				}
				b.Children(func() {
					ui.Icon(c, theme.Icon("pencil")).FontSize(12)
					ui.Text(c, "Edit").FontSize(12)
				})
				if b.Clicked() {
					a.dialogs.editEmbed(n)
				}
			}
			ui.Row(c).Gap(4).AlignItems(ui.Center).Tooltip("Runs apart from your data, with no network").Children(func() {
				ui.Icon(c, theme.Icon("shield-check")).FontSize(12).TextColor(pal.TextMuted)
				ui.Text(c, "Sandboxed").FontSize(11).TextColor(pal.TextMuted)
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
// shows, gap above it, rather than a blank page in the frame's color.
// The view loads again when the HTML or the theme changes: by hand, by an
// agent, by undo or a restored version.
func (a *App) sandbox(c *ui.Context, key, html string, w float32, fit func(float32) float32, height, gap float32) {
	pal := theme.Of(c)
	src := embedPage(key, html, c.Theme().Dark)
	size, ready := a.embedSizes[key]
	if ready && abs32(size.w-w) < 4 {
		height = fit(size.h)
	}
	v := a.embeds[key]
	if v == nil && a.win != nil {
		bg := "#ffffff"
		if c.Theme().Dark {
			bg = "#1b1e24"
		}
		wv, err := a.win.NewWebView(mygo.WebViewOptions{BackgroundColor: bg})
		if err != nil {
			ui.Text(c, "This cannot show here: "+err.Error()).TextColor(pal.TextMuted)
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
		v.Page().LoadHTML(src, "about:blank")
		a.embedSrc[key] = src
		delete(a.embedSizes, key)
		ready = false
	}
	wv := ui.WebView(c, v).Height(height).FillWidth().Radius(6)
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
