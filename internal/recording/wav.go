package recording

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
)

// SampleRate is what audio is captured at: 16 kHz mono PCM16, what every
// speech model here wants.
const SampleRate = 16000

// WavWriter writes PCM16 mono audio to a WAV file as it arrives.
type WavWriter struct {
	Path  string
	f     *os.File
	bytes int64
}

// CreateWav starts a WAV file.
func CreateWav(path string) (*WavWriter, error) {
	f, err := os.Create(path) // #nosec G304 -- a recording's audio file, which the app names.
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(wavHeader(0)); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &WavWriter{Path: path, f: f}, nil
}

// DurationMs is how long the audio written so far lasts.
func (w *WavWriter) DurationMs() int { return int(w.bytes * 1000 / 2 / SampleRate) }

// Write adds PCM16 little-endian samples.
func (w *WavWriter) Write(pcm []byte) error {
	n, err := w.f.Write(pcm)
	w.bytes += int64(n)
	return err
}

// Close writes the header's sizes and closes the file.
func (w *WavWriter) Close() error {
	if _, err := w.f.WriteAt(wavHeader(w.bytes), 0); err != nil {
		_ = w.f.Close()
		return err
	}
	return w.f.Close()
}

// repairWav writes the header's sizes of a WAV file that was never closed,
// from the samples that reached it, and returns its duration in
// milliseconds, or 0 when it cannot.
func repairWav(path string) int {
	f, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304 -- a recording's audio file, which the app names.
	if err != nil {
		return 0
	}
	fi, err := f.Stat()
	if err != nil || fi.Size() < 44 {
		_ = f.Close()
		return 0
	}
	data := (fi.Size() - 44) &^ 1 // whole 16-bit samples
	if _, err := f.WriteAt(wavHeader(data), 0); err != nil {
		_ = f.Close()
		return 0
	}
	if err := f.Close(); err != nil {
		return 0
	}
	return int(data * 1000 / 2 / SampleRate)
}

func wavHeader(dataBytes int64) []byte {
	b := make([]byte, 44)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataBytes))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], SampleRate)
	binary.LittleEndian.PutUint32(b[28:], SampleRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataBytes))
	return b
}

// WavFromPCM wraps PCM16 mono samples in a WAV file's bytes.
func WavFromPCM(pcm []byte) []byte {
	return append(wavHeader(int64(len(pcm))), pcm...)
}

// ReadWav reads the PCM16 samples of a simple WAV file.
func ReadWav(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a recording's audio file, which the app names.
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

// LevelWindow is the samples each bar of the meter measures: 50 ms.
const LevelWindow = SampleRate / 20

// Level is the loudness of a PCM16 chunk from 0 to 1, for the meter.
func Level(pcm []byte) float64 {
	var m Meter
	if l := m.levels(pcm, len(pcm)/2); len(l) > 0 {
		return l[0]
	}
	return 0
}

// Meter turns the audio as it comes into one level per LevelWindow,
// however the audio is split into chunks.
type Meter struct {
	sum float64
	n   int
}

// Levels adds a chunk and returns the levels of the windows it completed.
func (m *Meter) Levels(pcm []byte) []float64 { return m.levels(pcm, LevelWindow) }

func (m *Meter) levels(pcm []byte, window int) []float64 {
	var out []float64
	for i := 0; i+1 < len(pcm); i += 2 {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) / 32768
		m.sum += s * s
		m.n++
		if m.n >= window {
			out = append(out, m.level())
			m.sum, m.n = 0, 0
		}
	}
	return out
}

// level is the window's RMS on a decibel scale from -55 dBFS (silence)
// to -10 dBFS (loud speech), so a laptop microphone's quiet speech still
// moves it.
func (m *Meter) level() float64 {
	if m.n == 0 {
		return 0
	}
	rms := math.Sqrt(m.sum / float64(m.n))
	if rms <= 0 {
		return 0
	}
	db := 20 * math.Log10(rms)
	return math.Max(0, math.Min((db+55)/45, 1))
}
