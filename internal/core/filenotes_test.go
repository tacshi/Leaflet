package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// wordsEngine is a device's engine that hears the same words in every
// file, after meanwhile runs, as what happens while it transcribes.
type wordsEngine struct{ meanwhile func() }

func (wordsEngine) Stream(string, func(string)) (recording.Stream, error) {
	return nil, errors.New("no stream")
}

func (e wordsEngine) File(string, string) ([]store.Segment, error) {
	if e.meanwhile != nil {
		e.meanwhile()
	}
	return []store.Segment{{StartMs: 5000, Text: "We ship on Friday."}}, nil
}

// audioPage makes a page with an audio file of its own in file block a.
func (f *pagesFixture) audioPage(t *testing.T, title string) (store.Page, store.File) {
	t.Helper()
	p := f.page(t, title, "")
	src := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := f.st.AddFile(src, "", p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	f.fileIn(t, p.ID, file.ID)
	return p, file
}

// fileIn makes page id's content file block a, of file fileID. It may run
// as a transcript is made, off the test's goroutine.
func (f *pagesFixture) fileIn(t *testing.T, id, fileID string) {
	t.Helper()
	n := &doc.Node{ID: "a", Type: "file", Data: map[string]any{"name": "call.m4a", "file_id": fileID}}
	if _, _, err := f.st.SaveContent(id, doc.FromBlocks([]*doc.Node{n})); err != nil {
		t.Error(err)
	}
}

// transcribe transcribes file block a of page id on the device's engine,
// with meanwhile run as it does, and waits for the transcript to be done
// with.
func (f *pagesFixture) transcribe(t *testing.T, id string, meanwhile func()) error {
	t.Helper()
	done := make(chan struct{}, 1)
	f.Rec = recording.NewController(f.st, nil)
	f.Rec.Attach(nil, func(fn func()) { fn(); done <- struct{}{} })
	f.Rec.SetEngine(wordsEngine{meanwhile})
	if err := f.TranscribeFile(id, "a", store.SttSettings{Provider: recording.OnDevice}, false); err != nil {
		return err
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the transcript never came")
	}
	return nil
}

// A page's own audio file is transcribed into its notes; a file of
// another page a block names is not read at all, nor are its words put in
// this page.
func TestTranscribeFileOnlyOfThePage(t *testing.T) {
	f := newPagesFixture(t)
	p, file := f.audioPage(t, "Meeting")
	if err := f.transcribe(t, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.st.Content(p.ID).Find("a").Str("notes"); got != "0:05 We ship on Friday." {
		t.Errorf("notes %q", got)
	}
	if !slices.Equal(f.told, []string{locales.RecFileTranscribed(file.Name)}) {
		t.Errorf("told %q", f.told)
	}

	shared := f.page(t, "Shared", "")
	f.fileIn(t, shared.ID, file.ID)
	read := false
	err := f.transcribe(t, shared.ID, func() { read = true })
	if err == nil || err.Error() != locales.RecErrFileElsewhere() || read {
		t.Errorf("another page's file: %v, read %v", err, read)
	}
	if got := f.st.Content(shared.ID).Find("a").Str("notes"); got != "" {
		t.Errorf("its words went in: %q", got)
	}
}

// A transcript is said to be done only once it is stored: one whose block
// went meanwhile says only that, and one that could not be stored says
// why.
func TestTranscribeFileToldOnlyWhenSaved(t *testing.T) {
	f := newPagesFixture(t)
	p, file := f.audioPage(t, "Meeting")
	f.told = nil
	if err := f.transcribe(t, p.ID, func() { f.fileIn(t, p.ID, "another") }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.told, []string{locales.RecFileBlockGone()}) {
		t.Errorf("block gone: told %q", f.told)
	}

	f.fileIn(t, p.ID, file.ID)
	f.told = nil
	if err := f.transcribe(t, p.ID, func() { f.fail(t) }); err != nil {
		t.Fatal(err)
	}
	if len(f.told) != 1 || slices.Contains(f.told, locales.RecFileTranscribed(file.Name)) {
		t.Errorf("not saved: told %q", f.told)
	}
}
