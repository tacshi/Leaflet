package recording

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
