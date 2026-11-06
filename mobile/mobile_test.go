package mobile

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// fakePlatform is the app, offline, with its keychain in memory.
type fakePlatform struct {
	t       *testing.T
	mu      sync.Mutex
	secrets map[string][]byte
	// ended says the test is over, which Log then no longer logs to.
	ended bool
}

func (p *fakePlatform) SecretGet(name string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.secrets[name], nil
}

func (p *fakePlatform) SecretSet(name string, value []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secrets[name] = value
	return nil
}

func (p *fakePlatform) SecretDelete(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.secrets, name)
	return nil
}

func (p *fakePlatform) Online() bool                    { return false }
func (p *fakePlatform) DeviceName() string              { return "Test iPhone" }
func (p *fakePlatform) OpenURL(string)                  {}
func (p *fakePlatform) WatchICloud(bool)                {}
func (p *fakePlatform) StartDownload(path string) error { return nil }

// Log logs to the test while it runs: the core's goroutines can log after
// it ended, which t.Log would panic on.
func (p *fakePlatform) Log(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ended {
		p.t.Log(strings.TrimSpace(line))
	}
}

// logUntilEnd has Log stop logging as the test ends, once the cleanups
// registered after it have run, and a Log under way is done.
func (p *fakePlatform) logUntilEnd() {
	p.t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.ended = true
	})
}

// update is a PageUpdated event, and when it came.
type update struct {
	pageID, content, reason string
	rev                     int64
	at                      time.Time
}

// fakeEvents keeps the pages' updates, and what PageSaving and
// PageReadOnly said, for the test to wait on.
type fakeEvents struct {
	updates  chan update
	saving   chan bool
	readOnly chan bool
	notices  chan string
}

func (e *fakeEvents) Changed(table, pageID string)           {}
func (e *fakeEvents) SyncState(running bool, failure string) {}
func (e *fakeEvents) Tint(string, string)                    {}
func (e *fakeEvents) AgentsChanged(string)                   {}
func (e *fakeEvents) RecordingChanged(string)                {}
func (e *fakeEvents) RecordingLevels(string)                 {}

// PageSaving drops what a test without a channel for it, or not waiting
// on it, says.
func (e *fakeEvents) PageSaving(_ string, saving bool) {
	select {
	case e.saving <- saving:
	default:
	}
}

// Notice drops what a test not waiting on it says.
func (e *fakeEvents) Notice(message string) {
	select {
	case e.notices <- message:
	default:
	}
}

// PageReadOnly drops what a test not waiting on it says.
func (e *fakeEvents) PageReadOnly(_ string, readOnly bool) {
	select {
	case e.readOnly <- readOnly:
	default:
	}
}

func (e *fakeEvents) PageUpdated(pageID, content string, rev int64, reason string, newStep bool) {
	e.updates <- update{pageID: pageID, content: content, reason: reason, rev: rev, at: time.Now()}
}

func open(t *testing.T) (*Core, *fakeEvents) {
	t.Helper()
	cfg, _ := json.Marshal(config{DataDir: t.TempDir(), Locale: "en"})
	ev := &fakeEvents{updates: make(chan update, 16), saving: make(chan bool, 64), readOnly: make(chan bool, 16), notices: make(chan string, 16)}
	p := &fakePlatform{t: t, secrets: map[string][]byte{}}
	p.logUntilEnd()
	c, err := Open(string(cfg), p, ev)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Stop()
		_ = c.core.Store.Close()
	})
	return c, ev
}

func paragraph(id, text string) *doc.Node {
	n := &doc.Node{ID: id, Type: "paragraph"}
	n.SetDelta(doc.Delta{{Text: text}})
	return n
}

func texts(c *doc.Content) string {
	var out []string
	for _, n := range c.All() {
		out = append(out, n.Delta().Plain())
	}
	return strings.Join(out, "|")
}

func TestLibraryAndPages(t *testing.T) {
	c, _ := open(t)
	raw, err := c.CreatePage(`{"title":"Groceries","tags":["home"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	if row.Title != "Groceries" || !row.CanEdit || len(row.Tags) != 1 {
		t.Fatalf("created %+v", row)
	}

	raw, err = c.Library()
	if err != nil {
		t.Fatal(err)
	}
	var lib library
	_ = json.Unmarshal([]byte(raw), &lib)
	found := false
	for _, p := range lib.Pages {
		found = found || p.ID == row.ID
	}
	if !found || lib.SpaceID == "" || len(lib.Tags) == 0 {
		t.Fatalf("library %+v", lib)
	}

	ids, err := c.DeletePage(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RestorePages(ids); err != nil {
		t.Fatal(err)
	}
}

func TestEditSavesAndReopens(t *testing.T) {
	c, _ := open(t)
	raw, _ := c.CreatePage(`{"title":"Notes"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)

	raw, err := c.OpenPage(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pg openedPage
	_ = json.Unmarshal([]byte(raw), &pg)
	if pg.ReadOnly || pg.Unreadable {
		t.Fatalf("opened %+v", pg)
	}

	edit := doc.FromBlocks([]*doc.Node{paragraph("a", "Milk")})
	if _, err := c.EditPage(row.ID, pg.Editor, edit.Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	c.ClosePage(row.ID, pg.Editor)

	raw, _ = c.OpenPage(row.ID)
	_ = json.Unmarshal([]byte(raw), &pg)
	if got := texts(pg.Content); got != "Milk" {
		t.Fatalf("reopened with %q", got)
	}
}

// An agent's change and the app's edit, made over the content before it,
// both stay: the edit merges, and the merge goes back to the app.
func TestEditOverAnAgentsChangeMerges(t *testing.T) {
	c, ev := open(t)
	raw, _ := c.CreatePage(`{"title":"Plan"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	start := doc.FromBlocks([]*doc.Node{paragraph("a", "One")})
	raw, _ = c.OpenPage(row.ID)
	var pg openedPage
	_ = json.Unmarshal([]byte(raw), &pg)
	if _, err := c.EditPage(row.ID, pg.Editor, start.Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}

	// The agent adds a block at the end.
	var err error
	c.q.do(func() {
		err = host{c}.Live(row.ID).ApplyOps([]doc.Op{doc.InsertOp(doc.Position{At: "end"}, paragraph("b", "Two"))})
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := wait(t, ev, "agent")

	// Meanwhile the app, not having heard of it, changes the first block.
	edit := doc.FromBlocks([]*doc.Node{paragraph("a", "One!")})
	if _, err := c.EditPage(row.ID, pg.Editor, edit.Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	merge := wait(t, ev, "merge")
	if merge.rev <= agent.rev {
		t.Fatalf("merge at rev %d, after the agent's %d", merge.rev, agent.rev)
	}
	if got := texts(doc.MustDecode(merge.content)); got != "One!|Two" {
		t.Fatalf("merged %q", got)
	}

	// The app, now at the merge's rev, edits on without another merge.
	edit = doc.MustDecode(merge.content)
	edit.Find("b").SetDelta(doc.Delta{{Text: "Two!"}})
	if _, err := c.EditPage(row.ID, pg.Editor, edit.Encode(), merge.rev); err != nil {
		t.Fatal(err)
	}
	c.ClosePage(row.ID, pg.Editor)
	raw, _ = c.OpenPage(row.ID)
	_ = json.Unmarshal([]byte(raw), &pg)
	if got := texts(pg.Content); got != "One!|Two!" {
		t.Fatalf("saved %q", got)
	}
}

// An agent's edits streaming in reach the app spaced, agentEvery apart
// rather than one whole page each, and an edit the app makes over a rev
// before them merges with all of them.
func TestStreamingAgentEditsReachTheAppSpaced(t *testing.T) {
	c, ev := open(t)
	row := create(t, c, `{"title":"Plan"}`)
	pg := openEditor(t, c, row.ID)
	if _, err := c.EditPage(row.ID, pg.Editor, doc.FromBlocks([]*doc.Node{paragraph("a", "One")}).Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	// The blocks of a call stream in every few hundredths of a second.
	const n = 30
	for i := range n {
		var err error
		c.q.do(func() {
			err = host{c}.Live(row.ID).ApplyOp(doc.InsertOp(doc.Position{At: "end"}, paragraph(fmt.Sprint("b", i), "x")), i == 0)
		})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The last of them reach the app too.
	time.Sleep(2 * agentEvery)
	// The app, not having heard of them, changes the first block.
	if _, err := c.EditPage(row.ID, pg.Editor, doc.FromBlocks([]*doc.Node{paragraph("a", "One!")}).Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	var shown []time.Time
	var merge update
	for merge.reason == "" {
		select {
		case u := <-ev.updates:
			switch u.reason {
			case "agent":
				shown = append(shown, u.at)
			case "merge":
				merge = u
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no merge update")
		}
	}
	if got := len(doc.MustDecode(merge.content).Children); got != n+1 {
		t.Fatalf("merged %d blocks", got)
	}
	if len(shown) < 2 || len(shown) > n/4 {
		t.Fatalf("%d updates for %d edits over %v", len(shown), n, n*10*time.Millisecond)
	}
	// Some slack for the events' own goroutine.
	for i := 1; i < len(shown); i++ {
		if gap := shown[i].Sub(shown[i-1]); gap < agentEvery*2/3 {
			t.Errorf("updates %d and %d %v apart, want %v", i-1, i, gap, agentEvery)
		}
	}
}

// An edit over a rev the editor no longer keeps merges from the newest it
// kept before it, not from nothing.
func TestEditorBaseOfARevNoLongerKept(t *testing.T) {
	old, last, newer := doc.Empty(), doc.Empty(), doc.Empty()
	e := &pageEditor{seen: map[int64]*doc.Content{3: old, 5: last, 40: newer}, last: 5}
	if e.base(5) != last || e.base(10) != last || e.base(40) != newer || e.base(2) != nil {
		t.Error("base of the wrong rev")
	}
}

// openEditor opens page id in another editor, as another window does.
func openEditor(t *testing.T, c *Core, id string) openedPage {
	t.Helper()
	raw, err := c.OpenPage(id)
	if err != nil {
		t.Fatal(err)
	}
	var pg openedPage
	_ = json.Unmarshal([]byte(raw), &pg)
	return pg
}

// A page open in two windows takes the edits of each: one's edit goes to
// the other at a new rev, and the other's, made before it heard of it,
// merges rather than replacing it. Closing one window leaves the other
// saving.
func TestTwoWindowsKeepEachOthersEdits(t *testing.T) {
	c, ev := open(t)
	raw, _ := c.CreatePage(`{"title":"Plan"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	a := openEditor(t, c, row.ID)
	start := doc.FromBlocks([]*doc.Node{paragraph("a", "One"), paragraph("b", "Two")})
	if _, err := c.EditPage(row.ID, a.Editor, start.Encode(), a.Rev); err != nil {
		t.Fatal(err)
	}
	b := openEditor(t, c, row.ID)
	if b.Editor == a.Editor || texts(b.Content) != "One|Two" {
		t.Fatalf("second window %+v", b)
	}

	edit := start.Copy()
	edit.Find("a").SetDelta(doc.Delta{{Text: "One!"}})
	if _, err := c.EditPage(row.ID, a.Editor, edit.Encode(), a.Rev); err != nil {
		t.Fatal(err)
	}
	if u := wait(t, ev, "merge"); u.rev <= b.Rev || texts(doc.MustDecode(u.content)) != "One!|Two" {
		t.Fatalf("the other window got %q at rev %d", texts(doc.MustDecode(u.content)), u.rev)
	}
	edit = b.Content.Copy()
	edit.Find("b").SetDelta(doc.Delta{{Text: "Two!"}})
	if _, err := c.EditPage(row.ID, b.Editor, edit.Encode(), b.Rev); err != nil {
		t.Fatal(err)
	}
	if u := wait(t, ev, "merge"); texts(doc.MustDecode(u.content)) != "One!|Two!" {
		t.Fatalf("merged %q", texts(doc.MustDecode(u.content)))
	}

	c.ClosePage(row.ID, a.Editor)
	b = openEditor(t, c, row.ID)
	edit = b.Content.Copy()
	edit.Find("b").SetDelta(doc.Delta{{Text: "Two!!"}})
	if _, err := c.EditPage(row.ID, b.Editor, edit.Encode(), b.Rev); err != nil {
		t.Fatalf("edit after the other window closed: %v", err)
	}
	closeEditors(c, row.ID)
	if got := texts(openEditor(t, c, row.ID).Content); got != "One!|Two!!" {
		t.Fatalf("saved %q", got)
	}
}

// Two windows' edits of one block over the same rev keep the one the
// merge loses as a conflict, to recover.
func TestTwoWindowsEditingOneBlockKeepTheConflict(t *testing.T) {
	c, ev := open(t)
	raw, _ := c.CreatePage(`{"title":"Plan"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	a := openEditor(t, c, row.ID)
	start := doc.FromBlocks([]*doc.Node{paragraph("a", "One")})
	if _, err := c.EditPage(row.ID, a.Editor, start.Encode(), a.Rev); err != nil {
		t.Fatal(err)
	}
	b := openEditor(t, c, row.ID)
	edit := start.Copy()
	edit.Find("a").SetDelta(doc.Delta{{Text: "first window"}})
	if _, err := c.EditPage(row.ID, a.Editor, edit.Encode(), a.Rev); err != nil {
		t.Fatal(err)
	}
	wait(t, ev, "merge")
	edit = start.Copy()
	edit.Find("a").SetDelta(doc.Delta{{Text: "second window"}})
	if _, err := c.EditPage(row.ID, b.Editor, edit.Encode(), b.Rev); err != nil {
		t.Fatal(err)
	}
	wait(t, ev, "merge")

	raw, err := c.Conflicts()
	if err != nil {
		t.Fatal(err)
	}
	var conflicts []conflictRow
	_ = json.Unmarshal([]byte(raw), &conflicts)
	if len(conflicts) != 1 || conflicts[0].PageID != row.ID {
		t.Fatalf("conflicts %+v", conflicts)
	}
	if got := conflicts[0].Kept + "|" + conflicts[0].Other; got != "second window|first window" && got != "first window|second window" {
		t.Errorf("kept and other %q", got)
	}
}

// Deleting an open page saves what was sent of it first, so Undo brings
// it back with its last edits.
func TestDeletingAnOpenPageKeepsItsEditsForUndo(t *testing.T) {
	c, _ := open(t)
	raw, _ := c.CreatePage(`{"title":"Notes"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	pg := openEditor(t, c, row.ID)
	if _, err := c.EditPage(row.ID, pg.Editor, doc.FromBlocks([]*doc.Node{paragraph("a", "Milk")}).Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	ids, err := c.DeletePage(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RestorePages(ids); err != nil {
		t.Fatal(err)
	}
	if got := texts(openEditor(t, c, row.ID).Content); got != "Milk" {
		t.Fatalf("restored with %q", got)
	}
}

// Deleting a page saves its subpages open in a window first, and closes
// them, so Undo brings them back with their last edits and no edit of
// theirs goes on saving to a deleted page.
func TestDeletingAPageKeepsItsOpenSubpagesEdits(t *testing.T) {
	c, _ := open(t)
	parent := create(t, c, `{"title":"Trip"}`)
	child := create(t, c, `{"title":"Packing","link":true,"parent_id":"`+parent.ID+`"}`)
	pg := openEditor(t, c, child.ID)
	if _, err := c.EditPage(child.ID, pg.Editor, doc.FromBlocks([]*doc.Node{paragraph("a", "Socks")}).Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	ids, err := c.DeletePage(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.pages[child.ID] != nil {
		t.Error("the deleted subpage stayed open")
	}
	if err := c.RestorePages(ids); err != nil {
		t.Fatal(err)
	}
	if got := texts(openEditor(t, c, child.ID).Content); got != "Socks" {
		t.Fatalf("restored with %q", got)
	}
}

// A page whose edits can't be saved as it closes stays open, and shows
// them opened again; it closes once a later save keeps them.
func TestPageClosedUnsavedKeepsItsEdits(t *testing.T) {
	c, _ := open(t)
	raw, _ := c.CreatePage(`{"title":"Notes"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	pg := openEditor(t, c, row.ID)
	if _, err := c.EditPage(row.ID, pg.Editor, doc.FromBlocks([]*doc.Node{paragraph("a", "Milk")}).Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	db := c.core.Store.DB()
	for _, when := range []string{"INSERT", "UPDATE"} {
		if _, err := db.Exec(`CREATE TRIGGER full_` + when + ` BEFORE ` + when + ` ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
			t.Fatal(err)
		}
	}
	c.ClosePage(row.ID, pg.Editor)
	pg = openEditor(t, c, row.ID)
	if got := texts(pg.Content); got != "Milk" {
		t.Fatalf("opened again with %q", got)
	}
	c.ClosePage(row.ID, pg.Editor)

	for _, when := range []string{"INSERT", "UPDATE"} {
		if _, err := db.Exec(`DROP TRIGGER full_` + when); err != nil {
			t.Fatal(err)
		}
	}
	if !c.SaveOpenPages() {
		t.Fatal("not saved")
	}
	var open bool
	c.q.do(func() { open = c.pages[row.ID] != nil })
	if open {
		t.Error("still open once saved")
	}
	if got := texts(c.core.Store.Content(row.ID)); got != "Milk" {
		t.Fatalf("stored %q", got)
	}
}

func wait(t *testing.T, ev *fakeEvents, reason string) update {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-ev.updates:
			if u.reason == reason {
				return u
			}
		case <-deadline:
			t.Fatalf("no %s update", reason)
		}
	}
}

// When says a time as the desktop does, in the language asked for, and
// nothing for none.
func TestWhen(t *testing.T) {
	now := time.Now()
	if got := When("ja", now.Add(-5*time.Minute).UnixMilli(), now.UnixMilli()); got != "5 分前" {
		t.Errorf("five minutes ago: %q", got)
	}
	if got := When("en", 0, now.UnixMilli()); got != "" {
		t.Errorf("no time: %q", got)
	}
}

func TestText(t *testing.T) {
	for _, c := range []struct{ locale, key, args, want string }{
		{"en", "commonCancel", "", "Cancel"},
		{"ja", "commonCancel", "", "キャンセル"},
		{"en", "appDeleted", `{"title":"Plan"}`, "Deleted Plan"},
		{"en", "cardSizeBytes", `{"count":1}`, "1 byte"},
		{"en", "cardSizeBytes", `{"count":3}`, "3 bytes"},
	} {
		if got := Text(c.locale, c.key, c.args); got != c.want {
			t.Errorf("Text(%s, %s, %s) = %q, want %q", c.locale, c.key, c.args, got, c.want)
		}
	}
	if got := Text("en", "pageTimeTodayAt", `{"at":{"date":0}}`); !strings.HasPrefix(got, "today at ") {
		t.Errorf("a date: %q", got)
	}
	if got := Language(`["ja-JP","en"]`); got != "ja" {
		t.Errorf("Language = %q", got)
	}
	var langs []language
	_ = json.Unmarshal([]byte(Languages()), &langs)
	if !slices.Contains(langs, language{Tag: "ja", Name: "日本語"}) {
		t.Errorf("Languages = %+v", langs)
	}
}

func TestCommentsAndAgents(t *testing.T) {
	c, _ := open(t)
	raw, _ := c.CreatePage(`{"title":"Plan"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)

	if err := c.AddComment(row.ID, "b1", "Is this right?", "", "the plan"); err != nil {
		t.Fatal(err)
	}
	raw, err := c.Threads(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var threads []threadJSON
	_ = json.Unmarshal([]byte(raw), &threads)
	if len(threads) != 1 || threads[0].Root.Quote != "the plan" {
		t.Fatalf("threads %+v", threads)
	}
	if err := c.AddComment(row.ID, "b1", "Yes", threads[0].Root.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.ResolveThread(threads[0].Root.ID, true); err != nil {
		t.Fatal(err)
	}
	raw, _ = c.Threads(row.ID)
	_ = json.Unmarshal([]byte(raw), &threads)
	if len(threads[0].Replies) != 1 || !threads[0].Root.Resolved {
		t.Fatalf("after a reply and resolving %+v", threads[0])
	}
	if !threads[0].MayResolve || !threads[0].Root.MayDelete || !threads[0].Replies[0].MayDelete {
		t.Fatalf("the user's own thread %+v", threads[0])
	}
	// The first comment deleted stays for its reply, without its words.
	if err := c.DeleteComment(threads[0].Root.ID); err != nil {
		t.Fatal(err)
	}
	raw, _ = c.Threads(row.ID)
	if strings.Contains(raw, "Is this right?") || strings.Contains(raw, "the plan") {
		t.Fatalf("a deleted comment's words are listed: %s", raw)
	}
	_ = json.Unmarshal([]byte(raw), &threads)
	if len(threads) != 1 || !threads[0].Root.Deleted || threads[0].Root.MayDelete || len(threads[0].Replies) != 1 {
		t.Fatalf("after deleting the first comment %+v", threads)
	}

	// No agent until a provider is added; a local one is kept here.
	raw, _ = c.AgentState(row.ID)
	var st agentState
	_ = json.Unmarshal([]byte(raw), &st)
	if st.Available || len(st.Options) != 0 {
		t.Fatalf("agents before a provider %+v", st)
	}
	if err := c.SaveProvider(`{"preset":"ollama","base_url":"http://127.0.0.1:11434","default_model":"llama3"}`); err != nil {
		t.Fatal(err)
	}
	raw, _ = c.AgentState(row.ID)
	_ = json.Unmarshal([]byte(raw), &st)
	if len(st.Options) != 1 || st.Options[0].Model != "llama3" {
		t.Fatalf("agents after a provider %+v", st)
	}
	// Signed out, a cloud provider is kept on this device, with its key:
	// without one it is not saved.
	if err := c.SaveProvider(`{"preset":"openai","base_url":"https://api.openai.com/v1","default_model":"gpt-5"}`); err == nil || err.Error() != locales.SettingsNeedKey() {
		t.Errorf("a cloud provider saved without its key: %v", err)
	}
}

func TestChartTextAndData(t *testing.T) {
	text := ChartText(`{"chart_type":"bar","labels":["Mon","Tue"],"series":[{"name":"You","values":[3,5.5]}]}`)
	if text != "label, You\nMon, 3\nTue, 5.5" {
		t.Fatalf("text: %q", text)
	}
	raw, err := ChartData("line", "Words", "", text+"\nWed, 7")
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	_ = json.Unmarshal([]byte(raw), &data)
	if data["chart_type"] != "line" || len(data["labels"].([]any)) != 3 {
		t.Fatalf("data: %v", data)
	}
	if _, err := ChartData("bar", "", "", "label, You\nMon, lots"); err == nil || !strings.Contains(err.Error(), "lots") {
		t.Fatalf("a value that is no number: %v", err)
	}
}

func TestTableRows(t *testing.T) {
	n, _ := doc.TableNode([][]string{{"A", "B"}, {"", ""}}, "")
	raw, _ := json.Marshal(n)
	rows, err := TableRows(string(raw))
	if err != nil || rows != `[["A","B"],["",""]]` {
		t.Fatalf("rows %s, %v", rows, err)
	}
	out, err := SetTableRows(string(raw), `[["A","B"],["*x*",""],["","y"]]`)
	if err != nil {
		t.Fatal(err)
	}
	var got doc.Node
	_ = json.Unmarshal([]byte(out), &got)
	if got.ID != n.ID || got.Int("rowsLen", 0) != 3 || doc.TableRows(&got)[1][0] != "*x*" {
		t.Errorf("table %+v", doc.TableRows(&got))
	}
}

// A comment runs an agent only where it shows the agent marked, not in
// code or an address; a run that fails says why in a notice.
func TestCommentMentionsRunAgents(t *testing.T) {
	c, ev := open(t)
	row := create(t, c, `{"title":"Plan"}`)
	// Nothing listens there.
	if err := c.SaveProvider(`{"preset":"ollama","name":"Ollama","base_url":"http://127.0.0.1:1","default_model":"llama3"}`); err != nil {
		t.Fatal(err)
	}
	running := func() (on bool) {
		c.q.do(func() { on = c.core.Hub.Session(row.ID).Running })
		return on
	}
	if err := c.AddComment(row.ID, "b1", "write to me@ollama.com, not `@agent`", "", ""); err != nil {
		t.Fatal(err)
	}
	if running() {
		t.Fatal("an agent runs for a comment that mentions none")
	}
	if err := c.AddComment(row.ID, "b1", "@Ollama sum it up", "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-ev.notices:
		if !strings.HasPrefix(msg, "Ollama") {
			t.Errorf("notice %q", msg)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no notice of the failed run")
	}
}

// @agent runs the default agent, not the first, and an agent the comment
// names runs before it.
func TestCommentMentionsRunTheDefaultForAgent(t *testing.T) {
	c, ev := open(t)
	row := create(t, c, `{"title":"Plan"}`)
	for _, name := range []string{"Ollama", "Studio"} {
		if err := c.SaveProvider(`{"preset":"ollama","name":"` + name + `","base_url":"http://127.0.0.1:1","default_model":"llama3"}`); err != nil {
			t.Fatal(err)
		}
	}
	c.q.do(func() {
		ps, _ := c.core.Store.Providers()
		s := c.core.Store.Settings()
		for _, p := range ps {
			if p.Name == "Studio" {
				s.DefaultModel = store.ModelChoice{ProviderID: p.ID}.Encode()
			}
		}
		_ = c.core.Store.SaveSettings(s)
	})
	for body, want := range map[string]string{"@agent sum it up": "Studio", "@agent ask @Ollama": "Ollama"} {
		if err := c.AddComment(row.ID, "b1", body, "", ""); err != nil {
			t.Fatal(err)
		}
		select {
		case msg := <-ev.notices:
			if !strings.HasPrefix(msg, want+":") {
				t.Errorf("%q: notice %q, want %s's", body, msg, want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%q: no notice of the failed run", body)
		}
	}
}

// The zone given before a core opens is time.Local, by name or else by
// offset; once one is open, time.Local stays as it was.
func TestSetTimeZoneOnlyBeforeOpen(t *testing.T) {
	zone.Lock()
	fixed, set := zone.fixed, setLocal
	zone.fixed = false
	var got []string
	setLocal = func(loc *time.Location) {
		_, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone()
		got = append(got, fmt.Sprintf("%s %d", loc, offset))
	}
	zone.Unlock()
	t.Cleanup(func() {
		zone.Lock()
		zone.fixed, setLocal = fixed, set
		zone.Unlock()
	})
	SetTimeZone("Pacific/Auckland", 0)
	SetTimeZone("Nowhere/Else", 3600)
	fixZone()
	SetTimeZone("Asia/Tokyo", 9*3600)
	if want := "Pacific/Auckland 46800,Nowhere/Else 3600"; strings.Join(got, ",") != want {
		t.Errorf("set %v, want %s", got, want)
	}
}

// Copy as plain text takes the blocks the app has selected, a subpage
// as its page's title.
func TestPlainCopy(t *testing.T) {
	c, _ := open(t)
	sub := create(t, c, `{"title":"Kyoto"}`)
	head := &doc.Node{ID: "h", Type: "heading", Data: map[string]any{"level": 1}}
	head.SetDelta(doc.FromMarkdown("Trip **plan**"))
	link := &doc.Node{ID: "s", Type: "subpage", Data: map[string]any{"page_id": sub.ID}}
	raw, _ := json.Marshal([]*doc.Node{head, link})
	got, err := c.PlainCopy(string(raw))
	if err != nil || got != "Trip plan\nKyoto" {
		t.Errorf("copied %q, %v", got, err)
	}
	if _, err := c.PlainCopy("not json"); err == nil {
		t.Error("copied blocks that are not JSON")
	}
}

// The library comes arranged as the desktop's sidebar arranges it: pages
// in their folders and under their parents, in order, and each tag's
// pages by title. A page whose folder is gone, as another device deleted
// the folder while this one moved the page into it, is at the top level:
// the sidebar shows no page with no place.
func TestLibraryTree(t *testing.T) {
	c, _ := open(t)
	var f folderRow
	raw, _ := c.CreateFolder("Work")
	_ = json.Unmarshal([]byte(raw), &f)
	plan := create(t, c, `{"title":"Plan","tags":["work"]}`)
	notes := create(t, c, `{"title":"notes","tags":["Work"],"folder_id":"`+f.ID+`"}`)
	second := create(t, c, `{"title":"Second","parent_id":"`+plan.ID+`"}`)
	first := create(t, c, `{"title":"First","parent_id":"`+plan.ID+`"}`)
	left := create(t, c, `{"title":"Left"}`)
	if _, err := c.core.Store.DB().Exec(`UPDATE pages SET folder_id = 'gone' WHERE id = ?`, left.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := c.Library()
	if err != nil {
		t.Fatal(err)
	}
	var lib library
	_ = json.Unmarshal([]byte(raw), &lib)
	if !slices.Contains(lib.Top, plan.ID) || !slices.Contains(lib.Top, left.ID) || slices.Contains(lib.Top, notes.ID) {
		t.Errorf("top %v", lib.Top)
	}
	if !slices.Equal(lib.InFolder[f.ID], []string{notes.ID}) || len(lib.Folders) != 1 || lib.Folders[0].ID != f.ID {
		t.Errorf("in folder %v, folders %+v", lib.InFolder, lib.Folders)
	}
	if !slices.Equal(lib.Children[plan.ID], []string{second.ID, first.ID}) {
		t.Errorf("under Plan %v, want Second, First", lib.Children[plan.ID])
	}
	i := slices.IndexFunc(lib.Tags, func(tg tagRow) bool { return strings.EqualFold(tg.Tag, "work") })
	if i < 0 || lib.Tags[i].Count != 2 || !slices.Equal(lib.Tags[i].Pages, []string{notes.ID, plan.ID}) {
		t.Errorf("tags %+v", lib.Tags)
	}
	if lib.Shared == nil {
		t.Error("no pages shared is null, not an empty list")
	}
}

// Copy puts blocks on the clipboard as their Markdown, which reads back
// as the blocks.
func TestCopyText(t *testing.T) {
	c, _ := open(t)
	item := func(text string) *doc.Node {
		n := &doc.Node{ID: doc.NewBlockID(), Type: "bulleted_list"}
		n.SetDelta(doc.FromMarkdown(text))
		return n
	}
	raw, _ := json.Marshal([]*doc.Node{item("abcd"), item("**efgh**")})
	got, err := c.CopyText(string(raw))
	if err != nil || got != "- abcd\n- **efgh**" {
		t.Fatalf("copied %q, %v", got, err)
	}
	if back := doc.ParseLines(got); len(back) != 2 || back[1].Type != "bulleted_list" || !back[1].Delta()[0].Attrs.Bold {
		t.Errorf("read back %+v", back)
	}
	if _, err := c.CopyText("not json"); err == nil {
		t.Error("copied what isn't blocks")
	}
}

// A colored cell reaches the table sheet as its Markdown, span and all,
// and saving the sheet keeps the color, of the cell edited too.
func TestTableRowsKeepColors(t *testing.T) {
	table, err := doc.TableNode([][]string{{`<span style="color:#ef4444ff">Red</span>`, "b"}}, "t")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := TableRows(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var grid [][]string
	if err := json.Unmarshal([]byte(rows), &grid); err != nil {
		t.Fatal(err)
	}
	if grid[0][0] != `<span style="color:#ef4444ff">Red</span>` {
		t.Fatalf("rows %q", grid)
	}
	grid[0][0] = `<span style="color:#ef4444ff">Red</span> edited`
	cells, _ := json.Marshal(grid)
	saved, err := SetTableRows(string(raw), string(cells))
	if err != nil {
		t.Fatal(err)
	}
	var got doc.Node
	if err := json.Unmarshal([]byte(saved), &got); err != nil {
		t.Fatal(err)
	}
	d := got.Children[0].Children[0].Delta()
	if len(d) != 2 || d[0].Attrs.Color != "#ef4444ff" || d[1].Text != " edited" {
		t.Fatalf("delta %+v", d)
	}
}
