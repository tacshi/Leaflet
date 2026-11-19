package app

import (
	"encoding/json"

	"github.com/egoist/mygo"
)

// devState is what mygo dev brings back after a change of the code restarts
// the app, beside what Leaflet keeps itself (the open page and its
// selection) and what MyGo brings back on its own (scroll offsets). MyGo
// keeps it only under mygo dev on desktops: a release never writes it.
type devState struct {
	PanelOpen  bool   `json:"panel_open,omitempty"`
	PanelTab   int    `json:"panel_tab,omitempty"`
	AgentScope int    `json:"agent_scope,omitempty"`
	Settings   string `json:"settings,omitempty"` // the section open, if any
	// The page whose history is open, with the version picked and how its
	// changes show.
	HistoryPage string `json:"history_page,omitempty"`
	HistoryPick int    `json:"history_pick,omitempty"`
	HistoryView int    `json:"history_view,omitempty"`
	SideBySide  bool   `json:"side_by_side,omitempty"`
}

// keepDevState has mygo dev keep devState. MyGo asks for it on the main
// thread, where the fields are changed.
func (a *App) keepDevState() {
	mygo.App.OnSaveState(func() []byte {
		data, _ := json.Marshal(a.devState())
		return data
	})
}

func (a *App) devState() devState {
	s := devState{PanelOpen: a.panelOpen, PanelTab: a.panelTab, AgentScope: a.agentScope}
	if a.settingsOpen {
		s.Settings = a.settingsSection
	}
	if d := &a.dialogs; d.historyOpen {
		s.HistoryPage, s.HistoryPick, s.HistoryView, s.SideBySide = d.historyPage, d.historyPick, d.historyView, d.sideBySide
	}
	return s
}

// restoreDevState brings back what keepDevState kept, once the app has
// started and reopened its page.
func (a *App) restoreDevState(data []byte) {
	var s devState
	if len(data) == 0 || json.Unmarshal(data, &s) != nil {
		return
	}
	a.panelOpen, a.panelTab, a.agentScope = s.PanelOpen, s.PanelTab, s.AgentScope
	if s.Settings != "" {
		a.openSettings(s.Settings)
	}
	if s.HistoryPage != "" && a.pageByID(s.HistoryPage) != nil {
		a.openHistory(s.HistoryPage)
		d := &a.dialogs
		d.historyPick = min(max(s.HistoryPick, 0), max(len(d.history)-1, 0))
		d.historyView, d.sideBySide = s.HistoryView, s.SideBySide
	}
}
