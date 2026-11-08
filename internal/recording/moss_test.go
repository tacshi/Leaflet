package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/keychain"
	"leaflet/internal/store"
	"leaflet/locales"
)

// fakeMoss is MOSI's API, as far as TranscribeMoss goes: a file goes up,
// a task transcribes it, asked twice, and the file is removed.
func fakeMoss(t *testing.T, audio []byte, task string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var removed []string
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mk" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/files":
			file, _, err := r.FormFile("file")
			if err != nil || r.ContentLength <= 0 || r.FormValue("purpose") != "audio" {
				http.Error(w, "bad upload", http.StatusBadRequest)
				return
			}
			if got, _ := io.ReadAll(file); audio != nil && !bytes.Equal(got, audio) {
				http.Error(w, "wrong audio", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"id":"file-1"}`)
		case r.Method == "POST" && r.URL.Path == "/audio/transcriptions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != mossModel || body["version"] != mossVersion || body["file_id"] != "file-1" ||
				body["diarize"] != true || body["async"] != true {
				http.Error(w, "wrong task", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"task_id":"task-1","status":"PENDING","retry_after":0}`)
		case r.Method == "GET" && r.URL.Path == "/audio/tasks/task-1":
			mu.Lock()
			polls++
			first := polls == 1
			mu.Unlock()
			if first {
				_, _ = io.WriteString(w, `{"status":"PROCESSING"}`)
				return
			}
			_, _ = io.WriteString(w, task)
		case r.Method == "DELETE":
			mu.Lock()
			removed = append(removed, r.URL.Path)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"deleted":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	api, poll := mossAPI, mossPoll
	mossAPI, mossPoll = srv.URL, time.Millisecond
	t.Cleanup(func() { mossAPI, mossPoll = api, poll })
	return srv, &removed
}

// Audio goes up as it is read, is transcribed with speakers, a line of
// only MOSS's tags is dropped, and the file goes from MOSI after.
func TestTranscribeMoss(t *testing.T) {
	audio := []byte("fake m4a audio")
	_, removed := fakeMoss(t, audio, `{"status":"SUCCESS","segments":[
		{"start":0.25,"end":1.5,"text":"你好。","speaker":"S01"},
		{"start":1.5,"end":1.75,"text":"[S02] [1.6]","speaker":"S02"},
		{"start":1.75,"end":4.25,"text":" 欢迎。 ","speaker":"S02"}]}`)
	src := struct{ io.Reader }{bytes.NewReader(audio)}
	segs, err := TranscribeMoss(context.Background(), "mk", src, int64(len(audio)), "audio.m4a", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Segment{
		{StartMs: 250, EndMs: 1500, Text: "你好。", Speaker: "S01"},
		{StartMs: 1750, EndMs: 4250, Text: "欢迎。", Speaker: "S02"},
	}
	if len(segs) != len(want) || segs[0] != want[0] || segs[1] != want[1] {
		t.Fatalf("segments %+v", segs)
	}
	if strings.Join(*removed, " ") != "/files/file-1" {
		t.Errorf("removed %v", *removed)
	}
	// Without speakers, the lines have none.
	segs, err = TranscribeMoss(context.Background(), "mk", bytes.NewReader(audio), int64(len(audio)), "audio.m4a", false)
	if err != nil || len(segs) != 2 || segs[0].Speaker != "" {
		t.Errorf("without speakers: %+v, %v", segs, err)
	}
}

// With no lines, the task's text is the transcript; a failed task says
// what MOSS said, and its file goes still.
func TestTranscribeMossTextAndFailure(t *testing.T) {
	fakeMoss(t, nil, `{"status":"SUCCESS","duration":2.75,"text":"Short message","segments":[]}`)
	segs, err := TranscribeMoss(context.Background(), "mk", strings.NewReader("x"), 1, "a.wav", true)
	if err != nil || len(segs) != 1 || segs[0].Text != "Short message" || segs[0].EndMs != 2750 {
		t.Fatalf("segments %+v, %v", segs, err)
	}

	_, removed := fakeMoss(t, nil, `{"status":"FAILED","error":{"error_code":16001,"error_msg":"billing error","internal_error_msg":"insufficient credits"}}`)
	_, err = TranscribeMoss(context.Background(), "mk", strings.NewReader("x"), 1, "a.wav", true)
	if err == nil || err.Error() != locales.RecErrMossFailed("billing error") {
		t.Fatalf("error %v", err)
	}
	if len(*removed) != 1 {
		t.Errorf("removed %v", *removed)
	}
}

// A key MOSI takes passes, whatever its models are called; one it refuses
// says so; and a file past MOSS's most goes nowhere.
func TestMossKeyAndLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer good":
			_, _ = io.WriteString(w, `{"data":[{"id":"moss-transcribe"}]}`)
		case "Bearer broke":
			w.WriteHeader(http.StatusPaymentRequired)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	api := mossAPI
	mossAPI = srv.URL
	defer func() { mossAPI = api }()
	ctx := context.Background()
	if err := TestMossKey(ctx, "good"); err != nil {
		t.Errorf("good key: %v", err)
	}
	if err := TestMossKey(ctx, "bad"); err == nil || err.Error() != locales.RecErrMossRejected() {
		t.Errorf("bad key: %v", err)
	}
	if err := TestMossKey(ctx, "broke"); err == nil || err.Error() != locales.RecErrMossBalance() {
		t.Errorf("no balance: %v", err)
	}
	if err := TestMossKey(ctx, " "); err == nil || err.Error() != locales.RecErrPasteKey() {
		t.Errorf("no key: %v", err)
	}
	if _, err := TranscribeMoss(ctx, "good", strings.NewReader(""), mossMaxBytes+1, "a.wav", false); err == nil || err.Error() != locales.RecErrMossTooLarge() {
		t.Errorf("too large: %v", err)
	}
}

// MOSS needs a key on this device, and a connection; with them, an audio
// file is transcribed from here, signed out, and the settings stay off
// the server.
func TestTranscribeFileWithMoss(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	stt := store.SttSettings{Provider: Moss, SpeakerLabels: true}
	if err := Usable(stt, true, true); err == nil || err.Error() != locales.RecErrSetUpMoss() {
		t.Errorf("no key: %v", err)
	}
	if err := SetMossKey(" mk "); err != nil {
		t.Fatal(err)
	}
	if err := Usable(stt, false, true); err == nil || err.Error() != locales.RecErrNeedsConnection() {
		t.Errorf("offline: %v", err)
	}
	if onServer(stt) {
		t.Error("MOSS transcribes on the server")
	}
	fakeMoss(t, []byte("audio"), `{"status":"SUCCESS","segments":[{"start":0,"end":1,"text":"Hello.","speaker":"S01"}]}`)

	r, st, _ := newTestController(t)
	src := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []store.Segment, 1)
	if err := r.TranscribeFile(f, "b", stt, true, func(segs []store.Segment, err error) {
		if err != nil {
			t.Error(err)
		}
		got <- segs
	}); err != nil {
		t.Fatal(err)
	}
	if segs := waitTranscript(t, got); len(segs) != 1 || segs[0].Text != "Hello." || segs[0].Speaker != "S01" {
		t.Errorf("segments %+v", segs)
	}
}
