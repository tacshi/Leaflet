package app

import (
	"encoding/json"
	"errors"
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
	for _, item := range []string{locales.RecInsertTranscript(), locales.RecSummarize(), locales.RecTranscribeAgain()} {
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

// The speech settings sent as each key is typed reach the server one at a
// time, the last last: sent at once, an earlier one could land after it.
func TestSpeechSettingsLastSentLast(t *testing.T) {
	first := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var saved []string
	var once sync.Once
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var row struct {
			BaseURL string `json:"base_url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&row)
		if row.BaseURL == "https://a" {
			once.Do(func() { close(first) })
			<-release
		}
		mu.Lock()
		saved = append(saved, row.BaseURL)
		mu.Unlock()
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(hs.Close)
	a := templatesApp(t)
	a.backend = remote.SignedInForTest(hs.URL, a.st, "u1")
	a.settings.Stt.BaseURL = "https://a"
	a.syncSttSettings()
	<-first
	a.settings.Stt.BaseURL = "https://ab"
	a.syncSttSettings()
	time.Sleep(100 * time.Millisecond)
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := append([]string(nil), saved...)
		mu.Unlock()
		if len(got) == 2 {
			if got[1] != "https://ab" {
				t.Errorf("the server ended with %q", got[1])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("saved %v", got)
		}
		time.Sleep(10 * time.Millisecond)
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
	posted := make(chan func(), 1)
	a.post = func(fn func()) { posted <- fn }
	a.speech.key, a.speech.soniox = "endpoint-key", "soniox-key"
	a.saveSttKey("soniox", a.speech.soniox)
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
