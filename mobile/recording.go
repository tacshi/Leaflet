package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/guard"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

type Microphone interface {
	Start(session, rate int, sink *AudioSink) error
	Pause(session int)
	Resume(session int)
	Stop(session int)
	Close(session int)
}

type AudioSink struct{ s recording.Sink }

func (a *AudioSink) Chunk(pcm []byte) {
	defer catch(nil)
	a.s.Chunk(append([]byte(nil), pcm...))
}

func (a *AudioSink) Started() {
	defer catch(nil)
	a.s.Started()
}

func (a *AudioSink) Failed(message string) {
	defer catch(nil)
	a.s.Failed(message)
}

func (a *AudioSink) Stalled(message string) {
	defer catch(nil)
	a.s.Stalled(message)
}

func (a *AudioSink) Recovered() {
	defer catch(nil)
	if a.s.Recovered != nil {
		a.s.Recovered()
	}
}

type capture struct{ m Microphone }

var micSessions atomic.Int64

func (c capture) Start(rate int, sink recording.Sink) (recording.Mic, error) {
	session := int(micSessions.Add(1))
	if err := c.m.Start(session, rate, &AudioSink{s: sink}); err != nil {
		return nil, err
	}
	return mic{m: c.m, session: session}, nil
}

type mic struct {
	m       Microphone
	session int
}

func (m mic) Pause()  { m.m.Pause(m.session) }
func (m mic) Resume() { m.m.Resume(m.session) }
func (m mic) Stop()   { m.m.Stop(m.session) }
func (m mic) Close()  { m.m.Close(m.session) }

type Transcriber interface {
	Start(id, language string, sink *TranscriptSink) error
	Send(id string, pcm []byte)
	Finish(id string) (string, error)
	Close(id string)
	File(path, language string) (string, error)
}

type TranscriptSink struct{ text func(string) }

func (s *TranscriptSink) Text(text string) {
	defer catch(nil)
	s.text(text)
}

type engine struct{ t Transcriber }

func (e engine) Stream(language string, text func(string)) (recording.Stream, error) {
	id := doc.NewRowID()
	if err := e.t.Start(id, language, &TranscriptSink{text: text}); err != nil {
		return nil, err
	}
	return &stream{t: e.t, id: id, language: language}, nil
}

func (e engine) File(path, language string) ([]store.Segment, error) {
	lasts := anyLength
	if fi, err := os.Stat(path); err == nil && strings.EqualFold(filepath.Ext(path), ".wav") {
		lasts = audioLasts(fi.Size() - 44)
	}
	raw, err := giveUpAfter(lasts, func() (string, error) { return e.t.File(path, language) })
	if err != nil {
		return nil, err
	}
	return recording.OnDeviceTranscript(raw, language)
}

var onDeviceSlack = 4 * time.Minute

const anyLength = 6 * time.Hour

func audioLasts(size int64) time.Duration {
	return time.Duration(max(size, 0)/2) * time.Second / recording.SampleRate
}

func giveUpAfter(d time.Duration, fn func() (string, error)) (string, error) {
	type answer struct {
		raw string
		err error
	}
	done := make(chan answer, 1)
	guard.Go(func() {
		raw, err := fn()
		done <- answer{raw, err}
	})
	timer := time.NewTimer(d + onDeviceSlack)
	defer timer.Stop()
	select {
	case a := <-done:
		return a.raw, a.err
	case <-timer.C:
		return "", errStuck
	}
}

var errStuck error = stuck{}

type stuck struct{}

func (stuck) Error() string { return locales.RecErrOnDeviceStuck() }

type stream struct {
	t        Transcriber
	id       string
	language string
	sent     atomic.Int64
}

func (s *stream) Send(pcm []byte) {
	s.sent.Add(int64(len(pcm)))
	s.t.Send(s.id, pcm)
}

func (s *stream) Finish() ([]store.Segment, error) {
	raw, err := giveUpAfter(audioLasts(s.sent.Load()), func() (string, error) { return s.t.Finish(s.id) })
	if err != nil {
		if errors.Is(err, errStuck) {
			s.t.Close(s.id)
		}
		return nil, err
	}
	return recording.OnDeviceTranscript(raw, s.language)
}

func (s *stream) Close() { s.t.Close(s.id) }

func (s *stream) Lost() error { return nil }

func (c *Core) UseTranscriber(t Transcriber) {
	defer catch(nil)
	c.q.do(func() {
		c.core.Rec.SetEngine(engine{t})
		s := c.core.Store.Settings()
		if !recording.PreferOnDevice(&s.Stt) {
			return
		}
		if err := c.core.Store.SaveSettings(s); err != nil {
			log.Printf("settings: %v", err)
		}
	})
}

func (c *Core) UseMicrophone(m Microphone) {
	defer catch(nil)
	c.q.do(func() {
		c.core.Rec.Attach(capture{m}, func(fn func()) {
			c.q.post(func() {
				fn()
				c.tellRecording()
			})
		})
	})
}

type recordingState struct {
	Active            bool      `json:"active"`
	Starting          bool      `json:"starting"`
	Paused            bool      `json:"paused"`
	PageID            string    `json:"page_id"`
	BlockID           string    `json:"block_id"`
	ID                string    `json:"id"`
	Levels            []float64 `json:"levels"`
	LiveText          string    `json:"live_text"`
	Engine            string    `json:"engine"`
	Error             string    `json:"error"`
	ElapsedMs         int64     `json:"elapsed_ms"`
	Transcribing      []string  `json:"transcribing"`
	TranscribingFiles []string  `json:"transcribing_files"`
	Finishing         []string  `json:"finishing"`
	Testing           bool      `json:"testing"`
	Heard             string    `json:"heard"`
}

const levelsSent = 240

func (c *Core) recState() recordingState {
	r := c.core.Rec
	st := r.State
	out := recordingState{
		Active: st.Active, Starting: st.Starting, Paused: st.Paused, PageID: st.PageID, BlockID: st.BlockID,
		ID: st.ID, Levels: st.Levels, LiveText: st.LiveText, Engine: st.Engine, Error: st.Error,
		ElapsedMs: r.Elapsed().Milliseconds(), Transcribing: []string{}, TranscribingFiles: []string{}, Finishing: []string{},
		Testing: c.micTesting, Heard: c.micHeard,
	}
	if n := len(out.Levels); n > levelsSent {
		out.Levels = out.Levels[n-levelsSent:]
	}
	if out.Levels == nil {
		out.Levels = []float64{}
	}
	for id := range r.TranscribingFiles {
		out.TranscribingFiles = append(out.TranscribingFiles, id)
	}
	for id := range r.Transcribing {
		out.Transcribing = append(out.Transcribing, id)
	}
	for id := range r.Finishing {
		out.Finishing = append(out.Finishing, id)
	}
	return out
}

func (c *Core) tellRecording() {
	c.settleVoiceNote()
	st := c.recState()
	levels := st.Levels
	st.Levels = []float64{}
	elapsed := st.ElapsedMs
	st.ElapsedMs = 0
	same, _ := encode(st)
	if same != c.recSent {
		c.recSent = same
		st.ElapsedMs = elapsed
		raw, _ := encode(st)
		c.ev.recordingChanged(raw)
	}
	if !slices.Equal(levels, c.levelsSent) {
		c.levelsSent = slices.Clone(levels)
		raw, _ := encode(levels)
		c.ev.recordingLevels(raw)
	}
}

func (c *Core) RecordingState() (_ string, err error) {
	defer catch(&err)
	var st recordingState
	c.q.do(func() { st = c.recState() })
	return encode(st)
}

func (c *Core) online(s store.Settings) bool { return c.plat.Online() && !s.WorkOffline }

func (c *Core) StartRecording(pageID, provider string) (_ string, err error) {
	defer catch(&err)
	var node *doc.Node
	keys := c.speechKeys()
	c.q.do(func() {
		if c.pages[pageID] == nil {
			err = errNotOpen
			return
		}
		s := c.core.Store.Settings()
		err = c.core.Rec.Start(pageID, c.sttFor(pageID, provider, keys), c.online(s), func(n *doc.Node) { node = n })
		c.tellRecording()
	})
	if err != nil {
		return "", err
	}
	return encode(node)
}

type voiceNote struct {
	page  store.NewPageOptions
	recID string
	block *doc.Node
}

func (c *Core) RecordNewPage(folderID string) (_ string, err error) {
	defer catch(&err)
	keys := c.speechKeys()
	id := doc.NewRowID()
	c.q.do(func() {
		var block *doc.Node
		s := c.core.Store.Settings()
		err = c.core.Rec.Start(id, c.sttFor(id, "", keys), c.online(s), func(n *doc.Node) { block = n })
		if err != nil {
			if c.core.Rec.State.PageID == id {
				c.dropVoiceNote(id)
			}
			return
		}
		c.voiceNote = &voiceNote{
			page:  store.NewPageOptions{ID: id, FolderID: folderID, Title: locales.RecBlockTitle(time.Now())},
			recID: c.core.Rec.State.ID, block: block,
		}
		c.tellRecording()
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (c *Core) settleVoiceNote() {
	v := c.voiceNote
	st := c.core.Rec.State
	if v == nil || st.ID == v.recID && st.Starting {
		return
	}
	c.voiceNote = nil
	if st.ID == v.recID && !st.Active {
		c.dropVoiceNote(v.page.ID)
		return
	}
	content := doc.Empty()
	if err := content.Insert(doc.Position{At: "end"}, v.block); err != nil {
		log.Printf("page %s: recording block: %v", v.page.ID, err)
	}
	v.page.Content = content
	if _, err := c.core.Store.CreatePage(v.page); err != nil {
		c.ev.notice(locales.RecCouldNotSave(err.Error()))
	}
}

func (c *Core) dropVoiceNote(id string) {
	if err := c.core.Store.RemovePages([]string{id}); err != nil {
		log.Printf("page %s: drop a recording that never began: %v", id, err)
	}
}

func (c *Core) StopRecording() {
	defer catch(nil)
	c.q.do(func() {
		c.core.Rec.Stop()
		c.tellRecording()
	})
}

func (c *Core) TogglePauseRecording() {
	defer catch(nil)
	c.q.do(func() {
		c.core.Rec.TogglePause()
		c.tellRecording()
	})
}

type recordingRow struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	DurationMs  int             `json:"duration_ms"`
	SttProvider string          `json:"stt_provider"`
	Error       string          `json:"error"`
	Transcript  []store.Segment `json:"transcript"`
	Plain       string          `json:"plain"`
	Label       string          `json:"label"`
	CreatedMs   int64           `json:"created_ms"`
	PageTitle   string          `json:"page_title"`
	Folded      int             `json:"folded"`
}

func (c *Core) Recording(id string) (_ string, err error) {
	defer catch(&err)
	var rec store.Recording
	var page store.Page
	c.q.do(func() {
		if rec, err = c.core.Store.Recording(id); err == nil {
			page, _ = c.core.Store.Page(rec.PageID)
		}
	})
	if err != nil {
		return "", err
	}
	row := recordingRow{
		ID: rec.ID, Status: rec.Status, DurationMs: rec.DurationMs, SttProvider: rec.SttProvider,
		Error: rec.Error, Transcript: recording.Tidy(rec.Transcript), Label: recording.Status(rec),
		PageTitle: page.Title,
	}
	if !rec.CreatedAt.IsZero() {
		row.CreatedMs = rec.CreatedAt.UnixMilli()
	}
	if row.Transcript == nil {
		row.Transcript = []store.Segment{}
	}
	if doc.FoldsTranscript(len(row.Transcript)) {
		row.Folded = doc.FoldedLines
	}
	row.Plain = recording.TranscriptText(rec)
	return encode(row)
}

func RecordingTitle(title string, createdMs int64, pageTitle string) string {
	defer catch(nil)
	return recording.BlockTitle(title, time.UnixMilli(createdMs), createdMs != 0, pageTitle)
}

func (c *Core) RecordingAudio(id string, token int64) (_ string, err error) {
	defer catch(&err)
	var rec store.Recording
	c.q.do(func() { rec, err = c.core.Store.Recording(id) })
	if err != nil {
		return "", err
	}
	if rec.LocalPath != "" {
		if _, err := os.Stat(rec.LocalPath); err == nil {
			return rec.LocalPath, nil
		}
	}
	ctx, done := c.cancellable(token, recording.DownloadWait(recording.AudioSize(rec.DurationMs)))
	defer done()
	audio, err := c.core.Rec.OpenAudio(ctx, rec)
	if err != nil {
		return "", err
	}
	defer func() { _ = audio.Close() }()
	return c.core.Store.DownloadedAudioFrom(id, audio)
}

type transcriptFile struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

func (c *Core) RecordingTranscriptFile(id, title, format string) (_ string, err error) {
	defer catch(&err)
	var rec store.Recording
	c.q.do(func() { rec, err = c.core.Store.Recording(id) })
	if err != nil {
		return "", err
	}
	return encode(transcriptFile{Name: export.SafeName(title) + "." + format, Text: recording.TranscriptFile(rec.Transcript, format)})
}

func RecordingAudioName(title string) string {
	defer catch(nil)
	return export.SafeName(title) + ".wav"
}

func (c *Core) Retranscribe(id, provider string) (err error) {
	defer catch(&err)
	keys := c.speechKeys()
	c.q.do(func() {
		s := c.core.Store.Settings()
		rec, rerr := c.core.Store.Recording(id)
		if rerr != nil {
			err = rerr
			return
		}
		err = c.core.Rec.Retranscribe(id, c.sttFor(rec.PageID, provider, keys), c.online(s))
		c.tellRecording()
	})
	return err
}

func (c *Core) TranscribeFile(pageID, blockID, provider string) (err error) {
	defer catch(&err)
	keys := c.speechKeys()
	c.q.do(func() {
		s := c.core.Store.Settings()
		err = c.pageCore().TranscribeFile(pageID, blockID, c.sttFor(pageID, provider, keys), c.online(s))
		c.tellRecording()
	})
	return err
}

type speechChoices struct {
	Choices []recording.SpeechProvider `json:"choices"`
	Current string                     `json:"current"`
}

func (c *Core) SpeechChoices(pageID string) (_ string, err error) {
	defer catch(&err)
	keys := c.speechKeys()
	var out speechChoices
	c.q.do(func() {
		stt := c.core.Store.Settings().Stt
		if out.Choices = recording.Choices(stt, keys); out.Choices == nil {
			out.Choices = []recording.SpeechProvider{}
		}
		out.Current = c.core.Rec.Choice(pageID, stt, keys)
	})
	return encode(out)
}

func (c *Core) speechKeys() recording.Keys {
	var onDevice bool
	c.q.do(func() { onDevice = c.core.Rec.Engine != nil })
	return recording.KeysOf(recording.LocalSonioxKey() != "", recording.MossKey() != "", onDevice)
}

func (c *Core) sttFor(pageID, provider string, keys recording.Keys) store.SttSettings {
	s := c.core.Store.Settings()
	stt, last := c.core.Rec.For(pageID, provider, s.Stt, keys)
	if last {
		s.Stt.Provider = provider
		if err := c.core.Store.SaveSettings(s); err != nil {
			log.Printf("speech settings: %v", err)
		}
	}
	return stt
}

func (c *Core) DismissRecordingError(id string) (err error) {
	defer catch(&err)
	c.q.do(func() { err = c.core.Rec.DismissError(id) })
	return err
}

type transcript struct {
	Blocks []*doc.Node `json:"blocks"`
}

func (c *Core) TranscriptBlocks(id string) (_ string, err error) {
	defer catch(&err)
	var rec store.Recording
	c.q.do(func() { rec, err = c.core.Store.Recording(id) })
	if err != nil {
		return "", err
	}
	return encode(transcript{Blocks: recording.TranscriptBlocks(rec)})
}

type speechLanguage struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type speechSettings struct {
	Stt         store.SttSettings          `json:"stt"`
	EndpointKey string                     `json:"endpoint_key"`
	SonioxSaved bool                       `json:"soniox_saved"`
	MossSaved   bool                       `json:"moss_saved"`
	OnDevice    bool                       `json:"on_device"`
	Status      string                     `json:"status"`
	Providers   []recording.SpeechProvider `json:"providers"`
	Addable     []recording.SpeechProvider `json:"addable"`
	Languages   []speechLanguage           `json:"languages"`
	KeepAudio   string                     `json:"keep_audio"`
}

func (c *Core) SpeechSettings() (_ string, err error) {
	defer catch(&err)
	var out speechSettings
	c.q.do(func() {
		s := c.core.Store.Settings()
		out.Stt, out.KeepAudio = s.Stt, s.KeepAudioOnDevice
		out.OnDevice = c.core.Rec.Engine != nil
	})
	out.EndpointKey = recording.EndpointKey(out.Stt.BaseURL)
	out.SonioxSaved = recording.LocalSonioxKey() != ""
	out.MossSaved = recording.MossKey() != ""
	keys := recording.KeysOf(out.SonioxSaved, out.MossSaved, out.OnDevice)
	out.Status = recording.Summary(out.Stt, keys)
	out.Providers, out.Addable = recording.SpeechProviders(out.Stt, keys)
	if out.Providers == nil {
		out.Providers = []recording.SpeechProvider{}
	}
	if out.Addable == nil {
		out.Addable = []recording.SpeechProvider{}
	}
	for _, code := range recording.LanguageCodes() {
		out.Languages = append(out.Languages, speechLanguage{Code: code, Name: recording.LanguageName(code)})
	}
	return encode(out)
}

func (c *Core) SetKeepAudio(keep string) (err error) {
	defer catch(&err)
	if !slices.Contains([]string{"until_uploaded", "30_days", "always"}, keep) {
		return fmt.Errorf("mobile: no way to keep audio %q", keep)
	}
	c.q.do(func() {
		s := c.core.Store.Settings()
		s.KeepAudioOnDevice = keep
		err = c.core.Store.SaveSettings(s)
	})
	return err
}

func (c *Core) SaveSpeechSettings(stt string) (err error) {
	defer catch(&err)
	var next store.SttSettings
	if err := json.Unmarshal([]byte(stt), &next); err != nil {
		return err
	}
	return c.saveStt(func(stt *store.SttSettings) error {
		*stt = next
		return nil
	})
}

func (c *Core) saveStt(change func(*store.SttSettings) error) (err error) {
	c.q.do(func() {
		s := c.core.Store.Settings()
		if err = change(&s.Stt); err != nil {
			return
		}
		keys := recording.KeysOf(recording.LocalSonioxKey() != "", recording.MossKey() != "", c.core.Rec.Engine != nil)
		recording.PreferSetUp(&s.Stt, keys)
		err = c.core.Store.SaveSettings(s)
	})
	return err
}

func (c *Core) RemoveSpeechProvider(id string) (err error) {
	defer catch(&err)
	return c.saveStt(func(stt *store.SttSettings) error {
		keys := recording.KeysOf(recording.LocalSonioxKey() != "", recording.MossKey() != "", c.core.Rec.Engine != nil)
		_, err := recording.RemoveProvider(id, stt, keys)
		return err
	})
}

func (c *Core) SetEndpointKey(url, key string) (err error) {
	defer catch(&err)
	return recording.SetEndpointKey(url, key)
}

func (c *Core) SaveSonioxKey(key string) (_ string, err error) {
	defer catch(&err)
	key = strings.TrimSpace(key)
	if key == "" {
		return "", nil
	}
	if err := recording.SaveSonioxKey(key); err != nil {
		return "", err
	}
	if err := c.saveStt(func(*store.SttSettings) error { return nil }); err != nil {
		return "", err
	}
	return locales.RecSttSavedOnDevice(), nil
}

func (c *Core) SaveMossKey(key string) (_ string, err error) {
	defer catch(&err)
	if key = strings.TrimSpace(key); key == "" {
		return "", nil
	}
	if err := recording.SetMossKey(key); err != nil {
		return "", errors.New(locales.RecSttKeyNotSaved(err.Error()))
	}
	if err := c.saveStt(func(*store.SttSettings) error { return nil }); err != nil {
		return "", err
	}
	return locales.RecSttSavedOnDevice(), nil
}

func (c *Core) TestMoss(key string) (_ string, err error) {
	defer catch(&err)
	if key = strings.TrimSpace(key); key == "" {
		key = recording.MossKey()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := recording.TestMossKey(ctx, key); err != nil {
		return "", err
	}
	return locales.RecSttItWorks(), nil
}

func (c *Core) TestEndpoint(url, model, key string) (_ string, err error) {
	defer catch(&err)
	if key == "" {
		key = recording.EndpointKey(url)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := recording.TestEndpoint(ctx, url, model, key); err != nil {
		return "", err
	}
	return locales.RecSttItWorks(), nil
}

func (c *Core) TestSoniox(key string) (_ string, err error) {
	defer catch(&err)
	if key = strings.TrimSpace(key); key == "" {
		key = recording.LocalSonioxKey()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := recording.TestSonioxKey(ctx, key); err != nil {
		return "", err
	}
	return locales.RecSttItWorks(), nil
}

func (c *Core) TestMicrophone() (err error) {
	defer catch(&err)
	c.q.do(func() {
		s := c.core.Store.Settings()
		c.micTesting, c.micHeard = true, ""
		err = c.core.Rec.TestMicrophone(s.Stt, c.online(s), func(heard string) {
			c.micTesting, c.micHeard = false, heard
		})
		if err != nil {
			c.micTesting = false
		}
		c.tellRecording()
	})
	return err
}
