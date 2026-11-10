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

func workspaceApp(t *testing.T) *App {
	t.Helper()
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	if err := a.st.PutProvider(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3"}); err != nil {
		t.Fatal(err)
	}
	return a
}

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
	if err := tt.Click(locales.PanelScopeAll()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.PanelScopePage()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.agentScope != 0 || !tt.HasText(locales.PanelChatEmpty()) {
		t.Fatalf("This page shows %q", tt.Texts())
	}
	a.agentScope = 1
	a.openPageAgent()
	tt.Frame()
	if !tt.HasText(locales.PanelChatEmpty()) {
		t.Errorf("This page shows %q", tt.Texts())
	}
}

func TestSearchWithNoPageAsksTheWorkspaceAgent(t *testing.T) {
	a := workspaceApp(t)
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

	a.hub.Session(agents.WorkspaceID).Running = true
	a.openSearch()
	tt.Frame()
	tt.Type("still nothing")
	tt.Frame()
	if err := tt.Click(locales.DialogSearchAskAgent()); err != nil {
		t.Fatal(err)
	}
	if got := a.draftsOf(agents.WorkspaceID).chat.draft; got != "still nothing" || len(a.hub.Session(agents.WorkspaceID).Messages) != len(msgs) {
		t.Errorf("draft %q, messages %+v", got, a.hub.Session(agents.WorkspaceID).Messages)
	}
}

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

func TestPanelShortcutWithNoPage(t *testing.T) {
	a := workspaceApp(t)
	made, err := a.st.CreatePage(store.NewPageOptions{Title: "Article"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.panelOpen, a.panelTab = true, 1
	a.togglePanel(0)
	if a.panelOpen {
		t.Fatal("the shortcut left the panel open")
	}
	a.togglePanel(1)
	if a.panelOpen {
		t.Fatal("Comments opened the panel with no page")
	}
	a.togglePanel(0)
	if !a.panelOpen {
		t.Fatal("the shortcut did not open the panel")
	}
	summary := locales.AgentEditCreatedPage("Article")
	sess := a.hub.Session(agents.WorkspaceID)
	sess.Runs = []*agents.Run{{ID: "r1", Status: "done", AgentName: "Ollama",
		Ops:   []doc.Op{{Op: "create_page", Page: &doc.NewPage{ID: made.ID, Title: "Article"}}},
		Edits: []agents.Edit{{Summary: summary, PageID: made.ID}}}}
	sess.Messages = []*agents.Message{{Mine: true, Text: "Save the article"}, {Text: "Saved it.", RunID: "r1"}}
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1100, 760)
	tt.Frame()
	if err := tt.Click(summary); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.route.ID != made.ID || a.agentScope != 1 || !tt.HasText(summary) {
		t.Errorf("route %+v of %s, scope %d: the page opened beside another chat: %q", a.route, made.ID, a.agentScope, tt.Texts())
	}
}

func TestPanelKeepsItsTabPastNoPage(t *testing.T) {
	a := workspaceApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.panelOpen, a.panelTab, a.agentScope = true, 1, 0
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1100, 760)
	tt.Frame()
	a.show(Route{Kind: "home"})
	tt.Frame()
	if !tt.HasText(locales.WorkspaceEmpty()) {
		t.Fatalf("no workspace chat with no page: %q", tt.Texts())
	}
	a.openPage(p.ID)
	tt.Frame()
	if a.panelTab != 1 || a.agentScope != 0 {
		t.Errorf("tab %d, scope %d after the screen of no page", a.panelTab, a.agentScope)
	}
}

func TestWorkspaceDraftGoesWithTheAccount(t *testing.T) {
	a := workspaceApp(t)
	a.saveSettings()
	a.draftsOf(agents.WorkspaceID).chat.draft = "My secret plans"
	posted := make(chan func(), 8)
	a.post = func(fn func()) { posted <- fn }
	a.leave(a.st.ForgetAccount, false)
	for a.drafts[agents.WorkspaceID] != nil {
		select {
		case fn := <-posted:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatalf("the draft %q stayed", a.drafts[agents.WorkspaceID].chat.draft)
		}
	}
}

func TestWorkspaceChatReadAsTheAccountLeavesGoes(t *testing.T) {
	a := workspaceApp(t)
	a.saveSettings()
	if err := a.st.SetValue("chat.workspace", `{"conversation_id":"c1","messages":[{"Mine":true,"Text":"File my secret plans"}]}`); err != nil {
		t.Fatal(err)
	}
	posted := make(chan func(), 8)
	a.post = func(fn func()) { posted <- fn }
	cleared := make(chan struct{})
	a.leave(func() error {
		defer close(cleared)
		(<-posted)()
		if msgs := a.hub.Session(agents.WorkspaceID).Messages; len(msgs) != 1 {
			t.Errorf("the chat read back %+v", msgs)
		}
		return a.st.ForgetAccount()
	}, false)
	<-cleared
	for {
		select {
		case fn := <-posted:
			fn()
			continue
		case <-time.After(time.Second):
		}
		break
	}
	if msgs := a.hub.Session(agents.WorkspaceID).Messages; len(msgs) != 0 {
		t.Errorf("the next account sees %+v", msgs)
	}
}
