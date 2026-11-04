package app

import (
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/locales"
)

// The workspace agent, the agent of all the user's pages rather than of
// one: the panel's Agent tab, at All pages beside a page, and alone with
// no page open.

// pageOpen reports whether the main area shows a page, whose panel is the
// page's: with none, it is the workspace agent's.
func (a *App) pageOpen() bool { return a.route.Kind == "page" && a.page != nil }

// workspacePanel is the workspace agent's chat: the panel's with no page
// open, and the Agent tab's at All pages beside one.
func (a *App) workspacePanel(c *ui.Context) {
	a.chatView(c, a.hub.Session(agents.WorkspaceID), &a.draftsOf(agents.WorkspaceID).chat, chatOptions{
		empty: locales.WorkspaceEmpty(), placeholder: locales.WorkspacePlaceholder(),
		suggestions: []string{locales.WorkspaceSuggestFile(), locales.WorkspaceSuggestTag(), locales.WorkspaceSuggestFind(), locales.WorkspaceSuggestNew()},
		send:        a.askWorkspace,
		focus:       &a.dialogs.focusChat,
		scope:       a.pageOpen(),
	})
}

// openWorkspace opens the panel at its Agent tab, at All pages, its
// composer taking the keyboard.
func (a *App) openWorkspace() {
	a.panelOpen, a.panelTab, a.agentScope = true, 0, 1
	a.dialogs.focusChat = true
}

// fromWorkspace has a page opened from the workspace agent's chat, as a
// page a run made or a link in a reply, open beside that chat still: the
// panel's Agent tab at All pages.
func (a *App) fromWorkspace() {
	a.panelTab, a.agentScope = 0, 1
}

// openPageAgent opens the panel at its Agent tab, at This page: the open
// page's agent, which a chat sent from the page shows in.
func (a *App) openPageAgent() {
	a.panelOpen, a.panelTab, a.agentScope = true, 0, 0
}

// agentTab is the panel's Agent tab beside a page: the page's agent or
// the workspace agent's, as the composer's scope menu chooses.
func (a *App) agentTab(c *ui.Context) {
	if a.agentScope == 1 {
		a.workspacePanel(c)
		return
	}
	a.agentPanel(c)
}

// askWorkspace asks the workspace agent text, telling it of the page open.
func (a *App) askWorkspace(text string) {
	open := ""
	if a.pageOpen() {
		open = a.page.id
	}
	a.hub.ChatWorkspace(text, open)
}
