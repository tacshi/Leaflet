// Package recording records audio into recording blocks and transcribes
// it: the microphone through the host's Capture, WAV files, Soniox
// real-time streaming, the device's own engine, and OpenAI-compatible
// /audio/transcriptions.
package recording

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Controller is the recording in progress, and the transcriptions running.
type Controller struct {
	st     *store.Store
	be     *remote.Client
	input  Capture
	update func(func())

	// Blobs is where audio goes up to for the other devices, and comes
	// from when it is not on this one: the sync service. Without it,
	// audio goes to Supabase when signed in.
	Blobs Blobs

	// Engine is the device's own speech engine, which transcribes for
	// OnDevice, or nil where the device has none. Read it on the main
	// thread, and give it with SetEngine there: the transcriptions read
	// it from goroutines of their own, under mu.
	Engine Engine
	// finding is open while the host looks for the device's engine
	// (FindingEngine), and closes once SetEngine gives what it found.
	// Guarded by mu.
	finding chan struct{}

	// The recording in progress, read and written on the main thread.
	State State

	mu      sync.Mutex
	wav     *WavWriter
	mic     Mic
	session string
	paused  bool
	elapsed time.Duration
	resumed time.Time
	stt     store.SttSettings
	meter   Meter
	// stream transcribes the recording in progress as it records: Soniox,
	// or the device's engine.
	stream Stream
	// connecting is whether a Soniox stream is on its way for the
	// recording in progress, and early the audio recorded meanwhile, sent
	// first once it is there: the transcript's times then match the WAV's.
	connecting bool
	early      [][]byte
	earlyLen   int
	// ending are the recordings stopped whose microphone still hands on
	// the audio it held, by session: it goes to their own file and
	// stream, though the next recording has started meanwhile.
	ending map[string]ending
	// withText is whether the recording in progress gets a transcript;
	// without an engine that can run, it keeps only its audio.
	withText bool
	// test gets the transcript of a test recording, which runs testFor and
	// cannot transcribe when testWhy says why.
	test    func(text string, err error)
	testFor time.Duration
	testWhy error
	// Transcribing are the recordings being transcribed; Finishing those
	// among them whose words came live, now getting their timestamps.
	Transcribing map[string]bool
	Finishing    map[string]bool
}

// ending is where the last audio of a recording stopped goes.
type ending struct {
	wav    *WavWriter
	stream Stream
	paused bool
}

// SetEngine gives the recorder the device's own engine, nil for none, and
// ends the search FindingEngine began. Call it on the main thread.
func (r *Controller) SetEngine(e Engine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Engine = e
	if r.finding != nil {
		close(r.finding)
		r.finding = nil
	}
}

// FindingEngine says the host is looking for the device's engine, which
// it gives with SetEngine once it knows: meanwhile the device's engine
// counts as usable, and what it is to transcribe waits for it, rather
// than a recording started then keeping only its audio.
func (r *Controller) FindingEngine() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finding == nil {
		r.finding = make(chan struct{})
	}
}

// engineNow is the device's engine, and whether the host is still looking
// for it.
func (r *Controller) engineNow() (Engine, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Engine, r.finding != nil
}

// waitEngine is the device's engine, once the host's search for it under
// way has ended, or ctx has.
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

// Capture is the microphone recordings hear through: on the desktop a
// page of the app's own in a web view, which the system hands the
// microphone; on iOS the app's audio engine.
type Capture interface {
	// Start turns the microphone on for a recording, to hand sink 16-bit
	// mono PCM at rate samples a second. It returns before the microphone
	// is on: sink's Started or Failed says when it is, or why it is not.
	Start(rate int, sink Sink) (Mic, error)
}

// Sink takes what a microphone hears. Its functions may run on any
// goroutine, but one at a time.
type Sink struct {
	// Chunk takes the audio heard next, in order.
	Chunk func(pcm []byte)
	// Started says the microphone is on.
	Started func()
	// Failed says why the microphone could not start.
	Failed func(message string)
}

// OnDevice is the speech-to-text provider that is the device's own
// engine.
const OnDevice = "on-device"

// Engine is a speech engine of the device's own, as iOS has.
type Engine interface {
	// Stream starts transcribing audio as it is recorded, 16-bit mono PCM
	// at SampleRate, in language ("auto" for the device's). text takes
	// the words so far, as they come.
	Stream(language string, text func(string)) (Stream, error)
	// File transcribes the WAV file at path.
	File(path, language string) ([]store.Segment, error)
}

// Stream transcribes a recording as it is recorded: Soniox, or the
// device's engine.
type Stream interface {
	// Send takes the audio recorded next, in order.
	Send(pcm []byte)
	// Finish waits for the words of the audio sent, and ends the stream.
	Finish() ([]store.Segment, error)
	// Close ends the stream at once.
	Close()
	// Lost says why the stream ended before Finish, if it did.
	Lost() error
}

// Mic is the microphone of one recording, which Capture started.
type Mic interface {
	// Pause stops handing on audio until Resume; neither waits.
	Pause()
	Resume()
	// Stop hands the sink the audio the microphone still holds, then turns
	// it off. It may wait for that audio, so it runs off the main thread.
	Stop()
	// Close lets the microphone go at once, after Stop or for a recording
	// that could not start. It runs on the main thread.
	Close()
}

// Blobs is where audio goes up to and comes from.
type Blobs interface {
	// Uploads reports whether audio goes up at all.
	Uploads() bool
	CanFetch() bool
	Fetch(ctx context.Context, bucket, path string) ([]byte, error)
}

func (r *Controller) uploads() bool {
	if r.Blobs != nil {
		return r.Blobs.Uploads()
	}
	return r.be.SignedIn()
}

// State is what the views show of the recording in progress.
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

// NewController returns the recorder.
func NewController(st *store.Store, be *remote.Client) *Controller {
	return &Controller{st: st, be: be, Transcribing: map[string]bool{}, Finishing: map[string]bool{}}
}

// RecoverInterrupted settles recordings that an earlier run left at
// "recording" or "transcribing", as when the app quit before they were
// done: nothing finishes them after a restart, and their blocks would say
// "Transcribing…" for good. A recording cut off mid-way gets its WAV header
// and duration from what reached the file. One with a transcript, the live
// text saved at Stop, keeps it and is ready; one without is failed, which
// offers "Transcribe again". Either way its audio, which nothing uploaded,
// goes up. Call it once, after Blobs is set and before any recording
// starts.
//
// A transcription may have gone on without this run: on the server, which
// saves its transcript to the recording there, or on the device that
// started it, when this one only pulled the recording mid-way. What is
// settled here goes up only over a recording still transcribing there
// (store.SettleRecording), and gives way to the finished one otherwise.
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
		// Cut off while recording, it was on no server or other device.
		over := "transcribing"
		if cutOff {
			over = ""
		}
		updated, err := r.st.SettleRecording(rec.ID, over, func(x *store.Recording) {
			if duration > 0 {
				x.DurationMs = duration
			}
			if len(x.Transcript) > 0 || x.SttProvider == "" {
				x.Status, x.Error = "ready", ""
				return
			}
			x.Status, x.Error = "failed", locales.RecErrQuit()
		})
		if err != nil {
			log.Printf("recording: %v", err)
			continue
		}
		if updated.AudioPath == "" && updated.LocalPath != "" && r.uploads() {
			if err := r.st.QueueUpload(updated); err != nil {
				log.Printf("recording: %v", err)
			}
		}
	}
}

// Attach gives the recorder its microphone, and a way to change the
// view's state from other goroutines: update runs a function on the main
// thread.
func (r *Controller) Attach(c Capture, update func(func())) {
	r.input, r.update = c, update
}

// Active reports whether a recording is in progress.
func (r *Controller) Active() bool { return r.State.Active || r.State.Starting }

// Elapsed is how long the recording has been running, without pauses.
func (r *Controller) Elapsed() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.elapsed
	if !r.paused && !r.resumed.IsZero() {
		d += time.Since(r.resumed)
	}
	return d
}

// EngineLabel names where transcription runs.
func EngineLabel(s store.SttSettings) string {
	if s.Provider == "soniox" {
		return locales.RecEngineSonioxCloud()
	}
	if s.Provider == OnDevice {
		return locales.RecEngineOnDevice()
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

// Usable says whether the chosen engine can transcribe now, and why not,
// on a device without an engine of its own; Controller.Usable knows the
// device's. Recording never needs it: without it a recording keeps only
// its audio.
func Usable(s store.SttSettings, online, signedIn bool) error {
	switch {
	case s.Provider == OnDevice:
		return errors.New(locales.RecErrOnDevice())
	case s.Provider == "openai-compatible" && IsLocalEndpoint(s.BaseURL):
		if s.BaseURL == "" {
			return errors.New(locales.RecErrSetUpEndpoint())
		}
		return nil
	case s.Provider == "soniox" && LocalSonioxKey() != "":
		if !online {
			return errors.New(locales.RecErrNeedsConnection())
		}
		return nil
	case !signedIn:
		return errors.New(locales.RecErrSignInOrServer())
	case !online:
		return errors.New(locales.RecErrOfflineServer())
	}
	return nil
}

// Usable is the package's Usable, on this device: its own engine
// transcribes offline, without an account.
func (r *Controller) Usable(s store.SttSettings, online bool) error {
	if e, finding := r.engineNow(); s.Provider == OnDevice && (e != nil || finding) {
		return nil
	}
	return Usable(s, online, r.be.SignedIn())
}

// model is the model a recording made with stt is transcribed by.
func model(stt store.SttSettings) string {
	switch stt.Provider {
	case "soniox":
		return stt.SonioxModel
	case OnDevice:
		return ""
	}
	return stt.Model
}

func (r *Controller) dir() (string, error) {
	d := filepath.Join(r.st.Dir, "recordings")
	return d, os.MkdirAll(d, 0o750)
}

// Start records into a block of a page. insert puts the recording block in
// the page; it runs on the main thread.
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
	return r.capture(wav, stt, transcribe, State{PageID: pageID, BlockID: blockID, ID: rec.ID})
}

// Test records d of audio into no page and transcribes it with stt, so
// Settings can try the microphone and the engine. done runs on the main
// thread with what was heard; the audio is not kept.
func (r *Controller) Test(stt store.SttSettings, online bool, d time.Duration, done func(text string, err error)) error {
	if r.Active() {
		return errors.New(locales.RecErrAlreadyRecording())
	}
	if r.input == nil {
		return errors.New(locales.RecErrWindowNotReady())
	}
	why := r.Usable(stt, online)
	// The server transcribes for a cloud endpoint only from a saved recording.
	if why == nil && stt.Provider == "openai-compatible" && !IsLocalEndpoint(stt.BaseURL) {
		why = errors.New(locales.RecErrCloudTest())
	}
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

// capture starts the microphone into wav; st says what is recording.
func (r *Controller) capture(wav *WavWriter, stt store.SttSettings, transcribe bool, st State) error {
	session := doc.NewBlockID()
	live := transcribe && stt.Provider == "soniox"
	r.mu.Lock()
	r.wav, r.session, r.stt, r.paused, r.elapsed, r.resumed = wav, session, stt, false, 0, time.Time{}
	// A stream still here is the last recording's, which its Stop is
	// finishing: it hears none of this one.
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
	// A test has no Stop button, so one whose microphone never starts ends.
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
	// Soniox streams as it records, and its final words are the transcript.
	if live {
		go r.startSoniox(session, stt)
	}
	// So does the device's engine, which needs no connection. One that
	// cannot start, or that the host is still looking for, transcribes the
	// saved audio once it stops.
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
		Chunk:   func(pcm []byte) { r.chunk(session, pcm) },
		Started: func() { r.started(session, testFor) },
		Failed:  func(msg string) { r.failed(session, msg) },
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

// failed ends a recording whose microphone could not start, unless it is
// another's: a page of a recording stopped since can answer late, while
// the next one starts.
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

// started is the microphone of the recording session running. A recording
// stopped or failed while it started stays so: the microphone answering
// late brings back no pill for it.
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

// startSoniox connects the live stream for the recording session.
func (r *Controller) startSoniox(session string, stt store.SttSettings) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A key on this device is used as is; otherwise the server makes a
	// short-lived one from the key the user saved there.
	key := LocalSonioxKey()
	var err error
	if key == "" {
		var res map[string]any
		res, err = r.be.Invoke(ctx, "stt", map[string]any{"action": "soniox_key"})
		key, _ = res["api_key"].(string)
	}
	if err == nil {
		var s *Soniox
		hints := stt.LanguageHints
		if _, ok := Languages[stt.Language]; ok {
			hints = []string{stt.Language}
		}
		s, err = DialSoniox(ctx, key, stt.SonioxModel, hints, stt.SpeakerLabels)
		if err == nil {
			// The words of a stream still finishing after the next
			// recording started are not that one's.
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
	}
	r.mu.Lock()
	same := r.session == session && r.connecting
	r.connecting, r.early, r.earlyLen = false, nil, 0
	r.mu.Unlock()
	if same {
		r.update(func() { r.State.Error = locales.RecErrLive(err.Error()) })
	}
}

// attachSoniox makes s the stream of the recording session, sending it
// first the audio recorded while it connected; a stream for a recording
// that has stopped or failed since is closed.
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
		// What is recorded while this goes waits for the next turn.
		for _, pcm := range early {
			s.Send(pcm)
		}
	}
}

// maxEarly is the most audio kept for a stream still connecting: 30 s at
// 16 kHz, longer than connecting may take.
const maxEarly = 30 * SampleRate * 2

func (r *Controller) chunk(session string, pcm []byte) {
	r.mu.Lock()
	if e, ok := r.ending[session]; ok {
		r.mu.Unlock()
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

// lateChunk takes audio a stopped recording's microphone handed on after
// Stop: the end of what was said, which goes to that recording's file and
// stream. The meter shows the recording in progress, so it does not move.
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

// MaxLevels is how many of the latest levels State keeps: 20 s.
const MaxLevels = 400

// TogglePause pauses or resumes the recording; one still starting has no
// time running to pause yet.
func (r *Controller) TogglePause() {
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
	switch {
	case m == nil:
	case paused:
		go m.Pause()
	default:
		go m.Resume()
	}
}

// abort ends a recording that could not start.
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
	}
	r.State.Active, r.State.Starting = false, false
}

// Stop ends the recording, saves the audio and writes the transcript.
func (r *Controller) Stop() {
	if !r.Active() {
		return
	}
	duration := r.Elapsed()
	r.mu.Lock()
	m, wav, stream, stt, withText := r.mic, r.wav, r.stream, r.stt, r.withText
	test, why, session := r.test, r.testWhy, r.session
	// The WAV file and the stream stay until the microphone has sent the
	// audio it still holds: that is the end of what was said, which goes
	// to them though the next recording starts meanwhile. A stream not
	// there yet is not waited for; the audio is transcribed instead.
	if r.ending == nil {
		r.ending = map[string]ending{}
	}
	r.ending[session] = ending{wav: wav, stream: stream, paused: r.paused}
	r.mic, r.wav, r.stream, r.test, r.testFor = nil, nil, nil, nil, 0
	r.connecting, r.early, r.earlyLen = false, nil, 0
	r.mu.Unlock()
	st := r.State
	r.State = State{}
	transcribes := test == nil && withText && st.ID != ""
	if transcribes {
		// Now, not once the transcription starts, so a Retranscribe
		// meanwhile refuses.
		r.Transcribing[st.ID] = true
		if stream != nil {
			r.Finishing[st.ID] = true
		}
	}
	go func() {
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
		if wav != nil {
			path = wav.Path
			if err := wav.Close(); err != nil {
				log.Printf("recording: %v", err)
			}
		}
		if test != nil {
			text, err := r.transcribeTest(path, stream, stt, why, st.Error)
			r.update(func() { test(text, err) })
			return
		}
		if !withText {
			// Audio only: it stays, whatever Keep the audio says, for a
			// transcript later.
			rec, err := r.st.UpdateRecording(st.ID, func(rec *store.Recording) {
				rec.Status, rec.DurationMs, rec.SttProvider, rec.SttModel = "ready", int(duration.Milliseconds()), "", ""
			})
			if err == nil && rec.AudioPath == "" && r.uploads() {
				if err := r.st.QueueUpload(rec); err != nil {
					log.Printf("recording: %v", err)
				}
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
			r.update(func() {
				delete(r.Transcribing, st.ID)
				delete(r.Finishing, st.ID)
			})
			return
		}
		r.finish(rec, path, stream, stt)
	}()
}

// transcribeTest transcribes a test recording and removes its audio.
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
		var audio []byte
		if audio, err = os.ReadFile(path); err == nil { // #nosec G304 -- the recording's own audio file.
			key, _ := keychain.Get("stt.endpoint.key")
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			segs, err = TranscribeEndpoint(ctx, stt.BaseURL, stt.Model, audio, string(key), stt.Language)
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

// Retranscribe transcribes a recording, again or for the first time,
// with the current settings; it says why when the engine cannot run now.
// Call it on the main thread, as Transcribing is the main thread's.
func (r *Controller) Retranscribe(id string, stt store.SttSettings, online bool) error {
	// One transcription at a time: the one ending last would write its
	// transcript and status over the other's.
	if r.Transcribing[id] {
		return errors.New(locales.RecErrTranscribing())
	}
	// Its end goes to the main thread, which Attach gives the way to.
	if r.update == nil {
		return errors.New(locales.RecErrWindowNotReady())
	}
	if err := r.Usable(stt, online); err != nil {
		return err
	}
	// Soniox transcribes saved audio through the server; with only a key
	// on this device it works while recording.
	if stt.Provider == "soniox" && !r.be.SignedIn() {
		return errors.New(locales.RecErrSonioxSaved())
	}
	rec, err := r.st.UpdateRecording(id, func(rec *store.Recording) {
		rec.Status, rec.Error, rec.SttProvider, rec.SttModel = "transcribing", "", stt.Provider, model(stt)
	})
	if err != nil {
		return err
	}
	// Set now, not once finish starts, so a second call refuses at once.
	r.Transcribing[id] = true
	go r.finish(rec, rec.LocalPath, nil, stt)
	return nil
}

func (r *Controller) finish(rec store.Recording, path string, stream Stream, stt store.SttSettings) {
	r.update(func() {
		r.Transcribing[rec.ID] = true
		if stream != nil {
			r.Finishing[rec.ID] = true
		}
	})
	defer r.update(func() {
		delete(r.Transcribing, rec.ID)
		delete(r.Finishing, rec.ID)
	})
	segs, err := r.transcribe(rec, path, stream, stt)
	var lost error
	if stream != nil {
		lost = stream.Lost()
	}
	updated, _ := r.st.UpdateRecording(rec.ID, func(x *store.Recording) {
		if err != nil {
			x.Status, x.Error = "failed", err.Error()
			return
		}
		// A stream that ended early heard only part of the recording: its
		// words stay, and so does the audio, to transcribe again.
		if lost != nil {
			x.Status, x.Error, x.Transcript = "failed", locales.RecErrLiveStopped(lost.Error()), segs
			return
		}
		x.Status, x.Error, x.Transcript = "ready", "", segs
	})
	// A recording keeps its audio, whatever transcribed it.
	if updated.AudioPath == "" && r.uploads() {
		if err := r.st.QueueUpload(updated); err != nil {
			log.Printf("recording: %v", err)
		}
	}
}

// storedAudio fetches the audio of a recording that is not on this device,
// recorded on another or removed here once it went up, for an engine on
// this device to transcribe.
func (r *Controller) storedAudio(ctx context.Context, rec store.Recording) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return r.Audio(ctx, rec)
}

// uploadAudio uploads a recording's audio, given as long as it takes over
// a slow connection (256 kbit/s) and two minutes more: storage transfers
// have no whole-request timeout of their own, so a stalled one ends here.
func (r *Controller) uploadAudio(path string, audio []byte) error {
	wait := 2*time.Minute + time.Duration(len(audio)/(32<<10))*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return r.be.Upload(ctx, "recordings", path, bytes.NewReader(audio), "audio/wav")
}

func (r *Controller) transcribe(rec store.Recording, path string, stream Stream, stt store.SttSettings) ([]store.Segment, error) {
	if stream != nil {
		return stream.Finish()
	}
	ctx := context.Background()
	readAudio := func() ([]byte, error) {
		if path == "" {
			return nil, errors.New(locales.RecErrAudioMissing())
		}
		return os.ReadFile(path) // #nosec G304 -- the recording's own audio file.
	}
	if stt.Provider == OnDevice {
		return r.transcribeOnDevice(ctx, rec, path, stt.Language)
	}
	if stt.Provider == "openai-compatible" && IsLocalEndpoint(stt.BaseURL) {
		audio, err := readAudio()
		if err != nil && rec.AudioPath != "" {
			audio, err = r.storedAudio(ctx, rec)
		}
		if err != nil {
			return nil, err
		}
		key, _ := keychain.Get("stt.endpoint.key")
		return TranscribeEndpoint(ctx, stt.BaseURL, stt.Model, audio, string(key), stt.Language)
	}
	if !r.be.SignedIn() {
		return nil, errors.New(locales.RecErrSignIn())
	}
	// The server reads the audio from Storage, so it goes up first.
	if rec.AudioPath == "" {
		audio, err := readAudio()
		if err != nil {
			return nil, err
		}
		up := r.be.UserID() + "/" + rec.ID + ".wav"
		if _, err := r.be.Upsert(ctx, "recordings", map[string]any{
			"id": rec.ID, "page_id": rec.PageID, "block_id": rec.BlockID, "status": "transcribing",
			"duration_ms": rec.DurationMs, "stt_provider": rec.SttProvider, "stt_model": rec.SttModel,
		}, ""); err != nil {
			return nil, err
		}
		if err := r.uploadAudio(up, audio); err != nil {
			return nil, err
		}
		if _, err := r.be.Upsert(ctx, "recordings", map[string]any{"id": rec.ID, "page_id": rec.PageID, "block_id": rec.BlockID, "audio_path": up}, ""); err != nil {
			return nil, err
		}
		if _, err := r.st.UpdateRecording(rec.ID, func(x *store.Recording) { x.AudioPath, x.UploadedAt = up, time.Now() }); err != nil {
			return nil, err
		}
	}
	body := map[string]any{"action": "transcribe", "recording_id": rec.ID}
	if stt.Language != "auto" && stt.Language != "" {
		body["language"] = stt.Language
	}
	// The function fetches the audio, up to 100 MB, and waits on the
	// provider for up to two minutes: past the other calls' timeout.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := r.be.InvokeLong(ctx, "stt", body)
	if err != nil {
		return nil, err
	}
	var segs []store.Segment
	list, _ := res["segments"].([]any)
	for _, s := range list {
		m, _ := s.(map[string]any)
		seg := store.Segment{StartMs: doc.AsInt(m["start_ms"], 0), EndMs: doc.AsInt(m["end_ms"], 0)}
		seg.Text, _ = m["text"].(string)
		if sp, ok := m["speaker"]; ok && sp != nil {
			seg.Speaker = fmt.Sprint(sp)
		}
		segs = append(segs, seg)
	}
	return segs, nil
}

// transcribeOnDevice transcribes a recording with the device's engine,
// from its file, or from a copy of its audio fetched from where it went up
// when the file is not on this device.
func (r *Controller) transcribeOnDevice(ctx context.Context, rec store.Recording, path, language string) ([]store.Segment, error) {
	e := r.waitEngine(ctx)
	if e == nil {
		return nil, errors.New(locales.RecErrOnDevice())
	}
	if _, err := os.Stat(path); path == "" || err != nil {
		audio, err := r.storedAudio(ctx, rec)
		if err != nil {
			return nil, err
		}
		if path, err = tempAudio(audio); err != nil {
			return nil, err
		}
		defer func() { _ = os.Remove(path) }()
	}
	return e.File(path, language)
}

// tempAudio writes audio to a file of its own in the system's temporary
// folder, readable only by the user, and returns its path.
func tempAudio(audio []byte) (string, error) {
	f, err := os.CreateTemp("", "leaflet-*.wav")
	if err != nil {
		return "", err
	}
	_, err = f.Write(audio)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Audio returns a recording's WAV audio: on this device, or from where it
// went up to.
func (r *Controller) Audio(ctx context.Context, rec store.Recording) ([]byte, error) {
	if rec.LocalPath != "" {
		if data, err := os.ReadFile(rec.LocalPath); err == nil {
			return data, nil
		}
	}
	if rec.AudioPath != "" && r.Blobs != nil && r.Blobs.CanFetch() {
		return r.Blobs.Fetch(ctx, "recordings", rec.AudioPath)
	}
	if rec.AudioPath != "" && r.Blobs == nil && r.be.SignedIn() {
		return r.be.Download(ctx, "recordings", rec.AudioPath)
	}
	return nil, errors.New(locales.RecErrAudioMissing())
}
