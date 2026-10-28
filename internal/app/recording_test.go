package app

import (
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
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
	t.Cleanup(func() { delete(players, rec.ID) })
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
	pl := players[rec.ID]
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
	pl := &player{loading: true}
	players["r1"] = pl
	t.Cleanup(func() { delete(players, "r1") })
	a.closePage()
	if len(players) != 0 {
		t.Errorf("players kept: %v", players)
	}
}
