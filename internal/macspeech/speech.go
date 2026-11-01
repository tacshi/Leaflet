// Package macspeech transcribes recordings with the Mac's own speech
// recognition, through leaflet-speech (macos/speech), the helper the Mac
// app ships in its resources: the recorder's on-device engine there.
package macspeech

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

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

// answer is a line the helper writes: the words so far, the transcript
// once done, or why it could not transcribe.
type answer struct {
	Text     *string         `json:"text"`
	Done     bool            `json:"done"`
	Segments []store.Segment `json:"segments"`
	Error    string          `json:"error"`
	// Code is "language" when the Mac cannot transcribe the language,
	// which Language names.
	Code     string `json:"code"`
	Language string `json:"language"`
}

// failure is the error of an answer, in the user's language where the
// helper names the reason.
func failure(a answer, language string) error {
	if a.Code == "language" {
		// "auto" is the Mac's own language, which the helper names.
		code := language
		if a.Language != "" {
			code = a.Language
		}
		name := code
		if _, ok := recording.Languages[code]; ok {
			name = recording.LanguageName(code)
		}
		return errors.New(locales.RecErrOnDeviceLanguage(name))
	}
	return errors.New(a.Error)
}

// File transcribes the WAV file at path.
func (h *Helper) File(path, language string) ([]store.Segment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
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

// read reads the helper's answers until its transcript, passing the words
// so far to text, if not nil.
func read(r io.Reader, language string, text func(string)) ([]store.Segment, error) {
	dec := json.NewDecoder(r)
	for {
		var a answer
		if err := dec.Decode(&a); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%s ended without a transcript", Name)
			}
			return nil, err
		}
		switch {
		case a.Error != "":
			return nil, failure(a, language)
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
	go s.write(in)
	go func() {
		segs, err := read(out, language, text)
		if werr := cmd.Wait(); err == nil && werr != nil {
			err = werr
		}
		s.mu.Lock()
		s.segs, s.err = segs, err
		s.mu.Unlock()
		close(s.done)
	}()
	return s, nil
}

// stream is one recording's transcription by the helper. What Send gives
// it waits in queue for the helper to take, so the microphone never waits
// on it.
type stream struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	wake *sync.Cond
	// queue is the audio not yet written; ended, that Finish was called.
	queue [][]byte
	ended bool
	done  chan struct{}
	segs  []store.Segment
	err   error
}

func (s *stream) Send(pcm []byte) {
	s.mu.Lock()
	if !s.ended {
		s.queue = append(s.queue, pcm)
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
		for len(s.queue) == 0 && !s.ended {
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
			return
		}
	}
}

// Finish waits for the words of the audio sent: as long as the helper
// takes to catch up, which is about as long as the recording at worst.
func (s *stream) Finish() ([]store.Segment, error) {
	s.mu.Lock()
	s.ended = true
	s.wake.Signal()
	s.mu.Unlock()
	<-s.done
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
	return fmt.Errorf("%s stopped", Name)
}
