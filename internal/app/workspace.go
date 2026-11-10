package app

import (
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/locales"
)

func (a *App) pageOpen() bool { return a.route.Kind == "page" && a.page != nil }

func (a *App) workspacePanel(c *ui.Context) {
	a.chatView(c, a.hub.Session(agents.WorkspaceID), &a.draftsOf(agents.WorkspaceID).chat, chatOptions{
		empty: locales.WorkspaceEmpty(), placeholder: locales.WorkspacePlaceholder(),
		suggestions: []string{locales.WorkspaceSuggestFile(), locales.WorkspaceSuggestTag(), locales.WorkspaceSuggestFind(), locales.WorkspaceSuggestNew()},
		send:        a.askWorkspace,
		focus:       &a.dialogs.focusChat,
		scope:       a.pageOpen(),
	})
}

func (a *App) openWorkspace() {
	a.panelOpen, a.panelTab, a.agentScope = true, 0, 1
	a.dialogs.focusChat = true
}

func (a *App) fromWorkspace() {
	a.panelTab, a.agentScope = 0, 1
}

func (a *App) openPageAgent() {
	a.panelOpen, a.panelTab, a.agentScope = true, 0, 0
}

func (a *App) agentTab(c *ui.Context) {
	if a.agentScope == 1 {
		a.workspacePanel(c)
		return
	}
	a.agentPanel(c)
}

func (a *App) askWorkspace(text string) {
	open := ""
	if a.pageOpen() {
		open = a.page.id
	}
	a.hub.ChatWorkspace(text, open)
}
