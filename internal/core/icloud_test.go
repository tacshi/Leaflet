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
	c.Sync.SetICloud(true)
	c.KeepICloud(true)
	if c.Sync.ICloudOn() || c.Store.Settings().ICloudSync || !slices.Equal(*watched, []bool{false}) {
		t.Errorf("syncing %v, setting %v, watched %v", c.Sync.ICloudOn(), c.Store.Settings().ICloudSync, *watched)
	}
}

// TestUsingAnAccountLeavesTheSettingsToTheApp signs in with iCloud sync on: UseAccount, which the app may run off its
// main thread, turns iCloud sync off and neither reads nor writes the app's settings, nor stops the watch on the
// iCloud folder. KeepICloud, which the app calls on its main thread once UseAccount returns, does.
func TestUsingAnAccountLeavesTheSettingsToTheApp(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Leaflet")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	c, watched := iCloudCore(t, root)
	if err := c.SwitchICloud(true); err != nil {
		t.Fatal(err)
	}
	c.KeepICloud(true)
	app, touched := c.Store.Settings(), 0
	c.Settings = func() store.Settings { touched++; return app }
	c.SaveSettings = func(s store.Settings) { touched++; app = s }
	*watched = nil
	if err := c.UseAccount(&store.User{ID: "u1", Email: "ada@example.com"}, false); err != nil {
		t.Fatal(err)
	}
	if c.Sync.ICloudOn() {
		t.Error("iCloud sync stayed on for the account signed in")
	}
	if touched != 0 || len(*watched) != 0 {
		t.Errorf("using the account touched the app's settings %d times, watched %v", touched, *watched)
	}
	c.KeepICloud(c.Sync.ICloudOn())
	if app.ICloudSync || !slices.Equal(*watched, []bool{false}) {
		t.Errorf("kept: setting %v, watched %v", app.ICloudSync, *watched)
	}
}
