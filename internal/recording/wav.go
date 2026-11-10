package recording

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

const SampleRate = 16000

type WavWriter struct {
	Path         string
	f            wavFile
	bytes, heard int64
}

type wavFile interface {
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

func CreateWav(path string) (*WavWriter, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304
	if err != nil {
		return nil, err
	}
	return newWav(f)
}

func newWav(f *os.File) (*WavWriter, error) {
	if _, err := f.Write(wavHeader(0)); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &WavWriter{Path: f.Name(), f: f}, nil
}

const maxWavData = math.MaxUint32 - 36

var errWavFull = errors.New("the recording is as long as a WAV file can be")

func (w *WavWriter) DurationMs() int { return int(w.bytes * 1000 / 2 / SampleRate) }

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

func (w *WavWriter) Close() error {
	if _, err := w.f.WriteAt(wavHeader(w.bytes), 0); err != nil {
		_ = w.f.Close()
		return err
	}
	return w.f.Close()
}

func repairWav(path string) int {
	f, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304
	if err != nil {
		return 0
	}
	fi, err := f.Stat()
	if err != nil || fi.Size() < 44 {
		_ = f.Close()
		return 0
	}
	data := (fi.Size() - 44) &^ 1
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
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], SampleRate)
	binary.LittleEndian.PutUint32(b[28:], SampleRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], data)
	return b
}

func WavFromPCM(pcm []byte) []byte {
	return append(wavHeader(int64(len(pcm))), pcm...)
}

const LevelWindow = SampleRate / 20

type Meter struct {
	sum  float64
	n    int
	half []byte
}

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
