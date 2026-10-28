package agents

import (
	"context"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/store"
)

func insertAfter(after, id, text string) doc.Op {
	n := &doc.Node{ID: id, Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown(text))
	return doc.Op{Op: "insert", Position: &doc.Position{After: after}, Nodes: []*doc.Node{n}}
}

func ids(c *doc.Content) string {
	var out []string
	for _, n := range c.Children {
		out = append(out, n.ID)
	}
	return strings.Join(out, ",")
}

// A page opened during a run gets the ops it missed once each, in order,
// however many times the run hands them over.
func TestDeliveryToAPageOpenedMidRun(t *testing.T) {
	ops := []doc.Op{insertAfter("p1", "x1", "one")}
	d := &delivery{}
	// The first op comes while the page is closed: nothing to apply it to.
	ed := editor.New(page(), nil)
	ops = append(ops, insertAfter("x1", "x2", "two"))
	d.to(ed, ops)
	ops = append(ops, insertAfter("x2", "x3", "three"))
	d.to(ed, ops)
	// The run ends with the page still open.
	d.to(ed, ops)
	if got := ids(ed.Content); got != "h1,p1,x1,x2,x3,l1" {
		t.Errorf("blocks %s", got)
	}
}

// A page closed during a run keeps what its editor saved, and the ops
// that came after are stored on top.
func TestDeliveryToAPageClosedMidRun(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	ed := editor.New(page(), nil)
	ops := []doc.Op{insertAfter("p1", "x1", "one")}
	d := &delivery{}
	d.to(ed, ops)
	// Leaving the page saves the editor, with an edit of its own.
	ed.Content.Children = append(ed.Content.Children, &doc.Node{ID: "mine", Type: "paragraph"})
	if _, _, err := st.SaveContent(p.ID, ed.Content); err != nil {
		t.Fatal(err)
	}
	ops = append(ops, insertAfter("x1", "x2", "two"))
	if err := d.save(st, p.ID, ops); err != nil {
		t.Fatal(err)
	}
	if got := ids(st.Content(p.ID)); got != "h1,p1,x1,x2,l1,mine" {
		t.Errorf("stored %s", got)
	}
}

// Ops for a closed page whose stored content cannot be read are not saved
// over it as if it were empty.
func TestDeliveryToAnUnreadablePage(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Broken"})
	if err != nil {
		t.Fatal(err)
	}
	const broken = `{"children": [`
	if _, err := st.DB().Exec(`UPDATE page_contents SET content = ? WHERE page_id = ?`, broken, p.ID); err != nil {
		t.Fatal(err)
	}
	d := &delivery{}
	if err := d.save(st, p.ID, []doc.Op{insertAfter("", "x1", "one")}); err == nil {
		t.Error("saved over an unreadable page")
	}
	if row, err := st.ContentRow(p.ID); err != nil || row.Content != broken {
		t.Errorf("content %q, %v", row.Content, err)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// On a page shared with the user to view, a local agent reads but does
// not change the page or make subpages under it.
func TestAgentOnAPageToView(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Theirs", SpaceID: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembers(p.ID, []store.Member{{PageID: p.ID, UserID: st.Session().UserID, Role: "viewer"}}); err != nil {
		t.Fatal(err)
	}
	c := page()
	x := &Executor{Ctx: NewHub(st, nil, nil).toolContext(p.ID, "Theirs", "Test", c, store.Settings{})}
	if !x.Ctx.ReadOnly {
		t.Fatal("tools may edit a page to view")
	}
	for name, args := range map[string]map[string]any{
		"insert_blocks":  {"position": map[string]any{"at": "end"}, "blocks": []any{map[string]any{"type": "paragraph", "text": "x"}}},
		"create_subpage": {"title": "Notes"},
	} {
		if out := x.Call(name, args); out["error"] == nil {
			t.Errorf("%s ran: %v", name, out)
		}
	}
	if out := x.Call("read_page", map[string]any{}); out["error"] != nil {
		t.Errorf("read_page: %v", out)
	}
	if kids, _ := st.Children(p.ID, ""); len(kids) != 0 || ids(c) != "h1,p1,l1" {
		t.Errorf("subpages %d, blocks %s", len(kids), ids(c))
	}
	if defs := ToolDefinitions(false, true); len(defs) != 1 {
		t.Errorf("%d tools offered", len(defs))
	}
}

// A page's chat comes back after a restart, with its runs still undoable
// and a reply that never finished marked as such.
func TestChatSurvivesARestart(t *testing.T) {
	st := openStore(t)
	h := NewHub(st, nil, nil)
	s := h.Session("p")
	run := &Run{ID: "r1", Status: "done", AgentName: "Test", Ops: []doc.Op{insertAfter("p1", "x1", "one")},
		Edits: []Edit{{Summary: "Added a line", BlockIDs: []string{"x1"}}}}
	pending := &Run{ID: "r2", Status: "running"}
	s.Runs = []*Run{run, pending}
	s.Messages = []*Message{
		{Mine: true, Text: "Add a line"}, {Text: "Added a line.", RunID: "r1", AgentName: "Test"},
		{Mine: true, Text: "Again"}, {Text: "Reading the page…", RunID: "r2", Pending: true},
	}
	h.saveChat(s)

	again := NewHub(st, nil, nil).Session("p")
	if again.ConversationID != s.ConversationID || len(again.Messages) != 4 {
		t.Fatalf("conversation %q, %d messages", again.ConversationID, len(again.Messages))
	}
	if m := again.Messages[3]; m.Pending || !m.Error {
		t.Errorf("unfinished reply %+v", m)
	}
	r := again.Run("r1")
	if r == nil || len(r.Ops) != 1 || r.Ops[0].Nodes[0].ID != "x1" || len(r.Edits) != 1 {
		t.Fatalf("run %+v", r)
	}
	if r2 := again.Run("r2"); r2 == nil || r2.Status != "failed" {
		t.Errorf("unfinished run %+v", r2)
	}
	// The run bar is for runs of this launch.
	if again.Latest() != nil {
		t.Error("a restored run shows in the run bar")
	}

	h2 := NewHub(st, nil, nil)
	h2.NewConversation("p")
	if got := NewHub(st, nil, nil).Session("p"); len(got.Messages) != 0 {
		t.Errorf("%d messages after a new conversation", len(got.Messages))
	}
}

// A new conversation keeps the one before among the earlier ones, which
// opens again with its runs, after a restart too.
func TestEarlierConversations(t *testing.T) {
	st := openStore(t)
	h := NewHub(st, nil, nil)
	s := h.Session("p")
	first := s.ConversationID
	s.Runs = []*Run{{ID: "r1", Status: "done", Ops: []doc.Op{insertAfter("p1", "x1", "one")}}}
	s.Messages = []*Message{{Mine: true, Text: "Add a line\nplease"}, {Text: "Added a line.", RunID: "r1"}}
	h.saveChat(s)

	h.NewConversation("p")
	// An empty conversation is not kept.
	h.NewConversation("p")
	if len(s.Messages) != 0 || len(s.Past) != 1 || s.Past[0].ID != first || s.Past[0].Title() != "Add a line" {
		t.Fatalf("%d messages, past %+v", len(s.Messages), s.Past)
	}
	second := s.ConversationID
	s.Messages = []*Message{{Mine: true, Text: "Summarize"}, {Text: "It is a plan."}}
	h.saveChat(s)

	again := NewHub(st, nil, nil)
	got := again.Session("p")
	if len(got.Past) != 1 || got.ConversationID != second {
		t.Fatalf("after a restart: conversation %q, %d earlier", got.ConversationID, len(got.Past))
	}
	again.OpenConversation("p", first)
	if got.ConversationID != first || len(got.Messages) != 2 || got.Messages[0].Text != "Add a line\nplease" {
		t.Fatalf("opened %q: %+v", got.ConversationID, got.Messages)
	}
	if r := got.Run("r1"); r == nil || len(r.Ops) != 1 {
		t.Errorf("run %+v", r)
	}
	if len(got.Past) != 1 || got.Past[0].ID != second || got.Past[0].Title() != "Summarize" {
		t.Errorf("earlier %+v", got.Past)
	}
	if last := NewHub(st, nil, nil).Session("p"); last.ConversationID != first || len(last.Past) != 1 {
		t.Errorf("after another restart: conversation %q, %d earlier", last.ConversationID, len(last.Past))
	}
}

// stubHost is a host with no page open.
type stubHost struct{}

func (stubHost) Update(fn func())                        { fn() }
func (stubHost) Editor(string) *editor.Editor            { return nil }
func (stubHost) SaveNow(string)                          {}
func (stubHost) Online() bool                            { return false }
func (stubHost) Settings() store.Settings                { return store.Settings{} }
func (stubHost) FlushPage(context.Context, string) error { return nil }
func (stubHost) SyncNow()                                {}
func (stubHost) Tint(string, []string)                   {}

// A run undone from the page's bar after its conversation was left stays
// undone in that conversation, after a restart too.
func TestUndoInAnEarlierConversation(t *testing.T) {
	st := openStore(t)
	h := NewHub(st, nil, stubHost{})
	s := h.Session("p")
	first := s.ConversationID
	s.Runs = []*Run{{ID: "r1", Status: "done", Ops: []doc.Op{insertAfter("p1", "x1", "one")}}}
	s.Messages = []*Message{{Mine: true, Text: "Add a line"}, {Text: "Added a line.", RunID: "r1"}}
	h.saveChat(s)
	h.NewConversation("p")
	h.UndoRun("p", "r1")

	again := NewHub(st, nil, stubHost{})
	again.OpenConversation("p", first)
	if r := again.Session("p").Run("r1"); r == nil || !r.Undone {
		t.Fatalf("run after a restart %+v", r)
	}
	again.RedoRun("p", "r1")
	if r := NewHub(st, nil, stubHost{}).Session("p").Run("r1"); r == nil || r.Undone {
		t.Errorf("redone run after a restart %+v", r)
	}
}

// An agent reads the user's other pages only from a page no one else
// has: text other people put in a shared page could have it copy them
// there.
func TestCrossPageOnlyOnTheUsersOwnPages(t *testing.T) {
	st := openStore(t)
	me := st.Session().UserID
	mine, err := st.CreatePage(store.NewPageOptions{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := st.CreatePage(store.NewPageOptions{Title: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembers(shared.ID, []store.Member{{PageID: shared.ID, UserID: me, Role: "owner"}, {PageID: shared.ID, UserID: "friend", Role: "editor"}}); err != nil {
		t.Fatal(err)
	}
	inside, err := st.CreatePage(store.NewPageOptions{Title: "Inside", ParentID: shared.ID})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := st.CreatePage(store.NewPageOptions{Title: "Theirs", SpaceID: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub(st, nil, nil)
	on := store.Settings{AgentsReadOtherPages: true}
	for _, c := range []struct {
		p    store.Page
		want bool
	}{{mine, true}, {shared, false}, {inside, false}, {theirs, false}} {
		tc := h.toolContext(c.p.ID, c.p.Title, "Test", page(), on)
		if got := tc.ListPages != nil; got != c.want {
			t.Errorf("%s: cross-page tools %v, want %v", c.p.Title, got, c.want)
		}
		if got := h.crossPage(c.p, on); got != c.want {
			t.Errorf("%s: cloud runs may read other pages %v, want %v", c.p.Title, got, c.want)
		}
	}
	if tc := h.toolContext(mine.ID, "Mine", "Test", page(), store.Settings{}); tc.ListPages != nil {
		t.Error("cross-page tools while the setting is off")
	}
}

// A page runs one agent at a time: a run asked for while another works on
// the page fails at once, and the first keeps the page's running state.
func TestOneRunAtATimeOnAPage(t *testing.T) {
	st := openStore(t)
	h := NewHub(st, nil, nil)
	s := h.Session("p1")
	s.Running = true
	reply := &Message{Pending: true}
	var done *Run
	run := h.Start("p1", RunOptions{Surface: "inline", Instruction: "go", Reply: reply, Done: func(r *Run) { done = r }})
	if run.Status != "failed" || run.Error != ErrRunning.Error() || done != run {
		t.Errorf("run %+v, done %v", run, done)
	}
	if reply.Pending || !reply.Error {
		t.Errorf("reply %+v", reply)
	}
	if !s.Running || len(s.Runs) != 0 {
		t.Errorf("running %v, runs %d", s.Running, len(s.Runs))
	}
}
