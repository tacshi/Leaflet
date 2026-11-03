package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

// iCloudCore is a Core whose Leaflet folder in iCloud Drive is root, and
// the watches of it it was told.
func iCloudCore(t *testing.T, root string) (*Core, *[]bool) {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Settings{}, st)
	c := &Core{Store: st, Remote: be, Sync: syncsvc.New(st, be)}
	c.Sync.ICloud = icloud.Folder{Root: root, Temp: t.TempDir()}
	var watched []bool
	c.WatchICloud = func(on bool) { watched = append(watched, on) }
	return c, &watched
}

// iCloud sync turns on through a folder it can use, and off again; the
// settings keep it, and the folder is watched while it is on.
func TestSwitchICloud(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Leaflet")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	c, watched := iCloudCore(t, root)
	if err := c.SwitchICloud(true); err != nil {
		t.Fatal(err)
	}
	c.KeepICloud(true)
	if !c.Sync.ICloudOn() || !c.Store.Settings().ICloudSync {
		t.Fatalf("syncing %v, setting %v", c.Sync.ICloudOn(), c.Store.Settings().ICloudSync)
	}
	if err := c.SwitchICloud(false); err != nil {
		t.Fatal(err)
	}
	c.KeepICloud(false)
	if c.Sync.ICloudOn() || c.Store.Settings().ICloudSync {
		t.Errorf("syncing %v, setting %v", c.Sync.ICloudOn(), c.Store.Settings().ICloudSync)
	}
	if !slices.Equal(*watched, []bool{true, false}) {
		t.Errorf("watched %v", *watched)
	}
}

// A folder that can't be used leaves iCloud sync off, though it was on,
// and says why as it came.
func TestSwitchICloudWithoutTheFolder(t *testing.T) {
	c, _ := iCloudCore(t, filepath.Join(t.TempDir(), "gone", "Leaflet"))
	c.Sync.SetICloud(true)
	if err := c.SwitchICloud(true); !errors.Is(err, icloud.ErrUnavailable) {
		t.Fatalf("turning on without the folder: %v", err)
	}
	if c.Sync.ICloudOn() {
		t.Error("iCloud sync stayed on")
	}
}

// Signed in, pages sync through the server: iCloud sync does not turn on,
// nor stay on in the settings once signing in came between.
func TestICloudStaysOffSignedIn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Leaflet")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	c, watched := iCloudCore(t, root)
	if _, err := c.Store.LoadSession(&store.User{ID: "u1", Email: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SwitchICloud(true); err == nil || err.Error() != locales.SettingsICloudSupabaseOn() {
		t.Fatalf("turning on signed in: %v", err)
	}
	if c.Sync.ICloudOn() {
		t.Error("iCloud sync turned on")
	}
	// Turned on just before signing in.
	c.Sync.SetICloud(true)
	c.KeepICloud(true)
	if c.Sync.ICloudOn() || c.Store.Settings().ICloudSync || !slices.Equal(*watched, []bool{false}) {
		t.Errorf("syncing %v, setting %v, watched %v", c.Sync.ICloudOn(), c.Store.Settings().ICloudSync, *watched)
	}
}
