package store

import (
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestKeyBetween(t *testing.T) {
	a := KeyBetween("", "")
	b := KeyBetween(a, "")
	c := KeyBetween(a, b)
	if a >= c || c >= b {
		t.Errorf("keys %q %q %q", a, c, b)
	}
	first := KeyBetween("", a)
	if first >= a {
		t.Errorf("first %q not before %q", first, a)
	}
	for i := 0; i < 50; i++ {
		next := KeyBetween(a, c)
		if a >= next || next >= c {
			t.Fatalf("step %d: %q not between %q and %q", i, next, a, c)
		}
		c = next
	}
}

func TestPagesAndContent(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := s.CreatePage(NewPageOptions{ParentID: p.ID, Title: "Notes"})
	c := s.Content(p.ID)
	c.Children[0].SetDelta(doc.Delta{{Text: "Ship the editor"}})
	if _, changed, err := s.SaveContent(p.ID, c); err != nil || !changed {
		t.Fatalf("save %v %v", changed, err)
	}
	if _, changed, _ := s.SaveContent(p.ID, s.Content(p.ID)); changed {
		t.Error("saving the same content changed it")
	}
	hits, _ := s.Search("editor", 10)
	if len(hits) != 1 || hits[0].PageID != p.ID {
		t.Errorf("hits %+v", hits)
	}
	// One outbox entry per changed row.
	n := 0
	entries, _ := s.Pending()
	for _, e := range entries {
		if e.Entity == "page_content" && e.EntityID == p.ID {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d page_content entries", n)
	}
	if err := s.MovePage(p.ID, kid.ID, "", ""); err != ErrMoveIntoSelf {
		t.Errorf("move into own child: %v", err)
	}
	ids, _ := s.DeletePage(p.ID)
	if len(ids) != 2 {
		t.Errorf("deleted %v", ids)
	}
	if pages, _ := s.Pages(); len(pages) != 0 {
		t.Errorf("pages left %d", len(pages))
	}
	if err := s.RestorePages(ids); err != nil {
		t.Fatal(err)
	}
	if pages, _ := s.Pages(); len(pages) != 2 {
		t.Errorf("restored %d", len(pages))
	}
}

func TestComments(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "x"})
	root, _ := s.AddComment(p.ID, "b1", "Is this right?", "", "quote", "", "")
	if _, err := s.AddComment(p.ID, "b1", "Yes", root.ID, "", "agent", "Claude"); err != nil {
		t.Fatal(err)
	}
	threads, _ := s.Threads(p.ID)
	if len(threads) != 1 || len(threads[0].Replies) != 1 || threads[0].Replies[0].AuthorName != "Claude" {
		t.Fatalf("threads %+v", threads)
	}
	if err := s.SetResolved(root.ID, true); err != nil {
		t.Fatal(err)
	}
	threads, _ = s.Threads(p.ID)
	if !threads[0].Root.Resolved {
		t.Error("not resolved")
	}
}

func TestJobEditors(t *testing.T) {
	s := open(t)
	edit := map[string]any{"ops": []any{map[string]any{"op": "insert"}}, "reply": ""}
	answer := map[string]any{"ops": nil, "reply": "Hello"}
	for _, j := range []Job{
		{ID: "1", PageID: "p", ProviderID: "chatgpt", Status: "done", Result: edit},
		{ID: "2", PageID: "p", ProviderID: "ollama", Status: "done", Result: answer},
		{ID: "3", PageID: "p", ProviderID: "claude", Status: "failed", Result: edit},
		{ID: "4", PageID: "p", ProviderID: "chatgpt", Status: "done", Result: edit},
		{ID: "5", PageID: "q", ProviderID: "claude", Status: "done", Result: edit},
	} {
		if err := s.PutJob(j); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.JobEditors("p"); len(got) != 1 || got[0] != "chatgpt" {
		t.Errorf("got %q", got)
	}
}

func TestSnippet(t *testing.T) {
	text := "Day 1: temples\nKiyomizu-dera\nDay 2: Arashiyama\n\nBook hotel\nBuy JR pass\nTravel is the only thing you buy that makes you richer."
	got := snippet(text, "hotel")
	if got != "…Day 2: Arashiyama · Book hotel · Buy JR pass · Travel is the only thing you…" {
		t.Errorf("snippet %q", got)
	}
	if got := snippet("short note", "note"); got != "short note" {
		t.Errorf("snippet %q", got)
	}
	if got := snippet("nothing here", "hotel"); got != "" {
		t.Errorf("snippet %q", got)
	}
}

func TestDequeueKeepsNewerPayload(t *testing.T) {
	s := open(t)
	if err := s.Queue("pages", "p1", map[string]any{"title": "Draft"}, "", "p1"); err != nil {
		t.Fatal(err)
	}
	sent, _ := s.Pending()
	// Renamed again while the first rename was on its way.
	if err := s.Queue("pages", "p1", map[string]any{"title": "Final"}, "", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dequeue(sent[0]); err != nil {
		t.Fatal(err)
	}
	left, _ := s.Pending()
	if len(left) != 1 || left[0].Payload != `{"title":"Final"}` {
		t.Fatalf("left %+v", left)
	}
	if err := s.Dequeue(left[0]); err != nil {
		t.Fatal(err)
	}
	if s.HasPending("pages", "p1") {
		t.Error("still pending after the latest was sent")
	}
}

func TestSignInAsAnotherAccountForgetsThePrevious(t *testing.T) {
	s := open(t)
	if _, err := s.CreatePage(NewPageOptions{Title: "A's plan"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProvider(Provider{ID: "ollama", Preset: "ollama", Name: "Ollama", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProvider(Provider{ID: "cloud", Preset: "openai", Name: "OpenAI"}); err != nil {
		t.Fatal(err)
	}
	// What sync does on A's first sign-in: adopt the device's space.
	local := s.Session().SpaceID
	for k, v := range map[string]string{"space.A": "remote-a", "space.local": "remote-a", "account": "A", "sync.cursor.pages": "t|p"} {
		_ = s.SetValue(k, v)
	}
	if err := s.MoveSpace(local, "remote-a"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.LoadSession(&User{ID: "A", Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if pages, _ := s.Pages(); len(pages) != 1 {
		t.Fatalf("A signing back in: %d pages", len(pages))
	}

	sess, err := s.LoadSession(&User{ID: "B", Email: "b@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if pages, _ := s.Pages(); len(pages) != 0 {
		t.Errorf("B sees %d of A's pages", len(pages))
	}
	if s.PendingCount() != 0 {
		t.Errorf("%d of A's changes still queued", s.PendingCount())
	}
	if sess.SpaceID == "remote-a" {
		t.Error("B's session is in A's space")
	}
	for _, k := range []string{"space.A", "account", "sync.cursor.pages"} {
		if _, ok := s.Value(k); ok {
			t.Errorf("%s kept", k)
		}
	}
	providers, _ := s.Providers()
	if len(providers) != 1 || providers[0].ID != "ollama" {
		t.Errorf("providers %+v", providers)
	}
}

func TestWatchOthers(t *testing.T) {
	dir := t.TempDir()
	app, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	other, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	seen := make(chan struct{}, 10)
	stop := make(chan struct{})
	defer close(stop)
	go app.WatchOthers(10*time.Millisecond, stop, func() { seen <- struct{}{} })
	time.Sleep(30 * time.Millisecond)
	// The app's own writes are not news to it.
	if err := app.SetValue("own", "1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(seen) != 0 {
		t.Fatal("the app's own write counted as another's")
	}
	if err := other.SetValue("theirs", "1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("another process's write went unseen")
	}
}

func TestSavedContentReadsBackTheSame(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	c := s.Content(p.ID)
	n := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown("Some **bold** text"))
	_ = c.Insert(doc.Position{At: "end"}, n)
	stamped, _, err := s.SaveContent(p.ID, c)
	if err != nil {
		t.Fatal(err)
	}
	// The app tells another process's save from its own by this.
	if got := s.Content(p.ID).Encode(); got != stamped.Encode() {
		t.Errorf("stored %s\nsaved  %s", got, stamped.Encode())
	}
}

func TestCopyPage(t *testing.T) {
	s := open(t)
	para := func(text string, a doc.Attrs) *doc.Node {
		n := &doc.Node{Type: "paragraph"}
		n.SetDelta(doc.Delta{{Text: text, Attrs: a}})
		return n
	}
	before, _ := s.CreatePage(NewPageOptions{Title: "Before"})
	prd, _ := s.CreatePage(NewPageOptions{Title: "PRD", Icon: "📄"})
	if _, err := s.CreatePage(NewPageOptions{Title: "After"}); err != nil {
		t.Fatal(err)
	}
	faq, _ := s.CreatePage(NewPageOptions{ParentID: prd.ID, Title: "FAQ", Content: doc.FromBlocks([]*doc.Node{para("Questions", doc.Attrs{})})})
	risks, _ := s.CreatePage(NewPageOptions{ParentID: prd.ID, Title: "Risks"})
	deep, _ := s.CreatePage(NewPageOptions{ParentID: faq.ID, Title: "Deep"})
	body := doc.FromBlocks([]*doc.Node{
		para("FAQ", doc.PageMention(faq.ID, "FAQ")),
		{Type: "subpage", Data: map[string]any{"page_id": risks.ID}},
		para("Elsewhere", doc.PageMention(before.ID, "Before")),
	})
	if _, _, err := s.SaveContent(prd.ID, body); err != nil {
		t.Fatal(err)
	}

	cp, err := s.CopyPage(prd.ID, CopyOptions{AfterID: prd.ID, Title: "PRD copy"})
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID == prd.ID || cp.Title != "PRD copy" || cp.Icon != "📄" || cp.Template {
		t.Fatalf("copy %+v", cp)
	}
	pages, _ := s.Children("", s.Session().SpaceID)
	var order []string
	for _, p := range pages {
		order = append(order, p.Title)
	}
	if strings.Join(order, ",") != "Before,PRD,PRD copy,After" {
		t.Errorf("order %v", order)
	}
	kids, _ := s.Children(cp.ID, "")
	if len(kids) != 2 || kids[0].Title != "FAQ" || kids[1].Title != "Risks" || kids[0].ID == faq.ID {
		t.Fatalf("copied subpages %+v", kids)
	}
	if grand, _ := s.Children(kids[0].ID, ""); len(grand) != 1 || grand[0].Title != "Deep" || grand[0].ID == deep.ID {
		t.Errorf("copied grandchildren %+v", grand)
	}
	if c := s.Content(kids[0].ID); c.PlainText() != "Questions" {
		t.Errorf("subpage content %q", c.PlainText())
	}
	c := s.Content(cp.ID)
	if m := c.Children[0].Delta()[0].Attrs.Mention; m.ID != kids[0].ID {
		t.Errorf("mention of a subpage goes to %s, not its copy", m.ID)
	}
	if got := c.Children[1].Str("page_id"); got != kids[1].ID {
		t.Errorf("subpage block goes to %s, not the copy", got)
	}
	if m := c.Children[2].Delta()[0].Attrs.Mention; m.ID != before.ID {
		t.Errorf("mention of another page changed to %s", m.ID)
	}
	if c.Children[0].ID == body.Children[0].ID {
		t.Error("block IDs were kept")
	}

	// A template and its subpages stay out of the page list.
	tpl, err := s.CopyPage(prd.ID, CopyOptions{Title: "PRD", Template: true})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := s.Pages()
	for _, p := range all {
		if p.Template || p.ID == tpl.ID {
			t.Errorf("template %q in Pages", p.Title)
		}
	}
	templates, _ := s.Templates()
	if len(templates) != 4 {
		t.Errorf("%d template pages, want the template and its 3 subpages", len(templates))
	}

	// Starting a page from it copies its subpages as pages.
	page, _ := s.CreatePage(NewPageOptions{Title: "New PRD"})
	in, err := s.CopyInto(tpl.ID, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	kids, _ = s.Children(page.ID, "")
	if len(kids) != 2 || kids[0].Template {
		t.Fatalf("subpages from the template %+v", kids)
	}
	if got := in.Children[1].Str("page_id"); got != kids[1].ID {
		t.Errorf("content from the template links %s, not the new subpage", got)
	}
}

// Search finds templates by title and text, marked as templates, and
// leaves them out of the recent pages.
func TestSearchFindsTemplates(t *testing.T) {
	s := open(t)
	body := func(text string) *doc.Content {
		n := &doc.Node{Type: "paragraph"}
		n.SetDelta(doc.Delta{{Text: text}})
		return doc.FromBlocks([]*doc.Node{n})
	}
	if _, err := s.CreatePage(NewPageOptions{Title: "Postmortem: outage", Content: body("Timeline of the outage")}); err != nil {
		t.Fatal(err)
	}
	tpl, err := s.CreatePage(NewPageOptions{Title: "Postmortem", Template: true, Content: body("Timeline and root cause")})
	if err != nil {
		t.Fatal(err)
	}
	kinds := func(hits []SearchHit) map[string]string {
		out := map[string]string{}
		for _, h := range hits {
			out[h.Title] = h.Kind
		}
		return out
	}
	hits, _ := s.Search("postmortem", 10)
	if k := kinds(hits); k["Postmortem"] != "template" || k["Postmortem: outage"] != "page" {
		t.Errorf("by title %+v", hits)
	}
	hits, _ = s.Search("root cause", 10)
	if len(hits) != 1 || hits[0].Kind != "template" || hits[0].PageID != tpl.ID || hits[0].Snippet == "" {
		t.Errorf("by text %+v", hits)
	}
	hits, _ = s.Search("", 10)
	if _, ok := kinds(hits)["Postmortem"]; ok {
		t.Errorf("a template in recents %+v", hits)
	}
}

// A named version stays in a page's history past its newest 50.
func TestHistoryKeepsNamedVersions(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "PRD"})
	c := s.Content(p.ID)
	if err := s.AddHistory(p.ID, c, "manual", "", "Sent to legal"); err != nil {
		t.Fatal(err)
	}
	// Named a day before the others.
	if _, err := s.db.Exec(`UPDATE page_history SET created_at = created_at - 86400000 WHERE reason = 'manual'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		if err := s.AddHistory(p.ID, c, "autosave", "", "Autosave"); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.History(p.ID)
	if err != nil || len(h) != 51 || h[len(h)-1].Label != "Sent to legal" {
		t.Errorf("%d entries, last %+v, %v", len(h), h[len(h)-1], err)
	}
}

// To-dos come from pages, not templates.
func TestTodos(t *testing.T) {
	s := open(t)
	todo := &doc.Node{Type: "todo_list"}
	todo.SetDelta(doc.Delta{{Text: "Send the draft"}})
	todo.SetDue(time.Now())
	p, _ := s.CreatePage(NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{todo})})
	if _, err := s.CopyPage(p.ID, CopyOptions{Title: "PRD", Template: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Todos()
	if err != nil || len(got) != 1 || got[0].PageID != p.ID || got[0].PageTitle != "PRD" || got[0].Text != "Send the draft" {
		t.Errorf("todos %+v %v", got, err)
	}
}
