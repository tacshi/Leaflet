package store

import (
	"testing"

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
