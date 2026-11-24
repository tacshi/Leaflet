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

// TestAnICloudDeviceWaitsForItsFolderToEmptyTheBin has iCloud Drive on before the app has found its folder, as on an
// iPhone just opened: the device syncs, and empties the bin only after a pull, not from what it knew before.
func TestAnICloudDeviceWaitsForItsFolderToEmptyTheBin(t *testing.T) {
	c, _ := iCloudCore(t, "")
	c.Sync.SetICloud(true)
	p := deletedPage(t, c.Store)
	c.sweepUnsynced(binLater(t))
	if c.Store.IsPurged(p.ID) {
		t.Fatal("deleted for good before the folder was found and pulled from")
	}
}

// TestAPurgeThroughTheServerWaitsForIt deletes for good on a device signed in to an account, without iCloud Drive: what
// was on the page stays until the server takes the deletion, which may refuse it.
func TestAPurgeThroughTheServerWaitsForIt(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.SignedInForTest("http://127.0.0.1:1", st, "u1")
	c := &Core{Store: st, Remote: be, Sync: syncsvc.New(st, be)}
	if c.purgeWipe() != store.WipeOnceTaken {
		t.Fatal("a device syncing through the server wipes a page deleted for good before the server takes it")
	}
	c.Sync.SetICloud(true)
	if c.purgeWipe() != store.WipeNow {
		t.Error("a device syncing through iCloud Drive waits for a server")
	}
	local, _ := iCloudCore(t, "")
	if local.purgeWipe() != store.WipeNow {
		t.Error("a device that does not sync waits for a server")
	}
}

func TestDropNewPage(t *testing.T) {
	c, _ := iCloudCore(t, "")
	p, err := c.Store.CreatePage(store.NewPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DropNewPage(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.Page(p.ID); !store.IsNotFound(err) || c.Store.PendingCount() != 0 {
		t.Errorf("a page that went nowhere stays: %v, %d waiting", err, c.Store.PendingCount())
	}
	sent, err := c.Store.CreatePage(store.NewPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := c.Store.Pending()
	for _, e := range pending {
		if err := c.Store.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.DropNewPage(sent.ID); err != nil {
		t.Fatal(err)
	}
	if !c.Store.IsPurged(sent.ID) {
		t.Error("a page that went up is not deleted for good")
	}
	if items, _ := c.Store.Bin(); len(items) != 0 {
		t.Errorf("a page that never opened is in the bin: %+v", items)
	}
}
