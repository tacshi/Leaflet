package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/provision"
	"leaflet/internal/store"
)

type Core struct {
	core       *core.Core
	plat       Platform
	ev         *events
	q          *queue
	pages      map[string]*openPage
	editors    int64
	cards      map[string]doc.Position
	locale     atomic.Value
	chatgpt    chatgptSignIn
	micTesting bool
	micHeard   string
	recSent    string
	levelsSent []float64
	voiceNote  *voiceNote
	cancelMu   sync.Mutex
	cancels    map[int64]context.CancelFunc
	cancelled  map[int64]bool
	linkAsked  map[string]bool
	settled    map[string]string
	copying    atomic.Bool
}

type config struct {
	DataDir    string `json:"data_dir"`
	ICloudRoot string `json:"icloud_root"`
	Locale     string `json:"locale"`
	Dev        bool   `json:"dev"`
}

func Open(settings string, platform Platform, ev Events) (_ *Core, err error) {
	defer catch(&err)
	var cfg config
	if err := json.Unmarshal([]byte(settings), &cfg); err != nil {
		return nil, err
	}
	if cfg.DataDir == "" {
		return nil, errors.New("Leaflet needs a folder for its data") //nolint:staticcheck
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
		settled:   map[string]string{},
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

func (c *Core) sharingChanged() {
	for id, p := range c.pages {
		if readOnly := !c.canEdit(id) || p.live.Unreadable(); readOnly != p.live.ReadOnly() {
			p.live.SetReadOnly(readOnly)
		}
	}
}

func (c *Core) SetLocale(locale string) {
	defer catch(nil)
	if locale != "" && c.locale.Load() != nil {
		c.locale.Store(locale)
	}
}

func (c *Core) Start() {
	defer catch(nil)
	c.q.do(c.core.Start)
}

func (c *Core) cancellable(token int64, d time.Duration) (ctx context.Context, done func()) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	if token == 0 {
		return ctx, cancel
	}
	c.cancelMu.Lock()
	defer c.cancelMu.Unlock()
	if c.cancelled[token] {
		delete(c.cancelled, token)
		cancel()
		return ctx, cancel
	}
	if c.cancels == nil {
		c.cancels = map[int64]context.CancelFunc{}
	}
	c.cancels[token] = cancel
	return ctx, func() {
		c.cancelMu.Lock()
		delete(c.cancels, token)
		c.cancelMu.Unlock()
		cancel()
	}
}

func (c *Core) Cancel(token int64) {
	defer catch(nil)
	c.cancelMu.Lock()
	defer c.cancelMu.Unlock()
	if cancel := c.cancels[token]; cancel != nil {
		cancel()
		return
	}
	if c.cancelled == nil {
		c.cancelled = map[int64]bool{}
	}
	c.cancelled[token] = true
}

func (c *Core) Stop() {
	defer catch(nil)
	c.core.Sync.Stop()
}

func (c *Core) Kick() {
	defer catch(nil)
	c.core.Sync.Kick()
}

func (c *Core) SyncNow() (err error) {
	defer catch(&err)
	return c.core.Sync.SyncNow()
}

type status struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	SpaceID     string `json:"space_id"`
	SignedIn    bool   `json:"signed_in"`
	Pending     int    `json:"pending"`
	ICloudOn    bool   `json:"icloud_on"`
	CanSync     bool   `json:"can_sync"`
}

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

type host struct{ c *Core }

func (h host) Update(pageID string, fn func()) {
	h.c.q.post(func() {
		fn()
		h.c.ev.agentsChanged(pageID)
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

func (h host) Pages() agents.Pages { return h.c.pageCore() }

func (h host) Tint(pageID string, ids []string) {
	h.c.q.post(func() { delete(h.c.settled, pageID) })
	h.c.ev.tint(pageID, ids)
}
