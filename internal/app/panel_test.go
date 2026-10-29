package app

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
	agent := func(by string) store.Comment {
		return store.Comment{AuthorKind: "agent", AuthorName: "Claude", AuthorID: by}
	}
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

// A run on this device stops from the chat's Stop, which shows in place
// of Send while it works; it ends as a run that made no change.
func TestStopAnAgentRun(t *testing.T) {
	asked, done := make(chan struct{}, 1), make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case asked <- struct{}{}:
		default:
		}
		// A model that takes its time, till the test is over.
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	t.Cleanup(hs.Close)
	t.Cleanup(func() { close(done) })
	a := templatesApp(t)
	var mu sync.Mutex
	var posted []func()
	a.post = func(fn func()) {
		mu.Lock()
		posted = append(posted, fn)
		mu.Unlock()
	}
	frame := func() {
		mu.Lock()
		fns := posted
		posted = nil
		mu.Unlock()
		for _, fn := range fns {
			fn()
		}
	}
	a.online = true
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	// An ID of its own, which has no key in the keychain.
	if err := a.st.PutProvider(store.Provider{ID: "test-stop-" + t.Name(), Preset: "ollama", Name: "Ollama", BaseURL: hs.URL,
		AuthKind: "none", DefaultModel: "llama3", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.panelOpen, a.panelTab = true, 0
	a.hub.Chat(p.ID, "Summarize this page", nil)
	sess := a.hub.Session(p.ID)
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the model was never asked")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		frame()
		a.view(c)
	}, 1300, 800)
	tt.Frame()
	if _, ok := tt.Find(locales.PanelSend()); ok {
		t.Error("Send shows while the run works")
	}
	if err := tt.Click(locales.PageStopRun()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !sess.Running })
	run := sess.Runs[len(sess.Runs)-1]
	if !run.Stopped || run.Status != "done" || run.Error != "" {
		t.Errorf("run %q stopped %v error %q", run.Status, run.Stopped, run.Error)
	}
	if m := sess.Messages[len(sess.Messages)-1]; m.Pending || m.Error || m.Text != agents.Describe(run) {
		t.Errorf("reply %+v", m)
	}
	if _, ok := tt.Find(locales.PanelSend()); !ok {
		t.Error("no Send once stopped")
	}
}
