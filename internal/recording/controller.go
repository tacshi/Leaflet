// Package recording records audio into recording blocks and transcribes
// it: capture through a page of the app's own (the system's web view hands
// it the microphone, so no native code is needed), WAV files, Soniox
// real-time streaming, and OpenAI-compatible /audio/transcriptions.
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

	"github.com/egoist/mygo"

	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// Controller is the recording in progress, and the transcriptions running.
type Controller struct {
	st     *store.Store
	be     *remote.Client
	win    *mygo.Window
	update func(func())

	// The recording in progress, read and written on the main thread.
	State State

	mu      sync.Mutex
	wav     *WavWriter
	soniox  *Soniox
	view    *mygo.WebView
	session string
	paused  bool
	elapsed time.Duration
	resumed time.Time
	stt     store.SttSettings
	meter   Meter
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

// State is what the views show of the recording in progress.
type State struct {
	Active     bool
	Starting   bool
	Paused     bool
	PageID     string
	BlockID    string
	ID         string
	Levels     []float64
	LiveText   string
	Engine     string
	Error      string
	StartedAt  time.Time
	elapsedFix time.Duration
}

// NewController returns the recorder.
func NewController(st *store.Store, be *remote.Client) *Controller {
	return &Controller{st: st, be: be, Transcribing: map[string]bool{}, Finishing: map[string]bool{}}
}

// Attach gives the recorder its window, and a way to change the view's
// state from other goroutines.
func (r *Controller) Attach(win *mygo.Window, update func(func())) {
	r.win, r.update = win, update
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

// View is the web view capturing audio, which the window must show (it
// can be tiny) while recording.
func (r *Controller) View() *mygo.WebView {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.view
}

// EngineLabel names where transcription runs.
func EngineLabel(s store.SttSettings) string {
	if s.Provider == "soniox" {
		return "Soniox · cloud"
	}
	name := s.Model
	if name == "" {
		name = "Transcription server"
	}
	if IsLocalEndpoint(s.BaseURL) {
		return name + " · your server"
	}
	return name + " · cloud"
}

// Usable says whether the chosen engine can transcribe now, and why not.
// Recording never needs it: without it a recording keeps only its audio.
func Usable(s store.SttSettings, online, signedIn bool) error {
	switch {
	case s.Provider == "openai-compatible" && IsLocalEndpoint(s.BaseURL):
		if s.BaseURL == "" {
			return errors.New("Set up a transcription endpoint in Settings › Speech to text.")
		}
		return nil
	case s.Provider == "soniox" && LocalSonioxKey() != "":
		if !online {
			return errors.New("Transcription needs a connection.")
		}
		return nil
	case !signedIn:
		return errors.New("Sign in to transcribe in the cloud, or set a transcription server on your network in Settings › Speech to text.")
	case !online:
		return errors.New("Transcription needs a connection. Set a server on your network in Settings › Speech to text to record offline.")
	}
	return nil
}

func (r *Controller) dir() string {
	d := filepath.Join(r.st.Dir, "recordings")
	os.MkdirAll(d, 0o755)
	return d
}

// Start records into a block of a page. insert puts the recording block in
// the page; it runs on the main thread.
func (r *Controller) Start(pageID string, stt store.SttSettings, online bool, insert func(n *doc.Node)) error {
	if r.Active() {
		return errors.New("Already recording.")
	}
	transcribe := Usable(stt, online, r.be.SignedIn()) == nil
	if r.win == nil {
		return errors.New("The window is not ready.")
	}
	id := doc.NewRowID()
	blockID := doc.NewBlockID()
	path := filepath.Join(r.dir(), id+".wav")
	wav, err := CreateWav(path)
	if err != nil {
		return err
	}
	model := stt.Model
	if stt.Provider == "soniox" {
		model = stt.SonioxModel
	}
	rec, err := r.st.CreateRecording(pageID, blockID, stt.Provider, model, path, stt.Language)
	if err != nil {
		wav.Close()
		return err
	}
	insert(&doc.Node{ID: blockID, Type: "recording", Data: map[string]any{
		"recording_id": rec.ID, "title": "Recording · " + time.Now().Format("Jan 2"), "duration_ms": 0,
	}})
	return r.capture(wav, stt, transcribe, State{PageID: pageID, BlockID: blockID, ID: rec.ID})
}

// Test records d of audio into no page and transcribes it with stt, so
// Settings can try the microphone and the engine. done runs on the main
// thread with what was heard; the audio is not kept.
func (r *Controller) Test(stt store.SttSettings, online bool, d time.Duration, done func(text string, err error)) error {
	if r.Active() {
		return errors.New("Already recording.")
	}
	if r.win == nil {
		return errors.New("The window is not ready.")
	}
	why := Usable(stt, online, r.be.SignedIn())
	// The server transcribes for a cloud endpoint only from a saved recording.
	if why == nil && stt.Provider != "soniox" && !IsLocalEndpoint(stt.BaseURL) {
		why = errors.New("A cloud endpoint transcribes saved recordings, so record on a page to try it.")
	}
	wav, err := CreateWav(filepath.Join(os.TempDir(), "leaflet-test-"+doc.NewRowID()+".wav"))
	if err != nil {
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
	r.mu.Lock()
	r.wav, r.session, r.stt, r.paused, r.elapsed, r.resumed = wav, session, stt, false, 0, time.Time{}
	r.meter = Meter{}
	r.withText = transcribe
	testFor := r.testFor
	r.mu.Unlock()
	st.Starting, st.Engine = true, EngineLabel(stt)
	if !transcribe {
		st.Engine = "Audio only"
	}
	r.State = st
	service.add(session, &captureSink{
		chunk: r.chunk,
		started: func() {
			r.mu.Lock()
			r.resumed = time.Now()
			r.mu.Unlock()
			r.update(func() {
				r.State.Starting, r.State.Active, r.State.StartedAt = false, true, time.Now()
			})
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
		},
		failed: func(msg string) {
			r.update(func() {
				r.State.Error = "The microphone did not start: " + msg
				r.abort()
			})
		},
	})
	// A test has no Stop button, so one whose microphone never starts ends.
	if testFor > 0 {
		time.AfterFunc(15*time.Second, func() {
			r.update(func() {
				r.mu.Lock()
				same := r.session == session
				r.mu.Unlock()
				if same && r.State.Starting {
					r.State.Error = "The microphone did not start."
					r.abort()
				}
			})
		})
	}
	// Soniox streams as it records, and its final words are the transcript.
	if transcribe && stt.Provider == "soniox" {
		go r.startSoniox(stt)
	}
	v, err := r.win.NewWebView(mygo.WebViewOptions{URL: CaptureURL(session)})
	if err != nil {
		r.abort()
		return err
	}
	r.mu.Lock()
	r.view = v
	r.mu.Unlock()
	return nil
}

func (r *Controller) startSoniox(stt store.SttSettings) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A key on this device is used as is; otherwise the server lends one.
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
			s.OnText = func(text string) { r.update(func() { r.State.LiveText = text }) }
			r.mu.Lock()
			r.soniox = s
			r.mu.Unlock()
			return
		}
	}
	r.update(func() { r.State.Error = "Live transcript unavailable: " + err.Error() })
}

func (r *Controller) chunk(pcm []byte) {
	r.mu.Lock()
	if r.paused || r.wav == nil {
		r.mu.Unlock()
		return
	}
	r.wav.Write(pcm)
	s := r.soniox
	r.mu.Unlock()
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

// MaxLevels is how many of the latest levels State keeps: 20 s.
const MaxLevels = 400

// TogglePause pauses or resumes the recording.
func (r *Controller) TogglePause() {
	r.mu.Lock()
	if r.paused {
		r.paused, r.resumed = false, time.Now()
	} else {
		r.elapsed += time.Since(r.resumed)
		r.paused = true
	}
	paused := r.paused
	v := r.view
	r.mu.Unlock()
	r.State.Paused = paused
	if v != nil {
		go v.Page().Eval(fmt.Sprintf("window.pauseCapture(%v)", paused))
	}
}

// abort ends a recording that could not start.
func (r *Controller) abort() {
	r.mu.Lock()
	v, wav, session, test := r.view, r.wav, r.session, r.test
	r.view, r.wav, r.test, r.testFor = nil, nil, nil, 0
	r.mu.Unlock()
	service.remove(session)
	if v != nil {
		v.Destroy()
	}
	if wav != nil {
		wav.Close()
		if test != nil {
			os.Remove(wav.Path)
		}
	}
	if test != nil {
		msg := r.State.Error
		if msg == "" {
			msg = "The microphone did not start."
		}
		test("", errors.New(msg))
	}
	if r.State.ID != "" {
		msg := r.State.Error
		r.st.UpdateRecording(r.State.ID, func(rec *store.Recording) { rec.Status, rec.Error = "failed", msg })
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
	v, wav, soniox, session, stt, withText := r.view, r.wav, r.soniox, r.session, r.stt, r.withText
	test, why := r.test, r.testWhy
	r.view, r.wav, r.soniox, r.test, r.testFor = nil, nil, nil, nil, 0
	r.mu.Unlock()
	st := r.State
	r.State = State{}
	go func() {
		if v != nil {
			v.Page().Eval("return window.stopCapture()")
			time.Sleep(200 * time.Millisecond)
		}
		service.remove(session)
		r.update(func() {
			if v != nil {
				v.Destroy()
			}
		})
		var path string
		if wav != nil {
			path = wav.Path
			wav.Close()
		}
		if test != nil {
			text, err := r.transcribeTest(path, soniox, stt, why, st.Error)
			r.update(func() { test(text, err) })
			return
		}
		if !withText {
			// Audio only: it stays, whatever Keep the audio says, for a
			// transcript later.
			rec, err := r.st.UpdateRecording(st.ID, func(rec *store.Recording) {
				rec.Status, rec.DurationMs, rec.SttProvider, rec.SttModel = "ready", int(duration.Milliseconds()), "", ""
			})
			if err == nil && rec.AudioPath == "" && r.be.SignedIn() {
				r.st.QueueUpload(rec)
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
			return
		}
		r.finish(rec, path, soniox, stt)
	}()
}

// transcribeTest transcribes a test recording and removes its audio.
func (r *Controller) transcribeTest(path string, soniox *Soniox, stt store.SttSettings, why error, liveErr string) (string, error) {
	defer os.Remove(path)
	if why != nil {
		return "", errors.New("The microphone works. " + why.Error())
	}
	var segs []store.Segment
	var err error
	switch {
	case soniox != nil:
		segs, err = soniox.Finish()
	case stt.Provider == "soniox":
		if liveErr == "" {
			liveErr = "Soniox did not connect in time."
		}
		err = errors.New(liveErr)
	default:
		var audio []byte
		if audio, err = os.ReadFile(path); err == nil {
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
		return "", errors.New("No words were heard. Check the microphone, and speak during the test.")
	}
	return strings.Join(words, " "), nil
}

// Retranscribe transcribes a recording, again or for the first time,
// with the current settings; it says why when the engine cannot run now.
func (r *Controller) Retranscribe(id string, stt store.SttSettings, online bool) error {
	if err := Usable(stt, online, r.be.SignedIn()); err != nil {
		return err
	}
	// Soniox transcribes saved audio through the server; with only a key
	// on this device it works while recording.
	if stt.Provider == "soniox" && !r.be.SignedIn() {
		return errors.New("Soniox transcribes as you record here; saved audio needs a Leaflet server.")
	}
	model := stt.Model
	if stt.Provider == "soniox" {
		model = stt.SonioxModel
	}
	rec, err := r.st.UpdateRecording(id, func(rec *store.Recording) {
		rec.Status, rec.Error, rec.SttProvider, rec.SttModel = "transcribing", "", stt.Provider, model
	})
	if err != nil {
		return err
	}
	go r.finish(rec, rec.LocalPath, nil, stt)
	return nil
}

func (r *Controller) finish(rec store.Recording, path string, soniox *Soniox, stt store.SttSettings) {
	r.update(func() {
		r.Transcribing[rec.ID] = true
		if soniox != nil {
			r.Finishing[rec.ID] = true
		}
	})
	defer r.update(func() {
		delete(r.Transcribing, rec.ID)
		delete(r.Finishing, rec.ID)
	})
	segs, err := r.transcribe(rec, path, soniox, stt)
	updated, _ := r.st.UpdateRecording(rec.ID, func(x *store.Recording) {
		if err != nil {
			x.Status, x.Error = "failed", err.Error()
			return
		}
		x.Status, x.Error, x.Transcript = "ready", "", segs
	})
	if stt.KeepAudio {
		if updated.AudioPath == "" && r.be.SignedIn() {
			r.st.QueueUpload(updated)
		}
	} else if path != "" && updated.Status == "ready" {
		os.Remove(path)
		r.st.ClearLocalPath(rec.ID)
	}
}

func (r *Controller) transcribe(rec store.Recording, path string, soniox *Soniox, stt store.SttSettings) ([]store.Segment, error) {
	if soniox != nil {
		return soniox.Finish()
	}
	ctx := context.Background()
	readAudio := func() ([]byte, error) {
		if path == "" {
			return nil, errors.New("The audio is not on this device.")
		}
		return os.ReadFile(path)
	}
	if stt.Provider == "openai-compatible" && IsLocalEndpoint(stt.BaseURL) {
		audio, err := readAudio()
		if err != nil {
			return nil, err
		}
		key, _ := keychain.Get("stt.endpoint.key")
		return TranscribeEndpoint(ctx, stt.BaseURL, stt.Model, audio, string(key), stt.Language)
	}
	if !r.be.SignedIn() {
		return nil, errors.New("Sign in to transcribe in the cloud.")
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
		if err := r.be.Upload(ctx, "recordings", up, bytes.NewReader(audio), "audio/wav"); err != nil {
			return nil, err
		}
		r.be.Upsert(ctx, "recordings", map[string]any{"id": rec.ID, "page_id": rec.PageID, "block_id": rec.BlockID, "audio_path": up}, "")
		r.st.UpdateRecording(rec.ID, func(x *store.Recording) { x.AudioPath, x.UploadedAt = up, time.Now() })
	}
	body := map[string]any{"action": "transcribe", "recording_id": rec.ID}
	if stt.Language != "auto" && stt.Language != "" {
		body["language"] = stt.Language
	}
	res, err := r.be.Invoke(ctx, "stt", body)
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

// Audio returns a recording's WAV audio: on this device, or from Storage.
func (r *Controller) Audio(ctx context.Context, rec store.Recording) ([]byte, error) {
	if rec.LocalPath != "" {
		if data, err := os.ReadFile(rec.LocalPath); err == nil {
			return data, nil
		}
	}
	if rec.AudioPath != "" && r.be.SignedIn() {
		return r.be.Download(ctx, "recordings", rec.AudioPath)
	}
	return nil, errors.New("The audio is not on this device.")
}
