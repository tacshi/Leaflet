package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"leaflet/locales"
)

func TestTranscribeEndpointStreamsTheAudio(t *testing.T) {
	audio := WavFromPCM(bytes.Repeat([]byte{1, 2}, SampleRate))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength <= 0 || len(r.TransferEncoding) > 0 {
			http.Error(w, "no length", http.StatusLengthRequired)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got, _ := io.ReadAll(file)
		if !bytes.Equal(got, audio) || r.FormValue("model") != "whisper" || r.FormValue("language") != "ja" {
			http.Error(w, "wrong form", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"text":" hello ","duration":1}`)
	}))
	defer srv.Close()
	wav := struct{ io.Reader }{bytes.NewReader(audio)}
	segs, err := TranscribeEndpoint(context.Background(), srv.URL, "whisper", wav, int64(len(audio)), "", "ja")
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Text != "hello" || segs[0].EndMs != 1000 {
		t.Fatalf("segments %+v", segs)
	}
}

func TestSonioxCloseDoesNotWait(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		<-release
	}))
	defer srv.Close()
	defer close(release)
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newSoniox(conn)
	s.Send([]byte{1, 2})
	start := time.Now()
	s.Close()
	if took := time.Since(start); took > time.Second {
		t.Errorf("Close took %v", took)
	}
	for name, ended := range map[string]chan struct{}{"reading": s.done, "writing": s.written} {
		select {
		case <-ended:
		case <-time.After(2 * time.Second):
			t.Errorf("the stream's %s goes on after Close", name)
		}
	}
	if err := s.Lost(); err != nil {
		t.Errorf("a stream closed here was lost: %v", err)
	}
	start = time.Now()
	_, _ = s.Finish()
	if took := time.Since(start); took > time.Second {
		t.Errorf("Finish after Close took %v", took)
	}
}

func TestFileWait(t *testing.T) {
	hour := int64(3600 * 4000)
	if got := fileWait(hour); got != 5*time.Minute+2*time.Hour {
		t.Errorf("an hour at the least rate waits %v", got)
	}
	if got := fileWait(1 << 40); got != 6*time.Hour {
		t.Errorf("a huge file waits %v", got)
	}
}

func TestTranscribeSonioxFromHere(t *testing.T) {
	audio := []byte("fake m4a audio")
	var removed []string
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-here" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/files":
			file, head, err := r.FormFile("file")
			if err != nil || r.ContentLength <= 0 || head.Filename != "audio.m4a" {
				http.Error(w, "bad upload", http.StatusBadRequest)
				return
			}
			if got, _ := io.ReadAll(file); !bytes.Equal(got, audio) {
				http.Error(w, "wrong audio", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"id":"f1"}`)
		case r.Method == "POST" && r.URL.Path == "/transcriptions":
			var body struct {
				Model    string   `json:"model"`
				FileID   string   `json:"file_id"`
				Hints    []string `json:"language_hints"`
				Speakers bool     `json:"enable_speaker_diarization"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Model != "stt-async-v6" || body.FileID != "f1" || len(body.Hints) != 1 || body.Hints[0] != "zh" || !body.Speakers {
				http.Error(w, "wrong transcription", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"id":"t1"}`)
		case r.Method == "GET" && r.URL.Path == "/transcriptions/t1":
			polls++
			if polls < 2 {
				_, _ = io.WriteString(w, `{"status":"processing"}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":"completed"}`)
		case r.Method == "GET" && r.URL.Path == "/transcriptions/t1/transcript":
			_, _ = io.WriteString(w, `{"tokens":[{"text":"你好","start_ms":0,"end_ms":500,"speaker":"1"},{"text":"。","start_ms":500,"end_ms":520}]}`)
		case r.Method == "DELETE":
			removed = append(removed, r.URL.Path)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	api, poll := sonioxAPI, sonioxPoll
	sonioxAPI, sonioxPoll = srv.URL, time.Millisecond
	defer func() { sonioxAPI, sonioxPoll = api, poll }()

	src := struct{ io.Reader }{bytes.NewReader(audio)}
	segs, err := TranscribeSoniox(context.Background(), "sk-here", "stt-rt-v6", src, int64(len(audio)), "audio.m4a", []string{"zh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Text != "你好。" || segs[0].Speaker != "1" || segs[0].EndMs != 500 {
		t.Fatalf("segments %+v", segs)
	}
	if strings.Join(removed, " ") != "/transcriptions/t1 /files/f1" {
		t.Errorf("removed %v", removed)
	}
}

func TestTranscribeSonioxFails(t *testing.T) {
	var removed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files" && r.Method == "POST":
			_, _ = io.WriteString(w, `{"id":"f1"}`)
		case r.URL.Path == "/transcriptions" && r.Method == "POST":
			_, _ = io.WriteString(w, `{"id":"t1"}`)
		case r.Method == "GET":
			_, _ = io.WriteString(w, `{"status":"error","error_message":"unsupported audio"}`)
		case r.Method == "DELETE":
			removed = append(removed, r.URL.Path)
		}
	}))
	defer srv.Close()
	api := sonioxAPI
	sonioxAPI = srv.URL
	defer func() { sonioxAPI = api }()
	_, err := TranscribeSoniox(context.Background(), "k", "", strings.NewReader("x"), 1, "a.mp3", nil, false)
	if err == nil || !strings.Contains(err.Error(), "unsupported audio") {
		t.Fatalf("error %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %v", removed)
	}
}

func TestEndpointAnswerHasALimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"text":"`+strings.Repeat("a", 4096)+`"}`)
	}))
	defer srv.Close()
	limit := maxAnswer
	maxAnswer = 1024
	defer func() { maxAnswer = limit }()
	audio := WavFromPCM(make([]byte, 64))
	segs, err := TranscribeEndpoint(t.Context(), srv.URL, "whisper", bytes.NewReader(audio), int64(len(audio)), "", "auto")
	if err == nil || err.Error() != locales.RecErrAnswerTooLarge() {
		t.Fatalf("segments %+v, error %v", segs, err)
	}
	maxAnswer = 8192
	if segs, err = TranscribeEndpoint(t.Context(), srv.URL, "whisper", bytes.NewReader(audio), int64(len(audio)), "", "auto"); err != nil || len(segs) != 1 {
		t.Errorf("within the limit: segments %+v, error %v", segs, err)
	}
}

func TestStartingAJobHasADeadline(t *testing.T) {
	stuck := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/files":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, `{"id":"f1"}`)
		case r.Method == "POST":
			select {
			case <-stuck:
			case <-r.Context().Done():
			}
		}
	}))
	defer srv.Close()
	defer close(stuck)
	soniox, moss, wait := sonioxAPI, mossAPI, jobWait
	sonioxAPI, mossAPI, jobWait = srv.URL, srv.URL, 50*time.Millisecond
	defer func() { sonioxAPI, mossAPI, jobWait = soniox, moss, wait }()
	audio := []byte("audio")
	for name, transcribe := range map[string]func() error{
		"soniox": func() error {
			_, err := TranscribeSoniox(context.Background(), "sk", "", bytes.NewReader(audio), int64(len(audio)), "a.wav", nil, false)
			return err
		},
		"moss": func() error {
			_, err := TranscribeMoss(context.Background(), "mk", bytes.NewReader(audio), int64(len(audio)), "a.wav", false)
			return err
		},
	} {
		done := make(chan error, 1)
		go func() { done <- transcribe() }()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s still waits for a job that never starts", name)
		}
	}
}

func TestSonioxLostAfterTheEndOfAudio(t *testing.T) {
	was := finishWait
	finishWait = 2 * time.Second
	t.Cleanup(func() { finishWait = was })
	for name, last := range map[string]string{"dropped": "", "refused": `{"error_message":"out of credit"}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c, err := websocket.Accept(w, req, nil)
			if err != nil {
				return
			}
			ctx := context.Background()
			for {
				kind, data, err := c.Read(ctx)
				if err != nil {
					return
				}
				if kind == websocket.MessageBinary && len(data) > 0 {
					_ = c.Write(ctx, websocket.MessageText, []byte(`{"tokens":[{"text":"hello","start_ms":0,"end_ms":500,"is_final":true}]}`))
				}
				if kind == websocket.MessageText && len(data) == 0 {
					if last != "" {
						_ = c.Write(ctx, websocket.MessageText, []byte(last))
					}
					_ = c.CloseNow()
					return
				}
			}
		}))
		conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		s := newSoniox(conn)
		s.Send([]byte{1, 2})
		for range 200 {
			s.mu.Lock()
			heard := len(s.final) > 0
			s.mu.Unlock()
			if heard {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		segs, _ := s.Finish()
		if len(segs) != 1 || s.Lost() == nil {
			t.Errorf("%s: segments %+v, lost %v", name, segs, s.Lost())
		}
		srv.Close()
	}
}

func TestPolledAsksAgainAfterAPassingFailure(t *testing.T) {
	calls := 0
	err := polled(context.Background(), time.Millisecond, func() error {
		if calls++; calls < 3 {
			return transientError{errors.New("dropped")}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("%d calls, %v", calls, err)
	}
	calls = 0
	if err := polled(context.Background(), time.Millisecond, func() error { calls++; return errors.New("rejected") }); err == nil || calls != 1 {
		t.Errorf("a lasting failure: %d calls, %v", calls, err)
	}
	calls = 0
	if err := polled(context.Background(), time.Millisecond, func() error { calls++; return transientError{errors.New("down")} }); err == nil || calls != pollRetries+1 {
		t.Errorf("a service that stays down: %d calls, %v", calls, err)
	}
}
