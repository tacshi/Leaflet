package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
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

// An agent runs for a comment that mentions it where the comment shows it
// marked, and not for one that only names it in an address or code; which
// mentions count is core.Mentioned's, tested there.
func TestAgentInCommentOnlyWhenMentioned(t *testing.T) {
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

// waitFor runs frames until ok, or fails.
func waitFor(t *testing.T, tt *ui.Tester, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
}

// Undo of a run that cannot revert its edits, as on content that cannot
// be read, says so, and the run stays as it was.
func TestUndoRunThatFailsSaysSo(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.DB().Exec(`UPDATE page_contents SET content = '{"children": [' WHERE page_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	if a.page == nil {
		t.Fatal("the page did not open")
	}
	n := &doc.Node{ID: "x1", Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown("one"))
	run := &agents.Run{ID: "r1", Status: "done", AgentName: "Ollama", Ops: []doc.Op{{Op: "insert", Position: &doc.Position{At: "end"}, Nodes: []*doc.Node{n}}}}
	a.hub.Session(p.ID).Runs = []*agents.Run{run}
	a.undoRun(run)
	if run.Undone {
		t.Error("the run counts as undone")
	}
	if len(a.toasts) != 1 || !strings.HasPrefix(a.toasts[0].text, strings.TrimSuffix(locales.PageUndoRunFailed("Ollama", ""), " ")) {
		t.Errorf("toasts %+v", a.toasts)
	}
}

// A run's status line shimmers: its text drawn, a band of it brighter
// that moves on as time does, and still where the desktop asks for less
// motion.
func TestStatusLineShimmers(t *testing.T) {
	const status = "Reading the page…"
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { shimmer(c, status, 13) })
	}, 300, 60)
	tt.Frame()
	r, ok := tt.Find(status)
	if !ok {
		t.Fatal("no status line")
	}
	// The pixels of the line, and how many of them are inked.
	line := func() ([]byte, int) {
		img := tt.Image()
		scale := float32(img.Bounds().Dx()) / 300
		ground := img.RGBAAt(int(5*scale), int(5*scale))
		var px []byte
		inked := 0
		for y := int(r.Y * scale); y < int((r.Y+r.H)*scale); y++ {
			for x := int(r.X * scale); x < int((r.X+r.W)*scale); x++ {
				p := img.RGBAAt(x, y)
				px = append(px, p.R, p.G, p.B)
				if p != ground {
					inked++
				}
			}
		}
		return px, inked
	}
	before, inked := line()
	if inked == 0 {
		t.Fatal("the status line's text isn't drawn")
	}
	if !tt.FramePending() {
		t.Error("the status line asks for no frame to move its band")
	}
	tt.Advance(500 * time.Millisecond)
	if after, _ := line(); string(after) == string(before) {
		t.Error("the status line's band didn't move")
	}

	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()
	still, inked := line()
	if inked == 0 {
		t.Fatal("with less motion, the status line's text isn't drawn")
	}
	tt.Advance(500 * time.Millisecond)
	if after, _ := line(); string(after) != string(still) {
		t.Error("with less motion, the status line still moves")
	}
}
