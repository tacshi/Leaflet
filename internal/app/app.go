package app

import (
	"context"
	"log"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/guard"
	"leaflet/internal/library"
	"leaflet/internal/live"
	"leaflet/internal/mediaserver"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/internal/templates"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

type Route struct {
	Kind string
	ID   string
}

type App struct {
	st       *store.Store
	win      *mygo.Window
	post     func(func())
	backend  *remote.Client
	sync     *syncsvc.Service
	hub      *agents.Hub
	rec      *recording.Controller
	cr       *core.Core
	capture  *webCapture
	winMu    sync.Mutex
	starting bool
	early    []func()

	settings      store.Settings
	endpointKeyMu sync.Mutex
	endpointKeys  atomic.Int64
	session       store.Session
	online        bool

	pages         []store.Page
	templates     []store.Page
	templateHints map[string]string
	goneStarters  map[string]bool
	todos         []store.PageTodo
	todosStale    bool
	todoState     ui.ListState
	folders       []store.Folder
	files         []store.File
	providers     []store.Provider
	lib           *library.Tree
	libFrom       arrangedFrom
	pageIndex     map[string]*store.Page
	pageIndexFrom listsFrom
	fileIndex     map[string]int
	fileIndexFrom listsFrom
	present       map[string]presence
	mentions      mentionMenu
	editableMu    sync.Mutex
	editable      map[string]bool
	recs          map[string]recView
	recOpen       map[string]bool
	pending       int
	conflicts     []store.ConflictRow
	syncing       bool
	syncError     string
	syncErr       error
	icloudStop    context.CancelFunc
	icloudBusy    bool

	route    Route
	router   *ui.Router
	routerAt string
	page     *pageState

	sidebarW        float32
	treeState       ui.OutlineState[string]
	treeRow         int
	treeIn          treeInput
	treeRows        []treeRow
	tagTree         ui.OutlineState[string]
	tagRow          int
	tagIn           treeInput
	panelOpen       bool
	panelW          float32
	panelTab        int
	agentScope      int
	panelTabsW      float32
	settingsOpen    bool
	settingsSection string
	toasts          []toast
	toastUntil      time.Time
	account         accountState
	form            providerForm
	formGen         int
	chatgptRead     map[string]bool
	speech          speechState
	external        externalState
	signIn          signInState
	supa            supabaseState
	supaRuns        int
	players         map[string]*player

	dialogs                  dialogs
	md                       markdown.Options
	commentMD                markdown.Options
	images                   *imageCache
	downloads                map[string][]func(bool)
	downloadFails            map[string]time.Time
	fetchingCopy             bool
	fetchedAudio             []string
	leaving                  bool
	embeds                   map[string]*mygo.WebView
	embedIn                  map[string]embedInput
	embedSizes               map[string]embedSize
	embedZoomed              map[string]bool
	embedToPage              map[string]bool
	embedFailed              map[string]string
	embedOver                string
	embedWheel               float32
	embedBuilt               map[string]bool
	videos                   map[string]*mygo.WebView
	media                    mediaserver.Server
	fileMedia                map[string]string
	fileFailed               map[string]bool
	now                      nowPlaying
	fileNow                  fileStatus
	fileNowAt                time.Time
	fileWatch                map[string]chan struct{}
	followLines, followRead  bool
	followAt                 float32
	sitePlayers              bool
	playSpeed                float64
	systemAudio              systemAudio
	systemWatch, systemHeard bool
	line                     lineKey
	lineRow                  ui.Handle
	lineMoved                bool
	rolls                    map[string]*roll
	fileToTranscribe         string
	fileTranscribeWith       string
	preview                  *filePreview
	videoInfo                webAnswers[videoInfo]
	notesRead                map[string]readNotes
	notesOpen                map[string]bool
	linkInfo                 webAnswers[web.LinkInfo]
	drafts                   map[string]*pageDrafts
	widthsTimer              *time.Timer

	tray         *mygo.Tray
	trayShown    trayState
	trayBuilt    bool
	updatable    bool
	updateBusy   string
	updateReady  string
	updateFailed string
	quitting     bool
	recKeeping   bool
	relaunching  bool
	recKeptAt    time.Time
	openView     store.OpenView
	viewTimer    *time.Timer

	fileToDelete *store.File

	pub         *pubLink
	cloudflared cloudflaredInfo
	pubPage     struct {
		sync.Mutex
		id     string
		served bool
	}
}

func (a *App) setPub(p *pubLink) {
	a.pub = p
	a.pubPage.Lock()
	a.pubPage.id, a.pubPage.served = "", false
	if p != nil {
		a.pubPage.id, a.pubPage.served = p.page, p.serving()
	}
	a.pubPage.Unlock()
}

func (a *App) publiclyLinked(pageID string) bool {
	a.pubPage.Lock()
	defer a.pubPage.Unlock()
	return a.pubPage.id != "" && a.pubPage.id == pageID
}

func (a *App) publiclyServed(pageID string) bool {
	a.pubPage.Lock()
	defer a.pubPage.Unlock()
	return a.pubPage.served && a.pubPage.id == pageID
}

type toast struct {
	text, action string
	fn           func()
}

type pageState struct {
	id                       string
	editor                   *editor.Editor
	title                    string
	titleIn                  ui.Handle
	savedTitle               string
	focusTitle               int
	selectTitle              bool
	titleTimer               *time.Timer
	titleAtEnd, titleLeave   bool
	tags                     []string
	tagsEditing, tagsFocused bool
	live                     *live.Page
	threads                  []store.Thread
	open                     map[string]int
	members                  []store.Member
	tinted                   map[string]bool
	lit                      map[string]time.Time
	caretSet                 bool
	scroll                   ui.ScrollState
	view                     ui.Handle
	scrollTo                 string
	scrollWait               int
	held                     string
	heldUntil                time.Time
	heldAt                   float32
	fullDiagram              string
	outline                  ui.OutlineState[string]
	outlineRow               int
	outlineSeen              map[string]bool
	outlineKeys              bool
}

func Run() {
	a := &App{embeds: map[string]*mygo.WebView{}, embedIn: map[string]embedInput{}, embedSizes: map[string]embedSize{},
		embedZoomed: map[string]bool{}, embedToPage: map[string]bool{}, embedFailed: map[string]string{}, videos: map[string]*mygo.WebView{}}
	if a.firstInstance() {
		launch(a)
	}
}

var singleInstance = func() bool { return mygo.App.RequestSingleInstanceLock() }

var launch = (*App).launch

func (a *App) firstInstance() bool {
	if os.Getenv("LEAFLET_DATA_DIR") != "" {
		return true
	}
	if !singleInstance() {
		return false
	}
	mygo.App.OnSecondInstance(func([]string, string) { a.update(a.showWindow) })
	return true
}

func (a *App) launch() {
	theme.RegisterFonts()
	registerCapture()
	mygo.BindAs("Embeds", &embedScroll{a: a})
	mygo.Use(native.New(updater.Options{DisableAutomaticChecks: true}))
	mygo.App.OnNotificationClick(a.notificationClicked)
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
	a.winMu.Lock()
	a.starting = true
	a.winMu.Unlock()
	c, err := core.Open(core.Config{
		Platform: core.Platform{
			DataDir:     DataDir(),
			Online:      func() bool { return mygo.Network.Status().Online },
			OpenURL:     func(u string) { _ = mygo.Shell.OpenExternal(u) },
			WatchICloud: a.watchICloud,
			Watched:     a.watched,
		},
		Agents: a,
		OnSyncState: func(running bool, err error) {
			a.update(func() {
				a.syncing = running
				a.syncErr, a.syncError = err, a.syncMessage(err)
			})
		},
		OnPageContent: func(pageID string) {
			a.update(func() { a.externalContent(pageID) })
		},
		OnChange: func(ch store.Change) { a.update(func() { a.reload(ch) }) },
	})
	if err != nil {
		return err
	}
	st := c.Store
	a.st, a.backend, a.sync, a.hub, a.rec = st, c.Remote, c.Sync, c.Hub, c.Rec
	a.keepCore(c)
	a.settings = st.Settings()
	a.sidebarW = a.settings.SidebarWidth
	a.panelW = a.settings.PanelWidth
	a.panelOpen = false
	a.applyTheme()
	a.session = st.Session()
	a.images = newImageCache(st.Dir)
	a.md.OnLink = func(url string) {
		if id, ok := doc.PageLinkID(url); ok {
			at, moment := doc.LinkMoment(url)
			if !moment {
				at = -1
			}
			a.update(func() {
				if !a.pageOpen() && a.panelShown() {
					a.fromWorkspace()
				}
				a.openLink(id, doc.LinkBlock(url), at)
			})
		} else if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "mailto:") {
			_ = mygo.Shell.OpenExternal(url)
		}
	}
	a.commentMD.OnLink = a.md.OnLink
	a.hub.PublicLink = a.publiclyLinked
	a.online = mygo.Network.Status().Online
	mygo.Network.OnChanged(func(s mygo.NetworkStatus) {
		a.update(func() {
			a.online = s.Online
			if s.Online {
				a.sync.Kick()
			}
		})
	})
	go st.WatchOthers(time.Second, nil, func() { a.update(a.outsideChange) })
	a.reloadAll()
	a.route = Route{Kind: "home"}
	_ = st.SetOpenView(nil)
	if last, ok := st.Value(store.LastPageKey); ok && a.pageByID(last) != nil {
		a.openPage(last)
	} else if len(a.pages) > 0 {
		a.openPage(a.pages[0].ID)
	}
	a.router = ui.NewRouter(a.route.path())
	a.router.Transition = ui.TransitionNone
	a.routerAt = a.router.Path()
	a.setMenu()
	mygo.App.OnLocaleChanged(func(string) {
		a.update(func() {
			a.setMenu()
			a.trayBuilt = false
			a.refreshTray()
		})
	})
	a.startReminders()
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:                "Leaflet",
		Width:                1240,
		Height:               820,
		MinWidth:             windowMinW,
		MinHeight:            480,
		StateKey:             "main",
		TitleBarStyle:        mygo.TitleBarHidden,
		TrafficLightPosition: &mygo.Point{X: 9, Y: 16},
		TitleBarHeight:       headerHeight,
		Vibrancy:             mygo.VibrancySidebar,
		Content:              ui.View(a.view),
	})
	a.winMu.Lock()
	a.win, a.starting = win, false
	for _, fn := range a.early {
		win.Update(fn)
	}
	a.early = nil
	a.winMu.Unlock()
	a.startTray()
	a.win.OnClose(a.closing)
	mygo.App.OnBeforeQuit(a.beforeQuit)
	mygo.App.OnActivate(func(visible bool) {
		if !visible {
			a.update(a.showWindow)
		}
	})
	a.updatable = mygo.Updater.Enabled()
	go a.checkForUpdate()
	a.capture = &webCapture{win: a.win}
	a.rec.Attach(a.capture, func(fn func()) { a.update(func() { fn(); a.refreshTray() }) })
	go a.findSpeech()
	a.loadSpeechKeys()
	a.core().Start()
	return nil
}

func (a *App) closing(e *mygo.CloseEvent) {
	saved := a.flush()
	if a.tray != nil && !a.quitting {
		e.PreventDefault()
		a.win.Hide()
		return
	}
	if !saved {
		e.PreventDefault()
		a.quitting = false
		return
	}
	if a.keepRecording() {
		e.PreventDefault()
		return
	}
	a.rec.Stop()
	a.stopPub()
	a.endView()
}

func (a *App) beforeQuit(e *mygo.QuitEvent) {
	if !a.flush() {
		e.PreventDefault()
		a.showWindow()
		return
	}
	if a.keepRecording() {
		e.PreventDefault()
		return
	}
	a.quitting = true
	a.stopPub()
	a.endView()
}

// quitWait is the longest a quit waits for a recording to be saved and its
// live transcript to end.
var quitWait = 20 * time.Second

var (
	quitApp     = func() { mygo.App.Quit() }
	relaunchApp = func() { mygo.App.Relaunch() }
)

// keepRecording reports whether a quit has to wait for a recording: it
// stops the one under way, and quits again once the audio and the
// transcript are stored or quitWait has passed. The quit that follows the
// wait is let through whatever is left unsaved.
func (a *App) keepRecording() bool {
	if a.recKeeping {
		return true
	}
	if time.Since(a.recKeptAt) < keptFor || !a.rec.Active() && a.rec.Saving() == 0 {
		return false
	}
	a.rec.Stop()
	a.recKeeping = true
	a.showWindow()
	a.toast(locales.AppSavingRecording())
	a.offMain(func() { a.rec.Settle(quitWait) }, func() {
		a.recKeeping, a.recKeptAt = false, time.Now()
		if relaunch := a.relaunching; relaunch {
			a.relaunching = false
			relaunchApp()
			return
		}
		quitApp()
	})
	return true
}

const keptFor = 5 * time.Second

func (a *App) watched(id string) bool {
	if a.publiclyServed(id) {
		return true
	}
	a.winMu.Lock()
	win := a.win
	a.winMu.Unlock()
	return win != nil && win.IsVisible() && !win.IsMinimized() && !mygo.App.IsHidden()
}

func (a *App) core() *core.Core {
	if c := a.cr; c != nil && c.Store == a.st && c.Remote == a.backend && c.Sync == a.sync && c.Hub == a.hub && c.Rec == a.rec {
		return c
	}
	return a.keepCore(&core.Core{Store: a.st, Remote: a.backend, Sync: a.sync, Hub: a.hub, Rec: a.rec})
}

func (a *App) keepCore(c *core.Core) *core.Core {
	c.OpenURL = func(u string) { _ = mygo.Shell.OpenExternal(u) }
	c.WatchICloud = a.watchICloud
	c.Settings = func() store.Settings { return a.settings }
	c.SaveSettings = func(s store.Settings) { a.settings = s; a.saveSettings() }
	a.cr = c
	return c
}

func (a *App) headless() bool {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	return a.win == nil && a.post == nil && !a.starting
}

func (a *App) offMain(work, then func()) {
	if a.headless() {
		work()
		then()
		return
	}
	go func() {
		defer guard.Recover()
		work()
		a.update(then)
	}()
}

func (a *App) later(d time.Duration, fn func()) *time.Timer {
	if a.headless() {
		fn()
		return nil
	}
	return time.AfterFunc(d, func() { a.update(fn) })
}

func (a *App) update(fn func()) {
	a.winMu.Lock()
	w := a.win
	if w == nil && a.starting {
		a.early = append(a.early, fn)
		a.winMu.Unlock()
		return
	}
	a.winMu.Unlock()
	if w == nil {
		if a.post != nil {
			a.post(fn)
			return
		}
		fn()
		return
	}
	w.Update(fn)
}

func (a *App) applyTheme() {
	theme.Look(a.settings.Theme == "sepia", a.settings.Accent)
	f := a.settings.Fonts
	theme.SetFonts(f.Latin, f.Chinese, f.Japanese, f.Code)
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

func (ps *pageState) setThreads(ts []store.Thread, _ error) {
	ps.threads, ps.open = ts, map[string]int{}
	for _, t := range ts {
		if !t.Root.Resolved {
			ps.open[t.Root.BlockID]++
		}
	}
}

func (a *App) reloadAll() {
	for _, t := range []string{"pages", "folders", "files", "model_providers", "outbox", "conflicts", "session"} {
		a.reload(store.Change{Table: t})
	}
}

func reread[T any](list *[]T, read func() ([]T, error)) {
	l, err := read()
	if err != nil {
		log.Printf("read: %v", err)
		return
	}
	*list = l
}

func (a *App) reload(ch store.Change) {
	all := ch.Table == "*"
	switch ch.Table {
	case "*", "pages", "page_members", "session":
		a.editableMu.Lock()
		a.editable = nil
		a.editableMu.Unlock()
	}
	if all || ch.Table == "recordings" {
		a.recs = nil
	}
	if all || ch.Table == "pages" || ch.Table == "page_contents" {
		reread(&a.pages, a.st.Pages)
		reread(&a.templates, a.st.Templates)
		maps.DeleteFunc(a.drafts, func(id string, _ *pageDrafts) bool { return id != agents.WorkspaceID && a.pageByID(id) == nil })
		a.todosStale = true
		if a.page != nil {
			if p := a.pageByID(a.page.id); p == nil {
				if _, err := a.st.Page(a.page.id); err == nil && !a.flush() {
					if err := a.st.AddHistory(a.page.id, a.page.editor.Doc(), "autosave", "", "Autosave"); err != nil {
						log.Printf("page %s: %v", a.page.id, err)
					}
				}
				a.discardPage()
				a.route = Route{Kind: "home"}
			} else if ps := a.page; p.Title != ps.savedTitle {
				if ps.title == ps.savedTitle {
					ps.title = p.Title
				}
				ps.savedTitle = p.Title
			}
		}
	}
	if all || ch.Table == "folders" {
		reread(&a.folders, a.st.Folders)
	}
	if all || ch.Table == "key_values" {
		a.goneStarters = nil
	}
	if all || ch.Table == "files" {
		reread(&a.files, a.st.Files)
		clear(a.present)
	}
	if all || ch.Table == "model_providers" {
		if providers, err := a.st.Providers(); err == nil {
			a.providers = providers
			if c, ok := a.settings.Default(); ok && !slices.ContainsFunc(providers, func(p store.Provider) bool { return p.ID == c.ProviderID }) {
				a.settings.DefaultModel = ""
			}
		}
	}
	if all || ch.Table == "outbox" {
		a.pending = a.st.PendingCount()
	}
	if all || ch.Table == "conflicts" || ch.Table == "pages" {
		reread(&a.conflicts, a.st.Conflicts)
	}
	if all || ch.Table == "session" {
		a.session = a.st.Session()
		a.leasePub()
	}
	if a.page != nil && (all || ch.Table == "comments" && (ch.PageID == "" || ch.PageID == a.page.id)) {
		a.page.setThreads(a.st.Threads(a.page.id))
	}
	if a.page != nil && (all || ch.Table == "page_members") {
		reread(&a.page.members, func() ([]store.Member, error) { return a.st.Members(a.page.id) })
	}
	if all || ch.Table == "page_members" || ch.Table == "pages" || ch.Table == "session" {
		a.pageEditable()
	}
	a.pubChanged(ch)
	if a.win != nil {
		a.win.Invalidate()
	}
}

type listsFrom struct {
	pages, templates   *store.Page
	files              *store.File
	nPages, nTemplates int
	nFiles             int
}

func (a *App) pageByID(id string) *store.Page {
	from := listsFrom{pages: firstOf(a.pages), templates: firstOf(a.templates), nPages: len(a.pages), nTemplates: len(a.templates)}
	if a.pageIndex == nil || from != a.pageIndexFrom {
		a.pageIndex, a.pageIndexFrom = make(map[string]*store.Page, len(a.pages)+len(a.templates)), from
		for _, list := range [][]store.Page{a.pages, a.templates} {
			for i := range list {
				if _, ok := a.pageIndex[list[i].ID]; !ok {
					a.pageIndex[list[i].ID] = &list[i]
				}
			}
		}
	}
	return a.pageIndex[id]
}

type arrangedFrom struct {
	pages, templates   *store.Page
	nPages, nTemplates int
	folders            *store.Folder
	nFolders           int
	space              string
}

func (a *App) library() *library.Tree {
	from := arrangedFrom{firstOf(a.pages), firstOf(a.templates), len(a.pages), len(a.templates), firstOf(a.folders), len(a.folders), a.session.SpaceID}
	if a.lib == nil || from != a.libFrom {
		a.lib, a.libFrom = library.Arrange(a.session.SpaceID, slices.Concat(a.pages, a.templates), a.folders), from
	}
	return a.lib
}

func (a *App) children(id string) []store.Page { return a.library().Children[id] }

func firstOf[T any](list []T) *T {
	if len(list) == 0 {
		return nil
	}
	return &list[0]
}

func (a *App) isShared(p store.Page) bool { return p.SpaceID != a.session.SpaceID }

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

func (a *App) isUnder(id, other string) bool {
	return id == other || slices.ContainsFunc(a.ancestors(id), func(p store.Page) bool { return p.ID == other })
}

func (r Route) path() string {
	switch r.Kind {
	case "page":
		return "/page/" + r.ID
	case "signin":
		return "/" + r.Kind
	}
	return "/"
}

func routeAt(path string) Route {
	kind, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	switch kind {
	case "page":
		return Route{Kind: "page", ID: rest}
	case "signin":
		return Route{Kind: kind}
	}
	return Route{Kind: "home"}
}

func (a *App) routeTitle() string {
	switch a.route.Kind {
	case "page":
		return a.pageTitleOf(a.route.ID)
	case "signin":
		return locales.CommonSignIn()
	}
	return "Leaflet"
}

func (a *App) openSettings(section string) {
	a.flush()
	if section == "" {
		section = "account"
	}
	a.settingsSection, a.settingsOpen = section, true
}

func (a *App) toggleSettings() {
	if a.settingsOpen {
		a.settingsOpen = false
		return
	}
	a.openSettings("account")
}

func (a *App) navigate(r Route) {
	a.settingsOpen = false
	if r == a.route {
		return
	}
	if a.show(r) {
		a.follow()
	}
}

func (a *App) show(r Route) bool {
	if !a.closePage() {
		return false
	}
	a.route = r
	if r.Kind == "page" {
		a.loadPage(r.ID)
	}
	return true
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
		if r != a.route && !a.show(r) {
			a.router.Push(a.route.path())
			a.routerAt = a.router.Path()
		}
		return
	}
	if p := a.route.path(); p != at {
		a.router.Push(p)
		a.routerAt = a.router.Path()
	}
}

func (a *App) openPage(id string) {
	if !a.pageExists(id) {
		a.toast(locales.EditorPageNotFound())
		return
	}
	a.closePreview()
	if a.route.Kind == "page" && a.route.ID == id {
		a.settingsOpen = false
		return
	}
	for _, p := range a.ancestors(id) {
		a.treeOpen("page", p.ID)
	}
	top := a.pageByID(id)
	if anc := a.ancestors(id); len(anc) > 0 {
		top = &anc[0]
	}
	if top != nil && top.FolderID != "" {
		a.openFolders(top.FolderID)
	}
	if a.win == nil {
		if a.closePage() {
			a.route = Route{Kind: "page", ID: id}
			a.loadPage(id)
		}
		return
	}
	a.navigate(Route{Kind: "page", ID: id})
}

// pageExists reports whether there is a page to open, reading the pages
// again for one made since they were last read.
func (a *App) pageExists(id string) bool {
	if a.pageByID(id) != nil {
		return true
	}
	if p, err := a.st.Page(id); err != nil || !p.DeletedAt.IsZero() {
		return false
	}
	reread(&a.pages, a.st.Pages)
	reread(&a.templates, a.st.Templates)
	return a.pageByID(id) != nil
}

func (a *App) loadPage(id string) {
	if !a.closePage() {
		return
	}
	p := a.pageByID(id)
	if p == nil {
		a.route = Route{Kind: "home"}
		return
	}
	lp, content := live.Open(a.st, id, live.Options{
		Post:    a.update,
		CanEdit: func() bool { return a.canEdit(id) && !a.leaving },
		Notify:  a.toast,
		Saved:   func() { a.sync.KickPush() },
	})
	ps := &pageState{id: id, title: p.Title, savedTitle: p.Title, live: lp, tinted: map[string]bool{}}
	ed := editor.New(content, &host{a: a})
	ed.Size = float32(a.settings.TextSize)
	ed.Tinted = ps.tinted
	ed.OnChange = func() {
		a.scheduleSave()
		a.viewChanged()
	}
	ed.OnSelect = a.viewChanged
	ps.editor = ed
	lp.Attach(pageEditor{ed})
	ps.setThreads(a.st.Threads(id))
	ps.members, _ = a.st.Members(id)
	a.page = ps
	a.pageEditable()
	a.sync.SetOpen(id, true)
	_ = a.st.SetValue(store.LastPageKey, id)
	a.viewChanged()
	if a.backend.SignedIn() {
		go a.loadMembers(id)
	}
}

func (a *App) closePage() bool {
	if a.page == nil {
		return true
	}
	if !a.flush() {
		return false
	}
	a.discardPage()
	return true
}

func (a *App) discardPage() {
	id := a.page.id
	a.page.live.Close()
	if t := a.page.titleTimer; t != nil {
		t.Stop()
	}
	a.page = nil
	a.viewChanged()
	a.pullStops(id)
	d := &a.dialogs
	d.newComment, d.inline = nil, nil
	d.chartOpen, d.chartEditor = false, nil
	d.embedOpen, d.linkOpen, d.sourceOpen = false, false, false
	d.recOpen, d.notesOpen, d.speakerOpen = false, false, false
	a.fileToTranscribe = ""
	a.dropEmbeds()
	a.dropVideos()
	a.dropPlayers()
}

func (a *App) pageEditable() {
	if a.page != nil {
		a.page.live.SetReadOnly(!a.canEdit(a.page.id) || a.page.live.Unreadable() || a.leaving)
	}
}

func (a *App) canEdit(id string) bool {
	a.editableMu.Lock()
	v, ok := a.editable[id]
	a.editableMu.Unlock()
	if ok {
		return v
	}
	p := a.pageByID(id)
	v = p != nil && a.st.CanEdit(*p)
	a.editableMu.Lock()
	if a.editable == nil {
		a.editable = map[string]bool{}
	}
	a.editable[id] = v
	a.editableMu.Unlock()
	return v
}

func (a *App) ownsPage(id string) bool {
	p := a.pageByID(id)
	return p != nil && !a.isShared(*p)
}

func (a *App) scheduleSave() {
	if a.page != nil {
		a.page.live.Edited()
	}
}

func (a *App) savePage() bool {
	if a.page == nil {
		return true
	}
	return a.page.live.Save()
}

func (a *App) flush() bool {
	if a.page == nil {
		return true
	}
	title := true
	if a.page.title != "" || a.pageByID(a.page.id) != nil {
		title = a.saveTitle()
	}
	return a.savePage() && title
}

func (a *App) saveTitle() bool {
	ps := a.page
	if ps == nil {
		return true
	}
	p := a.pageByID(ps.id)
	if p == nil || p.Title != ps.title {
		// The pages listed may be a change behind the one stored.
		q, err := a.st.Page(ps.id)
		if err != nil {
			return true
		}
		p = &q
	}
	switch {
	case p.Title == ps.title:
		ps.savedTitle = ps.title
	case ps.title == ps.savedTitle:
		// Renamed elsewhere, not here: the stored title is the newer one.
		ps.title, ps.savedTitle = p.Title, p.Title
	case a.st.CanEdit(*p):
		if err := a.st.RenamePage(ps.id, ps.title); err != nil {
			a.toast(locales.AppErrorRename(err.Error()))
			return false
		}
		ps.savedTitle = ps.title
	}
	return true
}

func (a *App) externalContent(pageID string) {
	if a.page != nil && a.page.id == pageID {
		a.page.live.External()
	}
}

type pageEditor struct{ *editor.Editor }

func (e pageEditor) SetReadOnly(readOnly bool) { e.ReadOnly = readOnly }

func (a *App) outsideChange() {
	a.reload(store.Change{Table: "*"})
	if a.page != nil {
		a.externalContent(a.page.id)
	}
	a.sync.Kick()
}

func (a *App) toast(text string) {
	a.toasts = append(a.toasts, toast{text: text})
	a.holdRunBar(4 * time.Second)
	if a.win != nil {
		a.win.Invalidate()
	}
}

const actionToastTime = 8 * time.Second

func (a *App) toastAction(text, action string, fn func()) {
	a.toasts = append(a.toasts, toast{text: text, action: action, fn: fn})
	a.holdRunBar(actionToastTime)
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) holdRunBar(d time.Duration) {
	if until := time.Now().Add(d); until.After(a.toastUntil) {
		a.toastUntil = until
		a.later(d+50*time.Millisecond, func() {})
	}
}

func (a *App) newPage(parent, folder string) {
	a.newPageFrom(parent, folder, a.defaultTemplate(parent, folder))
}

func (a *App) newPageFrom(parent, folder string, t *templates.Choice) {
	if parent != "" && !a.canEdit(parent) {
		return
	}
	if !a.flush() {
		return
	}
	p, err := a.st.CreatePage(store.NewPageOptions{ParentID: parent, FolderID: folder})
	if err != nil {
		a.toast(locales.AppErrorCreatePage(err.Error()))
		return
	}
	reread(&a.pages, a.st.Pages)
	if parent != "" {
		a.treeOpen("page", parent)
	}
	a.openPage(p.ID)
	if a.page == nil || a.page.id != p.ID {
		if _, err := a.st.DeletePage(p.ID); err != nil {
			log.Printf("page %s: %v", p.ID, err)
		}
		reread(&a.pages, a.st.Pages)
		return
	}
	if parent != "" {
		a.linkSubpage(parent, p.ID, "")
	}
	a.startFrom(t)
}

func (a *App) linkSubpage(parent, id, after string) {
	if err := a.pageCore().LinkSubpage(parent, id, after); err != nil {
		a.toast(locales.AppErrorLinkSubpage(err.Error()))
	}
}

func (a *App) pageCore() core.Pages {
	return core.Pages{Core: a.core(), Open: a.openLive, Notify: a.toast}
}

func (a *App) openLive(id string) *live.Page {
	if a.page != nil && a.page.id == id {
		return a.page.live
	}
	return nil
}

func (a *App) focusTitle(selectAll bool) {
	if a.page != nil {
		a.page.focusTitle, a.page.selectTitle = 3, selectAll
	}
}

func (a *App) deletePage(id string) {
	title := locales.CommonUntitled()
	if p := a.pageByID(id); p != nil {
		title = p.DisplayTitle()
	}
	if a.page != nil && a.isUnder(a.page.id, id) && !a.flush() {
		return
	}
	ids, card, err := a.pageCore().DeletePage(id)
	if err != nil {
		a.toast(locales.AppErrorDelete(err.Error()))
		return
	}
	reread(&a.pages, a.st.Pages)
	if a.route.Kind == "page" {
		for _, d := range ids {
			if d == a.route.ID {
				a.discardPage()
				a.route = Route{Kind: "home"}
			}
		}
	}
	a.toastAction(locales.AppDeleted(title), locales.CommonUndo(), func() {
		if err := a.pageCore().RestorePages(ids, card); err != nil {
			a.toast(locales.AppErrorRestore(err.Error()))
			return
		}
		reread(&a.pages, a.st.Pages)
	})
}

func (a *App) setMenu() { mygo.App.SetMenu(mygo.NewMenu(a.menu(updater.MenuItem()))) }

func (a *App) menu(update *mygo.MenuItem) []*mygo.MenuItem {
	return []*mygo.MenuItem{
		{Label: "Leaflet", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			update,
			mygo.Separator(),
			{Label: locales.AppMenuSettings(), Accelerator: "CmdOrCtrl+,", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.update(a.toggleSettings)
			}},
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
			{Label: locales.AppMenuExport(), Submenu: []*mygo.MenuItem{
				{Label: locales.AppMenuExportMarkdown(), Click: func(*mygo.MenuItem, *mygo.Window) {
					a.update(func() {
						if a.page != nil {
							a.exportMarkdown(a.page.id)
						}
					})
				}},
				{Label: locales.AppMenuExportPDF(), Click: func(*mygo.MenuItem, *mygo.Window) {
					a.update(func() {
						if a.page != nil {
							a.exportPDF(a.page.id)
						}
					})
				}},
			}},
			{Type: mygo.MenuItemSeparator},
			{Role: mygo.RoleClose},
		}},
		{Role: mygo.RoleEditMenu, Submenu: a.editMenu()},
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
	}
}

func (a *App) editMenu() []*mygo.MenuItem {
	mac := runtime.GOOS == "darwin"
	accel := "Shift+CmdOrCtrl+C"
	if mac {
		accel = "Shift+Alt+CmdOrCtrl+C"
	}
	items := []*mygo.MenuItem{
		{Role: mygo.RoleUndo},
		{Role: mygo.RoleRedo},
		mygo.Separator(),
		{Role: mygo.RoleCut},
		{Role: mygo.RoleCopy},
		{Label: locales.EditorCopyPlainText(), Accelerator: accel, Click: func(*mygo.MenuItem, *mygo.Window) {
			a.update(func() {
				if a.page != nil && a.page.editor.Focused() {
					a.page.editor.CopyPlainText()
				}
			})
		}},
		{Role: mygo.RolePaste},
	}
	if !mac {
		return append(items, &mygo.MenuItem{Role: mygo.RoleDelete}, mygo.Separator(), &mygo.MenuItem{Role: mygo.RoleSelectAll})
	}
	return append(items,
		&mygo.MenuItem{Role: mygo.RolePasteAndMatchStyle},
		&mygo.MenuItem{Role: mygo.RoleDelete},
		&mygo.MenuItem{Role: mygo.RoleSelectAll},
		mygo.Separator(),
		&mygo.MenuItem{Label: locales.AppMenuSpeech(), Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleStartSpeaking},
			{Role: mygo.RoleStopSpeaking},
		}},
	)
}

func (a *App) toggleSidebar() {
	a.settings.SidebarOpen = !a.settings.SidebarOpen
	a.saveSettings()
}

func (a *App) togglePanel(tab int) {
	if !a.pageOpen() {
		if tab == 0 {
			a.panelOpen = !a.panelOpen
		}
		return
	}
	if a.panelOpen && a.panelTab == tab {
		a.panelOpen = false
		return
	}
	a.panelOpen, a.panelTab = true, tab
}

func (a *App) pageTitleOf(id string) string {
	if p := a.pageByID(id); p != nil {
		return p.DisplayTitle()
	}
	return ""
}
