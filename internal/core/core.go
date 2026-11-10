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

type Platform struct {
	DataDir     string
	Secrets     keychain.Store
	Online      func() bool
	DeviceName  func() string
	OpenURL     func(url string)
	WatchICloud func(on bool)
	Watched     func(pageID string) bool
	ICloudRoot  string
	Log         func(line string)
}

type Config struct {
	Platform
	Locale        func() string
	Agents        agents.Host
	OnSyncState   func(running bool, err error)
	OnPageContent func(pageID string)
	OnChange      func(store.Change)
}

type Core struct {
	Store           *store.Store
	Remote          *remote.Client
	Sync            *syncsvc.Service
	Hub             *agents.Hub
	Rec             *recording.Controller
	updatingProject atomic.Bool
	startedUpdate   atomic.Bool
	updatingRun     atomic.Bool
	mgmtMu          sync.Mutex
	mgmt            *supabaseapi.Client
	signInEnded     atomic.Int32

	OpenURL      func(url string)
	WatchICloud  func(on bool)
	Settings     func() store.Settings
	SaveSettings func(store.Settings)
}

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
	c.Hub.ICloudLease, c.Hub.ICloudLinks = c.Sync.ICloudLease, c.Sync.ICloudLinksElsewhere
	c.Sync.Offline = func() bool { return st.Settings().WorkOffline }
	c.Sync.KeepAudio = func() string { return st.Settings().KeepAudioOnDevice }
	c.Rec.Blobs = c.Sync
	c.startICloud(cfg.ICloudRoot)
	c.Rec.RecoverInterrupted()
	guard.Go(c.Rec.SweepAudio)
	if cfg.OnChange != nil {
		st.Subscribe(cfg.OnChange)
	}
	if err := c.createWelcome(); err != nil {
		log.Printf("welcome page: %v", err)
	}
	return c, nil
}

func (c *Core) Start() {
	c.watch(c.Sync.ICloudOn())
	first := c.startedUpdate.CompareAndSwap(false, true)
	if c.projectOutOfDate() && c.updatingRun.CompareAndSwap(false, true) {
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

func (c *Core) watch(on bool) {
	if c.WatchICloud != nil {
		c.WatchICloud(on)
	}
}

func (c *Core) settings() store.Settings {
	if c.Settings != nil {
		return c.Settings()
	}
	return c.Store.Settings()
}

func (c *Core) saveSettings(s store.Settings) {
	if c.SaveSettings != nil {
		c.SaveSettings(s)
		return
	}
	if err := c.Store.SaveSettings(s); err != nil {
		log.Printf("settings: %v", err)
	}
}

type lineWriter func(line string)

func (w lineWriter) Write(p []byte) (int, error) {
	for line := range bytes.Lines(p) {
		w(string(bytes.TrimSuffix(line, []byte("\n"))))
	}
	return len(p), nil
}
