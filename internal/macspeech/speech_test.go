package macspeech

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/recording"
)

const fake = `#!/bin/sh
if [ "$2" = stuck ]; then
	cat >/dev/null
	exec sleep 60
fi
if [ "$2" = dies ]; then
	exit 3
fi
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
	if runtime.GOOS == "windows" {
		t.Skip("the fake helper is a shell script")
	}
	path := filepath.Join(t.TempDir(), Name)
	if err := os.WriteFile(path, []byte(fake), 0o700); err != nil { // #nosec G306
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
	say := exec.Command("say", "-o", wav, "--data-format=LEI16@16000", "Leaflet keeps your notes.") // #nosec G204
	if out, err := say.CombinedOutput(); err != nil {
		t.Skipf("say: %v %s", err, out)
	}
	audio, err := os.ReadFile(wav) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	var streamed string
	for _, chunk := range []struct {
		size int
		pace time.Duration
	}{{3200, 0}, {1601, 50 * time.Millisecond}} {
		s, err := h.Stream("en", func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		pcm := samples(t, audio)
		for len(pcm) > 0 {
			n := min(chunk.size, len(pcm))
			s.Send(pcm[:n])
			pcm = pcm[n:]
			time.Sleep(chunk.pace)
		}
		segs, err := s.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if len(segs) == 0 || !strings.Contains(strings.ToLower(segs[0].Text), "notes") {
			t.Fatalf("streamed in chunks of %d bytes: %+v", chunk.size, segs)
		}
		if text := segs[0].Text; streamed == "" {
			streamed = text
		} else if text != streamed {
			t.Fatalf("streamed in chunks of %d bytes: %q, not %q", chunk.size, text, streamed)
		}
	}
	segs, err := h.File(wav, "en")
	if err != nil || len(segs) == 0 || !strings.Contains(strings.ToLower(segs[0].Text), "notes") {
		t.Fatalf("file %+v, %v", segs, err)
	}
}

func samples(t *testing.T, wav []byte) []byte {
	t.Helper()
	for off := 12; off+8 <= len(wav); {
		size := int(binary.LittleEndian.Uint32(wav[off+4:]))
		if string(wav[off:off+4]) == "data" {
			return wav[off+8 : min(off+8+size, len(wav))]
		}
		off += 8 + size + size%2
	}
	t.Fatal("no audio in the file")
	return nil
}

func TestFinishEndsAStuckHelper(t *testing.T) {
	was := finishSlack
	finishSlack = 100 * time.Millisecond
	t.Cleanup(func() { finishSlack = was })
	s, err := helper(t).Stream("stuck", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	s.Send(make([]byte, 3200))
	done := make(chan error, 1)
	go func() {
		_, err := s.Finish()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a helper that never answered finished")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Finish waits on a stuck helper")
	}
}

func TestSendToAnEndedHelperKeepsNothing(t *testing.T) {
	rs, err := helper(t).Stream("dies", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	s := rs.(*stream)
	<-s.done
	for range 100 {
		s.Send(make([]byte, 3200))
	}
	s.mu.Lock()
	queued := len(s.queue)
	s.mu.Unlock()
	if queued != 0 || s.Lost() == nil {
		t.Errorf("%d chunks queued, lost %v", queued, s.Lost())
	}
}

func TestFileWaitFollowsTheAudio(t *testing.T) {
	dir := t.TempDir()
	wav := func(name string, data int64) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, recording.WavFromPCM(nil), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, 44+data); err != nil {
			t.Fatal(err)
		}
		return path
	}
	threeHours := int64(3 * 3600 * 2 * recording.SampleRate)
	if got := fileWait(wav("long.wav", threeHours)); got < 3*time.Hour {
		t.Errorf("three hours of WAV given %v", got)
	}
	short := fileWait(wav("short.WAV", 2*recording.SampleRate))
	if short < time.Minute || short > 30*time.Minute {
		t.Errorf("a second of WAV given %v", short)
	}
	broken := filepath.Join(dir, "broken.wav")
	if err := os.WriteFile(broken, []byte("not a wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, other := fileWait(broken), fileWait(filepath.Join(dir, "a.m4a")); got != other || other < 3*time.Hour {
		t.Errorf("an unreadable WAV given %v, another file %v", got, other)
	}
}
