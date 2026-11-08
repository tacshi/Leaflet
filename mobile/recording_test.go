package mobile

import (
	"encoding/json"
	"errors"
	"fmt"
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

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// fakeMic is the app's microphone, one for all recordings: on at once,
// it hands on a second of silence to the recording stopping, once hold
// lets it, and keeps the calls it was given.
type fakeMic struct {
	mu     sync.Mutex
	sinks  map[int]*AudioSink
	calls  []string
	hold   chan struct{}
	closed chan struct{}
}

func (m *fakeMic) call(what string, session int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, fmt.Sprintf("%s %d", what, session))
}

func (m *fakeMic) Start(session, rate int, sink *AudioSink) error {
	m.call("start", session)
	m.mu.Lock()
	if m.sinks == nil {
		m.sinks = map[int]*AudioSink{}
	}
	m.sinks[session] = sink
	m.mu.Unlock()
	go sink.Started()
	return nil
}

func (m *fakeMic) Pause(session int)  { m.call("pause", session) }
func (m *fakeMic) Resume(session int) { m.call("resume", session) }

func (m *fakeMic) Stop(session int) {
	m.call("stop", session)
	if m.hold != nil {
		<-m.hold
	}
	m.mu.Lock()
	sink := m.sinks[session]
	m.mu.Unlock()
	sink.Chunk(make([]byte, 32000))
}

func (m *fakeMic) Close(session int) {
	m.call("close", session)
	m.closed <- struct{}{}
}

func TestRecordingKeepsAudioWithoutAnEngine(t *testing.T) {
	c, _ := open(t)
	raw, err := c.CreatePage(`{"title":"Standup"}`)
	if err != nil {
		t.Fatal(err)
	}
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	if _, err := c.StartRecording(row.ID, ""); err == nil {
		t.Fatal("recorded into a page that isn't open")
	}
	if _, err := c.OpenPage(row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartRecording(row.ID, ""); err == nil {
		t.Fatal("recorded without a microphone")
	}
	mic := &fakeMic{closed: make(chan struct{}, 1)}
	c.UseMicrophone(mic)

	raw, err = c.StartRecording(row.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var block doc.Node
	_ = json.Unmarshal([]byte(raw), &block)
	id := block.Str("recording_id")
	if block.Type != "recording" || id == "" {
		t.Fatalf("block %s", raw)
	}
	var st recordingState
	for deadline := time.Now().Add(5 * time.Second); !st.Active; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never started: %+v", st)
		}
		raw, _ = c.RecordingState()
		_ = json.Unmarshal([]byte(raw), &st)
	}
	if st.ID != id || st.PageID != row.ID {
		t.Fatalf("state %+v", st)
	}

	c.StopRecording()
	select {
	case <-mic.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the microphone was never let go")
	}
	var rec recordingRow
	for deadline := time.Now().Add(5 * time.Second); rec.Status != "ready"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never ready: %+v", rec)
		}
		raw, err = c.Recording(id)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(raw), &rec)
	}
	// Offline and with no endpoint, it keeps only its audio.
	if rec.SttProvider != "" {
		t.Fatalf("recording %+v", rec)
	}
	path, err := c.RecordingAudio(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() <= 44 {
		t.Fatalf("audio at %q: %v", path, err)
	}
}

// RecordNewPage records into a page of its own, in the folder asked for,
// with no editor to put its block in: the block goes at the page's end.
// One that can't start leaves no page behind.
func TestRecordNewPage(t *testing.T) {
	c, _ := open(t)
	raw, err := c.CreateFolder("Voice notes")
	if err != nil {
		t.Fatal(err)
	}
	var folder folderRow
	_ = json.Unmarshal([]byte(raw), &folder)
	pages := func() int {
		n := 0
		all, _ := c.core.Store.Pages()
		for _, p := range all {
			if p.DeletedAt.IsZero() {
				n++
			}
		}
		return n
	}
	had := pages()

	if _, err := c.RecordNewPage(folder.ID, ""); err == nil {
		t.Fatal("recorded without a microphone")
	}
	if n := pages(); n != had {
		t.Fatalf("a recording that never began left a page: %d, had %d", n, had)
	}

	mic := &fakeMic{closed: make(chan struct{}, 1)}
	c.UseMicrophone(mic)
	before := time.Now()
	raw, err = c.RecordNewPage(folder.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	// Named after when it began, which a minute may have turned over.
	named := row.Title == locales.RecBlockTitle(before) || row.Title == locales.RecBlockTitle(time.Now())
	if row.FolderID != folder.ID || !named {
		t.Fatalf("page %+v", row)
	}
	var block *doc.Node
	c.q.do(func() {
		content := c.core.Store.Content(row.ID)
		if n := len(content.Children); n > 0 {
			block = content.Children[n-1]
		}
	})
	if block == nil || block.Type != "recording" || block.Str("recording_id") == "" {
		t.Fatalf("last block %+v", block)
	}
	var st recordingState
	for deadline := time.Now().Add(5 * time.Second); !st.Active; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never started: %+v", st)
		}
		raw, _ = c.RecordingState()
		_ = json.Unmarshal([]byte(raw), &st)
	}
	if st.PageID != row.ID || st.BlockID != block.ID {
		t.Fatalf("state %+v, block %s", st, block.ID)
	}
	if _, err := c.RecordNewPage("", ""); err == nil {
		t.Fatal("a second recording started over the first")
	}
	if n := pages(); n != had+1 {
		t.Fatalf("%d pages, want the recording's alone beside the %d there were", n, had)
	}

	c.StopRecording()
	select {
	case <-mic.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the microphone was never let go")
	}
}

// A recording started before the last one's microphone has stopped gets
// a session of its own: the last one's Stop and Close, late, are for it
// alone, and its last audio goes to its own file.
func TestRecordingsShareTheMicrophoneApart(t *testing.T) {
	c, _ := open(t)
	raw, _ := c.CreatePage(`{"title":"Standup"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	if _, err := c.OpenPage(row.ID); err != nil {
		t.Fatal(err)
	}
	mic := &fakeMic{hold: make(chan struct{}), closed: make(chan struct{}, 2)}
	c.UseMicrophone(mic)
	record := func() string {
		t.Helper()
		raw, err := c.StartRecording(row.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		var block doc.Node
		_ = json.Unmarshal([]byte(raw), &block)
		var st recordingState
		for deadline := time.Now().Add(5 * time.Second); !st.Active; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("never started: %+v", st)
			}
			raw, _ = c.RecordingState()
			_ = json.Unmarshal([]byte(raw), &st)
		}
		return block.Str("recording_id")
	}
	closed := func() {
		t.Helper()
		select {
		case <-mic.closed:
		case <-time.After(5 * time.Second):
			t.Fatal("the microphone was never let go")
		}
	}
	first := record()
	c.StopRecording()
	second := record()
	close(mic.hold)
	closed()
	c.StopRecording()
	closed()

	mic.mu.Lock()
	calls := slices.Clone(mic.calls)
	mic.mu.Unlock()
	var starts []int
	for _, call := range calls {
		var n int
		if _, err := fmt.Sscanf(call, "start %d", &n); err == nil {
			starts = append(starts, n)
		}
	}
	if len(starts) != 2 || starts[0] == starts[1] {
		t.Fatalf("calls %v", calls)
	}
	// Each recording's own, and nothing else.
	for _, n := range starts {
		for _, what := range []string{"stop", "close"} {
			if want := fmt.Sprintf("%s %d", what, n); !slices.Contains(calls, want) {
				t.Errorf("calls %v, want %s", calls, want)
			}
		}
	}
	if len(calls) != 6 {
		t.Errorf("calls %v", calls)
	}
	for _, id := range []string{first, second} {
		var rec recordingRow
		for deadline := time.Now().Add(5 * time.Second); rec.Status != "ready"; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("never ready: %+v", rec)
			}
			raw, _ := c.Recording(id)
			_ = json.Unmarshal([]byte(raw), &rec)
		}
		// Each has the second its own Stop handed on.
		if rec.DurationMs != 1000 {
			t.Errorf("recording %s lasts %d ms", id, rec.DurationMs)
		}
	}
}

// fakeTranscriber is the device's engine: a stream's words say how many
// bytes it was sent, and every file says "from the file".
type fakeTranscriber struct {
	mu    sync.Mutex
	heard map[string]int
	sinks map[string]*TranscriptSink
}

func (f *fakeTranscriber) Start(id, language string, sink *TranscriptSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heard[id], f.sinks[id] = 0, sink
	return nil
}

func (f *fakeTranscriber) Send(id string, pcm []byte) {
	f.mu.Lock()
	f.heard[id] += len(pcm)
	sink := f.sinks[id]
	f.mu.Unlock()
	sink.Text("hello")
}

func (f *fakeTranscriber) Finish(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprintf(`{"done":true,"segments":[{"start_ms":0,"end_ms":1000,"text":"heard %d"}]}`, f.heard[id]), nil
}

func (f *fakeTranscriber) Close(id string) {}

func (f *fakeTranscriber) File(path, language string) (string, error) {
	return `{"done":true,"segments":[{"start_ms":0,"end_ms":1000,"text":"from the file"}]}`, nil
}

func TestRecordingTranscribedOnTheDevice(t *testing.T) {
	c, _ := open(t)
	c.UseMicrophone(&fakeMic{closed: make(chan struct{}, 1)})
	c.UseTranscriber(&fakeTranscriber{heard: map[string]int{}, sinks: map[string]*TranscriptSink{}})
	raw, err := c.SpeechSettings()
	if err != nil {
		t.Fatal(err)
	}
	var speech speechSettings
	_ = json.Unmarshal([]byte(raw), &speech)
	// An endpoint with no address turns to the device's engine.
	if speech.Stt.Provider != recording.OnDevice || !speech.OnDevice {
		t.Fatalf("speech settings %+v", speech)
	}

	raw, _ = c.CreatePage(`{"title":"Standup"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	if _, err := c.OpenPage(row.ID); err != nil {
		t.Fatal(err)
	}
	raw, err = c.StartRecording(row.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var block doc.Node
	_ = json.Unmarshal([]byte(raw), &block)
	id := block.Str("recording_id")
	var st recordingState
	for deadline := time.Now().Add(5 * time.Second); !st.Active; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never started: %+v", st)
		}
		raw, _ = c.RecordingState()
		_ = json.Unmarshal([]byte(raw), &st)
	}
	c.StopRecording()

	ready := func(want string) recordingRow {
		t.Helper()
		var rec recordingRow
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			raw, err := c.Recording(id)
			if err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal([]byte(raw), &rec)
			if rec.Status == "ready" && rec.Plain == want {
				return rec
			}
			if time.Now().After(deadline) {
				t.Fatalf("recording %+v, want %q", rec, want)
			}
		}
	}
	// The stream heard the second the microphone held as it stopped.
	if rec := ready("heard 32000"); rec.SttProvider != recording.OnDevice {
		t.Fatalf("recording %+v", rec)
	}
	if err := c.Retranscribe(id, ""); err != nil {
		t.Fatal(err)
	}
	ready("from the file")
}

// The speech settings the app saves are the whole of them: a provider or
// language hints it leaves out are cleared, not kept from before.
func TestSaveSpeechSettingsClearsWhatItLeavesOut(t *testing.T) {
	c, _ := open(t)
	if err := c.SaveSpeechSettings(`{"provider":"openai-compatible","base_url":"https://api.example.com/v1","model":"whisper-1","provider_id":"p1","language_hints":["en","ja"]}`); err != nil {
		t.Fatal(err)
	}
	// "No key", and no hints.
	if err := c.SaveSpeechSettings(`{"provider":"openai-compatible","base_url":"https://api.example.com/v1","model":"whisper-1"}`); err != nil {
		t.Fatal(err)
	}
	var stt store.SttSettings
	c.q.do(func() { stt = c.core.Store.Settings().Stt })
	if stt.ProviderID != "" || len(stt.LanguageHints) != 0 || stt.Model != "whisper-1" {
		t.Fatalf("saved %+v", stt)
	}
}

// TestEndpoint sends the endpoint a second of silence.
func TestTestEndpointSendsASecond(t *testing.T) {
	c, _ := open(t)
	sent := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(f)
		sent <- len(data)
		_, _ = io.WriteString(w, `{"text":""}`)
	}))
	defer srv.Close()
	if _, err := c.TestEndpoint(srv.URL+"/v1", "whisper-1", ""); err != nil {
		t.Fatal(err)
	}
	if n := <-sent; n != 44+2*recording.SampleRate {
		t.Errorf("sent %d bytes", n)
	}
}

// recordingEvents keeps what the core tells the app of the recording.
type recordingEvents struct {
	fakeEvents
	states, levels chan string
}

func (e *recordingEvents) RecordingChanged(s string) { e.states <- s }
func (e *recordingEvents) RecordingLevels(l string)  { e.levels <- l }

// The recording's state goes to the app as it changes, its levels alone:
// told again with nothing new, it goes nowhere.
func TestRecordingToldAsItChanges(t *testing.T) {
	c, ev := open(t)
	re := &recordingEvents{fakeEvents: *ev, states: make(chan string, 8), levels: make(chan string, 8)}
	c.ev.q.do(func() { c.ev.to = re })
	c.q.do(func() {
		st := &c.core.Rec.State
		st.Active, st.ID, st.Levels = true, "r1", []float64{0.1}
		c.tellRecording()
		c.tellRecording()
		st.Levels = []float64{0.1, 0.2}
		c.tellRecording()
		st.LiveText = "Hello"
		c.tellRecording()
	})
	c.ev.q.do(func() {})
	if n := len(re.states); n != 2 {
		t.Errorf("%d states told, want the first and the words'", n)
	}
	if n := len(re.levels); n != 2 {
		t.Errorf("%d levels told, want 2", n)
	}
	<-re.states
	var last recordingState
	if err := json.Unmarshal([]byte(<-re.states), &last); err != nil || last.LiveText != "Hello" || len(last.Levels) != 0 {
		t.Errorf("then %+v, %v", last, err)
	}
	if l := <-re.levels; l != "[0.1]" {
		t.Errorf("levels %s", l)
	}
}

// stuckTranscriber is the device's engine stuck, as a speech model that
// stops answering: Finish and File return only once released.
type stuckTranscriber struct {
	release chan struct{}
	closed  chan string
}

func (s *stuckTranscriber) Start(id, language string, sink *TranscriptSink) error { return nil }
func (s *stuckTranscriber) Send(id string, pcm []byte)                            {}
func (s *stuckTranscriber) Close(id string)                                       { s.closed <- id }

func (s *stuckTranscriber) Finish(id string) (string, error) {
	<-s.release
	return `{"done":true,"segments":[]}`, nil
}

func (s *stuckTranscriber) File(path, language string) (string, error) {
	<-s.release
	return `{"done":true,"segments":[]}`, nil
}

// The device's engine stuck is given up on once the audio's length and
// onDeviceSlack have gone by, as the Mac's speech helper is: the
// transcription fails saying so, rather than run until the app quits,
// and a stream is ended.
func TestStuckEngineIsGivenUpOn(t *testing.T) {
	slack := onDeviceSlack
	onDeviceSlack = 50 * time.Millisecond
	t.Cleanup(func() { onDeviceSlack = slack })
	tr := &stuckTranscriber{release: make(chan struct{}), closed: make(chan string, 1)}
	t.Cleanup(func() { close(tr.release) })
	e := engine{tr}

	s, err := e.Stream("en", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	s.Send(make([]byte, 3200))
	if _, err := s.Finish(); !errors.Is(err, errStuck) {
		t.Fatalf("Finish: %v", err)
	}
	select {
	case <-tr.closed:
	default:
		t.Error("the stream given up on was not ended")
	}

	path := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(path, recording.WavFromPCM(make([]byte, 3200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.File(path, "en"); !errors.Is(err, errStuck) || err.Error() != locales.RecErrOnDeviceStuck() {
		t.Fatalf("File: %v", err)
	}
}

// An audio file's transcript comes into its open page's block as a step of
// its own, which the app is told is that block's, and the state says it
// runs until then.
func TestFileTranscribedIntoItsNotes(t *testing.T) {
	c, ev := open(t)
	c.UseMicrophone(&fakeMic{closed: make(chan struct{}, 1)})
	c.UseTranscriber(&fakeTranscriber{heard: map[string]int{}, sinks: map[string]*TranscriptSink{}})
	raw, _ := c.CreatePage(`{"title":"Meeting"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	src := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := c.core.Store.AddFile(src, "", row.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "f1", Type: "file", Data: map[string]any{"file_id": f.ID, "name": f.Name}}
	if _, _, err := c.core.Store.SaveContent(row.ID, doc.FromBlocks([]*doc.Node{n})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenPage(row.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.TranscribeFile(row.ID, "f1", ""); err != nil {
		t.Fatal(err)
	}
	for deadline := time.After(5 * time.Second); ; {
		select {
		case u := <-ev.updates:
			if u.reason != "notes:f1" {
				continue
			}
			if !strings.Contains(u.content, "0:00 from the file") {
				t.Errorf("content %s", u.content)
			}
			return
		case <-deadline:
			t.Fatal("the transcript never came")
		}
	}
}

// A transcript saves as a file named for its recording.
func TestRecordingTranscriptFile(t *testing.T) {
	c, _ := open(t)
	if err := c.core.Store.PutRecording(store.Recording{ID: "r1", PageID: "p", Status: "ready",
		Transcript: []store.Segment{{StartMs: 1000, EndMs: 2000, Text: "Hello."}}}); err != nil {
		t.Fatal(err)
	}
	raw, err := c.RecordingTranscriptFile("r1", "Stand/up", recording.TranscriptSrt)
	if err != nil {
		t.Fatal(err)
	}
	var file transcriptFile
	_ = json.Unmarshal([]byte(raw), &file)
	if file.Name != "Stand-up.srt" || file.Text != "1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n" {
		t.Errorf("file %+v", file)
	}
	if got := RecordingAudioName("Stand/up"); got != "Stand-up.wav" {
		t.Errorf("audio name %q", got)
	}
}
