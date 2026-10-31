package recording

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"leaflet/internal/store"
)

func TestRecoverInterrupted(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	// Cut off while recording: a header still saying 0 bytes, and a second
	// of audio after it.
	wav := filepath.Join(dir, "cut.wav")
	if err := os.WriteFile(wav, append(wavHeader(0), make([]byte, 2*SampleRate)...), 0o600); err != nil {
		t.Fatal(err)
	}
	cut, _ := st.CreateRecording("p", "b1", "soniox", "", wav, "auto")
	if _, err := st.UpdateRecording(cut.ID, func(r *store.Recording) {
		r.Status, r.Transcript = "recording", []store.Segment{{Text: "Hello"}}
	}); err != nil {
		t.Fatal(err)
	}
	// Quit while the transcript was finished: the live text is kept.
	live, _ := st.CreateRecording("p", "b2", "soniox", "", "", "auto")
	if _, err := st.UpdateRecording(live.ID, func(r *store.Recording) {
		r.Status, r.Transcript = "transcribing", []store.Segment{{Text: "Hello, hello."}}
	}); err != nil {
		t.Fatal(err)
	}
	// Quit before any words came: nothing to keep.
	empty, _ := st.CreateRecording("p", "b3", "openai-compatible", "", "", "auto")
	if _, err := st.UpdateRecording(empty.ID, func(r *store.Recording) { r.Status = "transcribing" }); err != nil {
		t.Fatal(err)
	}

	NewController(st, nil).RecoverInterrupted()

	if r, _ := st.Recording(cut.ID); r.Status != "ready" || r.DurationMs != 1000 {
		t.Errorf("cut off: %s, %d ms", r.Status, r.DurationMs)
	}
	if pcm, err := ReadWav(wav); err != nil || len(pcm) != 2*SampleRate {
		t.Errorf("repaired WAV: %d bytes, %v", len(pcm), err)
	}
	if r, _ := st.Recording(live.ID); r.Status != "ready" || len(r.Transcript) != 1 {
		t.Errorf("live text: %s, %v", r.Status, r.Transcript)
	}
	if r, _ := st.Recording(empty.ID); r.Status != "failed" || r.Error == "" {
		t.Errorf("no text: %s, %q", r.Status, r.Error)
	}
}

// uploading stands in for sync: audio goes up.
type uploading struct{}

func (uploading) Uploads() bool  { return true }
func (uploading) CanFetch() bool { return true }
func (uploading) Fetch(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("not here")
}

// A recording settled after a quit has its audio go up, as one finished
// before it would: nothing else queues it.
func TestRecoverInterruptedUploadsAudio(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	wav := filepath.Join(dir, "cut.wav")
	if err := os.WriteFile(wav, append(wavHeader(0), make([]byte, 2*SampleRate)...), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, _ := st.CreateRecording("p", "b1", "", "", wav, "auto")
	if _, err := st.UpdateRecording(rec.ID, func(r *store.Recording) { r.Status = "recording" }); err != nil {
		t.Fatal(err)
	}

	c := NewController(st, nil)
	c.Blobs = uploading{}
	c.RecoverInterrupted()

	entries, _ := st.Pending()
	queued := false
	for _, e := range entries {
		queued = queued || e.Entity == "storage" && e.EntityID == "recordings/"+rec.ID
	}
	if !queued {
		t.Errorf("audio not queued: %+v", entries)
	}
}

// fetching stands in for sync, with the audio of other devices.
type fetching struct{ asked *string }

func (fetching) Uploads() bool  { return true }
func (fetching) CanFetch() bool { return true }
func (f fetching) Fetch(_ context.Context, _, path string) ([]byte, error) {
	*f.asked = path
	return nil, errors.New("fetched")
}

// Transcribing again on this device a recording whose audio is not here,
// made on another device, fetches the audio first.
func TestRetranscribeLocallyFetchesStoredAudio(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var asked string
	c := NewController(st, nil)
	c.Blobs = fetching{&asked}
	_, err = c.transcribe(store.Recording{ID: "r1", AudioPath: "u1/r1.wav"}, "", nil,
		store.SttSettings{Provider: "openai-compatible", BaseURL: "http://localhost:1234/v1"})
	if asked != "u1/r1.wav" || err == nil || err.Error() != "fetched" {
		t.Errorf("fetched %q, %v", asked, err)
	}
}

// Transcribing again a recording being transcribed is refused, as is a
// second ask before the first has finished: the one ending last would
// write over the other's transcript.
func TestRetranscribeOneAtATime(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	rec, _ := st.CreateRecording("p", "b1", "", "", "", "auto")
	if _, err := st.UpdateRecording(rec.ID, func(r *store.Recording) { r.Status = "ready" }); err != nil {
		t.Fatal(err)
	}
	updates := make(chan func(), 4)
	c := NewController(st, nil)
	c.update = func(fn func()) { updates <- fn }
	stt := store.SttSettings{Provider: "openai-compatible", BaseURL: "http://localhost:1/v1"}

	c.Transcribing[rec.ID] = true
	if err := c.Retranscribe(rec.ID, stt, true); err == nil {
		t.Error("transcribed again while transcribing")
	}
	if r, _ := st.Recording(rec.ID); r.Status != "ready" {
		t.Errorf("status %s", r.Status)
	}

	delete(c.Transcribing, rec.ID)
	if err := c.Retranscribe(rec.ID, stt, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Retranscribe(rec.ID, stt, true); err == nil {
		t.Error("a second transcription started before the first finished")
	}
	// The first runs its course: started, then done.
	for range 2 {
		(<-updates)()
	}
	if c.Transcribing[rec.ID] {
		t.Error("still transcribing once done")
	}
}

// A transcription a quit stopped may have finished without this run, on
// the server or the device that started it: settling it goes up only over
// a recording still transcribing there. One cut off while recording was
// nowhere else, and goes up as it is.
func TestRecoverInterruptedGivesWayToAFinishedTranscription(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	server, _ := st.CreateRecording("p", "b1", "openai-compatible", "", "", "auto")
	if _, err := st.UpdateRecording(server.ID, func(r *store.Recording) { r.Status = "transcribing" }); err != nil {
		t.Fatal(err)
	}
	cut, _ := st.CreateRecording("p", "b2", "openai-compatible", "", "", "auto")
	if _, err := st.UpdateRecording(cut.ID, func(r *store.Recording) { r.Status = "recording" }); err != nil {
		t.Fatal(err)
	}

	NewController(st, nil).RecoverInterrupted()

	if r, _ := st.Recording(server.ID); r.Status != "failed" {
		t.Errorf("still %s here", r.Status)
	}
	entries, _ := st.Pending()
	over := map[string]any{}
	for _, e := range entries {
		var payload map[string]any
		if e.Entity == "recordings" && json.Unmarshal([]byte(e.Payload), &payload) == nil {
			over[e.EntityID] = payload[store.IfStatusKey]
		}
	}
	if over[server.ID] != "transcribing" || over[cut.ID] != nil {
		t.Errorf("queued over %v", over)
	}
}
