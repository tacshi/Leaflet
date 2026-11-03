package mobile

import "testing"

// Saving settings keeps keeping all pages on this device, as it turns off
// and on, with the other settings saved beside it.
func TestSaveSettingsKeepsAllPagesOffline(t *testing.T) {
	c, _ := open(t)
	for _, on := range []bool{false, true} {
		settings := `{"all_pages_offline": false, "work_offline": true}`
		if on {
			settings = `{"all_pages_offline": true, "work_offline": false}`
		}
		if err := c.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
		if s := c.core.Store.Settings(); s.AllPagesOffline != on || s.WorkOffline == on {
			t.Errorf("all pages kept %v, working offline %v; want %v", s.AllPagesOffline, s.WorkOffline, on)
		}
	}
}
