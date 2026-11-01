package macspeech

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fake is a helper that answers as leaflet-speech does: a stream's words
// say how many bytes it was sent, a file's are its path, and language "xx"
// is one it cannot transcribe.
const fake = `#!/bin/sh
if [ "$2" = xx ]; then
	echo '{"error":"unsupported language: xx","code":"language","language":"xx"}'
	exit 1
fi
case "$1" in
stream)
	n=$(wc -c | tr -d ' ')
	echo '{"text":"so far"}'
	echo '{"done":true,"segments":[{"start_ms":0,"end_ms":1000,"text":"heard '$n'"}]}'
	;;
file)
	echo '{"done":true,"segments":[{"start_ms":0,"end_ms":1000,"text":"'$3'"}]}'
	;;
esac
`

func helper(t *testing.T) *Helper {
	t.Helper()
	path := filepath.Join(t.TempDir(), Name)
	if err := os.WriteFile(path, []byte(fake), 0o700); err != nil { // #nosec G306 -- a test's own script.
		t.Fatal(err)
	}
	return &Helper{path: path}
}

func TestStreamSendsAllTheAudio(t *testing.T) {
	h := helper(t)
	var mu sync.Mutex
	var heard []string
	s, err := h.Stream("en", func(text string) {
		mu.Lock()
		heard = append(heard, text)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		s.Send(make([]byte, 3200))
	}
	segs, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Text != "heard 320000" {
		t.Fatalf("segments %+v", segs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 1 || heard[0] != "so far" {
		t.Fatalf("words so far %q", heard)
	}
	if err := s.Lost(); err != nil {
		t.Fatalf("lost after Finish: %v", err)
	}
}

func TestFileAndAnUnknownLanguage(t *testing.T) {
	h := helper(t)
	segs, err := h.File("/tmp/a.wav", "en")
	if err != nil || len(segs) != 1 || segs[0].Text != "/tmp/a.wav" {
		t.Fatalf("file %+v, %v", segs, err)
	}
	if _, err := h.File("/tmp/a.wav", "xx"); err == nil || !strings.Contains(err.Error(), "xx") {
		t.Fatalf("unknown language: %v", err)
	}
	s, err := h.Stream("xx", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(); err == nil {
		t.Fatal("a stream in a language the Mac can't transcribe finished")
	}
}

// The helper scripts/build-speech.sh built transcribes speech macOS says,
// as it streams and from its file. It is skipped where there is none, or
// the Mac cannot transcribe.
func TestTheMacTranscribes(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "resources", "darwin-"+runtime.GOARCH))
	if err != nil {
		t.Fatal(err)
	}
	h := Find(dir)
	if h == nil {
		t.Skip("no leaflet-speech here that can transcribe; scripts/build-speech.sh builds it")
	}
	wav := filepath.Join(t.TempDir(), "say.wav")
	say := exec.Command("say", "-o", wav, "--data-format=LEI16@16000", "Leaflet keeps your notes.") // #nosec G204 -- fixed arguments.
	if out, err := say.CombinedOutput(); err != nil {
		t.Skipf("say: %v %s", err, out)
	}
	audio, err := os.ReadFile(wav) // #nosec G304 -- the test's own file.
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.Stream("en", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	// What follows the WAV header, in the microphone's chunks of 100 ms.
	pcm := audio[44:]
	for len(pcm) > 0 {
		n := min(3200, len(pcm))
		s.Send(pcm[:n])
		pcm = pcm[n:]
	}
	segs, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) == 0 || !strings.Contains(strings.ToLower(segs[0].Text), "notes") {
		t.Fatalf("streamed %+v", segs)
	}
	segs, err = h.File(wav, "en")
	if err != nil || len(segs) == 0 || !strings.Contains(strings.ToLower(segs[0].Text), "notes") {
		t.Fatalf("file %+v, %v", segs, err)
	}
}
