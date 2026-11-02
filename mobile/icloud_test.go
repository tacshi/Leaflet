package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"leaflet/internal/icloud"
	"leaflet/locales"
)

// watchingPlatform is the app, keeping what WatchICloud was told.
type watchingPlatform struct {
	fakePlatform
	wmu     sync.Mutex
	watches []bool
}

func (p *watchingPlatform) WatchICloud(on bool) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.watches = append(p.watches, on)
}

func (p *watchingPlatform) watched() []bool {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return slices.Clone(p.watches)
}

func openWatching(t *testing.T) (*Core, *watchingPlatform) {
	t.Helper()
	cfg, _ := json.Marshal(config{DataDir: t.TempDir(), Locale: "en"})
	p := &watchingPlatform{fakePlatform: fakePlatform{t: t, secrets: map[string][]byte{}}}
	p.logUntilEnd()
	c, err := Open(string(cfg), p, &fakeEvents{updates: make(chan update, 16)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Stop()
		_ = c.core.Store.Close()
	})
	return c, p
}

func iCloudStatus(t *testing.T, c *Core) status {
	t.Helper()
	raw, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	var s status
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

func TestSetICloudSyncsThroughThePickedFolder(t *testing.T) {
	c, p := openWatching(t)
	root := filepath.Join(t.TempDir(), "Leaflet")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreatePage(`{"title":"Groceries"}`); err != nil {
		t.Fatal(err)
	}

	if err := c.SetICloud(root, true); err != nil {
		t.Fatal(err)
	}
	if s := iCloudStatus(t, c); !s.ICloudOn || !s.CanSync {
		t.Fatalf("status after turning on %+v", s)
	}
	if !c.core.Store.Settings().ICloudSync {
		t.Fatal("the setting stayed off")
	}
	if err := c.SyncNow(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, icloud.SpacePath)); err != nil {
		t.Fatalf("the pass wrote no space: %v", err)
	}
	if s := iCloudStatus(t, c); s.Pending != 0 {
		t.Fatalf("%d changes still wait to go up", s.Pending)
	}

	if err := c.SetICloud("", false); err != nil {
		t.Fatal(err)
	}
	if s := iCloudStatus(t, c); s.ICloudOn {
		t.Fatal("iCloud sync stayed on")
	}
	if c.core.Store.Settings().ICloudSync {
		t.Fatal("the setting stayed on")
	}
	if got := c.core.Sync.ICloudFolder().Root; got != root {
		t.Fatalf("turning off forgot the folder: %q", got)
	}
	if got := p.watched(); !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("watched %v", got)
	}
}

func TestSetICloudStaysOffWithoutTheFolder(t *testing.T) {
	c, p := openWatching(t)
	root := filepath.Join(t.TempDir(), "gone", "Leaflet")

	err := c.SetICloud(root, true)
	if err == nil || err.Error() != locales.In("en").SettingsICloudUnavailable() {
		t.Fatalf("turning on without the folder: %v", err)
	}
	if s := iCloudStatus(t, c); s.ICloudOn {
		t.Fatal("iCloud sync turned on")
	}
	if c.core.Store.Settings().ICloudSync {
		t.Fatal("the setting turned on")
	}
	if got := p.watched(); !slices.Equal(got, []bool{false}) {
		t.Fatalf("watched %v", got)
	}
}

// Picking another folder waits for the pass under way, which can take
// minutes, but the rest of the core does not wait with it.
func TestSetICloudDuringAPassLeavesTheCoreFree(t *testing.T) {
	c, _ := openWatching(t)
	root := filepath.Join(t.TempDir(), "Leaflet")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	inPass, endPass := make(chan struct{}), make(chan struct{})
	go c.core.Sync.Hold(func() {
		close(inPass)
		<-endPass
	})
	<-inPass
	set := make(chan error, 1)
	go func() { set <- c.SetICloud(root, false) }()
	status := make(chan error, 1)
	go func() {
		_, err := c.Status()
		status <- err
	}()
	select {
	case err := <-status:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the core waited for the pass")
	}
	close(endPass)
	if err := <-set; err != nil {
		t.Fatal(err)
	}
	if got := c.core.Sync.ICloudFolder().Root; got != root {
		t.Errorf("folder %q", got)
	}
}
