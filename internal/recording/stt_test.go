package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// An endpoint gets the audio as it is read, in a form whose length is
// known before, with the model and the language.
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
	// A reader of its own, not bytes, whose length the request can't see.
	wav := struct{ io.Reader }{bytes.NewReader(audio)}
	segs, err := TranscribeEndpoint(context.Background(), srv.URL, "whisper", wav, int64(len(audio)), "", "ja")
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Text != "hello" || segs[0].EndMs != 1000 {
		t.Fatalf("segments %+v", segs)
	}
}

// Closing a stream returns at once, though its server never answers the
// close, as over a slow network, and the stream's goroutines end with it.
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
	// Finish after Close, as a recording that failed as it stopped may
	// call both, returns at once too.
	start = time.Now()
	_, _ = s.Finish()
	if took := time.Since(start); took > time.Second {
		t.Errorf("Finish after Close took %v", took)
	}
}

// A compressed file's size says little of how long it lasts: its wait
// goes by the least bit rate of speech, up to a most.
func TestFileWait(t *testing.T) {
	hour := int64(3600 * 4000)
	if got := fileWait(hour); got != 5*time.Minute+2*time.Hour {
		t.Errorf("an hour at the least rate waits %v", got)
	}
	if got := fileWait(1 << 40); got != 6*time.Hour {
		t.Errorf("a huge file waits %v", got)
	}
}

// Saved audio goes to Soniox from this device, with its key: it goes up
// as it is read, is transcribed by an async model with the hints given,
// and both the file and the transcription are removed once it is done.
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
			if body.Model != "stt-async-v3" || body.FileID != "f1" || len(body.Hints) != 1 || body.Hints[0] != "zh" || !body.Speakers {
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
	segs, err := TranscribeSoniox(context.Background(), "sk-here", "stt-rt-v3", src, int64(len(audio)), "audio.m4a", []string{"zh"}, true)
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

// A transcription Soniox fails says so, and still leaves nothing there.
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
