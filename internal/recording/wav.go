package recording

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

// SampleRate is what audio is captured at: 16 kHz mono PCM16, what every
// speech model here wants.
const SampleRate = 16000

// WavWriter writes PCM16 mono audio to a WAV file as it arrives.
type WavWriter struct {
	Path string
	f    wavFile
	// bytes is the audio the file holds, and heard the audio that came to
	// be written, more than it holds once a disk filled up.
	bytes, heard int64
}

// wavFile is what a WavWriter writes to: the file, or in tests one that
// fills up.
type wavFile interface {
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

// CreateWav starts a WAV file, new and readable only by the user: a test
// recording's goes in the system's shared temporary folder.
func CreateWav(path string) (*WavWriter, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a recording's audio file, which the app names.
	if err != nil {
		return nil, err
	}
	return newWav(f)
}

// newWav starts a WAV file in f, new and empty; it closes f when it
// cannot.
func newWav(f *os.File) (*WavWriter, error) {
	if _, err := f.Write(wavHeader(0)); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &WavWriter{Path: f.Name(), f: f}, nil
}

// maxWavData is the most audio a WAV file holds: its header counts it in
// 32 bits, with the 36 bytes of header after the count. A recording that
// long, past 37 hours, keeps what fits.
const maxWavData = math.MaxUint32 - 36

// errWavFull is audio past what a WAV file holds.
var errWavFull = errors.New("the recording is as long as a WAV file can be")

// DurationMs is how long the audio written so far lasts.
func (w *WavWriter) DurationMs() int { return int(w.bytes * 1000 / 2 / SampleRate) }

// Write adds PCM16 little-endian samples. A disk that fills up can take
// only part of them: half a sample left at the end comes off, or all the
// audio after it, once there is room again, would play as noise.
func (w *WavWriter) Write(pcm []byte) error {
	if w.bytes+int64(len(pcm)) > maxWavData {
		return errWavFull
	}
	w.heard += int64(len(pcm))
	n, err := w.f.Write(pcm)
	w.bytes += int64(n)
	if err != nil && (w.heard-w.bytes)%2 != 0 && w.bytes > 0 {
		if _, serr := w.f.Seek(-1, io.SeekCurrent); serr == nil {
			w.bytes--
		}
	}
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
	// What does not fit the header's count is left out of it.
	var data uint32 = maxWavData
	if dataBytes < 0 {
		data = 0
	} else if dataBytes < maxWavData {
		data = uint32(dataBytes)
	}
	b := make([]byte, 44)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 36+data)
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
	binary.LittleEndian.PutUint32(b[40:], data)
	return b
}

// WavFromPCM wraps PCM16 mono samples in a WAV file's bytes.
func WavFromPCM(pcm []byte) []byte {
	return append(wavHeader(int64(len(pcm))), pcm...)
}

// LevelWindow is the samples each bar of the meter measures: 50 ms.
const LevelWindow = SampleRate / 20

// Meter turns the audio as it comes into one level per LevelWindow,
// however the audio is split into chunks, inside a sample too.
type Meter struct {
	sum float64
	n   int
	// half is the first byte of a sample the last chunk ended inside.
	half []byte
}

// Levels adds a chunk and returns the levels of the windows it completed.
func (m *Meter) Levels(pcm []byte) []float64 { return m.levels(pcm, LevelWindow) }

func (m *Meter) levels(pcm []byte, window int) []float64 {
	if len(m.half) > 0 && len(pcm) > 0 {
		pcm = append(m.half, pcm...)
		m.half = nil
	}
	if len(pcm)%2 == 1 {
		m.half = []byte{pcm[len(pcm)-1]}
	}
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
