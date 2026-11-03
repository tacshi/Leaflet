package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A click on a line of a transcript plays the recording from the line,
// once its audio loads, and the menu copies the transcript.
func TestTranscriptLinePlays(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 20000, SttProvider: "soniox", Status: "ready", Transcript: []store.Segment{
		{StartMs: 0, EndMs: 4000, Text: "A client waited for a result."},
		{StartMs: 5000, EndMs: 9000, Text: "She could neither sleep nor concentrate."},
	}}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": rec.ID, "title": "Recording"}}
	// The audio loads on its own goroutine; what it hands back runs in a
	// frame, as Window.Update runs it on the main thread.
	var mu sync.Mutex
	var posted []func()
	a.post = func(fn func()) {
		mu.Lock()
		posted = append(posted, fn)
		mu.Unlock()
	}
	tt := ui.NewTester(func(c *ui.Context) {
		mu.Lock()
		fns := posted
		posted = nil
		mu.Unlock()
		for _, fn := range fns {
			fn()
		}
		a.recordingBlock(c, n)
	}, 700, 400)
	tt.Frame()
	if err := tt.Click("She could neither sleep nor concentrate."); err != nil {
		t.Fatal(err)
	}
	pl := a.players[rec.ID]
	if pl == nil || pl.from != 5*time.Second {
		t.Fatalf("player %+v", pl)
	}
	if err := tt.Click("Recording actions"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy transcript"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got, want := tt.Clipboard(), "A client waited for a result.\nShe could neither sleep nor concentrate."; got != want {
		t.Errorf("copied %q", got)
	}
}

// A page's recordings stop playing and let their audio go as it closes.
func TestClosingPageDropsPlayers(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.players = map[string]*player{"r1": {loading: true}}
	a.closePage()
	if len(a.players) != 0 {
		t.Errorf("players kept: %v", a.players)
	}
}

// A recording whose block is deleted, or undone, stops playing.
func TestDeletedRecordingStopsPlaying(t *testing.T) {
	a := templatesApp(t)
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": "Recording"}}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{n})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.players = map[string]*player{"r1": {}, "r2": {}}
	a.dropGonePlayers()
	if a.players["r1"] == nil || a.players["r2"] != nil {
		t.Fatalf("players %v", a.players)
	}
	a.page.editor.Edit(func(c *doc.Content) { c.Children = c.Children[:0] })
	a.dropGonePlayers()
	if len(a.players) != 0 {
		t.Errorf("players kept: %v", a.players)
	}
}

// Audio that could not load says why.
func TestPlaybackFailureShows(t *testing.T) {
	a := templatesApp(t)
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 2000, Status: "ready"}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	posted := make(chan func(), 1)
	a.post = func(fn func()) { posted <- fn }
	a.play(rec)
	(<-posted)()
	if len(a.toasts) != 1 || !strings.HasPrefix(a.toasts[0].text, strings.TrimSuffix(locales.RecCouldNotPlay(""), " ")) {
		t.Errorf("toasts %+v", a.toasts)
	}
}

// On a page that can't be changed, a recording's menu only copies its
// transcript.
func TestRecordingMenuOnAViewOnlyPage(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 2000, SttProvider: "soniox", Status: "ready",
		Transcript: []store.Segment{{StartMs: 0, EndMs: 1000, Text: "Hello."}}}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": rec.ID, "title": "Recording"}}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{n})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.page.editor.ReadOnly = true
	tt := ui.NewTester(func(c *ui.Context) { a.recordingBlock(c, n) }, 700, 400)
	tt.Frame()
	for _, item := range []string{locales.RecRename(), locales.RecInsertTranscript(), locales.RecSummarize(), locales.RecTranscribeAgain(), locales.CommonDelete()} {
		if err := tt.Click(locales.RecActions()); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(item); err == nil {
			t.Errorf("%s is offered", item)
		}
		tt.CloseMenu()
	}
	if err := tt.Click(locales.RecActions()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.RecCopyTranscript()); err != nil {
		t.Error(err)
	}
}

// Summarize asks the agent in the app's language, in a short message, as
// the agent reads the transcript with the block.
func TestSummarizeRecording(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.post = func(func()) {}
	if err := a.st.PutProvider(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 2000, SttProvider: "soniox", Status: "ready",
		Transcript: []store.Segment{{StartMs: 0, EndMs: 1000, Text: "Hello there."}}}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": rec.ID, "title": "Weekly sync"}}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{n})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.recordingBlock(c, n) }, 700, 400)
	tt.Frame()
	if err := tt.Click(locales.RecActions()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.RecSummarize()); err != nil {
		t.Fatal(err)
	}
	msgs := a.hub.Session(p.ID).Messages
	if len(msgs) == 0 || msgs[0].Text != locales.RecSummarizePrompt("Weekly sync") {
		t.Errorf("asked %+v", msgs)
	}
}

// Saving the Soniox key on the server empties its field alone: an
// endpoint's key typed beside it stays.
func TestSavingSonioxKeyKeepsEndpointKey(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(hs.Close)
	a := templatesApp(t)
	a.backend = remote.SignedInForTest(hs.URL, a.st, "u1")
	a.rec = recording.NewController(a.st, a.backend)
	posted := make(chan func(), 1)
	a.post = func(fn func()) { posted <- fn }
	a.speech.key, a.speech.soniox = "endpoint-key", "soniox-key"
	a.saveSonioxKey()
	(<-posted)()
	if a.speech.soniox != "" || a.speech.key != "endpoint-key" {
		t.Errorf("soniox %q, endpoint key %q", a.speech.soniox, a.speech.key)
	}
}

// The endpoint key typed is kept in the keychain once typing pauses, the
// last typed alone, and an emptied one is removed.
func TestEndpointKeyKeptOncePaused(t *testing.T) {
	keychain.Use(keys{})
	t.Cleanup(func() { keychain.Use(mygoSecrets{}) })
	a := templatesApp(t)
	kept := func(want string) {
		t.Helper()
		for end := time.Now().Add(3 * time.Second); recording.EndpointKey() != want; {
			if time.Now().After(end) {
				t.Fatalf("kept %q, want %q", recording.EndpointKey(), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for _, k := range []string{"s", "sk", "sk-1"} {
		a.keepEndpointKey(k)
	}
	if got := recording.EndpointKey(); got != "" {
		t.Errorf("kept %q as it was typed", got)
	}
	kept("sk-1")
	a.keepEndpointKey("")
	kept("")
	if _, err := keychain.Get("stt.endpoint.key"); !errors.Is(err, keychain.ErrNotFound) {
		t.Errorf("an emptied key stays: %v", err)
	}
}

// A recording block shows its transcript as it is once the recordings
// change, though it reads it once for the frames between.
func TestRecordingBlockFollowsChanges(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Call"})
	r, err := a.st.CreateRecording(p.ID, "r1", "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	if v := a.recordingView(r.ID); v.err != nil || len(v.lines) != 0 {
		t.Fatalf("a new recording shows %+v", v)
	}
	if _, err := a.st.UpdateRecording(r.ID, func(r *store.Recording) {
		r.Transcript = []store.Segment{{StartMs: 0, EndMs: 900, Text: "Hello."}}
	}); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "recordings", PageID: p.ID})
	if v := a.recordingView(r.ID); len(v.lines) != 1 || v.lines[0].Text != "Hello." {
		t.Errorf("once transcribed it shows %+v", v.lines)
	}
}

// A long transcript shows its first lines until it is opened, and closes
// again; a short one shows whole.
func TestLongTranscriptFolds(t *testing.T) {
	a := templatesApp(t)
	var segs []store.Segment
	for i := range doc.FoldedLines + 4 {
		segs = append(segs, store.Segment{StartMs: i * 2000, EndMs: i*2000 + 1500, Text: fmt.Sprintf("Line %d.", i+1)})
	}
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 30000, SttProvider: "soniox", Status: "ready", Transcript: segs}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": rec.ID, "title": "Call"}}
	tt := ui.NewTester(func(c *ui.Context) { a.recordingBlock(c, n) }, 700, 900)
	tt.Frame()
	last := fmt.Sprintf("Line %d.", len(segs))
	if !tt.HasText(fmt.Sprintf("Line %d.", doc.FoldedLines)) || tt.HasText(fmt.Sprintf("Line %d.", doc.FoldedLines+1)) {
		t.Fatalf("folded, it shows %q", tt.Texts())
	}
	if err := tt.Click(locales.RecShowAll(len(segs))); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText(last) {
		t.Fatalf("opened, it shows %q", tt.Texts())
	}
	if err := tt.Click(locales.RecShowLess()); err != nil {
		t.Fatal(err)
	}
	if tt.HasText(last) {
		t.Errorf("closed again, it shows %q", tt.Texts())
	}
	if doc.FoldsTranscript(doc.FoldedLines + 2) {
		t.Error("a transcript of two lines more than it folds to folds")
	}
}

// The menu renames a recording block, as one step to undo, and deletes it,
// which Undo brings back with its recording.
func TestRenameAndDeleteRecording(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 2000, SttProvider: "soniox", Status: "ready",
		Transcript: []store.Segment{{StartMs: 0, EndMs: 1000, Text: "Hello."}}}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": rec.ID, "title": "Weekly sync"}}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{n, {ID: "p1", Type: "paragraph"}})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ed := a.page.editor
	tt := ui.NewTester(func(c *ui.Context) {
		if n := ed.Content.Find("b1"); n != nil {
			a.recordingBlock(c, n)
		}
		a.dialogs.view(c, a)
	}, 700, 500)
	tt.Frame()
	if err := tt.Click(locales.RecActions()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.RecRename()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame() // the name is selected as the field takes the focus
	tt.Type("Standup")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if got := ed.Content.Find("b1").Str("title"); got != "Standup" || a.dialogs.recOpen {
		t.Fatalf("renamed %q, dialog open %v", got, a.dialogs.recOpen)
	}
	if !tt.HasText("Standup") {
		t.Errorf("the block shows %q", tt.Texts())
	}
	ed.Undo()
	if got := ed.Content.Find("b1").Str("title"); got != "Weekly sync" {
		t.Fatalf("after undo %q", got)
	}

	if err := tt.Click(locales.RecActions()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.CommonDelete()); err != nil {
		t.Fatal(err)
	}
	if ed.Content.Find("b1") != nil {
		t.Fatal("the block is still there")
	}
	ed.Undo()
	if ed.Content.Find("b1") == nil {
		t.Fatal("undo did not bring the block back")
	}
	if _, err := a.st.Recording(rec.ID); err != nil {
		t.Errorf("its recording went: %v", err)
	}
}

// A recording's notice closes with its ×, for good, on a page that can be
// changed: a warning over its transcript, as a live stream ending early
// leaves, and its status says it is ready.
func TestRecordingNoticeCloses(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 273000, SttProvider: "soniox", Status: "ready",
		Error: locales.RecErrLiveStopped(locales.RecLostNoEnd()), Transcript: []store.Segment{{StartMs: 0, EndMs: 1000, Text: "Hello."}}}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": rec.ID, "title": "Call"}}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{n})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.recordingBlock(c, n) }, 700, 400)
	tt.Frame()
	if !tt.HasText(rec.Error) || tt.HasText(locales.RecTranscriptionFailed()) || !tt.HasText(locales.RecStatus("4:33", "Soniox")) {
		t.Fatalf("it shows %q", tt.Texts())
	}
	if err := tt.Click(locales.PageDismiss()); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "recordings", PageID: p.ID})
	tt.Frame()
	if got, _ := a.st.Recording(rec.ID); got.Error != "" || tt.HasText(rec.Error) {
		t.Errorf("closed, the recording has %q and the block shows %q", got.Error, tt.Texts())
	}
}

// While it records, a block shows the last two lines of the words heard:
// as more come, it keeps its height.
func TestLiveTextKeepsTwoLines(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": "Call"}}
	a.rec.State = recording.State{Active: true, ID: "r1", BlockID: "b1"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			a.recordingBlock(c, n)
			ui.Text(c, "Below")
		})
	}, 500, 900)
	below := func(words int) float32 {
		a.rec.State.LiveText = strings.TrimSpace(strings.Repeat("words heard live ", words))
		tt.Frame()
		r, ok := tt.Find("Below")
		if !ok {
			t.Fatalf("no text below the block: %q", tt.Texts())
		}
		return r.Y
	}
	one, two, many := below(1), below(12), below(200)
	if two <= one || many != two {
		t.Errorf("the text below is at %v with a line heard, %v with two and %v with many", one, two, many)
	}
}
