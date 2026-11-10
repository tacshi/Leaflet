// Package core opens Leaflet's parts and wires them together, the same for
// the desktop app and the iOS app: the store, the server and the session
// kept from before, sync through it or iCloud Drive, the agents, recording
// and the welcome page; and signing in and out. What differs between them
// comes in through Platform.
package core

import (
	"bytes"
	"errors"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/agents"
	"leaflet/internal/guard"
	"leaflet/internal/icloud"
	"leaflet/internal/keychain"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/supabaseapi"
	syncsvc "leaflet/internal/sync"
)

// Platform is what the app on each system gives the core.
type Platform struct {
	// DataDir is the folder of the database.
	DataDir string
	// Secrets keeps the secrets: MyGo's on the desktop, the keychain
	// through SecItem on iOS. Nil keeps what keychain.Use set.
	Secrets keychain.Store
	// Online reports whether the device has a connection; nil counts as
	// online.
	Online func() bool
	// DeviceName names this device in Account › Devices; nil is the
	// desktop's name for it (sync.DeviceName).
	DeviceName func() string
	// OpenURL opens a web page in the browser, as signing in with
	// Supabase does.
	OpenURL func(url string)
	// WatchICloud starts or stops watching the Leaflet folder in iCloud
	// Drive while iCloud sync is on, to have sync run soon after another
	// device writes to it; nil leaves that to the 30-second passes.
	WatchICloud func(on bool)
	// Watched reports whether someone sees an open page as it changes
	// elsewhere, on screen or served to a public link's visitors, for sync
	// to check it every few seconds for what changed on the server. Sync
	// asks from its own goroutine; nil is every open page, as on iOS,
	// where sync stops in the background.
	Watched func(pageID string) bool
	// ICloudRoot is the Leaflet folder in iCloud Drive: "" is the one
	// icloud.Root finds, as on the desktop; the iOS app passes the folder
	// the user picked.
	ICloudRoot string
	// Log takes the lines the core logs; nil leaves the log package's
	// output as it is.
	Log func(line string)
}

// Config is how Open wires the parts.
type Config struct {
	Platform
	// Locale returns the language the app shows, for the strings the
	// store and the welcome page say; nil leaves i18n's own, which MyGo
	// sets on the desktop.
	Locale func() string
	// Agents is the agents' host: the app.
	Agents agents.Host
	// OnSyncState runs as a sync pass starts and ends, with its error.
	OnSyncState func(running bool, err error)
	// OnPageContent runs after a page's stored content changed from sync.
	OnPageContent func(pageID string)
	// OnChange runs after each change the store makes.
	OnChange func(store.Change)
}

// Core is Leaflet's parts, wired together, and the hooks of the app they
// run in. It keeps what it has under way too: the project being brought
// up to date, which sync waits for as Open wired it to, and the Supabase
// account's sign-in for that. An app keeps the Core that Open made for
// as long as it runs: another made over the same parts has none of it.
type Core struct {
	Store  *store.Store
	Remote *remote.Client
	Sync   *syncsvc.Service
	Hub    *agents.Hub
	Rec    *recording.Controller
	// updatingProject is set while the project is first brought up to
	// date as the app starts (Start), as sync reads through Updating.
	updatingProject atomic.Bool
	// startedUpdate is set once Start has first looked at whether the
	// project needs bringing up to date, as a run of the app starts: only
	// then do the changes wait for it. updatingRun is set while a run of
	// tries to bring it up to date is under way (keepProjectUpdated), which
	// a later Start, as the iOS app comes back to the screen, leaves be.
	startedUpdate atomic.Bool
	updatingRun   atomic.Bool
	// mgmt is the Supabase account's sign-in as updateProject or signing
	// in last had it, with its access token, for the next update to use
	// rather than renew the sign-in again; guarded by mgmtMu.
	mgmtMu sync.Mutex
	mgmt   *supabaseapi.Client
	// signInEnded is what ProjectNeedsSignIn last read for the project in
	// use: 1 no, 2 yes, 0 to read again.
	signInEnded atomic.Int32

	// OpenURL and WatchICloud are the Platform's.
	OpenURL     func(url string)
	WatchICloud func(on bool)
	// Settings returns the settings as the app has them, and SaveSettings
	// keeps changed ones, on the thread signing in runs on; nil reads and
	// writes the store's.
	Settings     func() store.Settings
	SaveSettings func(store.Settings)
}

// Open opens the store in cfg.DataDir and wires the parts over it: the
// server and the session kept from before, recording, the agents, sync and
// iCloud sync as the settings have it, and the welcome page of a new space.
// Start starts what runs in the background.
func Open(cfg Config) (*Core, error) {
	if cfg.Secrets != nil {
		keychain.Use(cfg.Secrets)
	}
	if cfg.Locale != nil {
		i18n.SetLocaleFunc(cfg.Locale)
	}
	if cfg.Log != nil {
		log.SetFlags(0)
		log.SetOutput(lineWriter(cfg.Log))
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	c := &Core{Store: st, OpenURL: cfg.OpenURL, WatchICloud: cfg.WatchICloud}
	c.Remote = remote.New(remote.Config(), st)
	var user *store.User
	if c.Remote.Configured() {
		user = c.Remote.RestoreSession()
	}
	_, err = st.LoadSession(user)
	if errors.Is(err, store.ErrUnsynced) {
		// Signed in as another account, and the app quit before the user
		// agreed to lose the changes of the one whose pages this device
		// keeps: they stay, and the other account signs in again to ask.
		c.Remote.SignOut()
		_, err = st.LoadSession(nil)
	}
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	c.Rec = recording.NewController(st, c.Remote)
	c.Hub = agents.NewHub(st, c.Remote, cfg.Agents)
	c.Sync = syncsvc.New(st, c.Remote)
	c.Sync.Online, c.Sync.DeviceName, c.Sync.Watched = cfg.Online, cfg.DeviceName, cfg.Watched
	c.Sync.OnState, c.Sync.OnPageContent = cfg.OnSyncState, cfg.OnPageContent
	c.Sync.Updating = c.updatingProject.Load
	// Signed out, the pages go to the other devices through iCloud Drive,
	// where the devices say which public links they serve.
	c.Hub.ICloudLease, c.Hub.ICloudLinks = c.Sync.ICloudLease, c.Sync.ICloudLinksElsewhere
	// Sync asks from its own goroutine: the stored settings, which the
	// app's own copy is not safe to read from.
	c.Sync.Offline = func() bool { return st.Settings().WorkOffline }
	c.Sync.KeepAudio = func() string { return st.Settings().KeepAudioOnDevice }
	c.Rec.Blobs = c.Sync
	c.startICloud(cfg.ICloudRoot)
	// Once it knows whether audio goes up, to the server or iCloud Drive.
	c.Rec.RecoverInterrupted()
	// Off the start's path: it reads every recording.
	guard.Go(c.Rec.SweepAudio)
	if cfg.OnChange != nil {
		st.Subscribe(cfg.OnChange)
	}
	if err := c.createWelcome(); err != nil {
		log.Printf("welcome page: %v", err)
	}
	return c, nil
}

// Start starts what runs in the background, once the app is ready for
// what it reports: watching the Leaflet folder in iCloud Drive while
// iCloud sync is on, the sync loop, and bringing the Supabase project up to
// this version of Leaflet, while it has not been yet (projectOutOfDate)
// and no run of tries at it is under way. A project up to date is not
// asked again, and its sign-in not renewed, each time the iOS app comes
// back to the screen.
func (c *Core) Start() {
	c.watch(c.Sync.ICloudOn())
	first := c.startedUpdate.CompareAndSwap(false, true)
	if c.projectOutOfDate() && c.updatingRun.CompareAndSwap(false, true) {
		// The changes wait to go up while the project is first brought up
		// to date as the app starts, a try that failed tried again later.
		if first {
			c.updatingProject.Store(true)
		}
		guard.Go(func() {
			defer c.updatingRun.Store(false)
			c.keepProjectUpdated(first)
		})
	}
	c.Sync.Start()
}

// startICloud sets iCloud sync up as the settings have it. Signed in, the
// pages sync through the server instead, and the setting is off, as
// signing in turns it off. With iCloud Drive gone, the passes say so.
func (c *Core) startICloud(root string) {
	if root == "" {
		root = icloud.Root()
	}
	c.Sync.ICloud = icloud.Folder{Root: root, Temp: filepath.Join(c.Store.Dir, "tmp")}
	s := c.Store.Settings()
	if c.Store.Session().SignedIn && s.ICloudSync {
		s.ICloudSync = false
		if err := c.Store.SaveSettings(s); err != nil {
			log.Printf("settings: %v", err)
		}
	}
	c.Sync.SetICloud(s.ICloudSync)
}

// watch starts or stops watching the Leaflet folder in iCloud Drive.
func (c *Core) watch(on bool) {
	if c.WatchICloud != nil {
		c.WatchICloud(on)
	}
}

// settings returns the settings as the app has them.
func (c *Core) settings() store.Settings {
	if c.Settings != nil {
		return c.Settings()
	}
	return c.Store.Settings()
}

// saveSettings keeps changed settings, where the app has them.
func (c *Core) saveSettings(s store.Settings) {
	if c.SaveSettings != nil {
		c.SaveSettings(s)
		return
	}
	if err := c.Store.SaveSettings(s); err != nil {
		log.Printf("settings: %v", err)
	}
}

// lineWriter hands what the log package writes to fn, a line at a time.
type lineWriter func(line string)

func (w lineWriter) Write(p []byte) (int, error) {
	for line := range bytes.Lines(p) {
		w(string(bytes.TrimSuffix(line, []byte("\n"))))
	}
	return len(p), nil
}
