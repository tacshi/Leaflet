package recording

import (
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
