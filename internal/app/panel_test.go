package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A new conversation keeps the one before, which the panel's menu of
// earlier conversations opens again.
func TestEarlierConversationsInThePanel(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	if err := a.st.PutProvider(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.panelOpen, a.panelTab = true, 0
	sess := a.hub.Session(p.ID)
	sess.Messages = []*agents.Message{{Mine: true, Text: "Summarize this page"}, {Text: "It is a plan for the launch."}}

	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	if _, ok := tt.Find(locales.PanelPastConversations()); ok {
		t.Fatal("the menu of earlier conversations shows with none")
	}
	if err := tt.Click(locales.PanelNewConversation()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("It is a plan for the launch.") {
		t.Fatal("the new conversation shows the old one")
	}
	if err := tt.Click(locales.PanelPastConversations()); err != nil {
		t.Fatal(err)
	}
	if got := tt.Menu(); len(got) != 1 || got[0] != "Summarize this page" {
		t.Fatalf("menu %q", got)
	}
	if err := tt.ChooseMenuItem("Summarize this page"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("It is a plan for the launch.") {
		t.Errorf("the earlier conversation did not open: %q", tt.Texts())
	}
	// The empty one it left is not kept.
	if len(sess.Past) != 0 {
		t.Errorf("%d earlier conversations", len(sess.Past))
	}
}

// An agent's comment asked for by someone else says whose it is, so a
// person's own words can't pass as an agent's.
func TestAgentCommentSaysWhoAsked(t *testing.T) {
	a := templatesApp(t)
	a.session.UserID = "me"
	a.page = &pageState{members: []store.Member{{UserID: "ann", DisplayName: "Ann"}}}
	agent := func(by string) store.Comment { return store.Comment{AuthorKind: "agent", AuthorName: "Claude", AuthorID: by} }
	for _, tc := range []struct {
		c    store.Comment
		want string
	}{
		{agent("me"), ""},
		{agent(""), ""},
		{store.Comment{AuthorKind: "user", AuthorID: "ann"}, ""},
		{agent("ann"), locales.PanelAskedBy("Ann")},
		{agent("bob"), locales.PanelAskedBySomeone()},
	} {
		if got := a.askedBy(tc.c); got != tc.want {
			t.Errorf("askedBy(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}
