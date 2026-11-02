package mobile

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
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
	if _, err := c.StartRecording(row.ID); err == nil {
		t.Fatal("recorded into a page that isn't open")
	}
	if _, err := c.OpenPage(row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartRecording(row.ID); err == nil {
		t.Fatal("recorded without a microphone")
	}
	mic := &fakeMic{closed: make(chan struct{}, 1)}
	c.UseMicrophone(mic)

	raw, err = c.StartRecording(row.ID)
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
	path, err := c.RecordingAudio(id)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() <= 44 {
		t.Fatalf("audio at %q: %v", path, err)
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
		raw, err := c.StartRecording(row.ID)
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
	return fmt.Sprintf(`[{"start_ms":0,"end_ms":1000,"text":"heard %d"}]`, f.heard[id]), nil
}

func (f *fakeTranscriber) Close(id string) {}

func (f *fakeTranscriber) File(path, language string) (string, error) {
	return `[{"start_ms":0,"end_ms":1000,"text":"from the file"}]`, nil
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
	raw, err = c.StartRecording(row.ID)
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
	if err := c.Retranscribe(id); err != nil {
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
