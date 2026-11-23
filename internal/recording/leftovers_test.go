package recording

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/keychain"
	"leaflet/internal/store"
	"leaflet/locales"
)

func noted(st *store.Store) []string {
	return slices.Sorted(maps.Keys(st.ValuesWithPrefix(leftoverPrefix)))
}

func until(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !done(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never %s", what)
		}
	}
}

func TestFileTranscriptionHoldsTheQuitAndDeletesItsUploadWhenCutShort(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	if err := SetLocalSonioxKey("sk-here"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var removed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == "DELETE":
			mu.Lock()
			removed = append(removed, req.URL.Path)
			mu.Unlock()
		case req.URL.Path == "/files":
			_, _ = io.WriteString(w, `{"id":"f1"}`)
		case req.URL.Path == "/transcriptions":
			_, _ = io.WriteString(w, `{"id":"t1"}`)
		default:
			_, _ = io.WriteString(w, `{"status":"processing"}`)
		}
	}))
	defer srv.Close()
	api, poll := sonioxAPI, sonioxPoll
	sonioxAPI, sonioxPoll = srv.URL, 5*time.Millisecond
	defer func() { sonioxAPI, sonioxPoll = api, poll }()

	r, st, _ := newTestController(t)
	src := filepath.Join(t.TempDir(), "Board meeting.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	if err := r.TranscribeFile(f, "b", store.SttSettings{Provider: "soniox"}, true, func(_ []store.Segment, err error) { done <- err }); err != nil {
		t.Fatal(err)
	}
	until(t, "noted the upload and the job", func() bool { return len(noted(st)) == 2 })
	if r.Saving() != 1 || r.Settle(50*time.Millisecond) {
		t.Fatalf("a quit would not wait for the file being transcribed: %d saving", r.Saving())
	}
	r.CutShort()
	if !r.Settle(5 * time.Second) {
		t.Fatal("the transcription cut short goes on")
	}
	if err := <-done; err == nil || err.Error() != locales.RecErrQuit() {
		t.Errorf("cut short: %v", err)
	}
	mu.Lock()
	got := strings.Join(removed, " ")
	mu.Unlock()
	if got != "/transcriptions/t1 /files/f1" {
		t.Errorf("deleted %q", got)
	}
	if left := noted(st); len(left) != 0 {
		t.Errorf("still noted: %q", left)
	}
}

func TestWhatADeleteCouldNotReachStaysNoted(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == "DELETE":
			w.WriteHeader(http.StatusServiceUnavailable)
		case req.URL.Path == "/files":
			_, _ = io.WriteString(w, `{"id":"file-1"}`)
		case req.URL.Path == "/audio/transcriptions":
			_, _ = io.WriteString(w, `{"task_id":"task-1"}`)
		default:
			_, _ = io.WriteString(w, `{"status":"SUCCESS","text":"Hello."}`)
		}
	}))
	defer srv.Close()
	api := mossAPI
	mossAPI = srv.URL
	defer func() { mossAPI = api }()
	segs, err := TranscribeMoss(context.Background(), "mk", strings.NewReader("x"), 1, "a.wav", false, Leftovers{st})
	if err != nil || len(segs) != 1 {
		t.Fatalf("segments %+v, %v", segs, err)
	}
	if left := noted(st); !slices.Equal(left, []string{"moss:/files/file-1"}) {
		t.Errorf("noted %q", left)
	}
}

func TestLeftoversAreDeletedAtTheNextStart(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	if err := SetLocalSonioxKey("sk-here"); err != nil {
		t.Fatal(err)
	}
	if err := SetMossKey("mk"); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "DELETE" {
			http.NotFound(w, req)
			return
		}
		deleted <- req.Header.Get("Authorization") + " " + req.URL.Path
		switch req.URL.Path {
		case "/files/busy":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/transcriptions/running":
			w.WriteHeader(http.StatusConflict)
		case "/files/gone":
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	soniox, moss := sonioxAPI, mossAPI
	sonioxAPI, mossAPI = srv.URL, srv.URL+"/moss"
	defer func() { sonioxAPI, mossAPI = soniox, moss }()

	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// What the transcriptions a quit cut short had noted.
	for _, name := range []string{"soniox:/files/f1", "soniox:/transcriptions/t1", "soniox:/files/busy",
		"soniox:/transcriptions/running", "soniox:/files/gone", "moss:/files/m1"} {
		if err := st.SetValue("recording.leftover."+name, "1"); err != nil {
			t.Fatal(err)
		}
	}
	NewController(st, nil).RecoverInterrupted()
	var got []string
	for range 6 {
		select {
		case d := <-deleted:
			got = append(got, d)
		case <-time.After(5 * time.Second):
			t.Fatalf("deleted only %q", got)
		}
	}
	if job, audio := slices.Index(got, "Bearer sk-here /transcriptions/t1"), slices.Index(got, "Bearer sk-here /files/f1"); job > audio {
		t.Errorf("the audio went before the job that reads it: %q", got)
	}
	slices.Sort(got)
	want := []string{"Bearer mk /moss/files/m1", "Bearer sk-here /files/busy", "Bearer sk-here /files/f1", "Bearer sk-here /files/gone",
		"Bearer sk-here /transcriptions/running", "Bearer sk-here /transcriptions/t1"}
	if !slices.Equal(got, want) {
		t.Errorf("deleted %q, want %q", got, want)
	}
	until(t, "dropped the notes of what was deleted", func() bool {
		return slices.Equal(noted(st), []string{"soniox:/files/busy", "soniox:/transcriptions/running"})
	})
}

// waitingEngine transcribes a file on the device until it is told to stop.
type waitingEngine struct {
	fakeEngine
	started chan struct{}
}

func (e waitingEngine) FileContext(ctx context.Context, _, _ string) ([]store.Segment, error) {
	close(e.started)
	<-ctx.Done()
	return nil, errors.New("the helper was ended")
}

func TestCutShortEndsATranscriptionOnTheDevice(t *testing.T) {
	r, st, _ := newTestController(t)
	e := waitingEngine{started: make(chan struct{})}
	r.SetEngine(e)
	path := filepath.Join(t.TempDir(), "r.wav")
	if err := os.WriteFile(path, WavFromPCM(make([]byte, 2*SampleRate)), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := st.CreateRecording("p", "b", OnDevice, "", path, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Retranscribe(rec.ID, store.SttSettings{Provider: OnDevice}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the device was never asked to stop with the transcription")
	}
	r.CutShort()
	if !r.Settle(5 * time.Second) {
		t.Fatal("the transcription cut short goes on")
	}
	if got, err := st.Recording(rec.ID); err != nil || got.Status != "failed" || got.Error != locales.RecErrQuit() {
		t.Errorf("recording %+v, %v", got, err)
	}
	if _, marked := st.Value(transcribingKey(rec.ID)); marked {
		t.Error("a recording cut short is still marked as transcribing here")
	}
}
