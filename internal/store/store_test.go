package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
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

// An edit queued while another process sends the queue is never lost:
// right after it is queued, it either waits or went up.
func TestQueueWhileSending(t *testing.T) {
	dir := t.TempDir()
	app, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	sync, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sync.Close() }()
	var mu gosync.Mutex
	sent := map[string]bool{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			entries, _ := sync.Pending()
			for _, e := range entries {
				mu.Lock()
				sent[e.Payload] = true
				mu.Unlock()
				_ = sync.Dequeue(e)
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for i := range 1000 {
		if err := app.Queue("pages", "p1", map[string]any{"title": i}, "", "p1"); err != nil {
			t.Fatal(err)
		}
		payload := `{"title":` + strconv.Itoa(i) + `}`
		mu.Lock()
		left, _ := app.Pending()
		waits := len(left) > 0 && left[len(left)-1].Payload == payload
		if !waits && !sent[payload] {
			mu.Unlock()
			t.Fatalf("edit %d was lost", i)
		}
		mu.Unlock()
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
	device := s.DeviceID()
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
	if s.DeviceID() == device {
		t.Error("B uses the device row A registered")
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

// A page keeps only the snapshots History shows: its newest 50 and its
// named versions. One pruned before it went up leaves the queue, and
// another page's are left alone.
func TestHistoryPrunesWhatItNoLongerShows(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "PRD"})
	other, _ := s.CreatePage(NewPageOptions{Title: "Notes"})
	c := s.Content(p.ID)
	if err := s.AddHistory(other.ID, c, "autosave", "", "Autosave"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHistory(p.ID, c, "manual", "", "Sent to legal"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if err := s.AddHistory(p.ID, c, "autosave", "", "Autosave "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	shown, err := s.History(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) != 51 || shown[0].Label != "Autosave 59" || shown[49].Label != "Autosave 10" || shown[50].Label != "Sent to legal" {
		t.Fatalf("%d shown: first %q, 50th %q, last %q", len(shown), shown[0].Label, shown[49].Label, shown[len(shown)-1].Label)
	}
	var kept int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM page_history WHERE page_id = ?`, p.ID).Scan(&kept); err != nil || kept != 51 {
		t.Errorf("%d kept, %v", kept, err)
	}
	ids := map[string]bool{}
	for _, h := range shown {
		ids[h.ID] = true
	}
	entries, _ := s.Pending()
	queued := 0
	for _, e := range entries {
		if e.Entity != "page_history" || e.PageID != p.ID {
			continue
		}
		queued++
		if !ids[e.EntityID] {
			t.Errorf("snapshot %s was pruned but is still queued", e.EntityID)
		}
	}
	if queued != 51 {
		t.Errorf("%d snapshots queued, want 51", queued)
	}
	if h, _ := s.History(other.ID); len(h) != 1 {
		t.Errorf("the other page has %d snapshots", len(h))
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

// A local agent run's snapshot keeps its job here, and goes up without
// it, as the job has no server row.
func TestAgentRunHistoryGoesUpWithoutItsJob(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddHistory(p.ID, doc.Empty(), "agent_run", "job-1", "Before Test"); err != nil {
		t.Fatal(err)
	}
	history, _ := s.History(p.ID)
	if len(history) != 1 || history[0].AIJobID != "job-1" {
		t.Fatalf("history %+v", history)
	}
	entries, _ := s.Pending()
	for _, e := range entries {
		if e.Entity == "page_history" && strings.Contains(e.Payload, "ai_job_id") {
			t.Errorf("queued %s", e.Payload)
		}
	}
}

func TestSaveContentOver(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	read, _ := s.ContentRow(p.ID)
	agent := doc.MustDecode(read.Content)
	agent.Children[0].SetDelta(doc.Delta{{Text: "From the agent"}})
	// Someone else saves after the agent read the page.
	human := doc.MustDecode(read.Content)
	human.Children[0].SetDelta(doc.Delta{{Text: "Typed"}})
	if _, _, err := s.SaveContent(p.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveContentOver(p.ID, read.Content, agent); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("saved over a change: %v", err)
	}
	if got := s.Content(p.ID).PlainText(); !strings.Contains(got, "Typed") {
		t.Errorf("content %q", got)
	}
	now, _ := s.ContentRow(p.ID)
	if _, changed, err := s.SaveContentOver(p.ID, now.Content, agent); err != nil || !changed {
		t.Fatalf("save %v %v", changed, err)
	}
	if got := s.Content(p.ID).PlainText(); !strings.Contains(got, "From the agent") {
		t.Errorf("content %q", got)
	}
}

// Saving over no content saves only while the page has none.
func TestSaveContentOverNone(t *testing.T) {
	s := open(t)
	c := &doc.Content{Children: []*doc.Node{{ID: "a", Type: "paragraph"}}}
	c.Children[0].SetDelta(doc.Delta{{Text: "First"}})
	if _, changed, err := s.SaveContentOver("p1", "", c); err != nil || !changed {
		t.Fatalf("save over none: %v %v", changed, err)
	}
	if !s.HasPending("page_content", "p1") {
		t.Error("not queued")
	}
	c.Children[0].SetDelta(doc.Delta{{Text: "Second"}})
	if _, _, err := s.SaveContentOver("p1", "", c); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("saved over content as over none: %v", err)
	}
	if got := s.Content("p1").PlainText(); !strings.Contains(got, "First") {
		t.Errorf("content %q", got)
	}
}

// writeDB makes a database at path with the statements, as an earlier
// version of Leaflet would have.
func writeDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func userVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpensADatabaseFromBeforeTheFirstRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	// The tables whose columns changed, as the first commit made them.
	writeDB(t, path,
		`CREATE TABLE pages (
		  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, parent_id TEXT, folder_id TEXT,
		  title TEXT NOT NULL DEFAULT '', icon TEXT, sort_key TEXT NOT NULL, created_by TEXT,
		  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER,
		  keep_offline INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE page_contents (
		  page_id TEXT PRIMARY KEY, content TEXT NOT NULL, plain_text TEXT NOT NULL DEFAULT '',
		  updated_at INTEGER NOT NULL, base_content TEXT, remote_updated_at INTEGER)`,
		`CREATE TABLE outbox (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, entity TEXT NOT NULL, entity_id TEXT NOT NULL, op TEXT NOT NULL,
		  payload TEXT NOT NULL, label TEXT NOT NULL DEFAULT '', page_id TEXT, attempts INTEGER NOT NULL DEFAULT 0,
		  last_error TEXT, created_at INTEGER NOT NULL)`,
		`INSERT INTO pages (id, space_id, title, sort_key, created_at, updated_at) VALUES ('p1', 's1', 'Plan', 'a', 1, 1)`,
		`INSERT INTO page_contents (page_id, content, plain_text, updated_at, remote_updated_at) VALUES ('p1', '`+
			(&doc.Content{Children: []*doc.Node{{ID: "a", Type: "paragraph"}}, Blocks: map[string]int64{}, Deleted: map[string]int64{}}).Encode()+
			`', '', 1, 1)`,
		`INSERT INTO outbox (entity, entity_id, op, payload, page_id, created_at) VALUES ('page_content', 'p1', 'upsert', '{}', 'p1', 1)`,
	)
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := userVersion(t, s); v != len(migrations) {
		t.Errorf("schema %d, want %d", v, len(migrations))
	}
	p, err := s.Page("p1")
	if err != nil || p.Title != "Plan" || p.Template {
		t.Fatalf("page %+v, %v", p, err)
	}
	row, err := s.ContentRow("p1")
	if err != nil || row.Content == "" || row.RemoteRevision != 0 {
		t.Fatalf("content %+v, %v", row, err)
	}
	pending, err := s.Pending()
	if err != nil || len(pending) != 1 || pending[0].EntityID != "p1" {
		t.Fatalf("outbox %+v, %v", pending, err)
	}
	if _, err := s.CreatePage(NewPageOptions{Title: "Next"}); err != nil {
		t.Fatal(err)
	}
}

func TestOpensADatabaseFromARelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	// The releases made the schema as it is, and kept no version.
	writeDB(t, path, schema,
		`INSERT INTO pages (id, space_id, title, sort_key, created_at, updated_at, is_template) VALUES ('p1', 's1', 'Plan', 'a', 1, 1, 1)`)
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := userVersion(t, s); v != len(migrations) {
		t.Errorf("schema %d, want %d", v, len(migrations))
	}
	if p, err := s.Page("p1"); err != nil || !p.Template {
		t.Errorf("page %+v, %v", p, err)
	}
}

func TestNewDatabaseHasTheLatestSchema(t *testing.T) {
	if v := userVersion(t, open(t)); v != len(migrations) {
		t.Errorf("schema %d, want %d", v, len(migrations))
	}
}

func TestRefusesADatabaseFromANewerVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	writeDB(t, path, schema, `PRAGMA user_version = 1000`)
	if s, err := OpenPath(path, dir); err == nil {
		_ = s.Close()
		t.Fatal("opened a database from a newer version")
	}
}

func TestADatabaseGainsTheICloudTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	// A release's schema, from before iCloud sync.
	before := strings.Split(schema, "CREATE TABLE IF NOT EXISTS peer_contents")[0]
	writeDB(t, path, before, `PRAGMA user_version = 1`)
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.PutPeerContent("d1", "p1", 5, "{}"); err != nil {
		t.Fatal(err)
	}
	if p, ok := s.PeerContent("d1", "p1"); !ok || p.Version != 5 {
		t.Errorf("peer content %+v %v", p, ok)
	}
	if err := s.SetRowVersion("pages", "p1", RowVersion{At: 9, Device: "d1"}); err != nil {
		t.Fatal(err)
	}
	if v := s.RowVersion("pages", "p1"); v != (RowVersion{9, "d1"}) {
		t.Errorf("row version %+v", v)
	}
	if err := s.ForgetICloud(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.PeerContent("d1", "p1"); ok {
		t.Error("peer content kept after ForgetICloud")
	}
}

// A save stores its content and queues it together: content another
// process finds changed is always waiting to go up, so a pull never takes
// it for the server's.
func TestSavedContentIsAlwaysQueued(t *testing.T) {
	dir := t.TempDir()
	app, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	if _, err := app.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	p, err := app.CreatePage(NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	sent, _ := app.Pending()
	for _, e := range sent {
		_ = app.Dequeue(e)
	}
	first, _ := app.ContentRow(p.ID)
	sync, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sync.Close() }()
	stop := make(chan struct{})
	lost := make(chan string, 1)
	go func() {
		defer close(lost)
		for {
			select {
			case <-stop:
				return
			default:
			}
			row, err := sync.ContentRow(p.ID)
			if err == nil && row.Content != first.Content && !sync.HasPending("page_content", p.ID) {
				lost <- row.Content
				return
			}
		}
	}()
	for i := range 200 {
		c := doc.MustDecode(first.Content)
		c.Children[0].SetDelta(doc.Delta{{Text: "edit " + strconv.Itoa(i)}})
		if _, _, err := app.SaveContent(p.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	if c, ok := <-lost; ok {
		t.Fatalf("content stored without its entry in the outbox: %s", c)
	}
}

// A save whose entry in the outbox cannot be stored stores nothing, so
// the edit is not kept here only, never to go up.
func TestASaveThatCannotQueueStoresNothing(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.ContentRow(p.ID)
	if _, err := s.db.Exec(`DROP TABLE outbox`); err != nil {
		t.Fatal(err)
	}
	c := doc.MustDecode(before.Content)
	c.Children[0].SetDelta(doc.Delta{{Text: "Typed"}})
	if _, _, err := s.SaveContent(p.ID, c); err == nil {
		t.Fatal("saved with no outbox")
	}
	if after, _ := s.ContentRow(p.ID); after.Content != before.Content {
		t.Errorf("content stored without its entry: %s", after.Content)
	}
	if _, err := s.CreatePage(NewPageOptions{ID: "p2", Title: "Next"}); err == nil {
		t.Fatal("created a page with no outbox")
	}
	if _, err := s.Page("p2"); !IsNotFound(err) {
		t.Errorf("page stored without its entry: %v", err)
	}
	if s.Content("p2") != nil {
		t.Error("content stored without its entry")
	}
}

func TestADatabaseGainsTheIndexes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	// Schema 1, without the indexes schema 2 added.
	var before []string
	for _, stmt := range strings.Split(schema, ";") {
		if !strings.Contains(stmt, "pages_parent") && !strings.Contains(stmt, "page_history_page") && !strings.Contains(stmt, "peer_contents_page") {
			before = append(before, stmt)
		}
	}
	writeDB(t, path, strings.Join(before, ";"), `PRAGMA user_version = 1`,
		`INSERT INTO pages (id, space_id, parent_id, title, sort_key, created_at, updated_at) VALUES ('p2', 's1', 'p1', 'Sub', 'a', 1, 1)`)
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := userVersion(t, s); v != len(migrations) {
		t.Errorf("schema %d, want %d", v, len(migrations))
	}
	for _, name := range []string{"pages_parent", "page_history_page", "peer_contents_page"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s: %d, %v", name, n, err)
		}
	}
	if kids, err := s.Children("p1", "s1"); err != nil || len(kids) != 1 {
		t.Errorf("children %+v, %v", kids, err)
	}
	// What the queries use.
	for q, index := range map[string]string{
		`SELECT id FROM pages WHERE parent_id = 'p1' AND deleted_at IS NULL ORDER BY sort_key`:      "pages_parent",
		`SELECT created_at FROM page_history WHERE page_id = 'p1' ORDER BY created_at DESC LIMIT 1`: "page_history_page",
		`SELECT peer_id, version, content FROM peer_contents WHERE page_id = 'p1' ORDER BY peer_id`: "peer_contents_page",
	} {
		var plan strings.Builder
		rows, err := s.db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			_ = rows.Scan(&id, &parent, &unused, &detail)
			plan.WriteString(detail + "\n")
		}
		_ = rows.Close()
		if !strings.Contains(plan.String(), index) {
			t.Errorf("%s\nplan:\n%s", q, plan.String())
		}
	}
}

// A database from before invites gains whether each member accepted, as
// a new one has it; the members it kept had all accepted.
func TestADatabaseGainsMembersAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	before := strings.Replace(schema, ", accepted INTEGER NOT NULL DEFAULT 1", "", 1)
	if before == schema {
		t.Fatal("schema has no accepted column to leave out")
	}
	writeDB(t, path, before, `PRAGMA user_version = 2`,
		`INSERT INTO page_members (page_id, user_id, role, display_name, email) VALUES ('p1', 'u2', 'editor', 'Sam', 'sam@example.com')`)
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := userVersion(t, s); v != len(migrations) {
		t.Errorf("schema %d, want %d", v, len(migrations))
	}
	members, err := s.Members("p1")
	if err != nil || len(members) != 1 || !members[0].Accepted {
		t.Fatalf("members %+v, %v", members, err)
	}
	if err := s.SetMembers("p1", []Member{{PageID: "p1", UserID: "u3", Role: "viewer", Email: "kim@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if members, _ := s.Members("p1"); len(members) != 1 || members[0].Accepted {
		t.Errorf("an invited member reads back %+v", members)
	}
	columns := func(s *Store) string {
		rows, err := s.db.Query(`SELECT name, type, "notnull", COALESCE(dflt_value, '') FROM pragma_table_info('page_members') ORDER BY cid`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var b strings.Builder
		for rows.Next() {
			var name, typ, def string
			var notNull int
			_ = rows.Scan(&name, &typ, &notNull, &def)
			fmt.Fprintf(&b, "%s %s %d %s;", name, typ, notNull, def)
		}
		return b.String()
	}
	if got, want := columns(s), columns(open(t)); got != want {
		t.Errorf("upgraded page_members\n%s\nnew\n%s", got, want)
	}
}

// Deleting a file removes its copy in the data folder, drops its upload
// still waiting, and queues the removal of its stored copy after the
// deletion. A copy outside the data folder is the user's, and stays.
func TestDeletingAFileRemovesItsCopy(t *testing.T) {
	s := open(t)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.LocalPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy in the data folder stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(f.LocalPath)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file's folder stayed: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the file the user added went: %v", err)
	}
	var queued []string
	entries, _ := s.Pending()
	for _, e := range entries {
		queued = append(queued, e.Entity+" "+e.EntityID)
	}
	if want := "files " + f.ID + ",storage_delete files/" + f.ID; strings.Join(queued, ",") != want {
		t.Errorf("queued %v, want %s", queued, want)
	}

	outside := File{ID: doc.NewRowID(), SpaceID: s.Session().SpaceID, Name: "notes.txt", LocalPath: src,
		StoragePath: "space/x/notes.txt", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.PutFile(outside); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFile(outside); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("a file outside the data folder went: %v", err)
	}
}

// A file deleted on another device loses its copy here as the deletion
// arrives.
func TestAPulledDeletionRemovesTheCopy(t *testing.T) {
	s := open(t)
	id := doc.NewRowID()
	row := map[string]any{"id": id, "space_id": s.Session().SpaceID, "name": "plan.pdf", "storage_path": "space/" + id + "/plan.pdf",
		"created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-01T00:00:00Z"}
	if err := s.PutRemoteFile(row); err != nil {
		t.Fatal(err)
	}
	local, err := s.DownloadedFile(id, []byte("%PDF"))
	if err != nil {
		t.Fatal(err)
	}
	row["deleted_at"], row["updated_at"] = "2026-10-02T00:00:00Z", "2026-10-02T00:00:00Z"
	if err := s.PutRemoteFile(row); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy stayed: %v", err)
	}
}

// leaflet mcp opens the app's database in a store of its own. When the app
// changes accounts, its next call reads the new session, and a page it
// makes before then fails rather than going into a space that is gone.
func TestReloadSessionFollowsTheApp(t *testing.T) {
	dir := t.TempDir()
	app, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	agent, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if _, err := app.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadSession(nil); err != nil {
		t.Fatal(err)
	}

	// The app signs in, and sync adopts the server's space.
	local := app.Session().SpaceID
	if err := app.PutSpace("remote-a", "My space", "A"); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"space.A": "remote-a", "space.local": "remote-a", "account": "A"} {
		_ = app.SetValue(k, v)
	}
	if err := app.MoveSpace(local, "remote-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.CreatePage(NewPageOptions{Title: "Stale"}); !errors.Is(err, ErrNoSpace) {
		t.Errorf("page in the space that is gone: %v", err)
	}
	sess, err := agent.ReloadSession()
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserID != "A" || sess.SpaceID != "remote-a" || !sess.SignedIn {
		t.Errorf("signed in: session %+v", sess)
	}
	p, err := agent.CreatePage(NewPageOptions{Title: "Fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if p.SpaceID != "remote-a" || p.CreatedBy != "A" {
		t.Errorf("page in %q by %q", p.SpaceID, p.CreatedBy)
	}

	// Signing out: no space until the app makes its new one.
	if err := app.ForgetAccount(); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ReloadSession(); !errors.Is(err, ErrNoSpace) {
		t.Errorf("between accounts: %v", err)
	}
	if _, err := app.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	if sess, err = agent.ReloadSession(); err != nil {
		t.Fatal(err)
	}
	if sess.UserID != LocalUserID || sess.SignedIn || sess.SpaceID != app.Session().SpaceID {
		t.Errorf("signed out: session %+v, app's space %q", sess, app.Session().SpaceID)
	}
}

// A page's change and its entry in the outbox are stored together: when
// queueing fails the change is not kept, so trying again queues it.
func TestFailedQueueKeepsNoChange(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.CreateFolder("Work")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM outbox`, `CREATE TRIGGER full BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT, 'disk full'); END`} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RenamePage(p.ID, "New"); err == nil {
		t.Error("rename stored without its entry")
	}
	if err := s.MovePage(p.ID, "", f.ID, ""); err == nil {
		t.Error("move stored without its entry")
	}
	if _, err := s.DeletePage(p.ID); err == nil {
		t.Error("delete stored without its entry")
	}
	if err := s.RenameFolder(f, "Play"); err == nil {
		t.Error("folder rename stored without its entry")
	}
	if got, _ := s.Page(p.ID); got.Title != "Old" || got.FolderID != "" || !got.DeletedAt.IsZero() {
		t.Errorf("page kept a change with no entry: %+v", got)
	}
	if folders, _ := s.Folders(); len(folders) != 1 || folders[0].Name != "Work" {
		t.Errorf("folders %+v", folders)
	}

	if _, err := s.DB().Exec(`DROP TRIGGER full`); err != nil {
		t.Fatal(err)
	}
	if err := s.RenamePage(p.ID, "New"); err != nil {
		t.Fatal(err)
	}
	if !s.HasPending("pages", p.ID) {
		t.Error("the rename tried again is not queued")
	}
}

// A page or folder pulled from the server leaves one with a change waiting
// here as it is, checked as it is stored: the change goes up next push.
func TestPutRemoteKeepsAWaitingChange(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeepOffline(p.ID, false); err != nil {
		t.Fatal(err)
	}
	f, err := s.CreateFolder("Mine")
	if err != nil {
		t.Fatal(err)
	}
	page, folder := PageToRemote(p), FolderToRemote(f)
	page["title"], folder["name"] = "Theirs", "Theirs"
	put := func() {
		t.Helper()
		if err := s.PutRemotePage(page); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRemoteFolder(folder); err != nil {
			t.Fatal(err)
		}
	}
	put()
	got, _ := s.Page(p.ID)
	folders, _ := s.Folders()
	if got.Title != "Mine" || folders[0].Name != "Mine" {
		t.Errorf("waiting changes overwritten: page %q, folder %q", got.Title, folders[0].Name)
	}

	if _, err := s.DB().Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	put()
	got, _ = s.Page(p.ID)
	folders, _ = s.Folders()
	if got.Title != "Theirs" || folders[0].Name != "Theirs" {
		t.Errorf("pulled with nothing waiting: page %q, folder %q", got.Title, folders[0].Name)
	}
	if got.KeepOffline {
		t.Error("the pull changed keep offline")
	}
}

// Pages left in a loop of parents, as two devices' moves can leave them
// until sync breaks it, are under no page outside the loop.
func TestParentLoop(t *testing.T) {
	s := open(t)
	at := time.Now().UTC().Format(time.RFC3339Nano)
	put := func(id, parent string) {
		t.Helper()
		r := map[string]any{"id": id, "space_id": s.Session().SpaceID, "title": id, "sort_key": "V", "created_at": at, "updated_at": at}
		if parent != "" {
			r["parent_id"] = parent
		}
		if err := s.PutRemotePage(r); err != nil {
			t.Fatal(err)
		}
	}
	put("a", "b")
	put("b", "a")
	put("c", "")
	if s.IsDescendant("a", "c") {
		t.Error("a page of the loop is under one outside it")
	}
	if !s.IsDescendant("a", "b") {
		t.Error("a is not under b")
	}
	if loop := s.ParentLoop("c", "a"); loop != nil {
		t.Errorf("c under a closes %v", loop)
	}
	if loop := s.ParentLoop("a", "b"); !slices.Equal(loop, []string{"a", "b"}) {
		t.Errorf("a under b closes %v", loop)
	}
	if err := s.DetachPage("a"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Page("a"); p.ParentID != "" || !s.IsDescendant("b", "a") {
		t.Errorf("after detaching a: parent %q", p.ParentID)
	}
	if n := s.PendingCount(); n != 0 {
		t.Errorf("detaching queued %d changes", n)
	}
}
