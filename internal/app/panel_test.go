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
	a.undoRun(p.ID, run)
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

// A chat asks about the block the caret was left in, sent from the
// composer, where the focus then is; a page whose caret was never placed
// gives none.
func TestChatTakesTheCaretsBlock(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{textNode("Hello there")})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	if got := a.focusBlocks(); got != nil {
		t.Fatalf("before the caret was placed: %v", got)
	}
	block := a.page.editor.Content.Children[0].ID
	a.page.editor.PlaceCaret(block, 3)
	a.page.editor.Focus()
	tt.Frame()
	tt.Frame()
	if !a.page.editor.Focused() {
		t.Fatal("the page didn't take the focus")
	}
	// The title takes the focus, as the composer would.
	a.focusTitle(false)
	tt.Frame()
	if a.page.editor.Focused() {
		t.Fatal("the page kept the focus")
	}
	if got := a.focusBlocks(); len(got) != 1 || got[0] != block {
		t.Errorf("from the composer: %v, want %s", got, block)
	}
}

// commentsApp is an app with a page open, Plan, of blocks b1 and b2, and
// the comments panel beside it.
func commentsApp(t *testing.T) (*App, store.Page) {
	t.Helper()
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	b1, b2 := textNode("Goals"), textNode("Risks")
	b1.ID, b2.ID = "b1", "b2"
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan", Content: doc.FromBlocks([]*doc.Node{b1, b2})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	if a.page == nil {
		t.Fatal("the page did not open")
	}
	a.panelOpen, a.panelTab = true, 1
	return a, p
}

// Enter posts a new comment and sends a reply, as it sends a chat, and
// Cmd+Enter too; Shift+Enter breaks the line.
func TestEnterPostsComments(t *testing.T) {
	a, p := commentsApp(t)
	a.dialogs.newComment = &newComment{blockID: "b1"}
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	tt.Type("Why May?")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("Not June")
	tt.Frame()
	if nc := a.dialogs.newComment; nc == nil || nc.text != "Why May?\nNot June" {
		t.Fatalf("new comment %+v", nc)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	threads, _ := a.st.Threads(p.ID)
	if a.dialogs.newComment != nil || len(threads) != 1 || threads[0].Root.Body != "Why May?\nNot June" {
		t.Fatalf("after Enter: box %v, threads %+v", a.dialogs.newComment != nil, threads)
	}
	a.page.setThreads(a.st.Threads(p.ID))
	tt.Frame()
	// The reply box is under the thread's comment.
	body, ok := tt.Find("Why May? Not June")
	if !ok {
		t.Fatalf("no comment in %q", tt.Texts())
	}
	tt.ClickAt(body.X+20, body.Y+body.H+10+17)
	tt.Type("The venue")
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	if threads, _ = a.st.Threads(p.ID); len(threads) != 1 || len(threads[0].Replies) != 1 || threads[0].Replies[0].Body != "The venue" {
		t.Errorf("after Cmd+Enter in the reply box: %+v", threads)
	}
}

// The threads of a block asked for, from its badge or a search, are lit
// for a moment, every one of them, and then are not.
func TestCommentsAskedForStayLit(t *testing.T) {
	a, p := commentsApp(t)
	for _, c := range []struct{ block, body string }{{"b1", "First question"}, {"b1", "Second question"}, {"b2", "Other question"}} {
		if _, err := a.st.AddComment(p.ID, c.block, c.body, "", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	a.page.setThreads(a.st.Threads(p.ID))
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	// A card's border, left of its comment's text, by the card's padding
	// and the comment's indent past its avatar.
	border := func(body string) [4]uint8 {
		t.Helper()
		r, ok := tt.Find(body)
		if !ok {
			t.Fatalf("no %q in %q", body, tt.Texts())
		}
		img := tt.Image()
		scale := float32(img.Bounds().Dx()) / 1300
		px := img.RGBAAt(int((r.X-28-12-0.75)*scale), int((r.Y+r.H/2)*scale))
		return [4]uint8{px.R, px.G, px.B, px.A}
	}
	bodies := []string{"First question", "Second question", "Other question"}
	var before [3][4]uint8
	for i, b := range bodies {
		before[i] = border(b)
	}
	(&host{a: a}).OpenComments("b1")
	tt.Frame()
	tt.Frame()
	for i, b := range bodies {
		if lit := border(b) != before[i]; lit != (i < 2) {
			t.Errorf("%q lit %v after two frames", b, lit)
		}
	}
	tt.Advance(commentLit)
	tt.Frame()
	for i, b := range bodies {
		if border(b) != before[i] {
			t.Errorf("%q still lit after %v", b, commentLit)
		}
	}
	if a.dialogs.commentFocus != "" {
		t.Errorf("focus %q", a.dialogs.commentFocus)
	}
}

// A comment that could not be posted, and an agent asked in one that
// could not answer, say so in the app's words around why.
func TestCommentErrorsSaySo(t *testing.T) {
	a, _ := commentsApp(t)
	if _, err := a.st.DB().Exec(`CREATE TRIGGER full BEFORE INSERT ON comments BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	a.dialogs.newComment = &newComment{blockID: "b1", text: "Why May?"}
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	if err := tt.Click(locales.CommonComment()); err != nil {
		t.Fatal(err)
	}
	prefix := func(msg string) string { return strings.TrimSuffix(msg, " ") }
	if !tt.HasText(prefix(locales.PanelCouldNotPost(""))) {
		t.Fatalf("no toast in %q", tt.Texts())
	}
	if a.dialogs.newComment == nil {
		t.Error("the comment not posted was dropped")
	}

	// A model that cannot be reached.
	hs := httptest.NewServer(http.NotFoundHandler())
	hs.Close()
	var mu sync.Mutex
	var posted []func()
	a.post = func(fn func()) {
		mu.Lock()
		posted = append(posted, fn)
		mu.Unlock()
	}
	a.online, a.toasts = true, nil
	if err := a.st.PutProvider(store.Provider{ID: "test-gone-" + t.Name(), Preset: "ollama", Name: "Ollama", BaseURL: hs.URL,
		AuthKind: "none", DefaultModel: "llama3", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.agentInComment(store.Comment{ID: "c1", BlockID: "b1"}, "@Ollama why May?")
	for deadline := time.Now().Add(10 * time.Second); len(a.toasts) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("no toast")
		}
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		fns := posted
		posted = nil
		mu.Unlock()
		for _, fn := range fns {
			fn()
		}
	}
	if !strings.HasPrefix(a.toasts[0].text, prefix(locales.PanelAgentFailed("Ollama", ""))) {
		t.Errorf("toast %q", a.toasts[0].text)
	}
}

// What was typed in a page's chat and reply boxes and not sent stays with
// the page as it closes and opens again, and goes once the page is gone.
func TestDraftsStayWithTheirPage(t *testing.T) {
	a := templatesApp(t)
	plan, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	other, _ := a.st.CreatePage(store.NewPageOptions{Title: "Other"})
	a.reloadAll()
	a.openPage(plan.ID)
	a.draftsOf(plan.ID).chat.draft = "Summarize"
	a.draftsOf(plan.ID).replies["c1"] = &threadDraft{text: "Agreed"}
	a.openPage(other.ID)
	a.openPage(plan.ID)
	if d := a.draftsOf(plan.ID); d.chat.draft != "Summarize" || d.replies["c1"] == nil || d.replies["c1"].text != "Agreed" {
		t.Fatalf("drafts back on the page: %+v", d)
	}
	if b := templatesApp(t); len(b.drafts) != 0 {
		t.Errorf("another app has drafts %v", b.drafts)
	}
	a.openPage(other.ID)
	if _, err := a.st.DeletePage(plan.ID); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "pages"})
	if _, ok := a.drafts[plan.ID]; ok {
		t.Error("the drafts of a page deleted stay")
	}
}

// A page an agent made opens from its edit in the chat, and an edit of
// this page's blocks still goes to them.
func TestCreatedPageEditOpensIt(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	if err := a.st.PutProvider(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	made, err := a.st.CreatePage(store.NewPageOptions{Title: "Article"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.panelOpen, a.panelTab = true, 0
	sess := a.hub.Session(p.ID)
	summary := locales.AgentEditCreatedPage("Article")
	sess.Runs = []*agents.Run{{ID: "r1", Status: "done", AgentName: "Ollama",
		Ops:   []doc.Op{{Op: "create_page", Page: &doc.NewPage{ID: made.ID, Title: "Article"}}},
		Edits: []agents.Edit{{Summary: summary, PageID: made.ID}}}}
	sess.Messages = []*agents.Message{{Mine: true, Text: "Save the article"}, {Text: "Saved it.", RunID: "r1"}}

	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	if err := tt.Click(summary); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.route.Kind != "page" || a.route.ID != made.ID {
		t.Errorf("route %+v, want the page made", a.route)
	}
}

// Undo of a run that made a subpage takes its card out of the open page
// and the subpage away, and Redo puts back one card, not two: the run's
// own and the one a subpage brought back gets.
func TestUndoOfASubpageRunOnTheOpenPage(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	if a.page == nil {
		t.Fatal("the page did not open")
	}
	sub, err := a.st.CreatePage(store.NewPageOptions{ParentID: p.ID, Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	card := doc.InsertOp(doc.Position{At: "end"}, doc.SubpageNode(sub.ID))
	if err := a.Live(p.ID).ApplyOps([]doc.Op{card}); err != nil {
		t.Fatal(err)
	}
	run := &agents.Run{ID: "r1", Status: "done", AgentName: "Ollama", Ops: []doc.Op{card,
		{Op: "create_page", Page: &doc.NewPage{ID: sub.ID, ParentID: p.ID, Title: "Notes"}}}}
	a.hub.Session(p.ID).Runs = []*agents.Run{run}
	cards := func() int {
		n := 0
		a.page.live.Doc().Walk(func(b, _ *doc.Node) {
			if b.Type == "subpage" && b.Str("page_id") == sub.ID {
				n++
			}
		})
		return n
	}
	a.undoRun(p.ID, run)
	if got, _ := a.st.Page(sub.ID); !run.Undone || cards() != 0 || got.DeletedAt.IsZero() {
		t.Errorf("after Undo: undone %v, %d cards, subpage deleted %v", run.Undone, cards(), !got.DeletedAt.IsZero())
	}
	a.redoRun(p.ID, run)
	if got, _ := a.st.Page(sub.ID); run.Undone || cards() != 1 || !got.DeletedAt.IsZero() {
		t.Errorf("after Redo: undone %v, %d cards, subpage deleted %v", run.Undone, cards(), !got.DeletedAt.IsZero())
	}
}
