package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
)

func binLater(t *testing.T) time.Time {
	t.Helper()
	later := time.Now().Add(store.BinKept + time.Hour)
	old := binNow
	binNow = func() time.Time { return later }
	t.Cleanup(func() { binNow = old })
	return later
}

func deletedPage(t *testing.T, st *store.Store) store.Page {
	t.Helper()
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plans"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestASyncingDeviceEmptiesTheBinOnlyAfterAPull(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Leaflet")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	c, _ := iCloudCore(t, root)
	c.Sync.SetICloud(true)
	p := deletedPage(t, c.Store)
	later := binLater(t)
	// What this device knew may be old: another device may have restored the page since.
	c.sweepUnsynced(later)
	if c.Store.IsPurged(p.ID) {
		t.Fatal("deleted for good before a pull")
	}
	c.sweepPulled()
	if !c.Store.IsPurged(p.ID) {
		t.Fatal("kept past its days in the bin after a pull")
	}
	again := deletedPage(t, c.Store)
	c.sweepPulled()
	if c.Store.IsPurged(again.ID) {
		t.Error("emptied the bin again within the hour")
	}
}

func TestADeviceThatDoesNotSyncEmptiesTheBinOnItsOwn(t *testing.T) {
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
	p := deletedPage(t, st)
	c.sweepUnsynced(binLater(t))
	if !st.IsPurged(p.ID) {
		t.Fatal("kept past its days in the bin")
	}
}
