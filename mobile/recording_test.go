package mobile

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
)

// fakeMic is the app's microphone: on at once, it hands on a second of
// silence as it stops.
type fakeMic struct {
	sink   *AudioSink
	closed chan struct{}
}

func (m *fakeMic) Start(rate int, sink *AudioSink) error {
	m.sink = sink
	go sink.Started()
	return nil
}

func (m *fakeMic) Pause()  {}
func (m *fakeMic) Resume() {}
func (m *fakeMic) Stop()   { m.sink.Chunk(make([]byte, 32000)) }
func (m *fakeMic) Close()  { close(m.closed) }

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
	mic := &fakeMic{closed: make(chan struct{})}
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
	c.UseMicrophone(&fakeMic{closed: make(chan struct{})})
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
