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

// Typing @ in a comment lists the agents, and choosing one writes its
// name, which runs it when the comment is posted.
func TestCommentAgentMenu(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	for _, p := range []store.Provider{
		{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true},
		{ID: "studio", Preset: "ollama", Name: "Studio", AuthKind: "none", DefaultModel: "qwen", IsLocal: true},
	} {
		if err := a.st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.panelOpen, a.panelTab = true, 1
	nc := &newComment{blockID: "b1", quote: "WAL"}
	a.dialogs.newComment = nc

	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	tt.Type("@")
	tt.Frame()
	if !tt.HasText("Ollama") || !tt.HasText("Studio") {
		t.Fatalf("@ shows no agents: %q", tt.Texts())
	}
	tt.Type("st")
	tt.Frame()
	if tt.HasText("Ollama") {
		t.Error("the menu keeps an agent the query doesn't match")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Type("hi")
	tt.Frame()
	if nc.text != "@Studio hi" {
		t.Errorf("text %q after Enter", nc.text)
	}

	// An address is no mention; Escape closes the menu until another @.
	tt.Type(" me@x")
	tt.Frame()
	if tt.HasText("Ollama") {
		t.Error("an email address opens the menu")
	}
	tt.Type(" @")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if tt.HasText("Ollama") {
		t.Error("Escape leaves the menu open")
	}
	tt.Type(" @")
	tt.Frame()
	if err := tt.Click("Ollama"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if want := "@Studio hi me@x @ @Ollama "; nc.text != want {
		t.Errorf("text %q after a click, want %q", nc.text, want)
	}
}

// A comment's mentions of agents are marked, and only them: not an email
// address, a longer word or code.
func TestMarkMentions(t *testing.T) {
	names := []string{"agent", "Chat", "ChatGPT"}
	for _, tc := range []struct{ in, want string }{
		{"@ChatGPT What is LWT?", "<mark><b>@ChatGPT</b></mark> What is LWT?"},
		{"ask @chatgpt, then @agent", "ask <mark><b>@chatgpt</b></mark>, then <mark><b>@agent</b></mark>"},
		{"me@ChatGPT.com @ChatGPTs @Bob", "me@ChatGPT.com @ChatGPTs @Bob"},
		{"`@ChatGPT` @Chat", "`@ChatGPT` <mark><b>@Chat</b></mark>"},
		// Code spans of longer runs, and over lines of a paragraph.
		{"``a ` @agent`` @agent", "``a ` @agent`` <mark><b>@agent</b></mark>"},
		{"see `x\n@agent` @agent", "see `x\n@agent` <mark><b>@agent</b></mark>"},
		// A backtick escaped, or never closed, opens no span.
		{"\\` @agent \\`", "\\` <mark><b>@agent</b></mark> \\`"},
		{"a ` @agent", "a ` <mark><b>@agent</b></mark>"},
		// Fenced and indented code blocks.
		{"```\n@agent\n```\n@agent", "```\n@agent\n```\n<mark><b>@agent</b></mark>"},
		{"~~~~go\n@agent\n~~~\n@agent\n~~~~\n@agent", "~~~~go\n@agent\n~~~\n@agent\n~~~~\n<mark><b>@agent</b></mark>"},
		{"code:\n\n    @agent\n\t@agent\n\n@agent", "code:\n\n    @agent\n\t@agent\n\n<mark><b>@agent</b></mark>"},
		// An indented line going on with a paragraph is not code.
		{"ask\n    @agent", "ask\n    <mark><b>@agent</b></mark>"},
	} {
		if got := markMentions(tc.in, names); got != tc.want {
			t.Errorf("markMentions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An agent runs for a comment that mentions it where the comment shows it
// marked: not in code, an address, a longer word or after a backslash.
func TestAgentInCommentOnlyWhenMentioned(t *testing.T) {
	names := []string{"agent", "Ollama"}
	for in, want := range map[string]bool{
		"@agent sum it up": true, "ask @Ollama": true,
		"`@agent`": false, "```\n@agent\n```": false, `\@agent`: false,
		"me@ollama.com": false, "@agentic": false, "@Ollamas": false,
	} {
		if got := len(mentioned(in, names)) > 0; got != want {
			t.Errorf("mentioned(%q) = %v", in, got)
		}
	}
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	if err := a.st.PutProvider(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	a.reloadAll()
	a.openPage(p.ID)
	a.agentInComment(store.Comment{ID: "c1", BlockID: "b1"}, "write to me@ollama.com, not `@agent`")
	if a.hub.Session(p.ID).Running {
		t.Error("an agent runs for a comment that mentions none")
	}
}

// Show resolved stays as chosen when a comment's badge opens its thread,
// and a search hit in a resolved thread shows the resolved ones.
func TestShowResolvedApartFromTheFocus(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	a.reloadAll()
	a.openPage(p.ID)
	root, err := a.st.AddComment(p.ID, "b1", "Old question", "", "", "user", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetResolved(root.ID, true); err != nil {
		t.Fatal(err)
	}
	a.page.threads, _ = a.st.Threads(p.ID)
	a.dialogs.showResolved = true
	(&host{a: a}).OpenComments("b2")
	if !a.dialogs.showResolved {
		t.Error("a badge's click turned Show resolved off")
	}
	a.dialogs.showResolved = false
	a.openHit(store.SearchHit{Kind: "comment", PageID: p.ID, BlockID: "b1"})
	if !a.dialogs.showResolved || a.dialogs.commentFocus != "b1" {
		t.Errorf("search hit: show resolved %v, focus %q", a.dialogs.showResolved, a.dialogs.commentFocus)
	}
}
