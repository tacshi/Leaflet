package core

import (
	"testing"

	"leaflet/internal/store"
)

// Keeping a page on this device, or letting it go, is kept in the store.
func TestSetKeepOffline(t *testing.T) {
	c, _ := iCloudCore(t, t.TempDir())
	p, err := c.Store.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []bool{false, true} {
		if err := c.SetKeepOffline(p.ID, keep); err != nil {
			t.Fatal(err)
		}
		if got, _ := c.Store.Page(p.ID); got.KeepOffline != keep {
			t.Errorf("kept %v, want %v", got.KeepOffline, keep)
		}
	}
}

// Keeping all pages is kept in the settings as the app has them, or in
// the store's when the app keeps none of its own.
func TestSetAllPagesOffline(t *testing.T) {
	c, _ := iCloudCore(t, t.TempDir())
	c.SetAllPagesOffline(false)
	if c.Store.Settings().AllPagesOffline {
		t.Error("the store keeps all pages")
	}
	app := c.Store.Settings()
	saved := 0
	c.Settings = func() store.Settings { return app }
	c.SaveSettings = func(s store.Settings) { app = s; saved++ }
	c.SetAllPagesOffline(true)
	if !app.AllPagesOffline || saved != 1 {
		t.Errorf("the app keeps all pages %v, saved %d times", app.AllPagesOffline, saved)
	}
}
