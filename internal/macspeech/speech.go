// Package macspeech transcribes recordings with the Mac's own speech
// recognition, through leaflet-speech (speech/), the helper the Mac
// app ships in its resources: the recorder's on-device engine there.
package macspeech

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"leaflet/internal/guard"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Name is the helper's file name in the app's resources.
const Name = "leaflet-speech"

// Helper is leaflet-speech, as the recorder's Engine.
type Helper struct{ path string }

// Find returns the helper in dir, the app's resources, when it is there
// and the Mac can transcribe; nil otherwise, as on other platforms and
// before macOS 26. It runs the helper, so call it off the main thread.
func Find(dir string) *Helper {
	if runtime.GOOS != "darwin" {
		return nil
	}
	path := filepath.Join(dir, Name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, path, "available").Run(); err != nil { // #nosec G204 -- the app's own helper.
		return nil
	}
	return &Helper{path: path}
}

// File transcribes the audio file at path, a recording's or another's,
// in fileWait at the most.
func (h *Helper) File(path, language string) ([]store.Segment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fileWait(path))
	defer cancel()
	cmd := exec.CommandContext(ctx, h.path, "file", language, path) // #nosec G204 -- the app's own helper, on the recording's own file.
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	segs, ferr := read(out, language, nil)
	if err := cmd.Wait(); ferr == nil && err != nil {
		ferr = err
	}
	return recording.Tidy(segs), ferr
}

// fileWait is how long the helper gets to transcribe the file at path:
// twice as long as a WAV's audio lasts, read from its size at its format's
// byte rate as the stream's Finish does, and five minutes more; three
// hours for another file, or a WAV whose format can't be read, whose
// length isn't known.
func fileWait(path string) time.Duration {
	const other = 3 * time.Hour
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		return other
	}
	data, rate := wavFormat(path)
	if rate <= 0 {
		return other
	}
	return time.Duration(2*data/rate)*time.Second + 5*time.Minute
}

// wavFormat reads the WAV file at path for the bytes of audio in it, all
// past its fmt chunk, and the byte rate they play at: 0 when it has none
// to read. The data chunk's own count is left alone, as a recording cut
// short leaves it 0 and one past 4 GB leaves it short.
func wavFormat(path string) (data, rate int64) {
	f, err := os.Open(path) // #nosec G304 -- the audio file to transcribe.
	if err != nil {
		return 0, 0
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return 0, 0
	}
	var head [12]byte
	if _, err := io.ReadFull(f, head[:]); err != nil || string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return 0, 0
	}
	// The chunks before fmt are few and small, where there are any.
	at := int64(12)
	for range 16 {
		var chunk [8]byte
		if _, err := f.ReadAt(chunk[:], at); err != nil {
			return 0, 0
		}
		size := int64(binary.LittleEndian.Uint32(chunk[4:]))
		if string(chunk[0:4]) == "fmt " {
			var format [16]byte
			if size < 16 {
				return 0, 0
			}
			if _, err := f.ReadAt(format[:], at+8); err != nil {
				return 0, 0
			}
			end := at + 8 + size + size&1
			return max(fi.Size()-end, 0), int64(binary.LittleEndian.Uint32(format[8:12]))
		}
		at += 8 + size + size&1
	}
	return 0, 0
}

// read reads the helper's answers until its transcript, passing the words
// so far to text, if not nil.
func read(r io.Reader, language string, text func(string)) ([]store.Segment, error) {
	dec := json.NewDecoder(r)
	for {
		var a recording.OnDeviceAnswer
		if err := dec.Decode(&a); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New(locales.RecErrOnDeviceEnded())
			}
			return nil, err
		}
		switch {
		case a.Error != "":
			return nil, a.Err(language)
		case a.Done:
			return a.Segments, nil
		case a.Text != nil && text != nil:
			text(*a.Text)
		}
	}
}

// Stream starts transcribing a recording as it is recorded.
func (h *Helper) Stream(language string, text func(string)) (recording.Stream, error) {
	cmd := exec.Command(h.path, "stream", language) // #nosec G204 -- the app's own helper.
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &stream{cmd: cmd, done: make(chan struct{})}
	s.wake = sync.NewCond(&s.mu)
	guard.Go(func() { s.write(in) })
	guard.Go(func() {
		segs, err := read(out, language, text)
		if werr := cmd.Wait(); err == nil && werr != nil {
			err = werr
		}
		s.mu.Lock()
		s.segs, s.err = segs, err
		s.stopped, s.queue = true, nil
		s.wake.Signal()
		s.mu.Unlock()
		close(s.done)
	})
	return s, nil
}

// stream is one recording's transcription by the helper. What Send gives
// it waits in queue for the helper to take, so the microphone never waits
// on it.
type stream struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	wake *sync.Cond
	// queue is the audio not yet written; ended, that Finish was called;
	// stopped, that the helper takes no more, as it ended or its input
	// failed; sent, how many bytes of audio Send was given.
	queue   [][]byte
	ended   bool
	stopped bool
	sent    int
	done    chan struct{}
	segs    []store.Segment
	err     error
}

func (s *stream) Send(pcm []byte) {
	s.mu.Lock()
	if !s.ended && !s.stopped {
		s.queue = append(s.queue, pcm)
		s.sent += len(pcm)
		s.wake.Signal()
	}
	s.mu.Unlock()
}

// write writes the queued audio to the helper, ending its input once
// Finish was called and the queue is empty.
func (s *stream) write(in io.WriteCloser) {
	defer func() { _ = in.Close() }()
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.ended && !s.stopped {
			s.wake.Wait()
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		pcm := s.queue[0]
		s.queue[0] = nil
		s.queue = s.queue[1:]
		s.mu.Unlock()
		if _, err := in.Write(pcm); err != nil {
			log.Printf("macspeech: %v", err)
			s.mu.Lock()
			s.stopped, s.queue = true, nil
			s.mu.Unlock()
			return
		}
	}
}

// finishSlack is how much longer than the audio sent Finish waits for the
// helper, which on its first use in a language downloads the model before
// it hears any audio; tests shorten it.
var finishSlack = 4 * time.Minute

// Finish waits for the words of the audio sent: as long as the helper
// takes to catch up, which is about as long as the recording at worst. A
// helper that takes longer still, stuck, is ended.
func (s *stream) Finish() ([]store.Segment, error) {
	s.mu.Lock()
	s.ended = true
	s.wake.Signal()
	wait := time.Duration(s.sent/2)*time.Second/recording.SampleRate + finishSlack
	s.mu.Unlock()
	select {
	case <-s.done:
	case <-time.After(wait):
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		<-s.done
		return nil, errors.New(locales.RecErrOnDeviceStuck())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return recording.Tidy(s.segs), s.err
}

func (s *stream) Close() {
	s.mu.Lock()
	s.ended, s.queue = true, nil
	s.wake.Signal()
	s.mu.Unlock()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// Lost says why the helper ended before Finish, if it did.
func (s *stream) Lost() error {
	select {
	case <-s.done:
	default:
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil
	}
	if s.err != nil {
		return s.err
	}
	return errors.New(locales.RecErrOnDeviceStopped())
}
