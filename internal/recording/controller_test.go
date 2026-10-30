package recording

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Audio recorded while the stream connects reaches it first, in order; a
// stream that connects for a recording gone since is closed, not used.
func TestSonioxGetsTheOpeningAudio(t *testing.T) {
	heard := make(chan []byte, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			heard <- data
		}
	}))
	defer srv.Close()
	dial := func() *Soniox {
		conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return &Soniox{conn: conn, done: make(chan struct{})}
	}

	wav, err := CreateWav(filepath.Join(t.TempDir(), "a.wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wav.Close() }()
	r := &Controller{update: func(fn func()) { fn() }, wav: wav, session: "s1", connecting: true}
	r.chunk("s1", []byte{1, 1})
	r.chunk("s1", []byte{2, 2})
	r.chunk("old", []byte{9, 9})
	s := dial()
	r.attachSoniox("s1", s)
	if r.soniox != s || r.connecting || len(r.early) != 0 {
		t.Fatalf("not attached: %v %v %d", r.soniox == s, r.connecting, len(r.early))
	}
	r.chunk("s1", []byte{3, 3})
	for _, want := range [][]byte{{1, 1}, {2, 2}, {3, 3}} {
		select {
		case got := <-heard:
			if !bytes.Equal(got, want) {
				t.Fatalf("heard %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("never heard %v", want)
		}
	}

	// The next recording started before this stream connected.
	r.session, r.soniox, r.connecting = "s2", nil, true
	late := dial()
	r.attachSoniox("s1", late)
	if r.soniox != nil || !r.connecting {
		t.Error("a stream for the last recording took this one")
	}
}

// Pause before the microphone starts changes nothing: there is no time
// running yet.
func TestPauseWhileStarting(t *testing.T) {
	r := &Controller{}
	r.TogglePause()
	if r.State.Paused || r.Elapsed() != 0 {
		t.Errorf("paused %v, elapsed %v", r.State.Paused, r.Elapsed())
	}
}

// A stream that hears no audio, as while a recording is paused, is kept
// open with keepalives; one that ends before it is finished says why, and
// one ended on purpose does not.
func TestSonioxKeepsAliveAndTellsALostStream(t *testing.T) {
	was := keepAliveAfter
	keepAliveAfter = 50 * time.Millisecond
	t.Cleanup(func() { keepAliveAfter = was })
	texts := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		for {
			kind, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			if kind == websocket.MessageText {
				texts <- string(data)
				if req.URL.Query().Get("drop") != "" {
					_ = c.Close(websocket.StatusPolicyViolation, "timeout")
					return
				}
			}
		}
	}))
	defer srv.Close()
	dial := func(query string) *Soniox {
		conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		return newSoniox(conn)
	}

	s := dial("")
	select {
	case got := <-texts:
		if got != `{"type":"keepalive"}` {
			t.Errorf("sent %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no keepalive")
	}
	s.Close()
	<-s.done
	if err := s.Lost(); err != nil {
		t.Errorf("a stream closed here was lost: %v", err)
	}

	s = dial("?drop=1")
	<-s.done
	if s.Lost() == nil {
		t.Error("a stream the server ended was not lost")
	}
}

// Chunks that come in out of order are written in order, each once; one
// that never comes is given up on.
func TestChunksGoInOrder(t *testing.T) {
	var got []byte
	s := &captureSink{chunk: func(pcm []byte) { got = append(got, pcm...) }}
	for _, seq := range []int{1, 0, 3, 2, 2, 0} {
		s.deliver(seq, []byte{byte(seq)})
	}
	if !bytes.Equal(got, []byte{0, 1, 2, 3}) {
		t.Fatalf("wrote %v", got)
	}
	// 4 never comes.
	got = nil
	for seq := 5; seq <= 5+maxHeld; seq++ {
		s.deliver(seq, []byte{byte(seq)})
	}
	if len(got) != maxHeld+1 || got[0] != 5 {
		t.Fatalf("wrote %v", got)
	}
}

// A microphone that answers after the recording stopped while starting
// brings back no recording; one that answers in time starts it.
func TestStartedAfterStopIsIgnored(t *testing.T) {
	r := &Controller{update: func(fn func()) { fn() }, session: "s1"}
	// Stop clears the state and keeps the session until the page is gone.
	r.State = State{}
	r.started("s1", 0)
	if r.State.Active || r.Active() {
		t.Errorf("stopped recording came back: %+v", r.State)
	}
	r.State = State{Starting: true}
	r.started("old", 0)
	if r.State.Active || !r.State.Starting {
		t.Errorf("another session's microphone started this one: %+v", r.State)
	}
	r.started("s1", 0)
	if !r.State.Active || r.State.Starting || r.resumed.IsZero() {
		t.Errorf("not started: %+v", r.State)
	}
}

// No audio goes after the end of it that Finish sends.
func TestSonioxSendsNothingAfterFinish(t *testing.T) {
	heard := make(chan []byte, 10)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		for ended := false; ; {
			wait := time.Minute
			if ended {
				wait = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			_, data, err := c.Read(ctx)
			cancel()
			if err != nil {
				_ = c.Close(websocket.StatusNormalClosure, "")
				return
			}
			heard <- data
			if len(data) == 0 && !ended {
				ended = true
				<-release
			}
		}
	}))
	defer srv.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newSoniox(conn)
	s.Send([]byte{1})
	finished := make(chan struct{})
	go func() {
		_, _ = s.Finish()
		close(finished)
	}()
	for _, want := range [][]byte{{1}, {}} {
		if got := <-heard; !bytes.Equal(got, want) {
			t.Fatalf("heard %v, want %v", got, want)
		}
	}
	s.Send([]byte{7})
	close(release)
	<-finished
	select {
	case got := <-heard:
		t.Errorf("heard %v after the end", got)
	default:
	}
}
