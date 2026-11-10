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

const Name = "leaflet-speech"

type Helper struct{ path string }

func Find(dir string) *Helper {
	if runtime.GOOS != "darwin" {
		return nil
	}
	path := filepath.Join(dir, Name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, path, "available").Run(); err != nil { // #nosec G204
		return nil
	}
	return &Helper{path: path}
}

func (h *Helper) File(path, language string) ([]store.Segment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fileWait(path))
	defer cancel()
	cmd := exec.CommandContext(ctx, h.path, "file", language, path) // #nosec G204
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

func wavFormat(path string) (data, rate int64) {
	f, err := os.Open(path) // #nosec G304
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

func (h *Helper) Stream(language string, text func(string)) (recording.Stream, error) {
	cmd := exec.Command(h.path, "stream", language) // #nosec G204
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

type stream struct {
	cmd     *exec.Cmd
	mu      sync.Mutex
	wake    *sync.Cond
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

var finishSlack = 4 * time.Minute

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
