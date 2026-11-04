package recording

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"leaflet/internal/store"
)

// tone is a chunk of a sine at an RMS of the given dBFS.
func tone(db float64) []byte {
	amp := math.Pow(10, db/20) * math.Sqrt2
	pcm := make([]byte, 2*1600)
	for i := 0; i < 1600; i++ {
		v := int16(amp * 32767 * math.Sin(float64(i)*2*math.Pi*440/16000))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	return pcm
}

func TestLevel(t *testing.T) {
	if l := Level(make([]byte, 3200)); l != 0 {
		t.Errorf("silence %v", l)
	}
	// Quiet speech into a laptop microphone, around -35 dBFS, shows
	// clearly; loud speech fills the meter.
	if l := Level(tone(-35)); l < 0.4 || l > 0.5 {
		t.Errorf("-35 dBFS %v", l)
	}
	if l := Level(tone(-6)); l != 1 {
		t.Errorf("-6 dBFS %v", l)
	}
}

func TestLatePunctuationEndsTheLine(t *testing.T) {
	segs := TokensToSegments([]Token{
		{Text: "I", StartMs: 23000, EndMs: 23200},
		{Text: " think", StartMs: 23200, EndMs: 23600},
		{Text: ".", StartMs: 26000, EndMs: 26100},
	})
	if len(segs) != 1 || segs[0].Text != "I think." {
		t.Errorf("segments %+v", segs)
	}
}

func TestTidyJoinsPunctuationLines(t *testing.T) {
	got := Tidy([]store.Segment{{StartMs: 23000, EndMs: 23600, Text: "I think"}, {StartMs: 26000, EndMs: 26100, Text: "."}})
	if len(got) != 1 || got[0].Text != "I think." || got[0].EndMs != 26100 {
		t.Errorf("segments %+v", got)
	}
}

func TestTidyJoinsSplitSentences(t *testing.T) {
	got := Tidy([]store.Segment{
		{StartMs: 0, EndMs: 9000, Text: "What builds tolerance is practice,"},
		{StartMs: 13000, EndMs: 15000, Text: "the approach is to seek"},
		{StartMs: 16000, EndMs: 20000, Text: "out small doses of it."},
		{StartMs: 21000, EndMs: 22000, Text: "Then it gets easier."},
		{StartMs: 23000, EndMs: 24000, Text: "and easier", Speaker: "2"},
	})
	want := []string{"What builds tolerance is practice, the approach is to seek out small doses of it.", "Then it gets easier.", "and easier"}
	if len(got) != len(want) {
		t.Fatalf("segments %+v", got)
	}
	for i, w := range want {
		if got[i].Text != w {
			t.Errorf("line %d: %q", i, got[i].Text)
		}
	}
	if got[0].StartMs != 0 || got[0].EndMs != 20000 {
		t.Errorf("times %+v", got[0])
	}
}

func TestMeterWindowsAcrossChunks(t *testing.T) {
	// Chunks of any size give one level per 50 ms window.
	var m Meter
	pcm := tone(-20) // 1600 samples: two windows
	got := 0
	for _, part := range [][]byte{pcm[:500], pcm[500:2100], pcm[2100:]} {
		got += len(m.Levels(part))
	}
	if got != 2 {
		t.Errorf("%d levels", got)
	}
}

// A WAV file is the user's alone, and never one that was there already.
// Windows keeps no Unix modes: Go reports any file it can write as 0666,
// and who else may read it is up to the access list of its folder.
func TestCreateWavIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.wav")
	w, err := CreateWav(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if _, err := CreateWav(path); err == nil {
		t.Error("an existing file was written over")
	}
}

// Only addresses on this machine or its network are local, not names
// that start like them.
func TestIsLocalEndpoint(t *testing.T) {
	for u, want := range map[string]bool{
		"http://localhost:8000/v1": true, "http://127.0.0.1:8000": true, "http://[::1]:8000": true,
		"http://10.0.0.5": true, "http://172.16.3.4": true, "http://172.31.255.1": true, "http://192.168.1.2": true,
		"http://169.254.1.1": true, "http://[fd00::1]": true, "http://[::ffff:192.168.1.2]": true, "http://studio.local:9000": true,
		"http://172.32.0.1": false, "https://10.example.com": false, "https://192.168.example.com": false,
		"https://api.openai.com/v1": false, "http://8.8.8.8": false, "": false,
	} {
		if got := IsLocalEndpoint(u); got != want {
			t.Errorf("IsLocalEndpoint(%q) = %v", u, got)
		}
	}
}

// A long recording gets longer to transcribe on an endpoint than a short
// one: an hour's audio is not given up on at five minutes.
func TestTranscribeWaitGrowsWithTheAudio(t *testing.T) {
	if got := transcribeWait(32000 * 60); got < 5*time.Minute || got > 10*time.Minute {
		t.Errorf("a minute's audio waits %v", got)
	}
	if got := transcribeWait(32000 * 3600); got < 90*time.Minute {
		t.Errorf("an hour's audio waits %v", got)
	}
}

// ReadWav reads the PCM16 samples of a simple WAV file.
func ReadWav(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a test's own file.
	if err != nil {
		return nil, err
	}
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4:]))
		if id == "data" {
			end := min(off+8+size, len(data))
			return data[off+8 : end], nil
		}
		off += 8 + size + size%2
	}
	return nil, errors.New("no audio in the file")
}

// Level is the loudness of a PCM16 chunk from 0 to 1, as one window of
// the meter.
func Level(pcm []byte) float64 {
	var m Meter
	if l := m.levels(pcm, len(pcm)/2); len(l) > 0 {
		return l[0]
	}
	return 0
}

// A WAV file as long as one can be takes no more audio, and its header
// counts what fits.
func TestWavCap(t *testing.T) {
	w, err := CreateWav(filepath.Join(t.TempDir(), "full.wav"))
	if err != nil {
		t.Fatal(err)
	}
	w.bytes = maxWavData - 2
	if err := w.Write(make([]byte, 4)); err == nil {
		t.Error("wrote past what a WAV file holds")
	}
	if err := w.Write(make([]byte, 2)); err != nil {
		t.Errorf("the last samples: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if h := wavHeader(maxWavData + 10); binary.LittleEndian.Uint32(h[40:]) != maxWavData {
		t.Errorf("header counts %d", binary.LittleEndian.Uint32(h[40:]))
	}
}

// A WAV file cut off inside a sample is repaired to its whole samples.
func TestRepairOddWav(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cut.wav")
	if err := os.WriteFile(path, append(wavHeader(0), make([]byte, 2*SampleRate+1)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if ms := repairWav(path); ms != 1000 {
		t.Errorf("lasts %d ms", ms)
	}
	if pcm, err := ReadWav(path); err != nil || len(pcm) != 2*SampleRate {
		t.Errorf("repaired: %d bytes, %v", len(pcm), err)
	}
}

// The meter measures the same audio the same, split into chunks of an odd
// number of bytes as it may come.
func TestMeterOddChunks(t *testing.T) {
	pcm := append(tone(-35), tone(-35)...)
	var whole, split Meter
	want := whole.Levels(pcm)
	var got []float64
	for len(pcm) > 0 {
		n := min(1601, len(pcm))
		got = append(got, split.Levels(pcm[:n])...)
		pcm = pcm[n:]
	}
	if len(got) != len(want) || len(got) == 0 || got[0] != want[0] {
		t.Errorf("levels %v, want %v", got, want)
	}
}
