package recording

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Audio recorded while the stream connects reaches it first, in order; a
// stream that connects for a recording gone since is closed, not used.
func TestSonioxGetsTheOpeningAudio(t *testing.T) {
	heard := make(chan []byte, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			heard <- data
		}
	}))
	defer srv.Close()
	dial := func() *Soniox {
		conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return newSoniox(conn)
	}

	wav, err := CreateWav(filepath.Join(t.TempDir(), "a.wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wav.Close() }()
	r := &Controller{update: func(fn func()) { fn() }, wav: wav, session: "s1", connecting: true}
	r.chunk("s1", []byte{1, 1})
	r.chunk("s1", []byte{2, 2})
	r.chunk("old", []byte{9, 9})
	s := dial()
	r.attachSoniox("s1", s)
	if r.stream != s || r.connecting || len(r.early) != 0 {
		t.Fatalf("not attached: %v %v %d", r.stream == s, r.connecting, len(r.early))
	}
	r.chunk("s1", []byte{3, 3})
	for _, want := range [][]byte{{1, 1}, {2, 2}, {3, 3}} {
		select {
		case got := <-heard:
			if !bytes.Equal(got, want) {
				t.Fatalf("heard %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("never heard %v", want)
		}
	}

	// The next recording started before this stream connected.
	r.session, r.stream, r.connecting = "s2", nil, true
	late := dial()
	r.attachSoniox("s1", late)
	if r.stream != nil || !r.connecting {
		t.Error("a stream for the last recording took this one")
	}
}

// Pause before the microphone starts changes nothing: there is no time
// running yet.
func TestPauseWhileStarting(t *testing.T) {
	r := &Controller{}
	r.TogglePause()
	if r.State.Paused || r.Elapsed() != 0 {
		t.Errorf("paused %v, elapsed %v", r.State.Paused, r.Elapsed())
	}
}

// A stream that hears no audio, as while a recording is paused, is kept
// open with keepalives; one that ends before it is finished says why, and
// one ended on purpose does not.
func TestSonioxKeepsAliveAndTellsALostStream(t *testing.T) {
	was := keepAliveAfter
	keepAliveAfter = 50 * time.Millisecond
	t.Cleanup(func() { keepAliveAfter = was })
	texts := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		for {
			kind, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			if kind == websocket.MessageText {
				texts <- string(data)
				if req.URL.Query().Get("drop") != "" {
					_ = c.Close(websocket.StatusPolicyViolation, "timeout")
					return
				}
			}
		}
	}))
	defer srv.Close()
	dial := func(query string) *Soniox {
		conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		return newSoniox(conn)
	}

	s := dial("")
	select {
	case got := <-texts:
		if got != `{"type":"keepalive"}` {
			t.Errorf("sent %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no keepalive")
	}
	s.Close()
	<-s.done
	if err := s.Lost(); err != nil {
		t.Errorf("a stream closed here was lost: %v", err)
	}

	s = dial("?drop=1")
	<-s.done
	if s.Lost() == nil {
		t.Error("a stream the server ended was not lost")
	}
}

// A microphone that answers after the recording stopped while starting
// brings back no recording; one that answers in time starts it.
func TestStartedAfterStopIsIgnored(t *testing.T) {
	r := &Controller{update: func(fn func()) { fn() }, session: "s1"}
	// Stop clears the state and keeps the session until the page is gone.
	r.State = State{}
	r.started("s1", 0)
	if r.State.Active || r.Active() {
		t.Errorf("stopped recording came back: %+v", r.State)
	}
	r.State = State{Starting: true}
	r.started("old", 0)
	if r.State.Active || !r.State.Starting {
		t.Errorf("another session's microphone started this one: %+v", r.State)
	}
	r.started("s1", 0)
	if !r.State.Active || r.State.Starting || r.resumed.IsZero() {
		t.Errorf("not started: %+v", r.State)
	}
}

// No audio goes after the end of it that Finish sends.
func TestSonioxSendsNothingAfterFinish(t *testing.T) {
	heard := make(chan []byte, 10)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		for ended := false; ; {
			wait := time.Minute
			if ended {
				wait = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			kind, data, err := c.Read(ctx)
			cancel()
			if err != nil {
				_ = c.Close(websocket.StatusNormalClosure, "")
				return
			}
			heard <- data
			// As Soniox does, only an empty text frame ends the stream.
			if kind == websocket.MessageText && len(data) == 0 && !ended {
				ended = true
				<-release
			}
		}
	}))
	defer srv.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newSoniox(conn)
	s.Send([]byte{1})
	finished := make(chan struct{})
	go func() {
		_, _ = s.Finish()
		close(finished)
	}()
	for _, want := range [][]byte{{1}, {}} {
		if got := <-heard; !bytes.Equal(got, want) {
			t.Fatalf("heard %v, want %v", got, want)
		}
	}
	s.Send([]byte{7})
	close(release)
	<-finished
	select {
	case got := <-heard:
		t.Errorf("heard %v after the end", got)
	default:
	}
}

// Sending audio does not wait for the network: a write that stalls, as
// over a slow connection, holds up neither the chunks nor the WAV file
// they go to; what was queued goes after, in order.
func TestSonioxSendDoesNotWaitForTheNetwork(t *testing.T) {
	heard := make(chan []byte, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			heard <- data
		}
	}))
	defer srv.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newSoniox(conn)
	defer s.Close()
	// A write under way holds the writes up.
	s.wmu.Lock()
	sent := make(chan struct{})
	go func() {
		s.Send([]byte{1})
		s.Send([]byte{2})
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("Send waited for the write")
	}
	s.wmu.Unlock()
	for _, want := range [][]byte{{1}, {2}} {
		select {
		case got := <-heard:
			if !bytes.Equal(got, want) {
				t.Fatalf("heard %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("never heard %v", want)
		}
	}
}

// A microphone that fails late, for a recording stopped since, leaves the
// next recording starting; one that fails for this one ends it.
func TestFailedForAnotherRecordingIsIgnored(t *testing.T) {
	r := &Controller{update: func(fn func()) { fn() }, session: "s2", State: State{Starting: true}}
	r.failed("s1", "denied")
	if !r.State.Starting || r.State.Error != "" {
		t.Errorf("another session's microphone ended this one: %+v", r.State)
	}
	r.failed("s2", "denied")
	if r.State.Starting || r.State.Error == "" {
		t.Errorf("not ended: %+v", r.State)
	}
}

// fakeMic is a microphone whose Stop hands on the audio it still holds,
// once let go.
type fakeMic struct {
	sink    Sink
	held    []byte
	letGo   chan struct{}
	stopped chan struct{}
	// toggles are its Pauses and Resumes, in order.
	mu      sync.Mutex
	toggles []string
}

func (m *fakeMic) Pause()  { m.toggle("pause") }
func (m *fakeMic) Resume() { m.toggle("resume") }

func (m *fakeMic) toggle(what string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toggles = append(m.toggles, what)
}
func (m *fakeMic) Stop() {
	<-m.letGo
	if m.held != nil {
		m.sink.Chunk(m.held)
	}
}
func (m *fakeMic) Close() { close(m.stopped) }

// fakeCapture starts fakeMics, the latest last.
type fakeCapture struct{ mics []*fakeMic }

func (c *fakeCapture) Start(_ int, sink Sink) (Mic, error) {
	m := &fakeMic{sink: sink, letGo: make(chan struct{}), stopped: make(chan struct{})}
	c.mics = append(c.mics, m)
	return m, nil
}

func newTestController(t *testing.T) (*Controller, *store.Store, *fakeCapture) {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := NewController(st, remote.New(remote.Settings{}, st))
	c := &fakeCapture{}
	var mu sync.Mutex
	r.Attach(c, func(fn func()) {
		mu.Lock()
		defer mu.Unlock()
		fn()
	})
	return r, st, c
}

// The audio a microphone still holds as its recording stops goes to that
// recording, though the next one has started before it came.
func TestLastAudioGoesToItsRecording(t *testing.T) {
	r, _, c := newTestController(t)
	dir := t.TempDir()
	first, err := CreateWav(filepath.Join(dir, "first.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.capture(first, store.SttSettings{}, false, State{}); err != nil {
		t.Fatal(err)
	}
	mic := c.mics[0]
	mic.sink.Started()
	mic.sink.Chunk([]byte{1, 1})
	mic.held = []byte{2, 2}
	r.Stop()

	second, err := CreateWav(filepath.Join(dir, "second.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.capture(second, store.SttSettings{}, false, State{}); err != nil {
		t.Fatal(err)
	}
	// Windows removes no file still open, so neither is when the folder
	// goes. The second recording, its microphone never started, ends with
	// its file closed at once.
	t.Cleanup(r.Stop)
	close(mic.letGo)
	<-mic.stopped
	c.mics[1].sink.Chunk([]byte{3, 3})
	deadline := time.Now().Add(5 * time.Second)
	for {
		// The header counts the audio once Close writes it, under late,
		// just before it closes the file: read under late, it is closed.
		r.late.Lock()
		got, _ := ReadWav(first.Path)
		r.late.Unlock()
		if bytes.Equal(got, []byte{1, 1, 2, 2}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first recording has %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if second.bytes != 2 {
		t.Errorf("the second recording has %d bytes", second.bytes)
	}
}

// Audio a microphone hands on from a thread of its own while its
// recording stops, as the desktop's web microphone does, goes in before
// the file closes, or not at all: the header counts every byte of it.
func TestLateAudioWhileStoppingGoesInWhole(t *testing.T) {
	r, st, c := newTestController(t)
	// The audio and the stop meet at a different moment each time.
	for range 20 {
		lateAudioWhileStopping(t, r, st, c)
	}
}

func lateAudioWhileStopping(t *testing.T, r *Controller, st *store.Store, c *fakeCapture) {
	t.Helper()
	if err := r.Start("p1", store.SttSettings{}, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	id := r.State.ID
	mic := c.mics[len(c.mics)-1]
	mic.sink.Started()
	close(mic.letGo)
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for {
			select {
			case <-mic.stopped:
				return
			default:
				mic.sink.Chunk([]byte{1, 1})
			}
		}
	}()
	r.Stop()
	<-sent
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, _ := st.Recording(id)
		if rec.Status == "ready" {
			raw, err := os.ReadFile(rec.LocalPath)
			if err != nil {
				t.Fatal(err)
			}
			if size := int(binary.LittleEndian.Uint32(raw[40:])); size != len(raw)-44 {
				t.Fatalf("the header counts %d bytes of %d", size, len(raw)-44)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never ready: %+v", rec)
		}
		time.Sleep(time.Millisecond)
	}
}

// A recording stopped is transcribing from then on: a Retranscribe
// before its transcription starts refuses, rather than run beside it.
func TestStoppedRecordingIsTranscribingAtOnce(t *testing.T) {
	r, st, c := newTestController(t)
	stt := store.SttSettings{Provider: "openai-compatible", BaseURL: "http://127.0.0.1:1/v1"}
	if err := r.Start("p1", stt, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	id := r.State.ID
	mic := c.mics[0]
	mic.sink.Started()
	r.Stop()
	if !r.Transcribing[id] {
		t.Error("not transcribing once stopped")
	}
	if err := r.Retranscribe(id, stt, false); err == nil {
		t.Error("a second transcription started")
	}
	close(mic.letGo)
	<-mic.stopped
	deadline := time.Now().Add(10 * time.Second)
	for {
		if rec, _ := st.Recording(id); rec.Status == "failed" || rec.Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A recording transcribed again before the app gave the recorder its
// microphone says so, rather than end on no main thread.
func TestRetranscribeBeforeAttach(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	r := NewController(st, remote.New(remote.Settings{}, st))
	rec, err := st.CreateRecording("p1", "b1", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Retranscribe(rec.ID, store.SttSettings{Provider: "openai-compatible", BaseURL: "http://127.0.0.1:1/v1"}, true); err == nil {
		t.Error("transcribing with no main thread")
	}
}

// fakeEngine transcribes every file as "hello".
type fakeEngine struct{}

func (fakeEngine) Stream(string, func(string)) (Stream, error) { return nil, errors.New("no stream") }
func (fakeEngine) File(string, string) ([]store.Segment, error) {
	return []store.Segment{{Text: "hello"}}, nil
}

// While the host looks for the device's engine, it counts as usable, and
// a transcription waits for it; the transcriptions read it from their own
// goroutines while the main thread gives it.
func TestEngineFoundLate(t *testing.T) {
	r := &Controller{}
	stt := store.SttSettings{Provider: OnDevice}
	r.FindingEngine()
	if err := r.Usable(stt, false); err != nil {
		t.Errorf("not usable while looking: %v", err)
	}
	got := make(chan Engine, 1)
	go func() { got <- r.waitEngine(context.Background()) }()
	r.SetEngine(fakeEngine{})
	if e := <-got; e == nil {
		t.Error("the transcription did not get the engine")
	}
	r.SetEngine(nil)
	if err := r.Usable(stt, false); err == nil {
		t.Error("usable with no engine")
	}
}

// The live text that comes as a stream starts reaches what is set to
// take it, however soon it comes.
func TestSonioxTextBeforeItIsTaken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"tokens":[{"text":"hi","is_final":true}]}`))
		_, _, _ = c.Read(context.Background())
	}))
	defer srv.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newSoniox(conn)
	defer s.Close()
	texts := make(chan string, 2)
	s.SetOnText(func(text string) { texts <- text })
	select {
	case got := <-texts:
		if got != "hi" {
			t.Errorf("text %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no text")
	}
}

// A recording stopped before its microphone started recorded nothing: it
// fails as one whose microphone could not start, and nothing transcribes
// it.
func TestStopWhileStarting(t *testing.T) {
	r, st, c := newTestController(t)
	stt := store.SttSettings{Provider: "openai-compatible", BaseURL: "http://127.0.0.1:1/v1"}
	if err := r.Start("p1", stt, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	id := r.State.ID
	r.Stop()
	select {
	case <-c.mics[0].stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the microphone was never let go")
	}
	if rec, _ := st.Recording(id); rec.Status != "failed" || rec.Error == "" || r.Transcribing[id] || r.Active() {
		t.Errorf("recording %+v, transcribing %v, active %v", rec, r.Transcribing[id], r.Active())
	}
}

// A recording lasts as long as the audio it saved, not the time it ran.
func TestDurationIsTheAudios(t *testing.T) {
	r, st, c := newTestController(t)
	if err := r.Start("p1", store.SttSettings{}, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	id := r.State.ID
	mic := c.mics[0]
	mic.sink.Started()
	mic.sink.Chunk(make([]byte, 2*SampleRate))
	mic.held = make([]byte, SampleRate)
	r.Stop()
	close(mic.letGo)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, _ := st.Recording(id)
		if rec.Status == "ready" {
			if rec.DurationMs != 1500 {
				t.Errorf("lasts %d ms", rec.DurationMs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never ready: %+v", rec)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Pause and Resume reach the microphone off the main thread, and it ends
// as the last asked for, however fast they come.
func TestTogglesEndWithTheLast(t *testing.T) {
	r, _, c := newTestController(t)
	if err := r.Start("p1", store.SttSettings{}, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	mic := c.mics[0]
	mic.sink.Started()
	for range 3 {
		r.TogglePause()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mic.mu.Lock()
		toggles := slices.Clone(mic.toggles)
		mic.mu.Unlock()
		if len(toggles) == 3 {
			if toggles[2] != "pause" {
				t.Errorf("toggled %v", toggles)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("toggled %v", toggles)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(mic.letGo)
	r.Stop()
	<-mic.stopped
}

// A microphone that stalls pauses its recording, saying why; another
// recording's says nothing.
func TestStalledPauses(t *testing.T) {
	r, _, c := newTestController(t)
	if err := r.Start("p1", store.SttSettings{}, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	mic := c.mics[0]
	mic.sink.Started()
	r.stalled("another", "taken")
	if r.State.Paused || r.State.Error != "" {
		t.Errorf("another session's microphone paused this one: %+v", r.State)
	}
	mic.sink.Stalled("taken")
	if !r.State.Paused || !strings.Contains(r.State.Error, "taken") {
		t.Errorf("not paused: %+v", r.State)
	}
	close(mic.letGo)
	r.Stop()
	<-mic.stopped
}

// A recording a stall paused goes on once its microphone is back, as a
// call ends; one the user paused meanwhile stays paused.
func TestRecoveredResumesWhatTheStallPaused(t *testing.T) {
	r, _, c := newTestController(t)
	if err := r.Start("p1", store.SttSettings{}, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	mic := c.mics[0]
	mic.sink.Started()
	mic.sink.Stalled("taken")
	if !r.State.Paused {
		t.Fatalf("not paused: %+v", r.State)
	}
	mic.sink.Recovered()
	if r.State.Paused || r.State.Error != "" {
		t.Errorf("not resumed: %+v", r.State)
	}
	mic.sink.Stalled("taken")
	r.TogglePause() // the user resumes it
	r.TogglePause() // and pauses it again
	mic.sink.Recovered()
	if !r.State.Paused {
		t.Errorf("the user's pause went: %+v", r.State)
	}
	close(mic.letGo)
	r.Stop()
	<-mic.stopped
}

// lostStream is a live stream that ended before Finish, having heard part
// of the recording.
type lostStream struct{}

func (lostStream) Send([]byte) {}
func (lostStream) Finish() ([]store.Segment, error) {
	return []store.Segment{{EndMs: 1000, Text: "Hello"}}, nil
}
func (lostStream) Close()      {}
func (lostStream) Lost() error { return errors.New("dropped") }

// A stream lost before Finish keeps the words it heard: the recording is
// ready, and its error warns they may end too soon, to transcribe again
// from its audio.
func TestLostBeforeFinish(t *testing.T) {
	r, st, _ := newTestController(t)
	rec, err := st.CreateRecording("p1", "b1", "soniox", "", "", "auto")
	if err != nil {
		t.Fatal(err)
	}
	r.finish(rec, "", lostStream{}, store.SttSettings{Provider: "soniox"})
	got, _ := st.Recording(rec.ID)
	if got.Status != "ready" || !strings.Contains(got.Error, "dropped") || len(got.Transcript) != 1 || r.Transcribing[rec.ID] {
		t.Errorf("recording %+v, transcribing %v", got, r.Transcribing[rec.ID])
	}
}

// A stream lost before it heard a word leaves the recording failed, and
// its own reasons are said in the app's language; the block's notice of
// either closes for good.
func TestLostWithoutWordsFails(t *testing.T) {
	r, st, _ := newTestController(t)
	rec, err := st.CreateRecording("p1", "b1", "soniox", "", "", "auto")
	if err != nil {
		t.Fatal(err)
	}
	r.finish(rec, "", silentLost{}, store.SttSettings{Provider: "soniox"})
	got, _ := st.Recording(rec.ID)
	if want := locales.RecErrLiveStopped(locales.RecLostNoEnd()); got.Status != "failed" || got.Error != want || len(got.Transcript) != 0 {
		t.Fatalf("recording %+v, want the error %q", got, want)
	}
	if err := r.DismissError(rec.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Recording(rec.ID); got.Error != "" || got.Status != "failed" {
		t.Errorf("dismissed, recording %+v", got)
	}
	if got := lostReason(errBehind); got != locales.RecLostBehind() {
		t.Errorf("a slow connection says %q", got)
	}
}

// silentLost is a live stream whose last words never came, having heard
// none before.
type silentLost struct{ lostStream }

func (silentLost) Finish() ([]store.Segment, error) { return nil, nil }
func (silentLost) Lost() error                      { return errNoEnd }

// A short recording, its words final before it stops, ends whole: Finish
// ends the stream as Soniox asks, with an empty text frame, and does not
// wait out its time for words that are not coming. An empty binary frame
// is only an empty piece of audio to Soniox, which then answers nothing.
func TestSonioxFinishEndsTheStreamAsSonioxAsks(t *testing.T) {
	was := finishWait
	finishWait = 2 * time.Second
	t.Cleanup(func() { finishWait = was })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		ctx := context.Background()
		for {
			kind, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if kind == websocket.MessageBinary && len(data) > 0 {
				_ = c.Write(ctx, websocket.MessageText, []byte(`{"tokens":[{"text":"现在开始录音。","start_ms":0,"end_ms":1500,"is_final":true}]}`))
			}
			if kind == websocket.MessageText && len(data) == 0 {
				_ = c.Write(ctx, websocket.MessageText, []byte(`{"tokens":[],"finished":true}`))
				_ = c.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}))
	defer srv.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newSoniox(conn)
	s.Send([]byte{1, 2})
	start := time.Now()
	segs, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took >= finishWait {
		t.Errorf("Finish waited %v for the end", took)
	}
	if lost := s.Lost(); lost != nil {
		t.Errorf("a stream that ended whole was lost: %v", lost)
	}
	if len(segs) != 1 || segs[0].Text != "现在开始录音。" {
		t.Errorf("segments %+v", segs)
	}
}

// Audio still queued when Finish stops waiting for a slow connection, or
// last words that never come, leave the stream lost: the recording keeps
// its words and fails, to transcribe again from its audio, rather than
// counting as whole.
func TestSonioxFinishTellsWhatItDropped(t *testing.T) {
	was := finishWait
	finishWait = 100 * time.Millisecond
	t.Cleanup(func() { finishWait = was })
	for _, answers := range []bool{true, false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c, err := websocket.Accept(w, req, nil)
			if err != nil {
				return
			}
			for {
				kind, data, err := c.Read(context.Background())
				if err != nil {
					return
				}
				if kind == websocket.MessageText && len(data) == 0 && answers {
					_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"tokens":[],"finished":true}`))
				}
			}
		}))
		conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		s := newSoniox(conn)
		if answers {
			// A slow connection: the write under way holds the queue up.
			s.wmu.Lock()
			for range 5 {
				s.Send([]byte{1, 2})
			}
			time.AfterFunc(3*finishWait/2, s.wmu.Unlock)
		}
		_, _ = s.Finish()
		want := map[bool]error{true: errBehind, false: errNoEnd}[answers]
		if got := s.Lost(); got != want {
			t.Errorf("server answers %v: lost %v, want %v", answers, got, want)
		}
		srv.Close()
	}
}

// A long recording, which the transcriber goes on with past a call's
// deadline, is asked for again until it is done; another failure, or
// asking for too long, ends it.
func TestTranscribeWaitsForALongRecording(t *testing.T) {
	defer func(every, long time.Duration) { stillEvery, stillFor = every, long }(stillEvery, stillFor)
	stillEvery = time.Millisecond
	var calls atomic.Int32
	still := int32(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch n := calls.Add(1); {
		case n <= still:
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"error":"still working","code":"still_transcribing"}`))
		case still < 0:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"failed"}`))
		default:
			_, _ = w.Write([]byte(`{"segments":[{"start_ms":0,"end_ms":1000,"text":"at last"}]}`))
		}
	}))
	defer srv.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	r := &Controller{st: st, be: remote.SignedInForTest(srv.URL, st, "u1")}
	rec := store.Recording{ID: "r1", AudioPath: "u1/r1.wav"}
	stt := store.SttSettings{Provider: "soniox"}

	segs, err := r.transcribe(rec, "", nil, stt)
	if err != nil || len(segs) != 1 || segs[0].Text != "at last" || calls.Load() != 3 {
		t.Errorf("after %d calls: %+v %v", calls.Load(), segs, err)
	}

	calls.Store(0)
	still = -1
	if _, err := r.transcribe(rec, "", nil, stt); err == nil || calls.Load() != 1 {
		t.Errorf("a failure was asked again: %d calls, %v", calls.Load(), err)
	}

	calls.Store(0)
	still, stillFor = 1<<30, 5*time.Millisecond
	if _, err := r.transcribe(rec, "", nil, stt); !remote.IsStillTranscribing(err) {
		t.Errorf("asked for too long: %v", err)
	}
}

// fileRecorder is a Controller that runs what goes to the main thread at
// once, on a store of its own, with one audio file in it, and the file.
func fileRecorder(t *testing.T, url, name string) (*Controller, store.File) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	r := NewController(st, remote.SignedInForTest(url, st, "u1"))
	r.update = func(fn func()) { fn() }
	return r, f
}

// waitTranscript waits for what TranscribeFile gives done.
func waitTranscript(t *testing.T, got chan []store.Segment) []store.Segment {
	t.Helper()
	select {
	case segs := <-got:
		return segs
	case <-time.After(5 * time.Second):
		t.Fatal("no transcript")
		return nil
	}
}

// An audio file transcribes on the device, its own file, and only one
// transcription of it runs at a time.
func TestTranscribeFileOnDevice(t *testing.T) {
	r, f := fileRecorder(t, "http://127.0.0.1:1", "call.m4a")
	r.SetEngine(fakeEngine{})
	stt := store.SttSettings{Provider: OnDevice}
	got := make(chan []store.Segment, 1)
	hold := make(chan struct{})
	var once sync.Once
	realUpdate := r.update
	r.update = func(fn func()) {
		<-hold
		realUpdate(fn)
	}
	done := func(segs []store.Segment, err error) {
		if err != nil {
			t.Error(err)
		}
		got <- segs
	}
	if err := r.TranscribeFile(f, "b1", stt, false, done); err != nil {
		t.Fatal(err)
	}
	if err := r.TranscribeFile(f, "b2", stt, false, done); err == nil || err.Error() != locales.RecErrFileTranscribing() {
		t.Errorf("a second transcription: %v", err)
	}
	once.Do(func() { close(hold) })
	if segs := waitTranscript(t, got); len(segs) != 1 || segs[0].Text != "hello" {
		t.Errorf("segments %+v", segs)
	}
	if len(r.TranscribingFiles) != 0 {
		t.Errorf("still transcribing %v", r.TranscribingFiles)
	}
}

// What the device can't read, or that is no audio, is refused at once; a
// file still going up can't go to the server.
func TestTranscribeFileRefused(t *testing.T) {
	r, f := fileRecorder(t, "http://127.0.0.1:1", "call.ogg")
	r.SetEngine(fakeEngine{})
	nothing := func([]store.Segment, error) { t.Error("it ran") }
	if err := r.TranscribeFile(f, "b", store.SttSettings{Provider: OnDevice}, false, nothing); err == nil || err.Error() != locales.RecErrFileKind() {
		t.Errorf("on the device: %v", err)
	}
	r, f = fileRecorder(t, "http://127.0.0.1:1", "notes.txt")
	if err := r.TranscribeFile(f, "b", store.SttSettings{Provider: OnDevice}, false, nothing); err == nil || err.Error() != locales.RecErrFileKind() {
		t.Errorf("no audio: %v", err)
	}
	r, f = fileRecorder(t, "http://127.0.0.1:1", "call.m4a")
	if err := r.TranscribeFile(f, "b", store.SttSettings{Provider: "soniox"}, true, nothing); err == nil || err.Error() != locales.RecErrFileUploading() {
		t.Errorf("not up yet: %v", err)
	}
}

// On the server a file goes by its ID, asked for again while it goes on,
// and word that the app has it goes after.
func TestTranscribeFileOnServer(t *testing.T) {
	defer func(every time.Duration) { stillEvery = every }(stillEvery)
	stillEvery = time.Millisecond
	var calls atomic.Int32
	bodies := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		bodies <- body
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"error":"still working","code":"still_transcribing"}`))
			return
		}
		_, _ = w.Write([]byte(`{"segments":[{"start_ms":5000,"end_ms":6000,"text":"Hello."}]}`))
	}))
	defer srv.Close()
	r, f := fileRecorder(t, srv.URL, "call.m4a")
	queued, err := r.st.Queued()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range queued {
		if err := r.st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	got := make(chan []store.Segment, 1)
	stt := store.SttSettings{Provider: "soniox", Language: "en"}
	err = r.TranscribeFile(f, "b", stt, true, func(segs []store.Segment, err error) {
		if err != nil {
			t.Error(err)
		}
		got <- segs
	})
	if err != nil {
		t.Fatal(err)
	}
	if segs := waitTranscript(t, got); len(segs) != 1 || segs[0].StartMs != 5000 {
		t.Errorf("segments %+v", segs)
	}
	if first := <-bodies; first["action"] != "transcribe" || first["file_id"] != f.ID || first["language"] != "en" {
		t.Errorf("asked %v", first)
	}
	<-bodies
	r.FileTranscribed(f, stt)
	select {
	case ack := <-bodies:
		if ack["action"] != "transcribed" || ack["file_id"] != f.ID {
			t.Errorf("told %v", ack)
		}
	case <-time.After(5 * time.Second):
		t.Error("the server was not told")
	}
}

// Functions older than the app say so in words for the user, not theirs.
func TestTranscribeFileOnAnOlderServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"recording_id is required"}`))
	}))
	defer srv.Close()
	r, f := fileRecorder(t, srv.URL, "call.m4a")
	if _, err := r.transcribeFile(context.Background(), f, store.SttSettings{Provider: "soniox"}); err == nil || err.Error() != locales.RecErrServerOld() {
		t.Errorf("told %v", err)
	}
}
