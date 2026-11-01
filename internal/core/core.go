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

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/agents"
	"leaflet/internal/icloud"
	"leaflet/internal/keychain"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
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
// run in. It keeps nothing else: a Core made over the same parts and
// hooks is the same Core.
type Core struct {
	Store  *store.Store
	Remote *remote.Client
	Sync   *syncsvc.Service
	Hub    *agents.Hub
	Rec    *recording.Controller

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
	c.Sync.Online, c.Sync.DeviceName = cfg.Online, cfg.DeviceName
	c.Sync.OnState, c.Sync.OnPageContent = cfg.OnSyncState, cfg.OnPageContent
	// Sync asks from its own goroutine: the stored settings, which the
	// app's own copy is not safe to read from.
	c.Sync.Offline = func() bool { return st.Settings().WorkOffline }
	c.Sync.KeepAudio = func() string { return st.Settings().KeepAudioOnDevice }
	c.Rec.Blobs = c.Sync
	c.startICloud(cfg.ICloudRoot)
	// Once it knows whether audio goes up, to the server or iCloud Drive.
	c.Rec.RecoverInterrupted()
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
// iCloud sync is on, the sync loop, and bringing the Supabase project up
// to this version of Leaflet.
func (c *Core) Start() {
	c.watch(c.Sync.ICloudOn())
	c.Sync.Start()
	go c.UpdateProject()
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
