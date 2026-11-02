package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/provision"
	"leaflet/internal/store"
)

// Core is Leaflet's core, open for the app. Its methods may be called
// from any thread but the core's own, and wait for their answer; the app
// calls them off its main thread. They run one at a time on the core's
// queue, which plays the part the desktop's main thread plays.
type Core struct {
	core *core.Core
	plat Platform
	ev   *events
	q    *queue
	// pages are the pages open in the app, by ID; touched only on q.
	pages map[string]*openPage
	// editors counts the page editors OpenPage has opened, to number
	// each; touched only on q.
	editors int64
	// cards are where deleted pages' cards were in their parents, by
	// page, for RestorePages to put them back; touched only on q.
	cards map[string]doc.Position
	// locale is the language the core says its messages in, as SetLocale
	// last chose it.
	locale atomic.Value
	// chatgpt is a ChatGPT sign-in begun or made in the provider form,
	// until SaveProvider keeps it.
	chatgpt chatgptSignIn
	// micTesting is whether Settings' test of the microphone runs, and
	// micHeard what the last one heard, or why it heard nothing; touched
	// only on q.
	micTesting bool
	micHeard   string
	// linkAsked are the links whose pages were asked for their link
	// cards' info this run; touched only on q.
	linkAsked map[string]bool
}

// config is how the app opens the core, as JSON.
type config struct {
	// DataDir is the folder of the database and the files kept on this
	// device: Application Support, which isn't backed up to iCloud as
	// Documents is.
	DataDir string `json:"data_dir"`
	// ICloudRoot is the Leaflet folder in iCloud Drive the user picked,
	// or "" while none is.
	ICloudRoot string `json:"icloud_root"`
	// Locale is the language the app shows, as a BCP 47 tag.
	Locale string `json:"locale"`
	// Dev is set by the app's Debug build, which syncs through the
	// desktop's development project, as a development build there does.
	Dev bool `json:"dev"`
}

// Open opens the core with settings, a config as JSON. Start then starts
// what runs in the background.
func Open(settings string, platform Platform, ev Events) (_ *Core, err error) {
	defer catch(&err)
	var cfg config
	if err := json.Unmarshal([]byte(settings), &cfg); err != nil {
		return nil, err
	}
	if cfg.DataDir == "" {
		return nil, errors.New("Leaflet needs a folder for its data") //nolint:staticcheck // shown to people as a sentence.
	}
	if cfg.Dev {
		provision.ProjectName = provision.DevProjectName
	}
	useDownloads(platform)
	fixZone()
	c := &Core{
		plat:      platform,
		ev:        &events{to: ev, q: newQueue()},
		q:         newQueue(),
		pages:     map[string]*openPage{},
		cards:     map[string]doc.Position{},
		linkAsked: map[string]bool{},
	}
	var locale func() string
	if cfg.Locale != "" {
		c.locale.Store(cfg.Locale)
		locale = func() string { return c.locale.Load().(string) }
	}
	c.q.do(func() {
		c.core, err = core.Open(core.Config{
			Platform: core.Platform{
				DataDir:     cfg.DataDir,
				Secrets:     secrets{platform},
				Online:      platform.Online,
				DeviceName:  platform.DeviceName,
				OpenURL:     platform.OpenURL,
				WatchICloud: platform.WatchICloud,
				ICloudRoot:  cfg.ICloudRoot,
				Log:         platform.Log,
			},
			Locale:      locale,
			Agents:      host{c},
			OnSyncState: c.ev.syncState,
			OnPageContent: func(id string) {
				c.q.post(func() { c.external(id) })
			},
			OnChange: func(ch store.Change) {
				c.ev.changed(ch)
				// Who may change a page comes with whom it is shared.
				if ch.Table == "*" || ch.Table == "page_members" {
					c.q.post(c.sharingChanged)
				}
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// sharingChanged lets the user change the open pages, or keeps them from
// it, as the desktop does as who a page is shared with changes: the app
// hears PageReadOnly for each page that changes so. Run on the queue.
func (c *Core) sharingChanged() {
	for id, p := range c.pages {
		if readOnly := !c.canEdit(id) || p.live.Unreadable(); readOnly != p.live.ReadOnly() {
			p.live.SetReadOnly(readOnly)
		}
	}
}

// SetLocale says the core's messages in locale, a tag of the catalogs, as
// the app's language changes in its settings. A core opened without a
// locale keeps the catalogs' own.
func (c *Core) SetLocale(locale string) {
	defer catch(nil)
	if locale != "" && c.locale.Load() != nil {
		c.locale.Store(locale)
	}
}

// Start starts what runs in the background: sync, and watching iCloud
// Drive while iCloud sync is on. The app calls it as it comes to the
// foreground; Stop, as it goes to the background.
func (c *Core) Start() {
	defer catch(nil)
	c.q.do(c.core.Start)
}

// Stop stops the sync loop. A pass running, the loop's or SyncNow's, is
// cut short; the changes it didn't send go on the next.
func (c *Core) Stop() {
	defer catch(nil)
	c.core.Sync.Stop()
}

// Kick asks for a sync pass soon, as another device writing to iCloud
// Drive does.
func (c *Core) Kick() {
	defer catch(nil)
	c.core.Sync.Kick()
}

// SyncNow runs a sync pass now and waits for it, for pull to refresh and
// the app's background refresh.
func (c *Core) SyncNow() (err error) {
	defer catch(&err)
	return c.core.Sync.SyncNow()
}

// status is who uses the app and how sync stands, as Status returns it.
type status struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	SpaceID     string `json:"space_id"`
	// SignedIn is signed in to a Supabase project, to sync through it.
	SignedIn bool `json:"signed_in"`
	// Pending counts the changes waiting to go up.
	Pending  int  `json:"pending"`
	ICloudOn bool `json:"icloud_on"`
	CanSync  bool `json:"can_sync"`
}

// Status returns who is signed in and how sync stands, as a status in JSON.
func (c *Core) Status() (_ string, err error) {
	defer catch(&err)
	var s status
	c.q.do(func() {
		se := c.core.Store.Session()
		s = status{
			UserID: se.UserID, DisplayName: se.DisplayName, Email: se.Email, SpaceID: se.SpaceID, SignedIn: se.SignedIn,
			Pending:  c.core.Store.PendingCount(),
			ICloudOn: c.core.Sync.ICloudOn(),
			CanSync:  c.core.Sync.CanSync(),
		}
	})
	return encode(s)
}

// host is the core as the agents' host.
type host struct{ c *Core }

// Update runs fn on the core's queue, where the hub changes its state,
// and tells the app it changed.
func (h host) Update(fn func()) {
	h.c.q.post(func() {
		fn()
		h.c.ev.agentsChanged()
	})
}

func (h host) Live(pageID string) agents.LiveDoc {
	if p := h.c.pages[pageID]; p != nil && !p.live.Unreadable() {
		return p.live
	}
	return nil
}

func (h host) SaveNow(pageID string) {
	if p := h.c.pages[pageID]; p != nil {
		p.live.Save()
	}
}

func (h host) Online() bool { return h.c.plat.Online() }

func (h host) Settings() store.Settings { return h.c.core.Store.Settings() }

func (h host) FlushPage(ctx context.Context, pageID string) error {
	return h.c.core.Sync.FlushPage(ctx, pageID)
}

func (h host) SyncNow() { h.c.core.Sync.Kick() }

func (h host) Tint(pageID string, ids []string) { h.c.ev.tint(pageID, ids) }
