package recording

import (
	"bytes"
	"context"
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
