package app

import (
	"encoding/json"
	"testing"

	"leaflet/internal/store"
)

// What mygo dev keeps across a restart comes back as it was: the side panel,
// Settings' section and a page's history with its version and view.
func TestDevStateComesBack(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"v1", "v2"} {
		if err := a.st.AddHistory(p.ID, a.st.Content(p.ID), "manual", "", name); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	a.panelOpen, a.panelTab, a.agentScope = true, 1, 1
	a.openSettings("speech")
	a.openHistory(p.ID)
	d := &a.dialogs
	d.historyPick, d.historyView, d.sideBySide = 1, 1, true
	data, err := json.Marshal(a.devState())
	if err != nil {
		t.Fatal(err)
	}

	b := templatesApp(t)
	b.st = a.st
	b.reloadAll()
	b.restoreDevState(data)
	if b.devState() != a.devState() {
		t.Errorf("came back as %+v, kept %+v", b.devState(), a.devState())
	}
	if !b.settingsOpen || len(b.dialogs.history) != 2 {
		t.Errorf("settings open %v, %d versions listed", b.settingsOpen, len(b.dialogs.history))
	}

	c := templatesApp(t)
	c.restoreDevState([]byte(`{"history_page":"gone","history_pick":5}`))
	c.restoreDevState(nil)
	c.restoreDevState([]byte("{"))
	if c.dialogs.historyOpen || c.settingsOpen {
		t.Error("a page that's gone, or nothing kept, opened something")
	}
}
