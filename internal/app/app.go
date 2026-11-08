// Package app is Leaflet's desktop app: the window, the shell around pages,
// and the screens and dialogs, in MyGo's native UI.
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

// Route is what the main area shows.
type Route struct {
	Kind string // home | page | signin
	ID   string // the page
}

// App is the state of the app, kept for as long as it runs.
type App struct {
	st  *store.Store
	win *mygo.Window
	// post, in tests that have no window, takes what update would run on
	// the main thread.
	post    func(func())
	backend *remote.Client
	sync    *syncsvc.Service
	hub     *agents.Hub
	rec     *recording.Controller
	// cr is the core over these parts (core), the one Open made, kept for
	// as long as the app runs: what it has under way is its own, as the
	// project brought up to date while sync waits for it.
	cr *core.Core
	// capture is the recordings' microphone, once the window is there.
	capture *webCapture
	// winMu guards win, starting and early for update, which other
	// goroutines call: while start makes the app, before the window is
	// there, what they hand the main thread waits in early.
	winMu    sync.Mutex
	starting bool
	early    []func()

	settings store.Settings
	// endpointKeyMu orders the endpoint keys kept in the keychain, and
	// endpointKeys numbers the last typed: a write overtaken by a later
	// one is left out, so the keychain ends with the last.
	endpointKeyMu sync.Mutex
	endpointKeys  atomic.Int64
	session       store.Session
	online        bool

	// What the views show, reloaded as the store changes.
	pages     []store.Page
	templates []store.Page
	// templateHints are templates' first lines, by ID and edit time.
	templateHints map[string]string
	// goneStarters are the starter templates deleted on this device, by
	// key; nil until read.
	goneStarters map[string]bool
	// todos are the open to-dos of every page, read again when stale;
	// todoState is the sidebar's To-dos tab's list.
	todos      []store.PageTodo
	todosStale bool
	todoState  ui.ListState
	folders    []store.Folder
	files      []store.File
	providers  []store.Provider
	// lib is the library as the sidebar arranges it, and libFrom what it
	// was arranged from (library).
	lib     *library.Tree
	libFrom arrangedFrom
	// pageIndex and fileIndex are the pages and templates, and the files,
	// by ID, as made from what pageIndexFrom and fileIndexFrom say
	// (pageByID, fileByID). present is whether each file of the app's is
	// on this device, as last looked (localFile).
	pageIndex     map[string]*store.Page
	pageIndexFrom listsFrom
	fileIndex     map[string]int
	fileIndexFrom listsFrom
	present       map[string]presence
	// mentions is the @ menu as last made, and filtered (MentionItems).
	mentions mentionMenu
	// editable is canEdit as it was read, by page, until the pages, their
	// members or the session change: the sidebar asks it of every row as
	// it draws, and each ask walks the page's parents in the database.
	// Guarded by editableMu, as an open page asks it as it saves.
	editableMu sync.Mutex
	editable   map[string]bool
	// recs is what recording blocks show of their recordings, read once
	// until the recordings change, as each frame draws them
	// (recordingView); on the main thread only.
	recs map[string]recView
	// recOpen are the recording blocks whose long transcripts show whole,
	// by block ID; on the main thread only.
	recOpen   map[string]bool
	pending   int
	conflicts []store.ConflictRow
	syncing   bool
	syncError string
	syncErr   error
	// icloudStop stops watching the Leaflet folder in iCloud Drive.
	icloudStop context.CancelFunc
	// icloudBusy is set while iCloud sync turns on or off, which the
	// switch waits for: a turn taken after it would finish first.
	icloudBusy bool

	route Route
	// router is MyGo's history of the routes; routerAt is its path the
	// app last followed.
	router   *ui.Router
	routerAt string
	page     *pageState

	// Shell.
	sidebarW float32
	// treeState is the sidebar's tree, its items open and its row chosen,
	// and treeIn its input (treeInput).
	treeState ui.OutlineState[string]
	treeRow   int
	treeIn    treeInput
	// treeRows are the tree's rows built this frame, for drops beside them.
	treeRows []treeRow
	// tagTree is the Tags tab's tree of tags with their pages, tagRow
	// its row chosen, and tagIn its input.
	tagTree   ui.OutlineState[string]
	tagRow    int
	tagIn     treeInput
	panelOpen bool
	panelW    float32
	panelTab  int
	// agentScope is the Agent tab's switch beside a page: 0 its agent, 1
	// the workspace agent's, of all the user's pages.
	agentScope int
	// panelTabsW is how wide the panel's tabs were last frame, which the
	// panel keeps room for.
	panelTabsW float32
	// settingsOpen shows Settings over the window, at settingsSection.
	settingsOpen    bool
	settingsSection string
	toasts          []toast
	// toastUntil is when the last toast goes, for the run bar to wait
	// rather than show under it.
	toastUntil time.Time
	// What Settings' sections and signing in show and have under way:
	// the account, the provider dialog (form, formGen numbering those
	// opened) and the ChatGPT sign-ins read for it (chatgptRead), speech
	// to text, the external agents, and a sign-in by email code or with
	// Supabase (supaRuns numbering those started).
	account     accountState
	form        providerForm
	formGen     int
	chatgptRead map[string]bool
	speech      speechState
	external    externalState
	signIn      signInState
	supa        supabaseState
	supaRuns    int
	// players are the recordings played on the open page, by recording.
	players map[string]*player

	dialogs dialogs
	md      markdown.Options
	// commentMD is how comments show: md's links, with mentions marked.
	commentMD markdown.Options
	images    *imageCache
	// downloads are the files downloading from the server, with what
	// waits for each, and downloadFails when those that failed to last
	// did.
	downloads     map[string][]func(bool)
	downloadFails map[string]time.Time
	// fetchingCopy is set while a copy of a page is being made: what it
	// needs downloading (copyable), then its files and recordings' audio
	// copying (copyPage), one copy at a time. fetchedAudio is the audio
	// downloaded for it, which goes once the copy has its own.
	fetchingCopy bool
	fetchedAudio []string
	// leaving is set while the account's pages are cleared from this
	// device (leave): the open page takes no typing meanwhile.
	leaving bool
	embeds  map[string]*mygo.WebView
	// embedIn is what each embed's web view last loaded its page from,
	// and embedSizes how tall its content said it is, once it has drawn;
	// embedZoomed the diagrams zoomed in; embedToPage the views that give
	// the page the wheel (sandbox's toPage); embedFailed why a view could
	// not be made, which is not tried again while the block stays.
	// embedOver is the embed whose view the pointer was over as the last
	// frame built, and embedWheel the wheel it passed the page since,
	// for the next frame to scroll it by; embedBuilt the embeds the page
	// built since it last let go of views (dropEmbeds).
	embedIn     map[string]embedInput
	embedSizes  map[string]embedSize
	embedZoomed map[string]bool
	embedToPage map[string]bool
	embedFailed map[string]string
	embedOver   string
	embedWheel  float32
	embedBuilt  map[string]bool
	// videos are the players of the open page's videos that were played;
	// videoInfo what each video's site said about it, by provider and ID.
	videos map[string]*mygo.WebView
	// media serves the files players play, and fileMedia is what it serves
	// for a file block's player, by block; fileFailed are the file blocks
	// whose player could not read their file.
	media      mediaserver.Server
	fileMedia  map[string]string
	fileFailed map[string]bool
	// now is the audio that started last, with fileNow where a file's has
	// got to, and fileWatch stops what asks each file's player how it is.
	now       nowPlaying
	fileNow   fileStatus
	fileNowAt time.Time
	fileWatch map[string]chan struct{}
	// followLines says transcripts follow the audio, as read once by
	// followRead; followAt is where that last scrolled the page, or -1, line
	// and lineRow the line being said, and lineMoved that it just changed.
	followLines, followRead bool
	followAt                float32
	// sitePlayers says a video's site's player opened since their data went.
	sitePlayers bool
	// playSpeed is how fast audio plays, 0 until read.
	playSpeed float64
	// systemAudio is what the system's media controls were last told, and
	// systemWatch says a check on them is due, systemHeard that the app
	// listens to them.
	systemAudio              systemAudio
	systemWatch, systemHeard bool
	line                     lineKey
	lineRow                  ui.Handle
	lineMoved                bool
	// rolls are the windows of long transcripts, by block.
	rolls map[string]*roll
	// fileToTranscribe is the file block whose notes a transcript is to
	// replace, while that is asked.
	fileToTranscribe string
	// preview is the file shown in the window, or nil.
	preview   *filePreview
	videoInfo webAnswers[videoInfo]
	// notesRead are videos' notes read in lines, by their text, and
	// notesOpen the video blocks whose long notes show whole, by block ID;
	// on the main thread only.
	notesRead map[string]readNotes
	notesOpen map[string]bool
	// linkInfo is what each link card's page said about itself, by link.
	linkInfo webAnswers[web.LinkInfo]
	// drafts are what was typed on each page and not sent yet, kept as it
	// closes and opens again (draftsOf); those of a page gone go as the
	// pages are read again.
	drafts map[string]*pageDrafts
	// widthsTimer keeps the sidebar's and the panel's widths once they
	// stop moving (saveWidthsSoon).
	widthsTimer *time.Timer

	// The menu bar icon, and the menu it last showed.
	tray      *mygo.Tray
	trayShown trayState
	trayBuilt bool
	// updatable is set when the app can update itself; updateBusy names
	// the check under way, or the download after it, for the icon beside
	// Settings; updateReady is the version installed in the background,
	// which runs once the app starts again; updateFailed is the last one
	// that would not install, which the app says once.
	updatable    bool
	updateBusy   string
	updateReady  string
	updateFailed string
	// quitting is set as a quit starts, when closing the window closes it
	// rather than hiding it to the menu bar.
	quitting bool
	// openView is the open page and selection as last published, and
	// viewTimer publishes them shortly after they change (viewChanged).
	openView  store.OpenView
	viewTimer *time.Timer

	// fileToDelete is the file whose deleting the sidebar asks about.
	fileToDelete *store.File

	// pub is the page shared through a public link, if one is, and
	// cloudflared what the app found of the tool that opens it.
	pub         *pubLink
	cloudflared cloudflaredInfo
	// pubPage is pub's page, for agent runs to read off the main thread:
	// a page anyone may read through a link keeps agents to itself. served
	// says the link still serves it (pubLink.serving), for sync to check
	// it as it changes elsewhere (watched).
	pubPage struct {
		sync.Mutex
		id     string
		served bool
	}
}

// setPub makes p the public link (nil for none).
func (a *App) setPub(p *pubLink) {
	a.pub = p
	a.pubPage.Lock()
	a.pubPage.id, a.pubPage.served = "", false
	if p != nil {
		a.pubPage.id, a.pubPage.served = p.page, p.serving()
	}
	a.pubPage.Unlock()
}

// publiclyLinked reports whether a page is shared through a public link,
// one still starting included; it may be called from any goroutine.
func (a *App) publiclyLinked(pageID string) bool {
	a.pubPage.Lock()
	defer a.pubPage.Unlock()
	return a.pubPage.id != "" && a.pubPage.id == pageID
}

// publiclyServed reports whether a public link serves a page, or will once
// live, not one that failed; it may be called from any goroutine.
func (a *App) publiclyServed(pageID string) bool {
	a.pubPage.Lock()
	defer a.pubPage.Unlock()
	return a.pubPage.served && a.pubPage.id == pageID
}

// publicPages are the pages with a public link open now: the one, or none.
func (a *App) publicPages() []string {
	a.pubPage.Lock()
	defer a.pubPage.Unlock()
	if a.pubPage.id == "" {
		return nil
	}
	return []string{a.pubPage.id}
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
	// savedTitle is the title as stored when title last was: a title that
	// differs from it was typed and is not saved yet, which a reload keeps.
	savedTitle string
	// focusTitle asks the title to take the focus, for this many more
	// frames until it has it: a native menu closing, or the page it
	// replaces, can take it back in the frames between.
	focusTitle int
	// selectTitle selects the title as it takes the focus, to rename.
	selectTitle bool
	// titleTimer renames the page shortly after the title's typing stops
	// (saveTitleSoon).
	titleTimer *time.Timer
	// titleAtEnd is whether the title's caret was at its end in the last
	// frame; titleLeave asks the caret to move from the title to the page.
	titleAtEnd, titleLeave bool
	// tags are the page's tags while tagsEditing shows them in a token
	// field, which tagsFocused says has had the focus, to close as it
	// loses it.
	tags                     []string
	tagsEditing, tagsFocused bool
	// live is the page as it is open: its edits saved shortly after the
	// typing stops, and what is stored from outside merged into them.
	live    *live.Page
	threads []store.Thread
	// open is how many open threads each block has, as its badge shows,
	// counted once as the threads are read (setThreads), not as each
	// block draws.
	open    map[string]int
	members []store.Member
	// tinted are the blocks the page shows lit: those an agent run
	// changed, and lit those of them goToBlock lit for a moment, since
	// the frame they first showed lit in (zero until then, dimLit).
	tinted map[string]bool
	lit    map[string]time.Time
	// caretSet is set once the caret has been in the page since it opened:
	// a chat asks about where it is then, from the composer too.
	caretSet bool
	// scroll is where the page is scrolled to, and view its scroll area,
	// whose box in the window its Bounds gives. scrollTo is a block to
	// bring to the top of it once the page has laid it out, scrollWait the
	// frames it has waited for that. held is the block brought there,
	// kept there until heldUntil while what is above it loads, unless the
	// page scrolls from heldAt, where it was put.
	scroll     ui.ScrollState
	view       ui.Handle
	scrollTo   string
	scrollWait int
	held       string
	heldUntil  time.Time
	heldAt     float32
	// fullDiagram is the diagram block filling the page's body, if one
	// is.
	fullDiagram string
	// outline is the Outline tab's list of headings, outlineRow its row
	// chosen, outlineSeen the headings it has opened once, and outlineKeys
	// whether it had the keyboard as it last built.
	outline     ui.OutlineState[string]
	outlineRow  int
	outlineSeen map[string]bool
	outlineKeys bool
}

// Run starts the app, unless it runs already: then the one running shows
// its window, and this one ends.
func Run() {
	a := &App{embeds: map[string]*mygo.WebView{}, embedIn: map[string]embedInput{}, embedSizes: map[string]embedSize{},
		embedZoomed: map[string]bool{}, embedToPage: map[string]bool{}, embedFailed: map[string]string{}, videos: map[string]*mygo.WebView{}}
	if a.firstInstance() {
		launch(a)
	}
}

// singleInstance takes the app's lock, which its identifier and name key,
// so a release and Leaflet Debug run side by side; it is false in a second
// instance, once that has told the first. The lock is the window app's
// alone: the command line and leaflet mcp, the same executable, end up in
// cli.Main and never here, and run beside it.
var singleInstance = func() bool { return mygo.App.RequestSingleInstanceLock() }

// launch runs the app; tests stand in for it.
var launch = (*App).launch

// firstInstance reports whether this process is the app on its data
// folder. A second one would settle the recording under way as cut off
// (Controller.RecoverInterrupted) and sync beside the first, and Windows
// starts one for each launch from the Start menu, while the window hides
// in the tray: macOS brings the running one back (OnActivate). A copy
// pointed at a data folder of its own shares nothing with the one running,
// and starts beside it.
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

// launch sets up the app and runs it until it quits.
func (a *App) launch() {
	theme.RegisterFonts()
	registerCapture()
	mygo.BindAs("Embeds", &embedScroll{a: a})
	// MyGo's updater, with its window for "Check for Updates…"; the checks
	// as the app starts and runs are checkForUpdate's, which installs quietly.
	mygo.Use(native.New(updater.Options{DisableAutomaticChecks: true}))
	// Before Run, to hear the click on a reminder that launched the app.
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
	// Links in agents' replies and previews: a page link opens the page
	// here, a web link the browser.
	a.md.OnLink = func(url string) {
		if id, ok := doc.PageLinkID(url); ok {
			at, moment := doc.LinkMoment(url)
			if !moment {
				at = -1
			}
			a.update(func() {
				// With no page open, a link is in the workspace agent's
				// chat, the panel's alone, which the page opens beside.
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
	a.hub.PublicLink, a.hub.PublicPages = a.publiclyLinked, a.publicPages
	a.online = mygo.Network.Status().Online
	mygo.Network.OnChanged(func(s mygo.NetworkStatus) {
		a.update(func() {
			a.online = s.Online
			if s.Online {
				a.sync.Kick()
			}
		})
	})
	// Agents outside the app (leaflet mcp) write to the database too.
	go st.WatchOthers(time.Second, nil, func() { a.update(a.outsideChange) })
	a.reloadAll()
	a.route = Route{Kind: "home"}
	// What a crash left of the view is no longer so.
	_ = st.SetOpenView(nil)
	if last, ok := st.Value(store.LastPageKey); ok && a.pageByID(last) != nil {
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
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:         "Leaflet",
		Width:         1240,
		Height:        820,
		MinWidth:      windowMinW,
		MinHeight:     480,
		StateKey:      "main",
		TitleBarStyle: mygo.TitleBarHidden,
		// Centred in the header rows, in line with their buttons: the
		// traffic lights on macOS, the window buttons on Windows and Linux.
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
	a.win.OnClose(func(e *mygo.CloseEvent) {
		saved := a.flush()
		// With the menu bar icon, closing the window hides it, and the
		// icon or the Dock brings it back.
		if a.tray != nil && !a.quitting {
			e.PreventDefault()
			a.win.Hide()
			return
		}
		// Edits that could not be saved keep the window open, with them,
		// to try again; the save said why.
		if !saved {
			e.PreventDefault()
			a.quitting = false
			return
		}
		// The recording's capture page closes with the window: the
		// recording ends, keeping what it has. So does a public link.
		a.rec.Stop()
		a.stopPub()
		a.endView()
	})
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		// As closing the window: edits not saved stop the quit.
		if !a.flush() {
			e.PreventDefault()
			a.showWindow()
			return
		}
		a.quitting = true
		a.stopPub()
		a.endView()
	})
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
	a.core().Start()
	return nil
}

// watched reports whether someone sees page id as it changes elsewhere,
// as sync asks from its loop, off the main thread, to check it every few
// seconds for what changed on the server: a public link serves it, or the
// window, which shows the one page open, is on screen, neither hidden, as
// closing it with the menu bar icon does, nor minimized, nor hidden with
// the app (macOS). The window and the app say how they are, rather than
// their events, as the app hides on macOS without its window's.
func (a *App) watched(id string) bool {
	if a.publiclyServed(id) {
		return true
	}
	a.winMu.Lock()
	win := a.win
	a.winMu.Unlock()
	return win != nil && win.IsVisible() && !win.IsMinimized() && !mygo.App.IsHidden()
}

// core is the app's parts as the core wires them, with the app's hooks:
// the one core the app keeps (cr), asked for on the main thread. Tests,
// which make an App of their own parts, get one made over them, and
// another once they change a part.
func (a *App) core() *core.Core {
	if c := a.cr; c != nil && c.Store == a.st && c.Remote == a.backend && c.Sync == a.sync && c.Hub == a.hub && c.Rec == a.rec {
		return c
	}
	return a.keepCore(&core.Core{Store: a.st, Remote: a.backend, Sync: a.sync, Hub: a.hub, Rec: a.rec})
}

// keepCore makes c the app's core, with the app's hooks, and returns it.
func (a *App) keepCore(c *core.Core) *core.Core {
	c.OpenURL = func(u string) { _ = mygo.Shell.OpenExternal(u) }
	c.WatchICloud = a.watchICloud
	// On the main thread, the settings as the app shows them.
	c.Settings = func() store.Settings { return a.settings }
	c.SaveSettings = func(s store.Settings) { a.settings = s; a.saveSettings() }
	a.cr = c
	return c
}

// headless reports whether the app has no window, nor one on its way, nor
// a post standing in for one, as in tests: what would go to the main
// thread then runs at once, on the goroutine that asks, for none other
// to run beside the test's.
func (a *App) headless() bool {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	return a.win == nil && a.post == nil && !a.starting
}

// offMain runs work off the main thread, as a copy of files that takes a
// while, then then on it. Headless, both run at once, as an update then
// does.
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

// later runs fn on the main thread once d has passed, as a save once the
// typing stops, or a frame once a wait is over; stopping the timer it
// returns keeps fn from running. Headless, fn runs at once and there is
// no timer, as offMain's work runs at once.
func (a *App) later(d time.Duration, fn func()) *time.Timer {
	if a.headless() {
		fn()
		return nil
	}
	return time.AfterFunc(d, func() { a.update(fn) })
}

// update runs fn on the main thread and redraws; while the app starts,
// once its window is there.
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

// setThreads keeps the page's threads as read, and how many of them are
// open on each block.
func (ps *pageState) setThreads(ts []store.Thread, _ error) {
	ps.threads, ps.open = ts, map[string]int{}
	for _, t := range ts {
		if !t.Root.Resolved {
			ps.open[t.Root.BlockID]++
		}
	}
}

// reloadAll reads everything the views show.
func (a *App) reloadAll() {
	for _, t := range []string{"pages", "folders", "files", "model_providers", "outbox", "conflicts", "session"} {
		a.reload(store.Change{Table: t})
	}
}

// reread sets *list to what read returns. A read that failed leaves the
// list as it was, not empty: an empty one would show no pages, draw the
// open page as Home, or stop what is shared.
func reread[T any](list *[]T, read func() ([]T, error)) {
	l, err := read()
	if err != nil {
		log.Printf("read: %v", err)
		return
	}
	*list = l
}

// reload rereads what a change touched.
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
		// What was typed on a page gone, and not sent, goes with it.
		// The workspace agent's draft is of no page, and stays.
		maps.DeleteFunc(a.drafts, func(id string, _ *pageDrafts) bool { return id != agents.WorkspaceID && a.pageByID(id) == nil })
		a.todosStale = true
		if a.page != nil {
			if p := a.pageByID(a.page.id); p == nil {
				// The page went, deleted elsewhere: what was typed into it
				// is saved first, for restoring it to bring back, or, when
				// it cannot be saved, kept in its history, as edits to
				// content that can't be read are. A page removed from this
				// device, its share withdrawn, has nothing to restore.
				if _, err := a.st.Page(a.page.id); err == nil && !a.flush() {
					if err := a.st.AddHistory(a.page.id, a.page.editor.Doc(), "autosave", "", "Autosave"); err != nil {
						log.Printf("page %s: %v", a.page.id, err)
					}
				}
				a.discardPage()
				a.route = Route{Kind: "home"}
			} else if ps := a.page; p.Title != ps.savedTitle {
				// A title typed and not saved yet stays, to be saved over
				// the new one; with nothing typed, the new one shows, in
				// the title's field with the caret too.
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
		// A file downloaded, added or gone is looked for again.
		clear(a.present)
	}
	if all || ch.Table == "model_providers" {
		// A default whose provider is gone, removed on another device, is
		// no default, as the store reads it; a read that failed says
		// nothing of it.
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
	// Whether the page can be edited follows its members, the pages it is
	// under, which may share it, and whose the session is.
	if all || ch.Table == "page_members" || ch.Table == "pages" || ch.Table == "session" {
		a.pageEditable()
	}
	a.pubChanged(ch)
	if a.win != nil {
		a.win.Invalidate()
	}
}

// listsFrom is what an index of the pages and the templates, or of the
// files, was made from: each list as read, by its first, which a new read
// makes another.
type listsFrom struct {
	pages, templates   *store.Page
	files              *store.File
	nPages, nTemplates int
	nFiles             int
}

// pageByID finds a page, or a template, which opens as one: in an index of
// them, made again once the pages or the templates are others than it was
// made from, as every subpage card and tree row asks as it draws.
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

// arrangedFrom is what the library was arranged from: the pages, the
// templates and the folders, each as read by its first, which a new read
// makes another, and the user's space.
type arrangedFrom struct {
	pages, templates   *store.Page
	nPages, nTemplates int
	folders            *store.Folder
	nFolders           int
	space              string
}

// library is the library as the sidebar arranges it (library.Arrange),
// arranged again once the pages, the templates, the folders or the
// user's space are others than it was arranged from.
func (a *App) library() *library.Tree {
	from := arrangedFrom{firstOf(a.pages), firstOf(a.templates), len(a.pages), len(a.templates), firstOf(a.folders), len(a.folders), a.session.SpaceID}
	if a.lib == nil || from != a.libFrom {
		a.lib, a.libFrom = library.Arrange(a.session.SpaceID, slices.Concat(a.pages, a.templates), a.folders), from
	}
	return a.lib
}

// children are the pages under a page, in order.
func (a *App) children(id string) []store.Page { return a.library().Children[id] }

// firstOf is the first of a list, or nil for none.
func firstOf[T any](list []T) *T {
	if len(list) == 0 {
		return nil
	}
	return &list[0]
}

// isShared reports whether a page belongs to someone else's space.
func (a *App) isShared(p store.Page) bool { return p.SpaceID != a.session.SpaceID }

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

// isUnder reports whether a page is another or among its subpages.
func (a *App) isUnder(id, other string) bool {
	return id == other || slices.ContainsFunc(a.ancestors(id), func(p store.Page) bool { return p.ID == other })
}

// Navigation is MyGo's Router: its history of the routes' paths, which
// Back and Forward, their keys and buttons go through.

// path is a route's path in the router.
func (r Route) path() string {
	switch r.Kind {
	case "page":
		return "/page/" + r.ID
	case "signin":
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
	case "signin":
		return Route{Kind: kind}
	}
	return Route{Kind: "home"}
}

// routeTitle names the route shown, as Back's list of pages does.
func (a *App) routeTitle() string {
	switch a.route.Kind {
	case "page":
		return a.pageTitleOf(a.route.ID)
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

// toggleSettings opens Settings at Account, or closes it, as the app
// menu's Settings… does, at ⌘,.
func (a *App) toggleSettings() {
	if a.settingsOpen {
		a.settingsOpen = false
		return
	}
	a.openSettings("account")
}

// navigate shows a route, after the router's current one. Settings, over
// the window, closes for it, as for the sign-in page or a page it opens.
func (a *App) navigate(r Route) {
	a.settingsOpen = false
	if r == a.route {
		return
	}
	if a.show(r) {
		a.follow()
	}
}

// show shows a route once the open page closes: it reports false, still
// at the page, when the page's edits could not be saved.
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
		if r != a.route && !a.show(r) {
			// The page stays, with the edits it could not save: the
			// router comes back to it.
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

// openPage shows a page.
func (a *App) openPage(id string) {
	a.closePreview()
	if a.route.Kind == "page" && a.route.ID == id {
		// Open already, as from a conflict in Settings: it shows.
		a.settingsOpen = false
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
		if a.closePage() {
			a.route = Route{Kind: "page", ID: id}
			a.loadPage(id)
		}
		return
	}
	a.navigate(Route{Kind: "page", ID: id})
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

// closePage closes the open page once its edits are saved. It reports
// false, and the page stays open with them, when they could not be.
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

// discardPage closes the open page, saved or not. What was started on it,
// a comment or a prompt not sent yet and the dialogs changing its blocks,
// goes with it: they would act on the page opened next.
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
	a.dropEmbeds()
	a.dropVideos()
	a.dropPlayers()
}

// pageEditable makes the open page read-only or not: read-only when the
// user may not change it, its content can't be read, or the account's
// pages are being cleared from this device (leave).
func (a *App) pageEditable() {
	if a.page != nil {
		a.page.live.SetReadOnly(!a.canEdit(a.page.id) || a.page.live.Unreadable() || a.leaving)
	}
}

// canEdit reports whether the user may change a page.
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

// ownsPage reports whether a page is in the user's own space, not shared
// with them by someone else.
func (a *App) ownsPage(id string) bool {
	p := a.pageByID(id)
	return p != nil && !a.isShared(*p)
}

// scheduleSave saves the open page shortly after the typing stops.
func (a *App) scheduleSave() {
	if a.page != nil {
		a.page.live.Edited()
	}
}

// savePage stores the open page's content. It reports whether nothing is
// left to save.
func (a *App) savePage() bool {
	if a.page == nil {
		return true
	}
	return a.page.live.Save()
}

// flush saves what is waiting to be saved. It reports whether the open
// page's edits, its title's too, are all stored.
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

// saveTitle stores the open page's title. It reports whether nothing is
// left to save.
func (a *App) saveTitle() bool {
	ps := a.page
	if ps == nil {
		return true
	}
	p := a.pageByID(ps.id)
	if p == nil {
		// Deleted elsewhere as it was typed: the title is kept for
		// restoring the page to bring back.
		if q, err := a.st.Page(ps.id); err == nil {
			p = &q
		}
	}
	if p != nil && p.Title != ps.title && a.st.CanEdit(*p) {
		if err := a.st.RenamePage(ps.id, ps.title); err != nil {
			a.toast(locales.AppErrorRename(err.Error()))
			return false
		}
		ps.savedTitle = ps.title
	} else if p != nil && p.Title == ps.title {
		ps.savedTitle = ps.title
	}
	return true
}

// externalContent brings content stored from outside the editor (sync,
// agent runs on the server, restores) into the open page, merging what is
// being typed.
func (a *App) externalContent(pageID string) {
	if a.page != nil && a.page.id == pageID {
		a.page.live.External()
	}
}

// pageEditor is the editor as the open page's live.Editor.
type pageEditor struct{ *editor.Editor }

func (e pageEditor) SetReadOnly(readOnly bool) { e.ReadOnly = readOnly }

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

// actionToastTime is how long a toast with an action shows, as MyGo's
// ToastAction shows one.
const actionToastTime = 8 * time.Second

func (a *App) toastAction(text, action string, fn func()) {
	a.toasts = append(a.toasts, toast{text: text, action: action, fn: fn})
	a.holdRunBar(actionToastTime)
	if a.win != nil {
		a.win.Invalidate()
	}
}

// holdRunBar keeps the run bar away while a toast shows for d, as both sit
// at the bottom of the page, and brings it back after.
func (a *App) holdRunBar(d time.Duration) {
	if until := time.Now().Add(d); until.After(a.toastUntil) {
		a.toastUntil = until
		a.later(d+50*time.Millisecond, func() {})
	}
}

// newPage creates a page and opens it: blank, or from the template set for
// new pages where it goes.
func (a *App) newPage(parent, folder string) {
	a.newPageFrom(parent, folder, a.defaultTemplate(parent, folder))
}

// newPageFrom creates a page from a template, or blank for nil, and opens
// it.
func (a *App) newPageFrom(parent, folder string, t *templates.Choice) {
	// A subpage changes its parent, which a page shared to view cannot be.
	if parent != "" && !a.canEdit(parent) {
		return
	}
	// The open page saves first: one whose edits can't be saved stays
	// open, and no page is made that could not take its place.
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
		// It did not open after all: the template is not put in the
		// page still open, and the new page is not left behind blank.
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

// linkSubpage puts a link to subpage id at the end of page parent, as
// / › Subpage does where the caret is, so a page made from the sidebar
// shows in the page it is inside. A copy goes after the link to the page
// it copies, after, when parent has one.
func (a *App) linkSubpage(parent, id, after string) {
	if err := a.pageCore().LinkSubpage(parent, id, after); err != nil {
		a.toast(locales.AppErrorLinkSubpage(err.Error()))
	}
}

// pageCore is the core as it changes pages: the open page through its
// editor, as a change from outside it, not an undo step. A subpage's link
// comes and goes as the subpage moves, is deleted or comes back: undoing
// in the page would take a link from a page still inside, or bring back
// one to a page gone or elsewhere, apart from the toast's Undo.
func (a *App) pageCore() core.Pages {
	return core.Pages{Core: a.core(), Open: a.openLive, Notify: a.toast}
}

// openLive returns page id's live page while it is open.
func (a *App) openLive(id string) *live.Page {
	if a.page != nil && a.page.id == id {
		return a.page.live
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

// deletePage deletes a page, and its link in the page it is in, and
// offers to undo, which puts the link back where it was.
func (a *App) deletePage(id string) {
	title := locales.CommonUntitled()
	if p := a.pageByID(id); p != nil {
		title = p.DisplayTitle()
	}
	// The open page goes, itself or under the page, with its edits saved,
	// for Undo to bring them back.
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

// menu is the menu bar, with update, the updater's item. Its accelerators
// are the app's shortcuts: the menu takes them before the window does.
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

// editMenu is MyGo's Edit menu with Copy as Plain Text after Copy: ⌥⇧⌘C
// on macOS, beside Paste and Match Style's ⌥⇧⌘V, and Ctrl+Shift+C
// elsewhere, beside the Ctrl+Shift+V that pastes plain text.
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
	// With no page open the panel is the workspace agent's chat alone,
	// whatever tab was last shown: the agent's shortcut shows or hides it,
	// and a page's other tabs have nothing to show.
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

// pageTitleOf is a page's title for display.
func (a *App) pageTitleOf(id string) string {
	if p := a.pageByID(id); p != nil {
		return p.DisplayTitle()
	}
	return ""
}
