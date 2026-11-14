package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/recording"
	"leaflet/internal/store"
)

type testMic struct{}

func (testMic) Pause()  {}
func (testMic) Resume() {}
func (testMic) Stop()   {}
func (testMic) Close()  {}

type testCapture struct{ sink recording.Sink }

func (c *testCapture) Start(_ int, sink recording.Sink) (recording.Mic, error) {
	c.sink = sink
	return testMic{}, nil
}

func recordingApp(t *testing.T) (*App, string) {
	t.Helper()
	keychain.Use(keys{})
	t.Cleanup(func() { keychain.Use(mygoSecrets{}) })
	a := templatesApp(t)
	mic := &testCapture{}
	a.rec.Attach(mic, a.update)
	stt := store.SttSettings{Provider: recording.Endpoint, BaseURL: "http://127.0.0.1:1/v1"}
	if err := a.rec.Start("p1", stt, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	mic.sink.Started()
	mic.sink.Chunk(make([]byte, 2*recording.SampleRate))
	a.rec.State.LiveText = "Heard so far"
	return a, a.rec.State.ID
}

func TestQuitWaitsForTheRecording(t *testing.T) {
	quits := 0
	quit := quitApp
	quitApp = func() { quits++ }
	defer func() { quitApp = quit }()
	for name, ask := range map[string]func(a *App) bool{
		"quit": func(a *App) bool {
			e := &mygo.QuitEvent{}
			a.beforeQuit(e)
			return e.DefaultPrevented()
		},
		"closing the window": func(a *App) bool {
			e := &mygo.CloseEvent{}
			a.closing(e)
			return e.DefaultPrevented()
		},
	} {
		a, id := recordingApp(t)
		quits = 0
		if !ask(a) || quits != 1 {
			t.Fatalf("%s: went on while recording, asked to quit %d times after", name, quits)
		}
		rec, err := a.st.Recording(id)
		if err != nil || rec.Status == "recording" || rec.DurationMs != 1000 || len(rec.Transcript) != 1 || rec.Transcript[0].Text != "Heard so far" {
			t.Errorf("%s: stored %+v, %v", name, rec, err)
		}
		if a.rec.Active() || a.rec.Saving() != 0 {
			t.Errorf("%s: active %v, saving %d", name, a.rec.Active(), a.rec.Saving())
		}
		if ask(a) || quits != 1 {
			t.Errorf("%s: held again once saved, asked to quit %d times", name, quits)
		}
	}
}

func TestQuitDoesNotWaitForever(t *testing.T) {
	quits := 0
	quit, wait := quitApp, quitWait
	quitApp, quitWait = func() { quits++ }, 10*time.Millisecond
	defer func() { quitApp, quitWait = quit, wait }()
	keychain.Use(keys{})
	t.Cleanup(func() { keychain.Use(mygoSecrets{}) })
	a := templatesApp(t)
	rec, err := a.st.CreateRecording("p1", "b1", recording.Endpoint, "", "", "auto")
	if err != nil {
		t.Fatal(err)
	}
	stuck := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-stuck:
		case <-r.Context().Done():
		}
	}))
	defer hs.Close()
	defer close(stuck)
	audio := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(audio, recording.WavFromPCM(make([]byte, 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.UpdateRecording(rec.ID, func(r *store.Recording) { r.LocalPath = audio }); err != nil {
		t.Fatal(err)
	}
	a.rec.Attach(&testCapture{}, a.update)
	if err := a.rec.Retranscribe(rec.ID, store.SttSettings{Provider: recording.Endpoint, BaseURL: hs.URL}, true); err != nil {
		t.Fatal(err)
	}
	e := &mygo.QuitEvent{}
	a.beforeQuit(e)
	if !e.DefaultPrevented() || quits != 1 {
		t.Fatalf("held %v, asked to quit %d times", e.DefaultPrevented(), quits)
	}
	e = &mygo.QuitEvent{}
	if a.beforeQuit(e); e.DefaultPrevented() {
		t.Error("the quit after the wait is held too")
	}
}

func TestRestartToUpdateStartsAgainAfterTheRecording(t *testing.T) {
	a, id := recordingApp(t)
	quits, relaunches, held := 0, 0, 0
	quit, relaunch := quitApp, relaunchApp
	quitApp = func() { quits++ }
	relaunchApp = func() {
		relaunches++
		e := &mygo.QuitEvent{}
		if a.beforeQuit(e); e.DefaultPrevented() {
			held++
		}
	}
	defer func() { quitApp, relaunchApp = quit, relaunch }()

	a.restartToUpdate()
	if relaunches != 2 || held != 1 || quits != 0 {
		t.Fatalf("asked to start again %d times, held %d, asked to quit %d", relaunches, held, quits)
	}
	if rec, err := a.st.Recording(id); err != nil || rec.Status == "recording" {
		t.Errorf("stored %+v, %v", rec, err)
	}
}

func TestLogoutDoesNotWaitForTheRecording(t *testing.T) {
	a, id := recordingApp(t)
	quits := 0
	quit := quitApp
	quitApp = func() { quits++ }
	defer func() { quitApp = quit }()
	e := &mygo.QuitEvent{SessionEnding: true}
	a.beforeQuit(e)
	if e.DefaultPrevented() || a.recKeeping || quits != 0 || !a.quitting {
		t.Fatalf("prevented %v, keeping %v, asked to quit %d times, quitting %v", e.DefaultPrevented(), a.recKeeping, quits, a.quitting)
	}
	if rec, err := a.st.Recording(id); err != nil || rec.Status != "recording" {
		t.Errorf("stored %+v, %v; want it left for RecoverInterrupted", rec, err)
	}
}

func TestRecordingHoldsOffAppNap(t *testing.T) {
	holds, releases := 0, 0
	keep := keepRunning
	keepRunning = func(string) func() { holds++; return func() { releases++ } }
	defer func() { keepRunning = keep }()
	a, _ := recordingApp(t)
	a.holdRunning()
	a.holdRunning()
	if holds != 1 || releases != 0 {
		t.Fatalf("while recording: held %d times, released %d", holds, releases)
	}
	a.rec.Stop()
	a.rec.Settle(5 * time.Second)
	a.holdRunning()
	if holds != 1 || releases != 1 || a.running != nil {
		t.Errorf("after the recording: held %d times, released %d", holds, releases)
	}
}
