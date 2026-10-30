package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
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

// No key sorts between a and a followed by 0s, as the agent function once
// made: one just after it is given, and dragging on from there works.
func TestKeyBetweenAKeyAndItsZeros(t *testing.T) {
	for _, pair := range [][2]string{{"a", "a0"}, {"", "0"}, {"a1", "a100"}} {
		k := KeyBetween(pair[0], pair[1])
		if k <= pair[1] || strings.HasSuffix(k, "0") {
			t.Errorf("between %q and %q: %q", pair[0], pair[1], k)
		}
	}
	// Dragging to the top of a list that starts at "a0", again and again.
	top := "a0"
	for i := 0; i < 20; i++ {
		top = KeyBetween("", top)
	}
}

// Keys from the server may hold any characters: a key between them never
// panics, and sorts strictly between whenever a key of digits can.
func TestKeyBetweenAnyKeys(t *testing.T) {
	for _, pair := range [][2]string{{"~", ""}, {"", "~"}, {" ", ""}, {" ", "!"}, {"a~", "b"}, {"~", "~~"},
		{"Z", "a"}, {"", "1"}, {"!", "1"}, {"", "05"}, {"z", ""}, {"zz", ""}, {"中", ""}, {"a", "中"}, {"V", "V0X"}} {
		k := KeyBetween(pair[0], pair[1])
		if k <= pair[0] || pair[1] != "" && k >= pair[1] {
			t.Errorf("between %q and %q: %q", pair[0], pair[1], k)
		}
	}
	// Nothing of digits sorts before " ": the key goes just after it.
	if k := KeyBetween("", " "); k <= " " {
		t.Errorf("before \" \": %q", k)
	}
	// Out of order, a key after a.
	if k := KeyBetween("b", "a"); k <= "b" {
		t.Errorf("between \"b\" and \"a\": %q", k)
	}
	rng := rand.New(rand.NewPCG(5, 6)) // #nosec G404 -- seeded, for keys a test can repeat.
	chars := "0aZz~ !\x7f\xe4"
	key := func() string {
		var b strings.Builder
		for range 1 + rng.IntN(4) {
			b.WriteByte(chars[rng.IntN(len(chars))])
		}
		return b.String()
	}
	for range 20000 {
		a, b := key(), key()
		if a > b {
			a, b = b, a
		}
		// No key of digits sorts between a and a followed by 0s and then
		// nothing or a character before 0.
		if rest, ok := strings.CutPrefix(b, a); ok {
			if rest = strings.TrimLeft(rest, "0"); rest == "" || rest[0] < '0' {
				continue
			}
		}
		if k := KeyBetween(a, b); k <= a || k >= b {
			t.Fatalf("between %q and %q: %q", a, b, k)
		}
	}
}

// Deleting a thread's first comment keeps its replies; the thread goes
// once they are deleted too.
func TestDeletedFirstCommentKeepsItsReplies(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Plan"})
	root, _ := s.AddComment(p.ID, "b1", "Is this right?", "", "", "", "")
	reply, _ := s.AddComment(p.ID, "b1", "Yes", root.ID, "", "agent", "Claude")
	if err := s.DeleteComment(root.ID); err != nil {
		t.Fatal(err)
	}
	threads, _ := s.Threads(p.ID)
	if len(threads) != 1 || threads[0].Root.DeletedAt.IsZero() || len(threads[0].Replies) != 1 {
		t.Fatalf("threads %+v", threads)
	}
	if err := s.DeleteComment(reply.ID); err != nil {
		t.Fatal(err)
	}
	if threads, _ := s.Threads(p.ID); len(threads) != 0 {
		t.Errorf("threads %+v", threads)
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

// A guest's comment, from a page's public link, keeps the guest's name
// through the server, in agent_name, as an agent's does.
func TestGuestCommentRemote(t *testing.T) {
	c := Comment{ID: "c", PageID: "p", BlockID: "b", AuthorKind: "guest", AuthorName: "Ann", Body: "Hi", AuthorID: "owner"}
	r := CommentToRemote(c)
	if r["agent_name"] != "Ann" || r["author_kind"] != "guest" {
		t.Fatalf("remote %v", r)
	}
	r["author_id"] = "owner"
	back := CommentFromRemote(r, "Owner Name")
	if back.AuthorKind != "guest" || back.AuthorName != "Ann" {
		t.Errorf("back %+v", back)
	}
	if r := CommentToRemote(Comment{AuthorKind: "user", AuthorName: "Me"}); r["agent_name"] != nil {
		t.Errorf("a person's comment has no agent_name: %v", r)
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

// A copy's recording blocks have recordings of their own, on the copy, so
// whoever the copy is shared with can read them. Audio that went up is
// shared; audio on this device is copied, and goes up as the copy's if the
// original's was waiting to.
func TestCopyPageCopiesRecordings(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Meeting"})
	var blocks []*doc.Node
	var originals []Recording
	for i, audioPath := range []string{"u/up.wav", ""} {
		local := filepath.Join(t.TempDir(), "rec.wav")
		if err := os.WriteFile(local, []byte("audio "+strconv.Itoa(i)), 0o600); err != nil {
			t.Fatal(err)
		}
		blockID := doc.NewBlockID()
		r, err := s.CreateRecording(p.ID, blockID, "soniox", "m", local, "en")
		if err != nil {
			t.Fatal(err)
		}
		if r, err = s.UpdateRecording(r.ID, func(x *Recording) {
			x.Status, x.AudioPath, x.Transcript = "ready", audioPath, []Segment{{EndMs: 9, Text: "hello"}}
		}); err != nil {
			t.Fatal(err)
		}
		if audioPath == "" {
			if err := s.QueueUpload(r); err != nil {
				t.Fatal(err)
			}
		}
		originals = append(originals, r)
		blocks = append(blocks, &doc.Node{ID: blockID, Type: "recording", Data: map[string]any{"recording_id": r.ID}})
	}
	if _, _, err := s.SaveContent(p.ID, doc.FromBlocks(blocks)); err != nil {
		t.Fatal(err)
	}

	cp, err := s.CopyPage(p.ID, CopyOptions{Title: "Meeting copy"})
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range s.Content(cp.ID).Children {
		orig := originals[i]
		id := n.Str("recording_id")
		r, err := s.Recording(id)
		if id == orig.ID || err != nil {
			t.Fatalf("block %d: recording %q (%v), the original is %q", i, id, err, orig.ID)
		}
		if r.PageID != cp.ID || r.BlockID != n.ID {
			t.Errorf("block %d: recording on page %s block %s, want %s %s", i, r.PageID, r.BlockID, cp.ID, n.ID)
		}
		if r.AudioPath != orig.AudioPath || len(r.Transcript) != 1 || r.Transcript[0].Text != "hello" || r.Status != "ready" {
			t.Errorf("block %d: copy %+v", i, r)
		}
		if data, err := os.ReadFile(r.LocalPath); r.LocalPath == orig.LocalPath || err != nil || string(data) != "audio "+strconv.Itoa(i) {
			t.Errorf("block %d: audio on this device %q: %q %v", i, r.LocalPath, data, err)
		}
		if !s.HasPending("recordings", id) {
			t.Errorf("block %d: the copy is not queued", i)
		}
		// Uploaded audio goes up again, as the copy's own.
		if !s.HasPending("storage", "recordings/"+id) {
			t.Errorf("block %d: no upload queued", i)
		}
		if o, _ := s.Recording(orig.ID); o.PageID != p.ID || o.BlockID != orig.BlockID {
			t.Errorf("block %d: the original moved to %s %s", i, o.PageID, o.BlockID)
		}
	}
}

// A copied recording whose audio is only stored goes up as its own from
// the original's, which it plays until then: only those who can view the
// original's page can read the original's.
func TestCopyPageUploadsStoredAudioAsItsOwn(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Meeting"})
	blockID := doc.NewBlockID()
	r, err := s.CreateRecording(p.ID, blockID, "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	if r, err = s.UpdateRecording(r.ID, func(x *Recording) { x.Status, x.AudioPath = "ready", "u1/"+r.ID+".wav" }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{{ID: blockID, Type: "recording", Data: map[string]any{"recording_id": r.ID}}})); err != nil {
		t.Fatal(err)
	}
	cp, err := s.CopyPage(p.ID, CopyOptions{Title: "Meeting copy"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Recording(s.Content(cp.ID).Children[0].Str("recording_id"))
	if err != nil {
		t.Fatal(err)
	}
	if c.AudioPath != r.AudioPath || c.LocalPath != "" {
		t.Errorf("copy plays %q, kept here at %q", c.AudioPath, c.LocalPath)
	}
	var payload string
	if err := s.db.QueryRow(`SELECT payload FROM outbox WHERE entity = 'storage' AND entity_id = ?`, "recordings/"+c.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var up map[string]any
	_ = json.Unmarshal([]byte(payload), &up)
	if up["from_path"] != r.AudioPath || up["recording_id"] != c.ID || up["path"] != nil {
		t.Errorf("upload %v", up)
	}
}

// A copied page's pictures and files are files of its own, on the copy,
// as only those who can view the page a file is on can read it. A file on
// this device is copied; one only stored goes up from the original's.
// Starting a page from a template copies them the same way.
func TestCopyPageCopiesFiles(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Trip"})
	src := filepath.Join(t.TempDir(), "map.png")
	if err := os.WriteFile(src, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	here, err := s.AddFile(src, "", p.ID, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	stored := File{ID: doc.NewRowID(), SpaceID: s.Session().SpaceID, PageID: p.ID, Name: "plan.pdf", MimeType: "application/pdf",
		SizeBytes: 3, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	stored.StoragePath = stored.SpaceID + "/" + stored.ID + "/plan.pdf"
	if err := s.PutFile(stored); err != nil {
		t.Fatal(err)
	}
	blocks := []*doc.Node{
		{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{"url": "file://" + here.LocalPath, "file_id": here.ID}},
		{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{"url": "file://" + here.LocalPath, "name": here.Name, "file_id": here.ID}},
		{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{"url": "file:///elsewhere/plan.pdf", "name": stored.Name, "file_id": stored.ID}},
	}
	if _, _, err := s.SaveContent(p.ID, doc.FromBlocks(blocks)); err != nil {
		t.Fatal(err)
	}
	check := func(how, pageID string, c *doc.Content) {
		t.Helper()
		ids := make([]string, len(c.Children))
		for i, n := range c.Children {
			ids[i] = n.Str("file_id")
			orig := blocks[i].Str("file_id")
			f, err := s.File(ids[i])
			if ids[i] == orig || err != nil || f.PageID != pageID {
				t.Fatalf("%s: block %d has file %q (%v) on page %q, the original is %s", how, i, ids[i], err, f.PageID, orig)
			}
			if f.StoragePath != f.SpaceID+"/"+f.ID+"/"+f.Name || n.Str("url") != "file://"+filepath.Join(s.Dir, "files", f.ID, f.Name) {
				t.Errorf("%s: block %d: stored at %q, url %q", how, i, f.StoragePath, n.Str("url"))
			}
			if !s.HasPending("files", f.ID) {
				t.Errorf("%s: block %d: the file's row is not queued", how, i)
			}
			var payload string
			if err := s.db.QueryRow(`SELECT payload FROM outbox WHERE entity = 'storage' AND entity_id = ?`, "files/"+f.ID).Scan(&payload); err != nil {
				t.Fatalf("%s: block %d: no upload: %v", how, i, err)
			}
			var up map[string]any
			_ = json.Unmarshal([]byte(payload), &up)
			if orig == here.ID {
				if data, err := os.ReadFile(f.LocalPath); err != nil || string(data) != "png" || up["local_path"] != f.LocalPath || up["from_path"] != nil {
					t.Errorf("%s: block %d: here at %q (%q, %v), upload %v", how, i, f.LocalPath, data, err, up)
				}
			} else if f.LocalPath != "" || up["from_path"] != stored.StoragePath || up["file_id"] != f.ID || up["path"] != f.StoragePath {
				t.Errorf("%s: block %d: here at %q, upload %v", how, i, f.LocalPath, up)
			}
		}
		if ids[0] != ids[1] {
			t.Errorf("%s: one file became two: %v", how, ids)
		}
	}

	cp, err := s.CopyPage(p.ID, CopyOptions{Title: "Trip copy"})
	if err != nil {
		t.Fatal(err)
	}
	check("duplicate", cp.ID, s.Content(cp.ID))
	tpl, _ := s.CreatePage(NewPageOptions{Title: "From template"})
	c, err := s.CopyInto(p.ID, tpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("template", tpl.ID, c)
	for i, n := range s.Content(p.ID).Children {
		if n.Str("file_id") != blocks[i].Str("file_id") {
			t.Errorf("the original's block %d now has file %q", i, n.Str("file_id"))
		}
	}
}

// A file's name from the server never puts it, or a copy of it, outside
// the app's files folder.
func TestFileNamesStayInTheFilesFolder(t *testing.T) {
	s := open(t)
	if f := FileFromRemote(map[string]any{"id": "f1", "name": "../../../x"}); f.Name != "x" {
		t.Errorf("name from the server %q", f.Name)
	}
	for in, want := range map[string]string{"a.pdf": "a.pdf", "../../a.pdf": "a.pdf", "..": "file", "": "file", "/": "file"} {
		if got := fileName(in); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
	p, _ := s.CreatePage(NewPageOptions{Title: "Trip"})
	src := filepath.Join(t.TempDir(), "map.png")
	if err := os.WriteFile(src, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	// As a row stored before names were checked has it.
	f := File{ID: doc.NewRowID(), SpaceID: s.Session().SpaceID, PageID: p.ID, Name: "../../../escaped.png", LocalPath: src,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.PutFile(f); err != nil {
		t.Fatal(err)
	}
	block := &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{"url": "file://" + src, "file_id": f.ID}}
	if _, _, err := s.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{block})); err != nil {
		t.Fatal(err)
	}
	cp, err := s.CopyPage(p.ID, CopyOptions{Title: "Trip copy"})
	if err != nil {
		t.Fatal(err)
	}
	n := s.Content(cp.ID).Children[0]
	c, err := s.File(n.Str("file_id"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Dir, "files", c.ID)
	if c.LocalPath != filepath.Join(dir, "escaped.png") || n.Str("url") != "file://"+c.LocalPath || strings.Contains(c.StoragePath, "..") {
		t.Errorf("copy at %q, url %q, stored at %q", c.LocalPath, n.Str("url"), c.StoragePath)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "..", "escaped.png")); err == nil {
		t.Error("the copy went outside the files folder")
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
	todo.SetDue(time.Now(), false)
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

// A data folder whose name has a character URIs treat specially opens the
// database in that folder, with the pragmas, and not one beside it.
func TestOpensTheDatabaseInAFolderOfAnyName(t *testing.T) {
	for _, name := range []string{"Notes #1", "What?", "100%", "a%20b", "Notizen ü"} {
		base := t.TempDir()
		dir := filepath.Join(base, name)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := s.SetValue("k", "v"); err != nil {
			t.Fatal(err)
		}
		var mode string
		if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
			t.Errorf("%s: journal mode %q, %v", name, mode, err)
		}
		_ = s.Close()
		if entries, _ := os.ReadDir(base); len(entries) != 1 || entries[0].Name() != name {
			t.Errorf("%s: beside the folder %v", name, entries)
		}
		if _, err := os.Stat(filepath.Join(dir, "leaflet.db")); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		s, err = Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := s.Value("k"); v != "v" {
			t.Errorf("%s: reopened, the value is %q", name, v)
		}
		_ = s.Close()
	}
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

// A file added while the session still names a space that sync has just
// moved the pages out of fails, as a page does, rather than going into a
// space that is gone; it leaves no copy behind.
func TestAddFileInASpaceThatIsGone(t *testing.T) {
	s := open(t)
	local := s.Session().SpaceID
	if err := s.PutSpace("remote-a", "My space", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveSpace(local, "remote-a"); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "plan.pdf")
	if err := os.WriteFile(src, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFile(src, "", "", "application/pdf"); !errors.Is(err, ErrNoSpace) {
		t.Errorf("file in the space that is gone: %v", err)
	}
	if files, _ := s.AllFiles(); len(files) != 0 || s.PendingCount() != 0 {
		t.Errorf("%d files, %d waiting", len(files), s.PendingCount())
	}
	if copies, _ := os.ReadDir(filepath.Join(s.Dir, "files")); len(copies) != 0 {
		t.Errorf("%d copies left", len(copies))
	}
}

// Signing out removes the account's files and recordings' audio from the
// data folder, not only their rows.
func TestForgetAccountRemovesFiles(t *testing.T) {
	s := open(t)
	src := filepath.Join(t.TempDir(), "plan.pdf")
	if err := os.WriteFile(src, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFile(src, "", "", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(s.Dir, "recordings", "r1.wav")
	if err := os.MkdirAll(filepath.Dir(audio), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetAccount(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(s.Dir, "files", f.ID), audio} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left: %v", path, err)
		}
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the file it copied went too: %v", err)
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

// Tags are tidied as they are set, go up with the page's row, and come
// back from one the same.
func TestPageTags(t *testing.T) {
	if got, want := NormalizeTags([]string{" #Work ", "work", "", "two  words", "##x", strings.Repeat("a", 70)}),
		[]string{"Work", "two words", "x", strings.Repeat("a", maxTagLen)}; !slices.Equal(got, want) {
		t.Errorf("normalized %q, want %q", got, want)
	}
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Plan"})
	if err := s.SetPageTags(p.ID, []string{"work", "#Q4"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Page(p.ID)
	if !slices.Equal(got.Tags, []string{"work", "Q4"}) {
		t.Fatalf("tags %q", got.Tags)
	}
	pending, _ := s.Pending()
	if !slices.ContainsFunc(pending, func(e OutboxEntry) bool {
		return e.Entity == "pages" && strings.Contains(e.Payload, `"tags":["work","Q4"]`)
	}) {
		t.Errorf("queued %+v", pending)
	}
	// A row through JSON, as the server and iCloud send it.
	var row map[string]any
	b, _ := json.Marshal(PageToRemote(got))
	_ = json.Unmarshal(b, &row)
	if back := PageFromRemote(row, true); !slices.Equal(back.Tags, got.Tags) {
		t.Errorf("read back %q", back.Tags)
	}
	delete(row, "tags")
	if back := PageFromRemote(row, true); back.Tags == nil || len(back.Tags) != 0 {
		t.Errorf("a row without tags read %q", back.Tags)
	}
	if c, err := s.CopyPage(p.ID, CopyOptions{Title: "Copy"}); err != nil || !slices.Equal(c.Tags, got.Tags) {
		t.Errorf("copy %q, %v", c.Tags, err)
	}
}

// The template a folder's new pages and a page's new subpages start from
// is kept, goes up with the row, and comes back from it.
func TestDefaultTemplates(t *testing.T) {
	s := open(t)
	tpl, _ := s.CreatePage(NewPageOptions{Title: "Weekly sync", Template: true})
	f, _ := s.CreateFolder("Meetings")
	p, _ := s.CreatePage(NewPageOptions{Title: "Standups"})
	if err := s.SetFolderDefaultTemplate(f, tpl.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPageDefaultTemplate(p.ID, tpl.ID); err != nil {
		t.Fatal(err)
	}
	folders, _ := s.Folders()
	if len(folders) != 1 || folders[0].DefaultTemplateID != tpl.ID {
		t.Errorf("folders %+v", folders)
	}
	got, _ := s.Page(p.ID)
	if got.DefaultTemplateID != tpl.ID {
		t.Errorf("page %+v", got)
	}
	pending, _ := s.Pending()
	for _, entity := range []string{"folders", "pages"} {
		if !slices.ContainsFunc(pending, func(e OutboxEntry) bool {
			return e.Entity == entity && strings.Contains(e.Payload, `"default_template_id":"`+tpl.ID+`"`)
		}) {
			t.Errorf("%s: queued %+v", entity, pending)
		}
	}
	// Rows through JSON, as the server and iCloud send them.
	var page, folder map[string]any
	b, _ := json.Marshal(PageToRemote(got))
	_ = json.Unmarshal(b, &page)
	b, _ = json.Marshal(FolderToRemote(folders[0]))
	_ = json.Unmarshal(b, &folder)
	if back := PageFromRemote(page, true); back.DefaultTemplateID != tpl.ID {
		t.Errorf("page read back %+v", back)
	}
	if back := FolderFromRemote(folder); back.DefaultTemplateID != tpl.ID {
		t.Errorf("folder read back %+v", back)
	}

	// Back to blank pages.
	if err := s.SetFolderDefaultTemplate(folders[0], ""); err != nil {
		t.Fatal(err)
	}
	if folders, _ := s.Folders(); folders[0].DefaultTemplateID != "" {
		t.Errorf("cleared folder %+v", folders[0])
	}
}

// A database from before default templates gains their columns, keeping
// its folders and pages.
func TestOpensADatabaseFromBeforeDefaultTemplates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	writeDB(t, path,
		`CREATE TABLE folders (
		  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, parent_folder_id TEXT, name TEXT NOT NULL,
		  sort_key TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER)`,
		`CREATE TABLE pages (
		  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, parent_id TEXT, folder_id TEXT,
		  title TEXT NOT NULL DEFAULT '', icon TEXT, sort_key TEXT NOT NULL, created_by TEXT,
		  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER,
		  keep_offline INTEGER NOT NULL DEFAULT 1, is_template INTEGER NOT NULL DEFAULT 0,
		  tags TEXT NOT NULL DEFAULT '[]')`,
		`INSERT INTO folders (id, space_id, name, sort_key, created_at, updated_at) VALUES ('f1', 's1', 'Meetings', 'a', 1, 1)`,
		`INSERT INTO pages (id, space_id, title, sort_key, created_at, updated_at) VALUES ('p1', 's1', 'Plan', 'a', 1, 1)`,
		`PRAGMA user_version = 4`,
	)
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := userVersion(t, s); v != len(migrations) {
		t.Errorf("schema %d, want %d", v, len(migrations))
	}
	if p, err := s.Page("p1"); err != nil || p.Title != "Plan" || p.DefaultTemplateID != "" {
		t.Errorf("page %+v, %v", p, err)
	}
	if folders, err := s.Folders(); err != nil || len(folders) != 1 || folders[0].Name != "Meetings" {
		t.Errorf("folders %+v, %v", folders, err)
	}
	if err := s.SetPageDefaultTemplate("p1", "t1"); err != nil {
		t.Fatal(err)
	}
}

// The tags of the pages are counted once in any case, by name, and are
// renamed, merged and deleted on every page.
func TestRenameAndDeleteTags(t *testing.T) {
	s := open(t)
	a, _ := s.CreatePage(NewPageOptions{Title: "A", Tags: []string{"work", "draft"}})
	b, _ := s.CreatePage(NewPageOptions{Title: "B", Tags: []string{"Work", "done"}})
	_, _ = s.CreatePage(NewPageOptions{Title: "T", Template: true, Tags: []string{"template-only"}})
	tags, _ := s.Tags()
	if want := []TagCount{{"done", 1}, {"draft", 1}, {"work", 2}}; !slices.Equal(tags, want) {
		t.Errorf("tags %+v, want %+v", tags, want)
	}
	if before, err := s.RenameTag("draft", "done"); err != nil || len(before) != 1 || !slices.Equal(before[a.ID], []string{"work", "draft"}) {
		t.Fatalf("renamed %v, %v", before, err)
	}
	if p, _ := s.Page(a.ID); !slices.Equal(p.Tags, []string{"work", "done"}) {
		t.Errorf("renamed to %q", p.Tags)
	}
	if before, err := s.RenameTag("done", "Work"); err != nil || len(before) != 2 {
		t.Fatalf("merged %v, %v", before, err)
	}
	if p, _ := s.Page(b.ID); !slices.Equal(p.Tags, []string{"Work"}) {
		t.Errorf("merged to %q", p.Tags)
	}
	if before, err := s.DeleteTag("WORK"); err != nil || len(before) != 2 {
		t.Fatalf("deleted %v, %v", before, err)
	}
	if tags, _ := s.Tags(); len(tags) != 0 {
		t.Errorf("left %+v", tags)
	}
}

// Words starting with # keep a search to the pages with those tags, which
// alone lists them; other words find a page by its tags too.
func TestSearchByTag(t *testing.T) {
	s := open(t)
	plan, _ := s.CreatePage(NewPageOptions{Title: "Plan", Tags: []string{"work"}})
	c := s.Content(plan.ID)
	c.Children[0].SetDelta(doc.Delta{{Text: "the budget for Q4"}})
	_, _, _ = s.SaveContent(plan.ID, c)
	notes, _ := s.CreatePage(NewPageOptions{Title: "Notes", Tags: []string{"Work", "reading"}})
	other, _ := s.CreatePage(NewPageOptions{Title: "Budget at home"})
	ids := func(q string) []string {
		hits, err := s.Search(q, 20)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.PageID)
		}
		slices.Sort(out)
		return out
	}
	sorted := func(s ...string) []string { slices.Sort(s); return s }
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"#work", sorted(plan.ID, notes.ID)},
		{"#work #reading", sorted(notes.ID)},
		{"#work budget", sorted(plan.ID)},
		{"budget", sorted(plan.ID, other.ID)},
		{"readi", sorted(notes.ID)},
		{"#rea", sorted(notes.ID)}, // while typing
		{"#nope", nil},
	} {
		if got := ids(c.q); !slices.Equal(got, c.want) {
			t.Errorf("%q found %v, want %v", c.q, got, c.want)
		}
	}
}

// A tag filter keeps to its pages before any limit: comments matching on
// other pages, more than a search takes, hide none of the tagged page's.
func TestSearchByTagPastOtherMatches(t *testing.T) {
	s := open(t)
	other, _ := s.CreatePage(NewPageOptions{Title: "Elsewhere"})
	for range 12 {
		if _, err := s.AddComment(other.ID, "b1", "a needle here", "", "", "user", "Me"); err != nil {
			t.Fatal(err)
		}
	}
	tagged, _ := s.CreatePage(NewPageOptions{Title: "Tagged", Tags: []string{"work"}})
	if _, err := s.AddComment(tagged.ID, "b1", "the needle", "", "", "user", "Me"); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("#work needle", 20)
	if err != nil || len(hits) != 1 || hits[0].PageID != tagged.ID || hits[0].Kind != "comment" {
		t.Errorf("hits %+v, %v", hits, err)
	}
}

// A database from before tags gains them, none on each page.
func TestADatabaseGainsTags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	before := strings.Replace(schema, ",\n  tags TEXT NOT NULL DEFAULT '[]'", "", 1)
	if before == schema {
		t.Fatal("schema has no tags column to leave out")
	}
	writeDB(t, path, before, `PRAGMA user_version = 3`,
		`INSERT INTO pages (id, space_id, title, sort_key, created_at, updated_at) VALUES ('p1', 's1', 'Plan', 'a', 1, 1)`)
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := userVersion(t, s); v != len(migrations) {
		t.Errorf("schema %d, want %d", v, len(migrations))
	}
	if p, err := s.Page("p1"); err != nil || p.Tags == nil || len(p.Tags) != 0 {
		t.Fatalf("page %+v, %v", p, err)
	}
	if err := s.SetPageTags("p1", []string{"work"}); err != nil {
		t.Fatal(err)
	}
}

// A folder renamed from a copy read before its default template changed
// keeps the template: the rename changes the name alone.
func TestRenameFolderKeepsChangesSinceRead(t *testing.T) {
	s := open(t)
	f, err := s.CreateFolder("Folder")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := s.CreatePage(NewPageOptions{Template: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolderDefaultTemplate(f, tpl.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameFolder(f, "Renamed"); err != nil {
		t.Fatal(err)
	}
	folders, err := s.Folders()
	if err != nil || len(folders) != 1 || folders[0].Name != "Renamed" || folders[0].DefaultTemplateID != tpl.ID {
		t.Fatalf("folders %+v, %v", folders, err)
	}
	if err := s.DeleteFolder(f); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := s.DB().QueryRow(`SELECT name FROM folders WHERE id = ?`, f.ID).Scan(&name); err != nil || name != "Renamed" {
		t.Errorf("deleted folder named %q, %v", name, err)
	}
}

// A comment or recording whose entry in the queue cannot be stored is not
// stored either: kept, it would never go up.
func TestRowsAndTheirQueueEntriesStoreTogether(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRecording(p.ID, "rec", "local", "model", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER reject_queue BEFORE INSERT ON outbox WHEN NEW.entity IN ('comments', 'recordings')
		BEGIN SELECT RAISE(ABORT, 'queue failed'); END`); err != nil {
		t.Fatal(err)
	}
	c, err := s.AddComment(p.ID, "block", "reply", "", "", "", "")
	if err == nil {
		t.Fatal("comment stored though its queue entry failed")
	}
	if _, err := s.Comment(c.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("comment kept without its queue entry: %v", err)
	}
	if _, err := s.UpdateRecording(r.ID, func(r *Recording) { r.Status = "ready" }); err == nil {
		t.Fatal("recording stored though its queue entry failed")
	}
	if got, _ := s.Recording(r.ID); got.Status != "recording" {
		t.Errorf("recording %s kept without its queue entry", got.Status)
	}
}

// A row from the server stores only over one with no change waiting here,
// checked as it stores: the change stays, to go up.
func TestRemoteRowsKeepChangesWaiting(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRecording(p.ID, "rec", "local", "model", "", "")
	if err != nil {
		t.Fatal(err)
	}
	stale := RecordingToRemote(r)
	if _, err := s.UpdateRecording(r.ID, func(r *Recording) {
		r.Status, r.Transcript = "ready", []Segment{{Text: "finished"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRemoteRecording(stale); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Recording(r.ID); len(got.Transcript) != 1 {
		t.Errorf("transcript waiting to go up replaced: %+v", got)
	}
	c, err := s.AddComment(p.ID, "block", "mine", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	theirs := CommentToRemote(c)
	theirs["body"] = "older"
	if err := s.PutRemoteComment(theirs, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Comment(c.ID); got.Body != "mine" {
		t.Errorf("comment waiting to go up replaced: %q", got.Body)
	}
}

// Signing out removes nothing while a change waits to go up.
func TestForgetSyncedAccountKeepsChangesWaiting(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "Waiting"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetSyncedAccount(); !errors.Is(err, ErrUnsynced) {
		t.Fatalf("forget with a change waiting: %v", err)
	}
	if _, err := s.Page(p.ID); err != nil {
		t.Fatalf("page removed: %v", err)
	}
	entries, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := s.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ForgetSyncedAccount(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Page(p.ID); err == nil {
		t.Error("page kept once everything went up")
	}
}

// A withdrawn share's pages leave the device with what is kept of them,
// their copies here and changes waiting too; the user's own pages stay.
func TestRemoveSharedPages(t *testing.T) {
	s := open(t)
	mine, err := s.CreatePage(NewPageOptions{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	old := "2026-01-01T00:00:00Z"
	for _, r := range []map[string]any{
		{"id": "root", "space_id": "theirs", "title": "Root", "sort_key": "V", "created_at": old, "updated_at": old},
		{"id": "kid", "space_id": "theirs", "parent_id": "root", "title": "Kid", "sort_key": "V", "created_at": old, "updated_at": old},
		{"id": "beside", "space_id": "theirs", "title": "Beside", "sort_key": "W", "created_at": old, "updated_at": old},
	} {
		if err := s.PutRemotePage(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.SaveContent("kid", doc.FromBlocks([]*doc.Node{{ID: "b1", Type: "paragraph"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment("kid", "b1", "waiting", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(s.Dir, "recordings", "r1.wav")
	if err := os.MkdirAll(filepath.Dir(audio), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRecording("root", "rec", "local", "model", audio, ""); err != nil {
		t.Fatal(err)
	}

	if ids, err := s.RemoveSharedPages(mine.ID); err != nil || len(ids) != 0 {
		t.Fatalf("own page removed: %v, %v", ids, err)
	}
	ids, err := s.RemoveSharedPages("root")
	if err != nil || len(ids) != 2 {
		t.Fatalf("removed %v, %v", ids, err)
	}
	for _, id := range []string{"root", "kid"} {
		if _, err := s.Page(id); err == nil {
			t.Errorf("%s stayed", id)
		}
	}
	for _, id := range []string{mine.ID, "beside"} {
		if _, err := s.Page(id); err != nil {
			t.Errorf("%s removed: %v", id, err)
		}
	}
	var left int
	if err := s.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM page_contents WHERE page_id = 'kid') + (SELECT COUNT(*) FROM comments)
		+ (SELECT COUNT(*) FROM recordings) + (SELECT COUNT(*) FROM outbox WHERE page_id IN ('root', 'kid'))`).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d rows of the shared pages left, %v", left, err)
	}
	if _, err := os.Stat(audio); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("recording's audio stayed: %v", err)
	}
	if !s.HasPending("pages", mine.ID) {
		t.Error("own page's change waiting removed")
	}
}
