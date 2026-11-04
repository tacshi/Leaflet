package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// workspaceApp is an app with an agent to ask.
func workspaceApp(t *testing.T) *App {
	t.Helper()
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	if err := a.st.PutProvider(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	return a
}

// With no page open, the title bar has the record button and the panel's
// toggle, nothing of a page's, and the panel its Agent tab alone: the
// workspace agent's chat.
func TestPanelWithNoPageIsTheWorkspaceAgents(t *testing.T) {
	a := workspaceApp(t)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1100, 760)
	tt.Frame()
	for _, label := range []string{locales.TrayNewRecording(), locales.PageShowPanel()} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("no %q in the title bar", label)
		}
	}
	if _, ok := tt.Find(locales.PageShare()); ok {
		t.Error("Share shows with no page open")
	}
	if err := tt.Click(locales.PageShowPanel()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(locales.WorkspaceEmpty()) || tt.HasText(locales.PanelTabComments()) || tt.HasText(locales.PanelScopeAll()) {
		t.Fatalf("the panel with no page: %q", tt.Texts())
	}
	a.panelOpen = false
	tt.Frame()
	if err := tt.Click(locales.ShellAskAgent()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.panelOpen || !tt.HasText(locales.WorkspaceEmpty()) {
		t.Errorf("Ask the agent opened no workspace chat: %q", tt.Texts())
	}
}

// Beside a page, the composer's scope menu goes from the page's agent to
// the workspace agent's, and back.
func TestAgentTabSwitchesToAllPages(t *testing.T) {
	a := workspaceApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.panelOpen, a.panelTab = true, 0
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	if !tt.HasText(locales.PanelChatEmpty()) || !tt.HasText(locales.PanelTabComments()) {
		t.Fatalf("the page's panel: %q", tt.Texts())
	}
	if err := tt.Click(locales.PanelScopePage()); err != nil {
		t.Fatal(err)
	}
	if got := tt.Menu(); len(got) != 2 || got[1] != locales.PanelScopeAll() {
		t.Fatalf("scope menu %q", got)
	}
	if err := tt.ChooseMenuItem(locales.PanelScopeAll()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.agentScope != 1 || !tt.HasText(locales.WorkspaceEmpty()) {
		t.Fatalf("All pages shows %q", tt.Texts())
	}
	// A chat sent from the page shows in the page's.
	a.openPageAgent()
	tt.Frame()
	if !tt.HasText(locales.PanelChatEmpty()) {
		t.Errorf("This page shows %q", tt.Texts())
	}
}

// Search's Ask agent, with no page open, asks the workspace agent.
func TestSearchWithNoPageAsksTheWorkspaceAgent(t *testing.T) {
	a := workspaceApp(t)
	// The main thread is the test's, which does not come back for what
	// the run sends there: run at once, it would go beside its frames.
	a.post = func(func()) {}
	a.openSearch()
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1100, 760)
	tt.Frame()
	tt.Type("nothing like it")
	tt.Frame()
	if err := tt.Click(locales.DialogSearchAskAgent()); err != nil {
		t.Fatal(err)
	}
	msgs := a.hub.Session(agents.WorkspaceID).Messages
	if !a.panelOpen || len(msgs) == 0 || msgs[0].Text != "nothing like it" {
		t.Errorf("panel open %v, messages %+v", a.panelOpen, msgs)
	}
}

// The workspace agent's Undo takes its run back, and an edit opens the
// page it made.
func TestWorkspaceEditOpensThePage(t *testing.T) {
	a := workspaceApp(t)
	made, err := a.st.CreatePage(store.NewPageOptions{Title: "Article"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	sess := a.hub.Session(agents.WorkspaceID)
	summary, kept := locales.AgentEditCreatedPage("Article"), locales.AgentEditCreatedPage("Notes")
	undone := &agents.Run{ID: "r1", Status: "done", AgentName: "Ollama",
		Ops:   []doc.Op{{Op: "create_page", Page: &doc.NewPage{ID: other.ID, Title: "Notes"}}},
		Edits: []agents.Edit{{Summary: kept, PageID: other.ID}}}
	run := &agents.Run{ID: "r2", Status: "done", AgentName: "Ollama",
		Ops:   []doc.Op{{Op: "create_page", Page: &doc.NewPage{ID: made.ID, Title: "Article"}}},
		Edits: []agents.Edit{{Summary: summary, PageID: made.ID}}}
	sess.Runs = []*agents.Run{undone, run}
	sess.Messages = []*agents.Message{{Mine: true, Text: "Start notes"}, {Text: "Started them.", RunID: "r1"},
		{Mine: true, Text: "Save the article"}, {Text: "Saved it.", RunID: "r2"}}
	a.openWorkspace()
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1100, 760)
	tt.Frame()
	if err := tt.Click(locales.CommonUndo()); err != nil {
		t.Fatal(err)
	}
	if p, _ := a.st.Page(other.ID); !undone.Undone || p.DeletedAt.IsZero() {
		t.Fatalf("undone %v, page deleted %v", undone.Undone, !p.DeletedAt.IsZero())
	}
	tt.Frame()
	if err := tt.Click(summary); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.route.Kind != "page" || a.route.ID != made.ID || a.agentScope != 1 || !tt.HasText(summary) {
		t.Errorf("route %+v, scope %d: the page made opened beside its chat", a.route, a.agentScope)
	}
}

// A run that made a page, changing none of this page's blocks, says what
// it did in the page's bar, not that it edited no block.
func TestRunBarSaysWhatAPageMakingRunDid(t *testing.T) {
	a := workspaceApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	summary := locales.AgentEditCreatedPage("Article")
	a.hub.Session(p.ID).Runs = []*agents.Run{{ID: "r1", Status: "done", AgentName: "Fake", At: time.Now(),
		Ops:   []doc.Op{{Op: "create_page", Page: &doc.NewPage{ID: "x", Title: "Article"}}},
		Edits: []agents.Edit{{Summary: summary, PageID: "x"}}}}
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1100, 760)
	tt.Frame()
	if !tt.HasText(locales.PageRunDid("Fake", summary)) || tt.HasText(locales.PageRunEdited("Fake", 0)) {
		t.Errorf("the bar says %q", tt.Texts())
	}
}
