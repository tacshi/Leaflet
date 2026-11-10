package recording

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

type Controller struct {
	st     *store.Store
	be     *remote.Client
	input  Capture
	update func(func())

	pickMu sync.Mutex
	picked map[string]string

	Blobs Blobs

	Engine  Engine
	finding chan struct{}

	State             State
	TranscribingFiles map[string]string
	stallPaused       bool

	mu           sync.Mutex
	toggling     sync.Mutex
	wav          *WavWriter
	mic          Mic
	session      string
	paused       bool
	elapsed      time.Duration
	resumed      time.Time
	stt          store.SttSettings
	meter        Meter
	stream       Stream
	connecting   bool
	early        [][]byte
	earlyLen     int
	ending       map[string]ending
	late         sync.Mutex
	saving       map[string]int
	settled      chan struct{}
	withText     bool
	test         func(text string, err error)
	testFor      time.Duration
	testWhy      error
	Transcribing map[string]bool
	Finishing    map[string]bool
}

type ending struct {
	wav    *WavWriter
	stream Stream
	paused bool
}

func (r *Controller) SetEngine(e Engine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Engine = e
	if r.finding != nil {
		close(r.finding)
		r.finding = nil
	}
}

func (r *Controller) FindingEngine() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finding == nil {
		r.finding = make(chan struct{})
	}
}

func (r *Controller) engineNow() (Engine, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Engine, r.finding != nil
}

func (r *Controller) waitEngine(ctx context.Context) Engine {
	r.mu.Lock()
	finding := r.finding
	r.mu.Unlock()
	if finding != nil {
		select {
		case <-finding:
		case <-ctx.Done():
		}
	}
	e, _ := r.engineNow()
	return e
}

type Capture interface {
	Start(rate int, sink Sink) (Mic, error)
}

type Sink struct {
	Chunk     func(pcm []byte)
	Started   func()
	Failed    func(message string)
	Stalled   func(message string)
	Recovered func()
}

const OnDevice = "on-device"

type Engine interface {
	Stream(language string, text func(string)) (Stream, error)
	File(path, language string) ([]store.Segment, error)
}

type Stream interface {
	Send(pcm []byte)
	Finish() ([]store.Segment, error)
	Close()
	Lost() error
}

type Mic interface {
	Pause()
	Resume()
	Stop()
	Close()
}

type Blobs interface {
	Uploads() bool
	CanFetch() bool
	Fetch(ctx context.Context, bucket, path string) ([]byte, error)
}

type blobReader interface {
	FetchReader(ctx context.Context, bucket, path string) (io.ReadCloser, error)
}

func (r *Controller) uploads() bool {
	if r.Blobs != nil {
		return r.Blobs.Uploads()
	}
	return r.be.SignedIn()
}

type State struct {
	Active    bool
	Starting  bool
	Paused    bool
	PageID    string
	BlockID   string
	ID        string
	Levels    []float64
	LiveText  string
	Engine    string
	Error     string
	StartedAt time.Time
}

func NewController(st *store.Store, be *remote.Client) *Controller {
	return &Controller{st: st, be: be, Transcribing: map[string]bool{}, Finishing: map[string]bool{}}
}

func (r *Controller) Saving() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.saving)
}

func (r *Controller) hold(id string) {
	r.mu.Lock()
	if r.saving == nil {
		r.saving = map[string]int{}
	}
	if len(r.saving) == 0 {
		r.settled = make(chan struct{})
	}
	r.saving[id]++
	r.mu.Unlock()
}

func (r *Controller) release(id string) {
	r.mu.Lock()
	if r.saving[id] > 1 {
		r.saving[id]--
	} else {
		delete(r.saving, id)
	}
	if len(r.saving) == 0 && r.settled != nil {
		close(r.settled)
		r.settled = nil
	}
	r.mu.Unlock()
}

// Settle waits until no recording is being saved or transcribed, for at
// most d, and reports whether none is.
func (r *Controller) Settle(d time.Duration) bool {
	r.mu.Lock()
	settled := r.settled
	r.mu.Unlock()
	if settled == nil {
		return true
	}
	select {
	case <-settled:
		return true
	case <-time.After(d):
		return false
	}
}

func (r *Controller) RecoverInterrupted() {
	recs, err := r.st.Recordings()
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec.Status != "recording" && rec.Status != "transcribing" {
			continue
		}
		cutOff := rec.Status == "recording"
		var duration int
		if cutOff && rec.LocalPath != "" {
			duration = repairWav(rec.LocalPath)
		}
		over := "transcribing"
		if cutOff {
			over = ""
		}
		if _, err := r.st.SettleRecording(rec.ID, over, r.uploads(), func(x *store.Recording) {
			if duration > 0 {
				x.DurationMs = duration
			}
			if len(x.Transcript) > 0 || x.SttProvider == "" {
				x.Status, x.Error = "ready", ""
				return
			}
			x.Status, x.Error = "failed", locales.RecErrQuit()
		}); err != nil {
			log.Printf("recording: %v", err)
		}
	}
}

func (r *Controller) Attach(c Capture, update func(func())) {
	r.input, r.update = c, update
}

func (r *Controller) Active() bool { return r.State.Active || r.State.Starting }

func (r *Controller) Elapsed() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.elapsed
	if !r.paused && !r.resumed.IsZero() {
		d += time.Since(r.resumed)
	}
	return d
}

func EngineLabel(s store.SttSettings) string {
	if s.Provider == "soniox" {
		return locales.RecEngineSonioxCloud()
	}
	if s.Provider == OnDevice {
		return locales.RecEngineOnDevice()
	}
	if s.Provider == Moss {
		return locales.RecEngineCloud("MOSS")
	}
	name := s.Model
	if name == "" {
		name = locales.RecEngineServer()
	}
	if IsLocalEndpoint(s.BaseURL) {
		return locales.RecEngineLocal(name)
	}
	return locales.RecEngineCloud(name)
}

func Usable(s store.SttSettings, online bool) error {
	switch s.Provider {
	case OnDevice:
		return errors.New(locales.RecErrOnDevice())
	case "soniox":
		if LocalSonioxKey() == "" {
			return errors.New(locales.RecErrSetUpSoniox())
		}
	case Moss:
		if MossKey() == "" {
			return errors.New(locales.RecErrSetUpMoss())
		}
	default:
		if strings.TrimSpace(s.BaseURL) == "" {
			return errors.New(locales.RecErrSetUpEndpoint())
		}
		if IsLocalEndpoint(s.BaseURL) {
			return nil
		}
	}
	if !online {
		return errors.New(locales.RecErrNeedsConnection())
	}
	return nil
}

func (r *Controller) Usable(s store.SttSettings, online bool) error {
	if e, finding := r.engineNow(); s.Provider == OnDevice && (e != nil || finding) {
		return nil
	}
	return Usable(s, online)
}

func model(stt store.SttSettings) string {
	switch stt.Provider {
	case "soniox":
		return stt.SonioxModel
	case Moss:
		return mossModel
	case OnDevice:
		return ""
	}
	return stt.Model
}

func (r *Controller) dir() (string, error) {
	d := filepath.Join(r.st.Dir, "recordings")
	return d, os.MkdirAll(d, 0o750)
}

func (r *Controller) Start(pageID string, stt store.SttSettings, online bool, insert func(n *doc.Node)) error {
	if r.Active() {
		return errors.New(locales.RecErrAlreadyRecording())
	}
	transcribe := r.Usable(stt, online) == nil
	if r.input == nil {
		return errors.New(locales.RecErrWindowNotReady())
	}
	id := doc.NewRowID()
	blockID := doc.NewBlockID()
	dir, err := r.dir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, id+".wav")
	wav, err := CreateWav(path)
	if err != nil {
		return err
	}
	rec, err := r.st.CreateRecording(pageID, blockID, stt.Provider, model(stt), path, stt.Language)
	if err != nil {
		_ = wav.Close()
		return err
	}
	insert(&doc.Node{ID: blockID, Type: "recording", Data: map[string]any{
		"recording_id": rec.ID, "title": locales.RecBlockTitle(time.Now()), "duration_ms": 0,
	}})
	r.hold(rec.ID)
	return r.capture(wav, stt, transcribe, State{PageID: pageID, BlockID: blockID, ID: rec.ID})
}

func (r *Controller) Test(stt store.SttSettings, online bool, d time.Duration, done func(text string, err error)) error {
	if r.Active() {
		return errors.New(locales.RecErrAlreadyRecording())
	}
	if r.input == nil {
		return errors.New(locales.RecErrWindowNotReady())
	}
	why := r.Usable(stt, online)
	f, err := os.CreateTemp("", "leaflet-test-*.wav")
	if err != nil {
		return err
	}
	wav, err := newWav(f)
	if err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	r.mu.Lock()
	r.test, r.testFor, r.testWhy = done, d, why
	r.mu.Unlock()
	return r.capture(wav, stt, why == nil, State{})
}

func (r *Controller) capture(wav *WavWriter, stt store.SttSettings, transcribe bool, st State) error {
	session := doc.NewBlockID()
	live := transcribe && stt.Provider == "soniox"
	r.mu.Lock()
	r.wav, r.session, r.stt, r.paused, r.elapsed, r.resumed = wav, session, stt, false, 0, time.Time{}
	r.stream, r.connecting, r.early, r.earlyLen = nil, live, nil, 0
	r.meter = Meter{}
	r.withText = transcribe
	testFor := r.testFor
	r.mu.Unlock()
	st.Starting, st.Engine = true, EngineLabel(stt)
	if !transcribe {
		st.Engine = locales.RecAudioOnly()
	}
	r.State = st
	if testFor > 0 {
		time.AfterFunc(15*time.Second, func() {
			r.update(func() {
				r.mu.Lock()
				same := r.session == session
				r.mu.Unlock()
				if same && r.State.Starting {
					r.State.Error = locales.RecErrMic()
					r.abort()
				}
			})
		})
	}
	if live {
		guard.Go(func() { r.startSoniox(session, stt) })
	}
	if e, _ := r.engineNow(); transcribe && stt.Provider == OnDevice && e != nil {
		s, err := e.Stream(stt.Language, func(text string) {
			r.update(func() {
				r.mu.Lock()
				same := r.session == session
				r.mu.Unlock()
				if same {
					r.State.LiveText = text
				}
			})
		})
		if err != nil {
			r.State.Error = locales.RecErrLive(err.Error())
		} else {
			r.mu.Lock()
			r.stream = s
			r.mu.Unlock()
		}
	}
	m, err := r.input.Start(SampleRate, Sink{
		Chunk:     func(pcm []byte) { r.chunk(session, pcm) },
		Started:   func() { r.started(session, testFor) },
		Failed:    func(msg string) { r.failed(session, msg) },
		Stalled:   func(msg string) { r.stalled(session, msg) },
		Recovered: func() { r.recovered(session) },
	})
	if err != nil {
		r.abort()
		return err
	}
	r.mu.Lock()
	r.mic = m
	r.mu.Unlock()
	return nil
}

func (r *Controller) failed(session, msg string) {
	r.update(func() {
		r.mu.Lock()
		same := r.session == session && r.State.Starting
		r.mu.Unlock()
		if !same {
			return
		}
		r.State.Error = locales.RecErrMicDetail(msg)
		r.abort()
	})
}

func (r *Controller) started(session string, testFor time.Duration) {
	r.update(func() {
		r.mu.Lock()
		same := r.session == session && r.State.Starting
		if same {
			r.resumed = time.Now()
		}
		r.mu.Unlock()
		if !same {
			return
		}
		r.State.Starting, r.State.Active, r.State.StartedAt = false, true, time.Now()
		if testFor > 0 {
			time.AfterFunc(testFor, func() {
				r.update(func() {
					r.mu.Lock()
					same := r.session == session
					r.mu.Unlock()
					if same {
						r.Stop()
					}
				})
			})
		}
	})
}

func (r *Controller) startSoniox(session string, stt store.SttSettings) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := DialSoniox(ctx, LocalSonioxKey(), stt.SonioxModel, sonioxHints(stt), stt.SpeakerLabels)
	if err == nil {
		s.SetOnText(func(text string) {
			r.update(func() {
				r.mu.Lock()
				same := r.session == session
				r.mu.Unlock()
				if same {
					r.State.LiveText = text
				}
			})
		})
		r.attachSoniox(session, s)
		return
	}
	r.mu.Lock()
	same := r.session == session && r.connecting
	if r.session == session {
		r.connecting, r.early, r.earlyLen = false, nil, 0
	}
	r.mu.Unlock()
	if same {
		r.update(func() { r.State.Error = locales.RecErrLive(err.Error()) })
	}
}

func (r *Controller) attachSoniox(session string, s *Soniox) {
	for {
		r.mu.Lock()
		if r.session != session || !r.connecting {
			r.mu.Unlock()
			s.Close()
			return
		}
		early := r.early
		r.early, r.earlyLen = nil, 0
		if len(early) == 0 {
			r.stream, r.connecting = s, false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		for _, pcm := range early {
			s.Send(pcm)
		}
	}
}

const maxEarly = 30 * SampleRate * 2

func (r *Controller) chunk(session string, pcm []byte) {
	r.mu.Lock()
	if e, ok := r.ending[session]; ok {
		r.late.Lock()
		r.mu.Unlock()
		defer r.late.Unlock()
		r.lateChunk(e, pcm)
		return
	}
	if r.paused || r.wav == nil || r.session != session {
		r.mu.Unlock()
		return
	}
	err := r.wav.Write(pcm)
	s := r.stream
	if s == nil && r.connecting && r.earlyLen+len(pcm) <= maxEarly {
		r.early = append(r.early, pcm)
		r.earlyLen += len(pcm)
	}
	r.mu.Unlock()
	if err != nil {
		r.update(func() { r.State.Error = locales.RecErrAudioNotSaved(err.Error()) })
	}
	if s != nil {
		s.Send(pcm)
	}
	r.mu.Lock()
	levels := r.meter.Levels(pcm)
	r.mu.Unlock()
	if len(levels) == 0 {
		return
	}
	r.update(func() {
		r.State.Levels = append(r.State.Levels, levels...)
		if n := len(r.State.Levels); n > MaxLevels {
			r.State.Levels = r.State.Levels[n-MaxLevels:]
		}
	})
}

func (r *Controller) lateChunk(e ending, pcm []byte) {
	if e.paused || e.wav == nil {
		return
	}
	if err := e.wav.Write(pcm); err != nil {
		log.Printf("recording: %v", err)
	}
	if e.stream != nil {
		e.stream.Send(pcm)
	}
}

const MaxLevels = 400

func (r *Controller) TogglePause() {
	r.stallPaused = false
	r.mu.Lock()
	if r.resumed.IsZero() {
		r.mu.Unlock()
		return
	}
	if r.paused {
		r.paused, r.resumed = false, time.Now()
	} else {
		r.elapsed += time.Since(r.resumed)
		r.paused = true
	}
	paused := r.paused
	m := r.mic
	r.mu.Unlock()
	r.State.Paused = paused
	if m != nil {
		guard.Go(func() { r.toggleMic(m) })
	}
}

func (r *Controller) toggleMic(m Mic) {
	r.toggling.Lock()
	defer r.toggling.Unlock()
	r.mu.Lock()
	paused, same := r.paused, r.mic == m
	r.mu.Unlock()
	switch {
	case !same:
	case paused:
		m.Pause()
	default:
		m.Resume()
	}
}

func (r *Controller) stalled(session, msg string) {
	r.update(func() {
		r.mu.Lock()
		same := r.session == session && r.State.Active && r.mic != nil
		r.mu.Unlock()
		if !same {
			return
		}
		r.State.Error = locales.RecErrMicStalled(msg)
		r.mu.Lock()
		paused := r.paused
		r.mu.Unlock()
		if !paused {
			r.TogglePause()
			r.stallPaused = true
		}
	})
}

func (r *Controller) recovered(session string) {
	r.update(func() {
		r.mu.Lock()
		same := r.session == session && r.State.Active && r.mic != nil && r.paused
		r.mu.Unlock()
		if !same || !r.stallPaused {
			return
		}
		r.State.Error = ""
		r.TogglePause()
	})
}

func (r *Controller) abort() {
	r.mu.Lock()
	m, wav, stream, test := r.mic, r.wav, r.stream, r.test
	r.mic, r.wav, r.stream, r.test, r.testFor = nil, nil, nil, nil, 0
	r.connecting, r.early, r.earlyLen = false, nil, 0
	r.mu.Unlock()
	if m != nil {
		m.Close()
	}
	if stream != nil {
		stream.Close()
	}
	if wav != nil {
		_ = wav.Close()
		if test != nil {
			_ = os.Remove(wav.Path)
		}
	}
	if test != nil {
		msg := r.State.Error
		if msg == "" {
			msg = locales.RecErrMic()
		}
		test("", errors.New(msg))
	}
	if r.State.ID != "" {
		msg := r.State.Error
		if _, err := r.st.UpdateRecording(r.State.ID, func(rec *store.Recording) { rec.Status, rec.Error = "failed", msg }); err != nil {
			log.Printf("recording: %v", err)
		}
		r.release(r.State.ID)
	}
	r.State.Active, r.State.Starting = false, false
}

func (r *Controller) Stop() {
	if !r.Active() {
		return
	}
	if r.State.Starting {
		r.State.Error = locales.RecErrMic()
		r.abort()
		return
	}
	r.mu.Lock()
	m, wav, stream, stt, withText := r.mic, r.wav, r.stream, r.stt, r.withText
	test, why, session := r.test, r.testWhy, r.session
	if r.ending == nil {
		r.ending = map[string]ending{}
	}
	r.ending[session] = ending{wav: wav, stream: stream, paused: r.paused}
	r.mic, r.wav, r.stream, r.test, r.testFor = nil, nil, nil, nil, 0
	r.connecting, r.early, r.earlyLen = false, nil, 0
	var soFar int
	if wav != nil {
		soFar = wav.DurationMs()
	}
	r.mu.Unlock()
	st := r.State
	r.State = State{}
	if test == nil && st.ID != "" {
		r.keep(st, soFar, withText)
	}
	transcribes := test == nil && withText && st.ID != ""
	if transcribes {
		r.Transcribing[st.ID] = true
		if stream != nil {
			r.Finishing[st.ID] = true
		}
	}
	guard.Go(func() {
		if st.ID != "" {
			defer r.release(st.ID)
		}
		if m != nil {
			m.Stop()
		}
		r.mu.Lock()
		delete(r.ending, session)
		r.mu.Unlock()
		r.update(func() {
			if m != nil {
				m.Close()
			}
		})
		var path string
		var duration time.Duration
		if wav != nil {
			r.late.Lock()
			path = wav.Path
			duration = time.Duration(wav.DurationMs()) * time.Millisecond
			if err := wav.Close(); err != nil {
				log.Printf("recording: %v", err)
			}
			r.late.Unlock()
		}
		if test != nil {
			text, err := r.transcribeTest(path, stream, stt, why, st.Error)
			r.update(func() { test(text, err) })
			return
		}
		if st.ID == "" {
			return // no row to finish, and gone would take its audio
		}
		if !withText {
			if _, err := r.st.FinishRecording(st.ID, r.uploads(), func(rec *store.Recording) {
				rec.Status, rec.DurationMs, rec.SttProvider, rec.SttModel = "ready", int(duration.Milliseconds()), "", ""
			}); err != nil {
				log.Printf("recording: %v", err)
				gone(path, err)
			}
			return
		}
		rec, err := r.st.UpdateRecording(st.ID, func(rec *store.Recording) {
			rec.Status, rec.DurationMs = "transcribing", int(duration.Milliseconds())
			if t := strings.TrimSpace(st.LiveText); t != "" {
				rec.Transcript = []store.Segment{{EndMs: int(duration.Milliseconds()), Text: t}}
			}
		})
		if err != nil {
			log.Printf("recording: %v", err)
			gone(path, err)
			if stream != nil {
				stream.Close()
			}
			r.update(func() {
				delete(r.Transcribing, st.ID)
				delete(r.Finishing, st.ID)
			})
			return
		}
		r.finish(rec, path, stream, stt)
	})
}

// gone removes the audio of a recording whose row went while it was
// recorded, with its page or its account: nothing would find it again.
func gone(path string, err error) {
	if path != "" && store.IsNotFound(err) {
		_ = os.Remove(path)
	}
}

// keep stores what a recording has when it stops, so that it is not lost
// should the app end before the microphone has let go and the audio is
// closed.
func (r *Controller) keep(st State, durationMs int, withText bool) {
	if _, err := r.st.UpdateRecording(st.ID, func(rec *store.Recording) {
		rec.DurationMs = durationMs
		if t := strings.TrimSpace(st.LiveText); withText && t != "" {
			rec.Transcript = []store.Segment{{EndMs: durationMs, Text: t}}
		}
	}); err != nil {
		log.Printf("recording: %v", err)
	}
}

func (r *Controller) transcribeTest(path string, stream Stream, stt store.SttSettings, why error, liveErr string) (string, error) {
	defer func() { _ = os.Remove(path) }()
	if why != nil {
		return "", errors.New(locales.RecErrMicWorks(why.Error()))
	}
	var segs []store.Segment
	var err error
	switch {
	case stream != nil:
		segs, err = stream.Finish()
	case stt.Provider == OnDevice:
		if e := r.waitEngine(context.Background()); e != nil {
			segs, err = e.File(path, stt.Language)
		} else {
			err = errors.New(locales.RecErrOnDevice())
		}
	case stt.Provider == "soniox":
		if liveErr == "" {
			liveErr = locales.RecErrSonioxTimeout()
		}
		err = errors.New(liveErr)
	default:
		var f *os.File
		var size int64
		if f, size, err = openAudio(path); err == nil {
			defer func() { _ = f.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if stt.Provider == Moss {
				segs, err = TranscribeMoss(ctx, MossKey(), f, size, "test.wav", false)
			} else {
				segs, err = TranscribeEndpoint(ctx, stt.BaseURL, stt.Model, f, size, EndpointKey(stt.BaseURL), stt.Language)
			}
		}
	}
	if err != nil {
		return "", err
	}
	var words []string
	for _, s := range segs {
		if t := strings.TrimSpace(s.Text); t != "" {
			words = append(words, t)
		}
	}
	if len(words) == 0 {
		return "", errors.New(locales.RecErrNoWords())
	}
	return strings.Join(words, " "), nil
}

func (r *Controller) Retranscribe(id string, stt store.SttSettings, online bool) error {
	if r.Transcribing[id] {
		return errors.New(locales.RecErrTranscribing())
	}
	if r.update == nil {
		return errors.New(locales.RecErrWindowNotReady())
	}
	if err := r.Usable(stt, online); err != nil {
		return err
	}
	rec, err := r.st.UpdateRecording(id, func(rec *store.Recording) {
		rec.Status, rec.Error, rec.SttProvider, rec.SttModel = "transcribing", "", stt.Provider, model(stt)
	})
	if err != nil {
		return err
	}
	r.Transcribing[id] = true
	r.hold(id)
	guard.Go(func() {
		defer r.release(id)
		r.finish(rec, rec.LocalPath, nil, stt)
	})
	return nil
}

func (r *Controller) finish(rec store.Recording, path string, stream Stream, stt store.SttSettings) {
	r.update(func() {
		r.Transcribing[rec.ID] = true
		if stream != nil {
			r.Finishing[rec.ID] = true
		}
	})
	segs, err := r.transcribe(rec, path, stream, stt)
	var lost error
	if stream != nil {
		lost = stream.Lost()
	}
	// The row and the marks change in one turn of the app, so a recording
	// that reads as done can be transcribed again at once.
	done := make(chan struct{})
	r.update(func() {
		defer close(done)
		defer func() {
			delete(r.Transcribing, rec.ID)
			delete(r.Finishing, rec.ID)
		}()
		if _, err := r.st.FinishRecording(rec.ID, r.uploads(), func(x *store.Recording) {
			if err != nil {
				x.Status, x.Error = "failed", err.Error()
				return
			}
			if lost != nil {
				x.Status, x.Error, x.Transcript = "failed", locales.RecErrLiveStopped(lostReason(lost)), segs
				if len(segs) > 0 {
					x.Status = "ready"
				}
				return
			}
			x.Status, x.Error, x.Transcript = "ready", "", segs
		}); err != nil && !store.IsNotFound(err) {
			log.Printf("recording: %v", err)
		}
	})
	<-done
	if done, err := r.st.Recording(rec.ID); err == nil {
		r.sweepAudio(done, time.Now())
	}
}

const keepAudioFor = 30 * 24 * time.Hour

func (r *Controller) SweepAudio() {
	recs, err := r.st.Recordings()
	if err != nil {
		return
	}
	for _, rec := range recs {
		r.sweepAudio(rec, time.Now())
	}
}

func (r *Controller) sweepAudio(rec store.Recording, now time.Time) {
	keep := r.st.Settings().KeepAudioOnDevice
	if rec.LocalPath == "" || rec.AudioPath == "" || rec.UploadedAt.IsZero() || rec.Status != "ready" {
		return
	}
	fi, err := os.Stat(rec.LocalPath)
	if err != nil {
		return
	}
	switch keep {
	case "until_uploaded":
		if fi.ModTime().After(rec.UploadedAt) {
			return
		}
	case "30_days":
		if now.Sub(rec.UploadedAt) < keepAudioFor || now.Sub(fi.ModTime()) < keepAudioFor {
			return
		}
	default:
		return
	}
	if err := os.Remove(rec.LocalPath); err != nil {
		return
	}
	_ = r.st.ClearLocalPath(rec.ID)
}

func lostReason(err error) string {
	switch {
	case errors.Is(err, errBehind):
		return locales.RecLostBehind()
	case errors.Is(err, errNoEnd):
		return locales.RecLostNoEnd()
	}
	return err.Error()
}

func (r *Controller) DismissError(id string) error {
	_, err := r.st.UpdateRecording(id, func(rec *store.Recording) { rec.Error = "" })
	return err
}

func (r *Controller) storedAudio(ctx context.Context, rec store.Recording) (string, error) {
	size := int64(rec.DurationMs) * 2 * SampleRate / 1000
	ctx, cancel := context.WithTimeout(ctx, downloadWait(size))
	defer cancel()
	audio, err := r.openAudioOf(ctx, rec)
	if err != nil {
		return "", err
	}
	defer func() { _ = audio.Close() }()
	return tempAudio(audio)
}

func transferWait(size int64) time.Duration {
	return 2*time.Minute + time.Duration(size/(32<<10))*time.Second
}

func downloadWait(size int64) time.Duration {
	return max(5*time.Minute, transferWait(size))
}

func DownloadWait(size int64) time.Duration { return downloadWait(size) }

func AudioSize(ms int) int64 { return int64(ms) * SampleRate * 2 / 1000 }

func openAudio(path string) (*os.File, int64, error) {
	if path == "" {
		return nil, 0, errors.New(locales.RecErrAudioMissing())
	}
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

func (r *Controller) transcribe(rec store.Recording, path string, stream Stream, stt store.SttSettings) ([]store.Segment, error) {
	if stream != nil {
		return stream.Finish()
	}
	ctx := context.Background()
	if stt.Provider == OnDevice {
		return r.transcribeOnDevice(ctx, rec, path, stt.Language)
	}
	f, size, err := openAudio(path)
	if err != nil && rec.AudioPath != "" {
		fetched, ferr := r.storedAudio(ctx, rec)
		if ferr != nil {
			return nil, ferr
		}
		defer func() { _ = os.Remove(fetched) }()
		f, size, err = openAudio(fetched)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	switch stt.Provider {
	case "soniox":
		return TranscribeSoniox(ctx, LocalSonioxKey(), stt.SonioxModel, f, size, "recording.wav", sonioxHints(stt), stt.SpeakerLabels)
	case Moss:
		return TranscribeMoss(ctx, MossKey(), f, size, "recording.wav", stt.SpeakerLabels)
	}
	return TranscribeEndpoint(ctx, stt.BaseURL, stt.Model, f, size, EndpointKey(stt.BaseURL), stt.Language)
}

func (r *Controller) TranscribeFile(f store.File, blockID string, stt store.SttSettings, online bool, done func([]store.Segment, error)) error {
	if r.update == nil {
		return errors.New(locales.RecErrWindowNotReady())
	}
	for block, file := range r.TranscribingFiles {
		if block == blockID || file == f.ID {
			return errors.New(locales.RecErrFileTranscribing())
		}
	}
	if !doc.Transcribable(f.Name, f.MimeType) {
		return errors.New(locales.RecErrFileKind())
	}
	if err := r.Usable(stt, online); err != nil {
		return err
	}
	if stt.Provider == OnDevice && !OnDeviceReads(f.Name) {
		return errors.New(locales.RecErrFileKind())
	}
	if r.TranscribingFiles == nil {
		r.TranscribingFiles = map[string]string{}
	}
	r.TranscribingFiles[blockID] = f.ID
	guard.Go(func() {
		segs, err := r.transcribeFile(context.Background(), f, stt)
		r.update(func() {
			delete(r.TranscribingFiles, blockID)
			done(segs, err)
		})
	})
	return nil
}

func sonioxHints(stt store.SttSettings) []string {
	if _, ok := Languages[stt.Language]; ok {
		return []string{stt.Language}
	}
	return stt.LanguageHints
}

func (r *Controller) transcribeFile(ctx context.Context, f store.File, stt store.SttSettings) ([]store.Segment, error) {
	path, err := r.fileHere(ctx, f)
	if err != nil {
		return nil, err
	}
	if stt.Provider == OnDevice {
		e := r.waitEngine(ctx)
		if e == nil {
			return nil, errors.New(locales.RecErrOnDevice())
		}
		return e.File(path, stt.Language)
	}
	audio, size, err := openAudio(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = audio.Close() }()
	name := "audio" + strings.ToLower(filepath.Ext(f.Name))
	switch stt.Provider {
	case "soniox":
		return TranscribeSoniox(ctx, LocalSonioxKey(), stt.SonioxModel, audio, size, name, sonioxHints(stt), stt.SpeakerLabels)
	case Moss:
		return TranscribeMoss(ctx, MossKey(), audio, size, name, stt.SpeakerLabels)
	}
	return transcribeEndpoint(ctx, stt.BaseURL, stt.Model, audio, size, name, fileWait(size), EndpointKey(stt.BaseURL), stt.Language)
}

func (r *Controller) fileHere(ctx context.Context, f store.File) (string, error) {
	if f.LocalPath != "" {
		if _, err := os.Stat(f.LocalPath); err == nil {
			return f.LocalPath, nil
		}
	}
	if f.StoragePath == "" {
		return "", errors.New(locales.RecErrAudioMissing())
	}
	ctx, cancel := context.WithTimeout(ctx, downloadWait(f.SizeBytes))
	defer cancel()
	var from io.ReadCloser
	var err error
	if br, ok := r.Blobs.(blobReader); ok && r.Blobs.CanFetch() {
		from, err = br.FetchReader(ctx, "files", f.StoragePath)
	} else if r.be.SignedIn() {
		from, err = r.be.Download(ctx, "files", f.StoragePath)
	} else {
		err = errors.New(locales.RecErrAudioMissing())
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = from.Close() }()
	return r.st.DownloadedFileFrom(f.ID, from)
}

func (r *Controller) transcribeOnDevice(ctx context.Context, rec store.Recording, path, language string) ([]store.Segment, error) {
	e := r.waitEngine(ctx)
	if e == nil {
		return nil, errors.New(locales.RecErrOnDevice())
	}
	if _, err := os.Stat(path); path == "" || err != nil {
		if path, err = r.storedAudio(ctx, rec); err != nil {
			return nil, err
		}
		defer func() { _ = os.Remove(path) }()
	}
	return e.File(path, language)
}

func tempAudio(audio io.Reader) (string, error) {
	f, err := os.CreateTemp("", "leaflet-*.wav")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, audio)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (r *Controller) Audio(ctx context.Context, rec store.Recording) ([]byte, error) {
	audio, err := r.openAudioOf(ctx, rec)
	if err != nil {
		return nil, err
	}
	defer func() { _ = audio.Close() }()
	return io.ReadAll(audio)
}

func (r *Controller) OpenAudio(ctx context.Context, rec store.Recording) (io.ReadCloser, error) {
	return r.openAudioOf(ctx, rec)
}

func (r *Controller) openAudioOf(ctx context.Context, rec store.Recording) (io.ReadCloser, error) {
	if rec.LocalPath != "" {
		if f, err := os.Open(rec.LocalPath); err == nil { // #nosec G304
			return f, nil
		}
	}
	if rec.AudioPath != "" && r.Blobs != nil && r.Blobs.CanFetch() {
		if br, ok := r.Blobs.(blobReader); ok {
			return br.FetchReader(ctx, "recordings", rec.AudioPath)
		}
		data, err := r.Blobs.Fetch(ctx, "recordings", rec.AudioPath)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	if rec.AudioPath != "" && r.Blobs == nil && r.be.SignedIn() {
		return r.be.Download(ctx, "recordings", rec.AudioPath)
	}
	return nil, errors.New(locales.RecErrAudioMissing())
}
