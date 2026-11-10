package recording

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

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

	r.session, r.stream, r.connecting = "s2", nil, true
	late := dial()
	r.attachSoniox("s1", late)
	if r.stream != nil || !r.connecting {
		t.Error("a stream for the last recording took this one")
	}
}

func TestPauseWhileStarting(t *testing.T) {
	r := &Controller{}
	r.TogglePause()
	if r.State.Paused || r.Elapsed() != 0 {
		t.Errorf("paused %v, elapsed %v", r.State.Paused, r.Elapsed())
	}
}

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

func TestStartedAfterStopIsIgnored(t *testing.T) {
	r := &Controller{update: func(fn func()) { fn() }, session: "s1"}
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

type fakeMic struct {
	sink    Sink
	held    []byte
	letGo   chan struct{}
	stopped chan struct{}
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
	t.Cleanup(r.Stop)
	close(mic.letGo)
	<-mic.stopped
	c.mics[1].sink.Chunk([]byte{3, 3})
	deadline := time.Now().Add(5 * time.Second)
	for {
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

func TestLateAudioWhileStoppingGoesInWhole(t *testing.T) {
	r, st, c := newTestController(t)
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

type fakeEngine struct{}

func (fakeEngine) Stream(string, func(string)) (Stream, error) { return nil, errors.New("no stream") }
func (fakeEngine) File(string, string) ([]store.Segment, error) {
	return []store.Segment{{Text: "hello"}}, nil
}

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
	r.TogglePause()
	r.TogglePause()
	mic.sink.Recovered()
	if !r.State.Paused {
		t.Errorf("the user's pause went: %+v", r.State)
	}
	close(mic.letGo)
	r.Stop()
	<-mic.stopped
}

type lostStream struct{}

func (lostStream) Send([]byte) {}
func (lostStream) Finish() ([]store.Segment, error) {
	return []store.Segment{{EndMs: 1000, Text: "Hello"}}, nil
}
func (lostStream) Close()      {}
func (lostStream) Lost() error { return errors.New("dropped") }

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

type silentLost struct{ lostStream }

func (silentLost) Finish() ([]store.Segment, error) { return nil, nil }
func (silentLost) Lost() error                      { return errNoEnd }

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

type toServer struct{ url string }

func (s toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	to := req.Clone(req.Context())
	to.URL.Scheme, to.URL.Host = "http", strings.TrimPrefix(s.url, "http://")
	to.Host = req.URL.Host
	return http.DefaultTransport.(*http.Transport).RoundTrip(to)
}

func TestCloudEndpointTranscribesFromTheDevice(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	type asked struct{ host, path, auth, model string }
	got := make(chan asked, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got <- asked{req.Host, req.URL.Path, req.Header.Get("Authorization"), req.FormValue("model")}
		_, _ = io.WriteString(w, `{"segments":[{"start":0,"end":1.5,"text":" Hello. "}]}`)
	}))
	defer srv.Close()
	client := http.DefaultClient.Transport
	http.DefaultClient.Transport = toServer{srv.URL}
	defer func() { http.DefaultClient.Transport = client }()

	r, _, _ := newTestController(t)
	stt := store.SttSettings{Provider: Endpoint, BaseURL: "https://api.example.com/v1", Model: "whisper-1"}
	if IsLocalEndpoint(stt.BaseURL) {
		t.Fatal("the address is on this network")
	}
	if err := SetEndpointKey(stt.BaseURL, "ek-cloud"); err != nil {
		t.Fatal(err)
	}
	if err := r.Usable(stt, true); err != nil {
		t.Errorf("not usable, signed out: %v", err)
	}
	if err := r.Usable(stt, false); err == nil || err.Error() != locales.RecErrNeedsConnection() {
		t.Errorf("offline: %v", err)
	}
	path := filepath.Join(t.TempDir(), "r1.wav")
	if err := os.WriteFile(path, WavFromPCM(make([]byte, 3200)), 0o600); err != nil {
		t.Fatal(err)
	}
	segs, err := r.transcribe(store.Recording{ID: "r1"}, path, nil, stt)
	if err != nil || len(segs) != 1 || segs[0].Text != "Hello." || segs[0].EndMs != 1500 {
		t.Fatalf("segments %+v, %v", segs, err)
	}
	if a := <-got; a != (asked{"api.example.com", "/v1/audio/transcriptions", "Bearer ek-cloud", "whisper-1"}) {
		t.Errorf("asked %+v", a)
	}
	stt.BaseURL = "https://elsewhere.example.com/v1"
	if _, err := r.transcribe(store.Recording{ID: "r1"}, path, nil, stt); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a.host != "elsewhere.example.com" || a.auth != "" {
		t.Errorf("another origin was asked as %+v", a)
	}
}

func TestEndpointKeyStaysWithItsOrigin(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	if err := SetEndpointKey(" https://api.example.com/v1/ ", "ek"); err != nil {
		t.Fatal(err)
	}
	for url, want := range map[string]string{
		"https://api.example.com/v1":       "ek",
		"https://API.example.com/other/v2": "ek",
		"https://api.example.com:8443/v1":  "",
		"http://api.example.com/v1":        "",
		"https://example.com/v1":           "",
		"http://localhost:8000/v1":         "",
		"":                                 "",
	} {
		if got := EndpointKey(url); got != want {
			t.Errorf("%q gets %q, want %q", url, got, want)
		}
	}
	if err := SetEndpointKey("http://localhost:8000/v1", "local"); err != nil {
		t.Fatal(err)
	}
	if EndpointKey("https://api.example.com/v1") != "" || EndpointKey("http://localhost:8000") != "local" {
		t.Errorf("a key typed for another address did not take the place of the one kept: %+v", LoadEndpointKey())
	}
	keychain.Use(memKeys{"stt.endpoint.key": []byte("old")})
	if got := EndpointKey("http://192.168.1.20:8000/v1"); got != "old" {
		t.Errorf("on this network: %q", got)
	}
	if got := EndpointKey("https://api.example.com/v1"); got != "" {
		t.Errorf("a key kept without its address went to the cloud: %q", got)
	}
	if err := SetEndpointKey("https://api.example.com/v1", ""); err != nil || LoadEndpointKey() != (KeptKey{}) {
		t.Errorf("removed: %v, kept %+v", err, LoadEndpointKey())
	}
}

func TestUsable(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := NewController(st, remote.SignedInForTest("http://127.0.0.1:1", st, "u1"))
	soniox := store.SttSettings{Provider: "soniox"}
	if err := r.Usable(soniox, true); err == nil || err.Error() != locales.RecErrSetUpSoniox() {
		t.Errorf("Soniox with no key here, signed in: %v", err)
	}
	if SetUp("soniox", soniox, KeysOf(LocalSonioxKey() != "", false, false)) {
		t.Error("Soniox is set up with no key here")
	}
	if err := SetLocalSonioxKey("sk"); err != nil {
		t.Fatal(err)
	}
	local := store.SttSettings{Provider: Endpoint, BaseURL: "http://192.168.1.20:8000/v1"}
	for _, c := range []struct {
		name   string
		stt    store.SttSettings
		online bool
		want   string
	}{
		{"Soniox", soniox, true, ""},
		{"Soniox offline", soniox, false, locales.RecErrNeedsConnection()},
		{"MOSS with no key", store.SttSettings{Provider: Moss}, true, locales.RecErrSetUpMoss()},
		{"an endpoint with no address", store.SttSettings{Provider: Endpoint, BaseURL: " "}, true, locales.RecErrSetUpEndpoint()},
		{"an endpoint on this network, offline", local, false, ""},
		{"the device's engine where there is none", store.SttSettings{Provider: OnDevice}, true, locales.RecErrOnDevice()},
	} {
		var got string
		if err := r.Usable(c.stt, c.online); err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

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
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	r, f = fileRecorder(t, "http://127.0.0.1:1", "call.m4a")
	if err := r.TranscribeFile(f, "b", store.SttSettings{Provider: "soniox"}, true, nothing); err == nil || err.Error() != locales.RecErrSetUpSoniox() {
		t.Errorf("Soniox with no key here: %v", err)
	}
}

func TestTranscribeFileWithSonioxHere(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	if err := SetLocalSonioxKey("sk-here"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/files":
			_, _ = io.WriteString(w, `{"id":"f1"}`)
		case "/transcriptions":
			_, _ = io.WriteString(w, `{"id":"t1"}`)
		case "/transcriptions/t1":
			_, _ = io.WriteString(w, `{"status":"completed"}`)
		case "/transcriptions/t1/transcript":
			_, _ = io.WriteString(w, `{"tokens":[{"text":"Hello.","start_ms":100,"end_ms":900}]}`)
		}
	}))
	defer srv.Close()
	api := sonioxAPI
	sonioxAPI = srv.URL
	defer func() { sonioxAPI = api }()

	r, st, _ := newTestController(t)
	src := filepath.Join(t.TempDir(), "Uncertainty.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []store.Segment, 1)
	stt := store.SttSettings{Provider: "soniox", SonioxModel: "stt-rt-v3"}
	if err := r.TranscribeFile(f, "b", stt, true, func(segs []store.Segment, err error) {
		if err != nil {
			t.Error(err)
		}
		got <- segs
	}); err != nil {
		t.Fatal(err)
	}
	if segs := waitTranscript(t, got); len(segs) != 1 || segs[0].Text != "Hello." {
		t.Errorf("segments %+v", segs)
	}
}

func TestSweepAudioFollowsKeepTheAudio(t *testing.T) {
	r, st, _ := newTestController(t)
	now := time.Now()
	add := func(status string, uploaded, written time.Time) store.Recording {
		t.Helper()
		path := filepath.Join(t.TempDir(), "a.wav")
		if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, written, written); err != nil {
			t.Fatal(err)
		}
		rec, err := st.CreateRecording("p", "b", "soniox", "m", path, "en")
		if err != nil {
			t.Fatal(err)
		}
		rec, err = st.UpdateRecording(rec.ID, func(x *store.Recording) {
			x.Status, x.UploadedAt = status, uploaded
			if !uploaded.IsZero() {
				x.AudioPath = "u/" + x.ID + ".wav"
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	here := func(rec store.Recording) bool {
		t.Helper()
		got, err := st.Recording(rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.LocalPath != ""
	}
	keep := func(k string) {
		t.Helper()
		s := st.Settings()
		s.KeepAudioOnDevice = k
		if err := st.SaveSettings(s); err != nil {
			t.Fatal(err)
		}
	}
	old, hour := now.Add(-40*24*time.Hour), now.Add(-time.Hour)
	uploaded := add("ready", hour, hour.Add(-time.Minute))
	aged := add("ready", old, old.Add(-time.Minute))
	back := add("ready", old, hour)
	waiting := add("transcribing", old, old.Add(-time.Minute))
	notUp := add("ready", time.Time{}, old)

	keep("always")
	r.SweepAudio()
	keep("30_days")
	r.SweepAudio()
	if !here(uploaded) || here(aged) || !here(back) || !here(waiting) || !here(notUp) {
		t.Errorf("30 days: uploaded %v aged %v back %v waiting %v not up %v", here(uploaded), here(aged), here(back), here(waiting), here(notUp))
	}
	keep("until_uploaded")
	r.SweepAudio()
	if here(uploaded) || !here(back) || !here(waiting) || !here(notUp) {
		t.Errorf("until uploaded: uploaded %v back %v waiting %v not up %v", here(uploaded), here(back), here(waiting), here(notUp))
	}
}
