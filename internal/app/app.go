// Package app is Leaflet's desktop app: the window, the shell around pages,
// and the screens and dialogs, in MyGo's native UI.
package app

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// Route is what the main area shows.
type Route struct {
	Kind string // home | page | todos | signin
	ID   string // the page
}

// App is the state of the app, kept for as long as it runs.
type App struct {
	st      *store.Store
	win     *mygo.Window
	backend *remote.Client
	sync    *syncsvc.Service
	hub     *agents.Hub
	rec     *recording.Controller

	settings store.Settings
	session  store.Session
	online   bool

	// What the views show, reloaded as the store changes.
	pages     []store.Page
	templates []store.Page
	// templateHints are templates' first lines, by ID and edit time.
	templateHints map[string]string
	// todos are the open to-dos of every page, read again when stale;
	// todoState is the To-dos view's list.
	todos      []store.PageTodo
	todosStale bool
	todoState  ui.ListState
	folders    []store.Folder
	files      []store.File
	providers  []store.Provider
	pending    int
	conflicts  []store.ConflictRow
	syncing    bool
	syncError  string

	route Route
	// router is MyGo's history of the routes; routerAt is its path the
	// app last followed.
	router   *ui.Router
	routerAt string
	page     *pageState

	// Shell.
	sidebarW float32
	// treeState is the sidebar's tree, its items open and its row chosen.
	treeState ui.OutlineState[string]
	treeRow   int
	// treeRows are the tree's rows built this frame, for drops beside them.
	treeRows  []treeRow
	panelOpen bool
	panelTab  int
	// settingsOpen shows Settings over the window, at settingsSection.
	settingsOpen    bool
	settingsSection string
	toasts          []toast
	// toastUntil is when the last toast goes, for the run bar to wait
	// rather than show under it.
	toastUntil time.Time

	dialogs dialogs
	md      markdown.Options
	images  *imageCache
	// downloads are the files downloading from the server, or that
	// failed to.
	downloads map[string]bool
	embeds    map[string]*mygo.WebView
	// embedSrc is the page each embed's web view last loaded, and
	// embedSizes how tall its content said it is, once it has drawn.
	embedSrc   map[string]string
	embedSizes map[string]embedSize
	// videos are the players of the open page's videos that were played;
	// videoInfo what each video's site said about it, once asked
	// (videoAsked), by provider and ID.
	videos     map[string]*mygo.WebView
	videoInfo  map[string]videoInfo
	videoAsked map[string]bool
	// linkInfo is what each link card's page said about itself, once
	// asked (linkAsked), by link.
	linkInfo  map[string]linkInfo
	linkAsked map[string]bool

	// The menu bar icon, and the menu it last showed.
	tray      *mygo.Tray
	trayShown trayState
	trayBuilt bool
	// updateReady is the version installed in the background, which runs
	// once the app starts again.
	updateReady string
	// quitting is set as a quit starts, when closing the window closes it
	// rather than hiding it to the menu bar.
	quitting bool
}

type toast struct {
	text, action string
	fn           func()
}

// pageState is the page open in the main area.
type pageState struct {
	id      string
	editor  *editor.Editor
	title   string
	titleIn ui.Handle
	// focusTitle asks the title to take the focus, for this many more
	// frames until it has it: a native menu closing, or the page it
	// replaces, can take it back in the frames between.
	focusTitle int
	// selectTitle selects the title as it takes the focus, to rename.
	selectTitle bool
	// loaded is the stored content the editor started from or last saved,
	// and stored that content as the store has it, encoded, before any
	// change that is no edit (page mentions' new titles).
	loaded  *doc.Content
	stored  string
	dirty   bool
	timer   *time.Timer
	threads []store.Thread
	members []store.Member
	// runs are the agent runs of this page, newest last, for Undo.
	tinted map[string]bool
	// scroll is where the page is scrolled to, and viewTop where its
	// scroll area starts in the window. scrollTo is a block to bring to
	// the top of it once the page has laid it out.
	scroll   ui.ScrollState
	viewTop  float32
	scrollTo string
	// outline is the Outline tab's list of headings, outlineRow its row
	// chosen, and outlineSeen the headings it has opened once.
	outline     ui.OutlineState[string]
	outlineRow  int
	outlineSeen map[string]bool
}

// Run starts the app.
func Run() {
	a := &App{embeds: map[string]*mygo.WebView{}, embedSrc: map[string]string{}, embedSizes: map[string]embedSize{},
		videos: map[string]*mygo.WebView{}, videoInfo: map[string]videoInfo{}, videoAsked: map[string]bool{},
		linkInfo: map[string]linkInfo{}, linkAsked: map[string]bool{}}
	theme.RegisterFonts()
	recording.Register()
	mygo.BindAs("Embeds", &embedScroll{a: a})
	// MyGo's updater, with its window for "Check for Updates…"; the check
	// as the app starts is checkForUpdate's, which installs quietly.
	mygo.Use(native.New(updater.Options{DisableAutomaticChecks: true}))
	mygo.App.WhenReady(func() {
		if err := a.start(); err != nil {
			mygo.Dialog.Error(locales.AppStartFailed(), err.Error())
			mygo.App.Quit()
			return
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// DataDir is the folder of the app's database: LEAFLET_DATA_DIR, or the
// app's data folder.
func DataDir() string {
	if d := os.Getenv("LEAFLET_DATA_DIR"); d != "" {
		return d
	}
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".leaflet")
	}
	return dir
}

func (a *App) start() error {
	st, err := store.Open(DataDir())
	if err != nil {
		return err
	}
	a.st = st
	a.settings = st.Settings()
	a.sidebarW = a.settings.SidebarWidth
	a.panelOpen = false
	a.applyTheme()
	a.backend = remote.New(remote.Config(), st)
	var user *store.User
	if a.backend.Configured() {
		user = a.backend.RestoreSession()
	}
	if _, err := st.LoadSession(user); err != nil {
		return err
	}
	a.session = st.Session()
	a.images = newImageCache(filepath.Join(st.Dir, "images"))
	// Links in agents' replies and previews: a page link opens the page
	// here, a web link the browser.
	a.md.OnLink = func(url string) {
		if id, ok := doc.PageLinkID(url); ok {
			a.update(func() { a.openBlock(id, doc.LinkBlock(url)) })
		} else if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "mailto:") {
			_ = mygo.Shell.OpenExternal(url)
		}
	}
	a.rec = recording.NewController(st, a.backend)
	a.rec.RecoverInterrupted()
	a.hub = agents.NewHub(st, a.backend, a)
	a.sync = syncsvc.New(st, a.backend)
	a.online = mygo.Network.Status().Online
	mygo.Network.OnChanged(func(s mygo.NetworkStatus) {
		a.update(func() {
			a.online = s.Online
			if s.Online {
				a.sync.Kick()
			}
		})
	})
	a.sync.OnState = func(running bool, err error) {
		a.update(func() {
			a.syncing = running
			a.syncError = ""
			if err != nil {
				a.syncError = err.Error()
			}
		})
	}
	a.sync.OnPageContent = func(pageID string) {
		a.update(func() { a.externalContent(pageID) })
	}
	a.sync.Offline = func() bool { return a.settings.WorkOffline }
	st.Subscribe(func(ch store.Change) { a.update(func() { a.reload(ch) }) })
	// Agents outside the app (leaflet mcp) write to the database too.
	go st.WatchOthers(time.Second, nil, func() { a.update(a.outsideChange) })
	if err := a.createWelcome(); err != nil {
		log.Printf("welcome page: %v", err)
	}
	a.reloadAll()
	a.route = Route{Kind: "home"}
	if last, ok := st.Value("last.page"); ok && a.pageByID(last) != nil {
		a.openPage(last)
	} else if len(a.pages) > 0 {
		a.openPage(a.pages[0].ID)
	}
	// The router starts at the route opened, with no history before it.
	a.router = ui.NewRouter(a.route.path())
	a.router.Transition = ui.TransitionNone
	a.routerAt = a.router.Path()
	a.setMenu()
	// What was set once says the new language when it changes.
	mygo.App.OnLocaleChanged(func(string) {
		a.update(func() {
			a.setMenu()
			a.trayBuilt = false
			a.refreshTray()
		})
	})
	a.startReminders()
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:         "Leaflet",
		Width:         1240,
		Height:        820,
		MinWidth:      720,
		MinHeight:     480,
		StateKey:      "main",
		TitleBarStyle: mygo.TitleBarHidden,
		Vibrancy:      mygo.VibrancySidebar,
		Content:       ui.View(a.view),
	})
	a.startTray()
	a.win.OnClose(func(e *mygo.CloseEvent) {
		a.flush()
		// With the menu bar icon, closing the window hides it, and the
		// icon or the Dock brings it back.
		if a.tray != nil && !a.quitting {
			e.PreventDefault()
			a.win.Hide()
		}
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.quitting = true })
	mygo.App.OnActivate(func(visible bool) {
		if !visible {
			a.update(a.showWindow)
		}
	})
	go a.checkForUpdate()
	a.rec.Attach(a.win, func(fn func()) { a.update(func() { fn(); a.refreshTray() }) })
	a.sync.Start()
	return nil
}

// update runs fn on the main thread and redraws.
func (a *App) update(fn func()) {
	if a.win == nil {
		fn()
		return
	}
	a.win.Update(fn)
}

func (a *App) applyTheme() {
	theme.Look(a.settings.Theme == "sepia", a.settings.Accent)
	switch a.settings.Theme {
	case "light", "sepia":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func (a *App) saveSettings() {
	if err := a.st.SaveSettings(a.settings); err != nil {
		log.Printf("settings: %v", err)
	}
}

// reloadAll reads everything the views show.
func (a *App) reloadAll() {
	for _, t := range []string{"pages", "folders", "files", "model_providers", "outbox", "conflicts", "session"} {
		a.reload(store.Change{Table: t})
	}
}

// reload rereads what a change touched.
func (a *App) reload(ch store.Change) {
	all := ch.Table == "*"
	if all || ch.Table == "pages" || ch.Table == "page_contents" {
		if pages, err := a.st.Pages(); err == nil {
			a.pages = pages
		}
		if templates, err := a.st.Templates(); err == nil {
			a.templates = templates
		}
		a.todosStale = true
		if a.page != nil {
			if p := a.pageByID(a.page.id); p == nil {
				a.page = nil
				a.route = Route{Kind: "home"}
			} else if !a.page.titleIn.Focused(nil) && p.Title != a.page.title {
				a.page.title = p.Title
			}
		}
	}
	if all || ch.Table == "folders" {
		a.folders, _ = a.st.Folders()
	}
	if all || ch.Table == "files" {
		a.files, _ = a.st.Files()
	}
	if all || ch.Table == "model_providers" {
		a.providers, _ = a.st.Providers()
	}
	if all || ch.Table == "outbox" {
		a.pending = a.st.PendingCount()
	}
	if all || ch.Table == "conflicts" {
		a.conflicts, _ = a.st.Conflicts()
	}
	if all || ch.Table == "session" {
		a.session = a.st.Session()
	}
	if a.page != nil && (all || ch.Table == "comments" && (ch.PageID == "" || ch.PageID == a.page.id)) {
		a.page.threads, _ = a.st.Threads(a.page.id)
	}
	if a.page != nil && (all || ch.Table == "page_members") {
		a.page.members, _ = a.st.Members(a.page.id)
		a.page.editor.ReadOnly = !a.canEdit(a.page.id)
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// pageByID finds a page, or a template, which opens as one.
func (a *App) pageByID(id string) *store.Page {
	for i := range a.pages {
		if a.pages[i].ID == id {
			return &a.pages[i]
		}
	}
	for i := range a.templates {
		if a.templates[i].ID == id {
			return &a.templates[i]
		}
	}
	return nil
}

// children returns the pages under a parent, or the top-level pages of a
// folder ("" for none) when parent is "".
func (a *App) children(parent, folder string, top bool) []store.Page {
	var out []store.Page
	for _, p := range a.pages {
		if p.ParentID != parent {
			continue
		}
		if top && p.FolderID != folder {
			continue
		}
		if parent == "" && p.SpaceID != a.session.SpaceID && !a.isShared(p) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SortKey < out[j].SortKey })
	return out
}

// isShared reports whether a page belongs to someone else's space.
func (a *App) isShared(p store.Page) bool { return p.SpaceID != a.session.SpaceID }

// sharedRoots are pages shared with the user whose parent is not.
func (a *App) sharedRoots() []store.Page {
	var out []store.Page
	for _, p := range a.pages {
		if !a.isShared(p) {
			continue
		}
		if p.ParentID == "" || a.pageByID(p.ParentID) == nil {
			out = append(out, p)
		}
	}
	return out
}

// ancestors returns a page's parents, the root first.
func (a *App) ancestors(id string) []store.Page {
	var out []store.Page
	seen := map[string]bool{}
	for p := a.pageByID(id); p != nil && p.ParentID != "" && !seen[p.ID]; {
		seen[p.ID] = true
		parent := a.pageByID(p.ParentID)
		if parent == nil {
			break
		}
		out = append([]store.Page{*parent}, out...)
		p = parent
	}
	return out
}

// Navigation is MyGo's Router: its history of the routes' paths, which
// Back and Forward, their keys and buttons go through.

// path is a route's path in the router.
func (r Route) path() string {
	switch r.Kind {
	case "page":
		return "/page/" + r.ID
	case "todos", "signin":
		return "/" + r.Kind
	}
	return "/"
}

// routeAt is the route of a path in the router.
func routeAt(path string) Route {
	kind, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	switch kind {
	case "page":
		return Route{Kind: "page", ID: rest}
	case "todos", "signin":
		return Route{Kind: kind}
	}
	return Route{Kind: "home"}
}

// routeTitle names the route shown, as Back's list of pages does.
func (a *App) routeTitle() string {
	switch a.route.Kind {
	case "page":
		return a.pageTitleOf(a.route.ID)
	case "todos":
		return locales.AppRouteTodos()
	case "signin":
		return locales.CommonSignIn()
	}
	return "Leaflet"
}

// openSettings shows Settings over the window at a section, Account if
// none.
func (a *App) openSettings(section string) {
	a.flush()
	if section == "" {
		section = "account"
	}
	a.settingsSection, a.settingsOpen = section, true
}

// toggleSettings opens Settings at Account, or closes it, as ⌘, does.
func (a *App) toggleSettings() {
	if a.settingsOpen {
		a.settingsOpen = false
		return
	}
	a.openSettings("account")
}

// navigate shows a route, after the router's current one.
func (a *App) navigate(r Route) {
	if r == a.route {
		return
	}
	a.flush()
	a.show(r)
	a.follow()
}

func (a *App) show(r Route) {
	a.route = r
	if r.Kind == "page" {
		a.loadPage(r.ID)
	} else {
		a.closePage()
	}
}

func (a *App) back() {
	if a.router != nil {
		a.router.Back()
		a.follow()
	}
}

func (a *App) forward() {
	if a.router != nil {
		a.router.Forward()
		a.follow()
	}
}

// follow keeps the app's route and the router's in step: a move of the
// router (Back, Forward, their keys and buttons) shows its route, and a
// route the app went to (navigate) goes into the router's history.
func (a *App) follow() {
	if a.router == nil {
		return
	}
	at := a.router.Path()
	if at != a.routerAt {
		a.routerAt = at
		r := routeAt(at)
		if r.Kind == "page" && a.pageByID(r.ID) == nil {
			r = Route{Kind: "home"}
		}
		if r != a.route {
			a.flush()
			a.show(r)
		}
		return
	}
	if p := a.route.path(); p != at {
		a.router.Push(p)
		a.routerAt = a.router.Path()
	}
}

// openPage shows a page.
func (a *App) openPage(id string) {
	if a.route.Kind == "page" && a.route.ID == id {
		return
	}
	// Show where it is in the sidebar.
	for _, p := range a.ancestors(id) {
		a.treeOpen("page", p.ID)
	}
	// A folder holds the page itself, or the page at the top of it.
	top := a.pageByID(id)
	if anc := a.ancestors(id); len(anc) > 0 {
		top = &anc[0]
	}
	if top != nil && top.FolderID != "" {
		a.treeOpen("folder", top.FolderID)
	}
	if a.win == nil {
		a.route = Route{Kind: "page", ID: id}
		a.loadPage(id)
		return
	}
	a.navigate(Route{Kind: "page", ID: id})
}

func (a *App) loadPage(id string) {
	a.closePage()
	p := a.pageByID(id)
	if p == nil {
		a.route = Route{Kind: "home"}
		return
	}
	content := a.st.Content(id)
	if content == nil {
		content = doc.Empty()
	}
	stored := content.Encode()
	// Mentions of pages show their titles now, as renamed since; this
	// is no edit, so it saves with the page's next one. The store has
	// the title the page just closed saved, which a.pages may not yet.
	content.RefreshPageMentions(func(id string) (string, bool) {
		p, err := a.st.Page(id)
		return p.DisplayTitle(), err == nil
	})
	ps := &pageState{id: id, title: p.Title, loaded: content.Copy(), stored: stored, tinted: map[string]bool{}}
	ed := editor.New(content, &host{a: a})
	ed.Size = float32(a.settings.TextSize)
	ed.ReadOnly = !a.canEdit(id)
	ed.Tinted = ps.tinted
	ed.OnChange = func() { a.scheduleSave() }
	ps.editor = ed
	ps.threads, _ = a.st.Threads(id)
	ps.members, _ = a.st.Members(id)
	a.page = ps
	a.sync.SetOpen(id, true)
	_ = a.st.SetValue("last.page", id)
	if a.backend.SignedIn() {
		go a.loadMembers(id)
	}
}

func (a *App) closePage() {
	if a.page == nil {
		return
	}
	a.flush()
	for id, v := range a.embeds {
		v.Destroy()
		delete(a.embeds, id)
	}
	clear(a.embedSrc)
	a.sync.SetOpen(a.page.id, false)
	a.page = nil
	a.dropVideos()
}

// canEdit reports whether the user may change a page.
func (a *App) canEdit(id string) bool {
	p := a.pageByID(id)
	return p != nil && a.st.CanEdit(*p)
}

// scheduleSave saves the open page shortly after the typing stops.
func (a *App) scheduleSave() {
	ps := a.page
	if ps == nil || ps.editor.ReadOnly {
		return
	}
	ps.dirty = true
	if ps.timer != nil {
		ps.timer.Stop()
	}
	ps.timer = time.AfterFunc(700*time.Millisecond, func() {
		a.update(func() {
			if a.page == ps {
				a.savePage()
			}
		})
	})
}

// savePage stores the open page's content.
func (a *App) savePage() {
	ps := a.page
	if ps == nil || !ps.dirty {
		return
	}
	// What was stored since, by another process, merges in first, so the
	// save keeps it.
	a.externalContent(ps.id)
	ps.dirty = false
	if ps.timer != nil {
		ps.timer.Stop()
	}
	stamped, changed, err := a.st.SaveContent(ps.id, ps.editor.Content)
	if err != nil {
		a.toast(locales.AppErrorSave(err.Error()))
		return
	}
	ps.editor.Content.Blocks, ps.editor.Content.Deleted = stamped.Blocks, stamped.Deleted
	ps.loaded, ps.stored = stamped.Copy(), stamped.Encode()
	if changed {
		_ = a.st.MaybeAutosave(ps.id, stamped)
		a.sync.Kick()
	}
}

// flush saves what is waiting to be saved.
func (a *App) flush() {
	if a.page != nil {
		if a.page.title != "" || a.pageByID(a.page.id) != nil {
			a.saveTitle()
		}
		a.savePage()
	}
}

func (a *App) saveTitle() {
	ps := a.page
	if ps == nil {
		return
	}
	if p := a.pageByID(ps.id); p != nil && p.Title != ps.title && a.canEdit(ps.id) {
		if err := a.st.RenamePage(ps.id, ps.title); err != nil {
			a.toast(locales.AppErrorRename(err.Error()))
		}
	}
}

// externalContent brings content stored from outside the editor (sync,
// agent runs on the server, restores) into the open page, merging what is
// being typed.
func (a *App) externalContent(pageID string) {
	ps := a.page
	if ps == nil || ps.id != pageID {
		return
	}
	stored := a.st.Content(pageID)
	if stored == nil || stored.Encode() == ps.stored {
		return
	}
	if ps.dirty {
		local := ps.editor.Content.Stamped(ps.loaded, doc.NowMs())
		merged, _ := doc.Merge(doc.MustDecode(ps.stored), local, stored)
		ps.editor.Replace(merged)
		ps.loaded, ps.stored = stored.Copy(), stored.Encode()
		a.scheduleSave()
		return
	}
	ps.editor.Replace(stored.Copy())
	ps.loaded, ps.stored = stored.Copy(), stored.Encode()
}

// outsideChange rereads everything after another process wrote to the
// database, brings the open page up to date, and syncs what it queued.
func (a *App) outsideChange() {
	a.reload(store.Change{Table: "*"})
	if a.page != nil {
		a.externalContent(a.page.id)
	}
	a.sync.Kick()
}

// toast shows a message at the bottom of the window.
func (a *App) toast(text string) {
	a.toasts = append(a.toasts, toast{text: text})
	a.holdRunBar(4 * time.Second)
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) toastAction(text, action string, fn func()) {
	a.toasts = append(a.toasts, toast{text: text, action: action, fn: fn})
	a.holdRunBar(8 * time.Second)
	if a.win != nil {
		a.win.Invalidate()
	}
}

// holdRunBar keeps the run bar away while a toast shows for d, as both sit
// at the bottom of the page, and brings it back after.
func (a *App) holdRunBar(d time.Duration) {
	if until := time.Now().Add(d); until.After(a.toastUntil) {
		a.toastUntil = until
		time.AfterFunc(d+50*time.Millisecond, func() { a.update(func() {}) })
	}
}

// newPage creates a page and opens it.
func (a *App) newPage(parent, folder string) {
	p, err := a.st.CreatePage(store.NewPageOptions{ParentID: parent, FolderID: folder})
	if err != nil {
		a.toast(locales.AppErrorCreatePage(err.Error()))
		return
	}
	a.pages, _ = a.st.Pages()
	if parent != "" {
		a.treeOpen("page", parent)
		a.linkSubpage(parent, p.ID, "")
	}
	a.openPage(p.ID)
	a.focusTitle(false)
}

// linkSubpage puts a link to subpage id at the end of page parent, as
// / › Subpage does where the caret is, so a page made from the sidebar
// shows in the page it is inside. A copy goes after the link to the page
// it copies, after, when parent has one.
func (a *App) linkSubpage(parent, id, after string) {
	link := &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": id}}
	insert := func(c *doc.Content) {
		at := doc.Position{At: "end"}
		c.Walk(func(n, _ *doc.Node) {
			if after != "" && n.Type == "subpage" && n.Str("page_id") == after {
				at = doc.Position{After: n.ID}
			}
		})
		_ = c.Insert(at, link)
	}
	if ed := a.Editor(parent); ed != nil {
		ed.Edit(insert)
		a.savePage()
		return
	}
	c := a.st.Content(parent)
	if c == nil {
		c = doc.Empty()
	}
	insert(c)
	if _, _, err := a.st.SaveContent(parent, c); err != nil {
		a.toast(locales.AppErrorLinkSubpage(err.Error()))
	}
}

// unlinkSubpage takes the links to subpage id out of page parent, as when
// the subpage moves out of it.
func (a *App) unlinkSubpage(parent, id string) {
	remove := func(c *doc.Content) {
		var links []string
		c.Walk(func(n, _ *doc.Node) {
			if n.Type == "subpage" && n.Str("page_id") == id {
				links = append(links, n.ID)
			}
		})
		for _, l := range links {
			c.Remove(l)
		}
	}
	if ed := a.Editor(parent); ed != nil {
		ed.Edit(remove)
		a.savePage()
		return
	}
	c := a.st.Content(parent)
	if c == nil {
		return
	}
	remove(c)
	if _, _, err := a.st.SaveContent(parent, c); err != nil {
		a.toast(locales.AppErrorUnlinkSubpage(err.Error()))
	}
}

// hasSubpageLink reports whether page parent links to subpage id.
func (a *App) hasSubpageLink(parent, id string) bool {
	c := a.st.Content(parent)
	if ed := a.Editor(parent); ed != nil {
		c = ed.Content
	}
	found := false
	if c != nil {
		c.Walk(func(n, _ *doc.Node) { found = found || n.Type == "subpage" && n.Str("page_id") == id })
	}
	return found
}

// movePage moves page id under page parent, or to the top level, by
// move, and keeps the links to it in step: a page it leaves loses its
// link, and a page it goes into links to it at its end.
func (a *App) movePage(id, parent string, move func() error) error {
	old := ""
	if p := a.pageByID(id); p != nil {
		old = p.ParentID
	}
	if err := move(); err != nil {
		return err
	}
	if old == parent {
		return nil
	}
	if old != "" && a.canEdit(old) {
		a.unlinkSubpage(old, id)
	}
	if parent != "" && a.canEdit(parent) && !a.hasSubpageLink(parent, id) {
		a.linkSubpage(parent, id, "")
	}
	return nil
}

// focusTitle puts the caret in the open page's title, selecting the title
// when select is set.
func (a *App) focusTitle(selectAll bool) {
	if a.page != nil {
		a.page.focusTitle, a.page.selectTitle = 3, selectAll
	}
}

// deletePage deletes a page and offers to undo.
func (a *App) deletePage(id string) {
	title := locales.CommonUntitled()
	if p := a.pageByID(id); p != nil {
		title = p.DisplayTitle()
	}
	if a.page != nil && a.page.id == id {
		a.flush()
	}
	ids, err := a.st.DeletePage(id)
	if err != nil {
		a.toast(locales.AppErrorDelete(err.Error()))
		return
	}
	a.pages, _ = a.st.Pages()
	if a.route.Kind == "page" {
		for _, d := range ids {
			if d == a.route.ID {
				a.closePage()
				a.route = Route{Kind: "home"}
			}
		}
	}
	a.toastAction(locales.AppDeleted(title), locales.CommonUndo(), func() {
		if err := a.st.RestorePages(ids); err != nil {
			a.toast(locales.AppErrorRestore(err.Error()))
		}
		a.pages, _ = a.st.Pages()
	})
}

func (a *App) setMenu() {
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Leaflet", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			updater.MenuItem(),
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Label: locales.AppMenuFile(), Submenu: []*mygo.MenuItem{
			{Label: locales.AppMenuNewPage(), Accelerator: "CmdOrCtrl+N", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.update(func() { a.newPage("", "") })
			}},
			{Label: locales.AppMenuNewFolder(), Accelerator: "CmdOrCtrl+Shift+N", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.update(func() { a.dialogs.openRenameFolder(nil) })
			}},
			{Type: mygo.MenuItemSeparator},
			{Label: locales.AppMenuSearch(), Accelerator: "CmdOrCtrl+P", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.update(func() { a.openSearch() })
			}},
			{Label: locales.AppMenuExportMarkdown(), Click: func(*mygo.MenuItem, *mygo.Window) {
				a.update(func() {
					if a.page != nil {
						a.exportPage(a.page.id)
					}
				})
			}},
			{Type: mygo.MenuItemSeparator},
			{Role: mygo.RoleClose},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: locales.AppMenuView(), Submenu: []*mygo.MenuItem{
			{Label: locales.AppMenuToggleSidebar(), Accelerator: "CmdOrCtrl+\\", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.update(func() { a.toggleSidebar() })
			}},
			{Label: locales.AppMenuToggleAgentPanel(), Accelerator: "CmdOrCtrl+J", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.update(func() { a.togglePanel(0) })
			}},
			{Label: locales.AppMenuComments(), Accelerator: "CmdOrCtrl+Shift+M", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.update(func() { a.togglePanel(1) })
			}},
			{Type: mygo.MenuItemSeparator},
			{Label: locales.CommonBack(), Accelerator: "CmdOrCtrl+[", Click: func(*mygo.MenuItem, *mygo.Window) { a.update(a.back) }},
			{Label: locales.AppMenuForward(), Accelerator: "CmdOrCtrl+]", Click: func(*mygo.MenuItem, *mygo.Window) { a.update(a.forward) }},
			{Type: mygo.MenuItemSeparator},
			{Role: mygo.RoleToggleFullScreen},
		}},
		{Role: mygo.RoleWindowMenu},
	}))
}

func (a *App) toggleSidebar() {
	a.settings.SidebarOpen = !a.settings.SidebarOpen
	a.saveSettings()
}

func (a *App) togglePanel(tab int) {
	if a.panelOpen && a.panelTab == tab {
		a.panelOpen = false
		return
	}
	a.panelOpen, a.panelTab = true, tab
}

// pageTitleOf is a page's title for display.
func (a *App) pageTitleOf(id string) string {
	if p := a.pageByID(id); p != nil {
		return p.DisplayTitle()
	}
	return ""
}
