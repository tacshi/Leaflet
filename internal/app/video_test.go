package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A video's notes show under it, folded when long; the menu writes them,
// as one step to undo, and copies a link to a line's moment.
func TestVideoNotes(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	var notes []string
	for i := range doc.FoldedLines + 3 {
		notes = append(notes, doc.Clock(i*90)+" Part "+string(rune('A'+i)))
	}
	n := &doc.Node{ID: "v1", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "title": "Talk",
		"notes": "A talk on caching, see 2:05.\n" + strings.Join(notes, "\n")}}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Videos", Content: doc.FromBlocks([]*doc.Node{n, {ID: "p1", Type: "paragraph"}})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ed := a.page.editor
	tt := ui.NewTester(func(c *ui.Context) {
		if n := ed.Content.Find("v1"); n != nil {
			a.videoNotes(c, n, true)
		}
		a.dialogs.view(c, a)
	}, 700, 600)
	tt.Frame()
	for _, want := range []string{"A talk on caching, see ", "2:05", "0:00", "Part A", "1:30", "Part B", locales.RecShowAll(doc.FoldedLines + 4)} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("Part I") {
		t.Errorf("long notes show whole: %q", tt.Texts())
	}
	if err := tt.Click(locales.RecShowAll(doc.FoldedLines + 4)); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Part I") {
		t.Errorf("opened, the notes show %q", tt.Texts())
	}

	if err := tt.RightClick("Part B"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.VideoCopyMoment("1:30")); err != nil {
		t.Fatalf("%v; shown %q", err, tt.Texts())
	}
	tt.Frame()
	if got := tt.Clipboard(); got != doc.MomentLink(p.ID, "v1", 90) {
		t.Errorf("clipboard %q", got)
	}

	if err := tt.Click(locales.VideoEditNotes()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.dialogs.notesOpen || a.dialogs.notesBlock != "v1" {
		t.Fatalf("the dialog is not open on the block: %+v", a.dialogs.notesOpen)
	}
	a.dialogs.notesText = "  0:00 Intro\r\n1:23 Caches\n"
	tt.Frame()
	if err := tt.Click(locales.CommonSave()); err != nil {
		t.Fatal(err)
	}
	if got := ed.Content.Find("v1").Str("notes"); got != "0:00 Intro\n1:23 Caches" || a.dialogs.notesOpen {
		t.Fatalf("notes %q, dialog open %v", got, a.dialogs.notesOpen)
	}
	ed.Undo()
	if got := ed.Content.Find("v1").Str("notes"); !strings.HasPrefix(got, "A talk on caching") {
		t.Fatalf("after undo %q", got)
	}
	// Notes left empty go.
	a.setVideoNotes("v1", "")
	if _, ok := ed.Content.Find("v1").Data["notes"]; ok {
		t.Error("empty notes stay")
	}
}

// A link to a moment of a recording on another page opens that page at the
// recording and plays it from that time.
func TestOpenMomentLink(t *testing.T) {
	a := templatesApp(t)
	a.post = func(func()) {}
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 90000, SttProvider: "soniox", Status: "ready"}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": rec.ID}}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Meeting", Content: doc.FromBlocks([]*doc.Node{{ID: "p0", Type: "paragraph"}, n})})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	a.reloadAll()
	a.openPage(other.ID)
	a.openLink(p.ID, "b1", 43)
	if a.page == nil || a.page.id != p.ID || a.page.editor.Caret().ID != "b1" {
		t.Fatalf("opened %v at %v", a.page != nil && a.page.id == p.ID, a.page.editor.Caret())
	}
	if pl := a.players[rec.ID]; pl == nil || pl.from != 43*time.Second {
		t.Errorf("player %+v", pl)
	}
	// A link to the block alone plays nothing.
	delete(a.players, rec.ID)
	a.openLink(p.ID, "p0", -1)
	if a.players[rec.ID] != nil {
		t.Error("a link to another block played the recording")
	}
}

// A file's notes show under its card, as a video's do: a menu writes them,
// as one step to undo, and they reach a file's own dialog.
func TestFileNotes(t *testing.T) {
	a := templatesApp(t)
	a.post = func(func()) {}
	src := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "f1", Type: "file", Data: map[string]any{"file_id": f.ID, "name": f.Name, "notes": "0:05 Hello\n1:30 Bye"}}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{n})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ed := a.page.editor
	tt := ui.NewTester(func(c *ui.Context) { a.fileBlock(c, ed.Content.Find("f1"), 600) }, 700, 600)
	tt.Frame()
	for _, want := range []string{"call.m4a", "0:05", "Hello", "1:30", "Bye"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	a.dialogs.openVideoNotes(ed.Content.Find("f1"))
	if !a.dialogs.notesFile {
		t.Error("the dialog doesn't know it is a file's")
	}
	a.setVideoNotes("f1", "0:01 New")
	if got := ed.Content.Find("f1").Str("notes"); got != "0:01 New" {
		t.Fatalf("notes %q", got)
	}
	ed.Undo()
	if got := ed.Content.Find("f1").Str("notes"); got != "0:05 Hello\n1:30 Bye" {
		t.Fatalf("after undo %q", got)
	}
	// With no window to play in, a time asks for nothing, and breaks nothing.
	a.openLink(p.ID, "f1", 90)
	if a.videos["f1"] != nil {
		t.Error("a player without a window")
	}
}

// A file the app can read shows in the window as what it is, until Close;
// one it can't leaves the page as it was.
func TestPreviewFile(t *testing.T) {
	a := templatesApp(t)
	dir := t.TempDir()
	file := func(name, text string) store.File {
		src := filepath.Join(dir, name)
		if err := os.WriteFile(src, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := a.st.AddFile(src, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		if a.preview != nil {
			a.previewView(c)
		}
	}, 800, 600)
	a.previewFile(file("people.csv", "name,age\nAda,36\n"))
	if a.preview == nil || a.preview.kind != doc.FileTable || len(a.preview.rows) != 2 {
		t.Fatalf("preview %+v", a.preview)
	}
	tt.Frame()
	if !tt.HasText("people.csv") || !tt.HasText("Ada") {
		t.Errorf("shown %q", tt.Texts())
	}
	if err := tt.Click(locales.CommonClose()); err != nil {
		t.Fatal(err)
	}
	if a.preview != nil {
		t.Error("Close left the preview")
	}
	a.previewFile(file("note.md", "# Title\n\nWords"))
	tt.Frame()
	if !tt.HasText("Title") || !tt.HasText("Words") {
		t.Errorf("shown %q", tt.Texts())
	}
	a.previewFile(file("page.html", "<script>alert(1)</script>"))
	if a.preview.code == nil {
		t.Fatalf("HTML shows as %+v", a.preview)
	}
	a.previewFile(file("binary.txt", "a\x00b"))
	if !a.preview.failed {
		t.Errorf("binary as text: %+v", a.preview)
	}
	tt.Frame()
	if !tt.HasText(locales.PreviewCannotShow()) {
		t.Errorf("shown %q", tt.Texts())
	}
	a.closePreview()
}

// wordsEngine is a speech engine that hears the same words in every file.
type wordsEngine struct{}

func (wordsEngine) Stream(string, func(string)) (recording.Stream, error) {
	return nil, errors.New("no stream")
}
func (wordsEngine) File(string, string) ([]store.Segment, error) {
	return []store.Segment{{StartMs: 5000, Text: "We ship on Friday."}}, nil
}

// An audio file's menu transcribes it into its notes, as one step to undo;
// transcribing it again asks first.
func TestTranscribeFileIntoNotes(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	src := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "f1", Type: "file", Data: map[string]any{"file_id": f.ID, "name": f.Name}}})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.rec.SetEngine(wordsEngine{})
	a.settings.Stt.Provider = recording.OnDevice
	done := make(chan struct{}, 1)
	a.rec.Attach(nil, func(fn func()) { fn(); done <- struct{}{} })
	ed := a.page.editor
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Text(c, "block").PopoverMenu(func(m *ui.Menu) { a.fileMenu(c, m, ed.Content.Find("f1")) })
		a.replaceNotesAlert(c)
	}, 700, 600)
	tt.Frame()
	if err := tt.RightClick("block"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.RecTranscribe()); err != nil {
		t.Fatalf("%v; shown %q", err, tt.Texts())
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no transcript")
	}
	if got := ed.Content.Find("f1").Str("notes"); got != "0:05 We ship on Friday." {
		t.Fatalf("notes %q", got)
	}
	ed.Undo()
	if got := ed.Content.Find("f1").Str("notes"); got != "" {
		t.Errorf("after undo %q", got)
	}
	ed.Redo()
	a.fileToTranscribe = "f1"
	tt.Frame()
	if !tt.HasText(locales.RecReplaceNotesTitle()) {
		t.Errorf("not asked: %q", tt.Texts())
	}
}

// A file's menu hides the speakers of its notes, as one step to undo, and
// offers it only for notes that name them.
func TestHideSpeakers(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Meeting", Content: doc.FromBlocks([]*doc.Node{
		{ID: "f1", Type: "file", Data: map[string]any{"name": "call.m4a", "notes": "0:05 Speaker 1: Hello"}},
		{ID: "f2", Type: "file", Data: map[string]any{"name": "other.m4a", "notes": "0:05 Hello"}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ed := a.page.editor
	show := "f1"
	tt := ui.NewTester(func(c *ui.Context) {
		n := ed.Content.Find(show)
		a.videoNotes(c, n, false)
		ui.Text(c, "block").PopoverMenu(func(m *ui.Menu) { a.fileMenu(c, m, n) })
	}, 700, 400)
	menu := func() {
		t.Helper()
		tt.Frame()
		if err := tt.RightClick("block"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	tt.Frame()
	if !tt.HasText("Speaker 1") || !tt.HasText("Hello") {
		t.Fatalf("shown %q", tt.Texts())
	}
	menu()
	if err := tt.Click(locales.CardHideSpeakers()); err != nil {
		t.Fatalf("%v; shown %q", err, tt.Texts())
	}
	tt.Frame()
	if tt.HasText("Speaker 1") || !tt.HasText("Hello") {
		t.Errorf("hidden, it shows %q", tt.Texts())
	}
	if got := ed.Content.Find("f1").Str("notes"); got != "0:05 Speaker 1: Hello" {
		t.Errorf("notes changed to %q", got)
	}
	menu()
	if err := tt.Click(locales.CardShowSpeakers()); err != nil {
		t.Fatalf("%v; shown %q", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("Speaker 1") {
		t.Errorf("shown again, it shows %q", tt.Texts())
	}
	ed.Undo()
	if !ed.Content.Find("f1").Bool(doc.SpeakersHidden) {
		t.Error("undo didn't bring the choice back")
	}
	show = "f2"
	menu()
	if tt.HasText(locales.CardHideSpeakers()) {
		t.Error("offered with no speakers to hide")
	}
}

// A speaker's name is the user's to give, for all their lines, and a line
// goes to another speaker or a new one from its own menu, each one step to
// undo.
func TestSpeakersFromALine(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	src := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "f1", Type: "file", Data: map[string]any{"file_id": f.ID, "name": f.Name,
		"notes": "0:05 Speaker 1: Hello\n0:09 Speaker 2: Goodbye\n0:12 Speaker 1: Later"}}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{n})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ed := a.page.editor
	notes := func() string { return ed.Content.Find("f1").Str("notes") }
	tt := ui.NewTester(func(c *ui.Context) {
		a.videoNotes(c, ed.Content.Find("f1"), false)
		a.dialogs.view(c, a)
	}, 700, 500)
	tt.Frame()
	if err := tt.RightClick("Goodbye"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{locales.CardChangeSpeaker(), locales.CardRenameSpeaker("Speaker 2"), locales.RecCopyTranscript(), locales.RecInsertTranscript(), locales.RecSummarize()} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click(locales.CardRenameSpeaker("Speaker 2")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.dialogs.speakerName = "Lin"
	tt.Frame()
	if err := tt.Click(locales.CommonSave()); err != nil {
		t.Fatalf("%v; shown %q", err, tt.Texts())
	}
	want := "0:05 Speaker 1: Hello\n0:09 Lin: Goodbye\n0:12 Speaker 1: Later"
	if notes() != want {
		t.Fatalf("notes %q", notes())
	}
	if names := doc.SpeakerNamesOf(ed.Content.Find("f1")); len(names) != 1 || names[0] != "Lin" {
		t.Fatalf("names %q", names)
	}
	a.editSpeakers("f1", func(notes string, names []string) (string, []string) {
		return doc.SetLineSpeaker(notes, names, 2, "Lin")
	})
	if got := notes(); got != "0:05 Speaker 1: Hello\n0:09 Lin: Goodbye\n0:12 Lin: Later" {
		t.Fatalf("changed line: %q", got)
	}
	ed.Undo()
	if notes() != want {
		t.Fatalf("after undo %q", notes())
	}
	a.dialogs.openSpeaker("f1", "", 0)
	if a.dialogs.speakerName != "" || a.dialogs.speakerLine != 0 {
		t.Errorf("a new speaker's dialog: %+v", a.dialogs.speakerName)
	}
}

// A file's transcript copies and goes in the page from its menu, and the
// menu leaves those out for notes on what isn't audio or video.
func TestFileTranscriptMenu(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	dir := t.TempDir()
	add := func(name string) store.File {
		src := filepath.Join(dir, name)
		if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := a.st.AddFile(src, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	audio, pdf := add("call.m4a"), add("report.pdf")
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Meeting", Content: doc.FromBlocks([]*doc.Node{
		{ID: "f1", Type: "file", Data: map[string]any{"file_id": audio.ID, "name": audio.Name, "notes": "0:05 Speaker 1: Hello"}},
		{ID: "f2", Type: "file", Data: map[string]any{"file_id": pdf.ID, "name": pdf.Name, "notes": "Page one"}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ed := a.page.editor
	show := "f1"
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Text(c, "block").PopoverMenu(func(m *ui.Menu) { a.fileMenu(c, m, ed.Content.Find(show)) })
	}, 700, 400)
	menu := func() {
		t.Helper()
		tt.Frame()
		if err := tt.RightClick("block"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	menu()
	if err := tt.Click(locales.RecCopyTranscript()); err != nil {
		t.Fatalf("%v; shown %q", err, tt.Texts())
	}
	tt.Frame()
	if got := tt.Clipboard(); got != "Speaker 1: Hello" {
		t.Errorf("copied %q", got)
	}
	menu()
	if err := tt.Click(locales.RecInsertTranscript()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := ed.Content.PlainText(); !strings.Contains(got, "0:05  Speaker 1: Hello") {
		t.Errorf("page %q", got)
	}
	show = "f2"
	menu()
	if tt.HasText(locales.RecCopyTranscript()) {
		t.Error("a PDF's notes offered as a transcript")
	}
}

// The player under the page goes by the top of a block, where its own
// player is: a long transcript may still show with that scrolled away.
func TestHeadGone(t *testing.T) {
	const viewTop, viewBottom = 50, 750
	for _, c := range []struct {
		top  float32
		want bool
	}{
		{100, false},
		{-40, false},
		{-61, true},
		{-5000, true},
		{749, false},
		{750, true},
		{5000, true},
	} {
		if got := headGone(c.top, viewTop, viewBottom); got != c.want {
			t.Errorf("headGone(%v) = %v", c.top, got)
		}
	}
}

// A recording that starts is what the player under the page plays, by its
// block; a video isn't, and playing nothing leaves none.
func TestNowPlaying(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Meeting", Content: doc.FromBlocks([]*doc.Node{
		{ID: "r1", Type: "recording", Data: map[string]any{"recording_id": "rec"}},
		{ID: "f1", Type: "file", Data: map[string]any{"name": "call.m4a"}},
		{ID: "f2", Type: "file", Data: map[string]any{"name": "film.mp4"}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.setNowPlaying("", "rec")
	if a.now != (nowPlaying{block: "r1", rec: "rec"}) {
		t.Errorf("recording: %+v", a.now)
	}
	a.fileMedia = map[string]string{"f1": "http://x", "f2": "http://y"}
	a.setNowPlaying("f1", "")
	if a.now != (nowPlaying{block: "f1"}) {
		t.Errorf("audio: %+v", a.now)
	}
	// Going on from a pause, it stays where its player said it is.
	a.fileNow = fileStatus{at: 42, length: 90}
	a.setNowPlaying("f1", "")
	if a.fileNow.at != 42 {
		t.Errorf("going on from %v", a.fileNow.at)
	}
	a.setNowPlaying("f2", "")
	if a.now != (nowPlaying{}) {
		t.Errorf("video: %+v", a.now)
	}
	a.setNowPlaying("f1", "")
	a.closePlayer("f1")
	if a.now != (nowPlaying{}) {
		t.Errorf("closed: %+v", a.now)
	}
}

// A skip stays within the audio, and waits for its length.
func TestSkipped(t *testing.T) {
	const s = time.Second
	for _, c := range []struct{ pos, by, dur, want time.Duration }{
		{30 * s, 15 * s, 90 * s, 45 * s},
		{30 * s, -15 * s, 90 * s, 15 * s},
		{5 * s, -15 * s, 90 * s, 0},
		{85 * s, 15 * s, 90 * s, 90 * s},
		{5 * s, 15 * s, 0, 5 * s},
	} {
		if got := skipped(c.pos, c.by, c.dur); got != c.want {
			t.Errorf("skipped(%v, %v, %v) = %v", c.pos, c.by, c.dur, got)
		}
	}
}

// A speed is kept for next time and reaches what plays, and one that isn't
// offered reads as normal.
func TestPlaySpeed(t *testing.T) {
	a := templatesApp(t)
	if a.speed() != 1 {
		t.Errorf("at first %v", a.speed())
	}
	pl := audio.NewSound(8000, 1, make([]float32, 8000)).NewPlayer()
	a.players = map[string]*player{"rec": {p: pl}}
	a.setSpeed(1.5)
	if pl.Speed() != 1.5 {
		t.Errorf("playing at %v", pl.Speed())
	}
	a.playSpeed = 0
	if a.speed() != 1.5 {
		t.Errorf("read back %v", a.speed())
	}
	// Its button shows the speed, and its menu changes it.
	tt := ui.NewTester(func(c *ui.Context) { a.playerBar(c, 0, time.Minute, func(time.Duration) {}) }, 700, 300)
	tt.Frame()
	if err := tt.Click("1.5×"); err != nil {
		t.Fatalf("%v; shown %q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("2×"); err != nil {
		t.Fatalf("%v; shown %q", err, tt.Texts())
	}
	tt.Frame()
	if a.speed() != 2 || pl.Speed() != 2 {
		t.Errorf("chose 2×, at %v, playing at %v", a.speed(), pl.Speed())
	}
	if err := a.st.SetValue(speedKey, "9"); err != nil {
		t.Fatal(err)
	}
	a.playSpeed = 0
	if a.speed() != 1 {
		t.Errorf("not offered, %v", a.speed())
	}
	if speedLabel(1.25) != "1.25×" || speedLabel(1) != "1×" {
		t.Errorf("labels %q %q", speedLabel(1.25), speedLabel(1))
	}
}

// An audio file is one card, as a recording: its name and controls over its
// transcript, with a speaker over their line; a video's notes stay in their
// panel, with the speaker in the line.
func TestAudioCard(t *testing.T) {
	a := templatesApp(t)
	src := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Audio"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "f1", Type: "file", Data: map[string]any{"file_id": f.ID, "name": f.Name, "notes": "0:05 Speaker 1: Hello"}},
		{ID: "f2", Type: "file", Data: map[string]any{"name": "film.mp4", "notes": "0:05 Speaker 1: Hello"}},
	})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ed := a.page.editor
	show := "f1"
	tt := ui.NewTester(func(c *ui.Context) { a.fileBlock(c, ed.Content.Find(show), 600) }, 700, 400)
	tt.Frame()
	for _, want := range []string{"call.m4a", fileDetail("call.m4a", f.SizeBytes), "Speaker 1", "Hello"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	a.rec.TranscribingFiles = map[string]string{"f1": p.ID}
	tt.Frame()
	if !tt.HasText(locales.RecTranscribing()) {
		t.Errorf("transcribing, it shows %q", tt.Texts())
	}
	a.fileFailed = map[string]bool{"f1": true}
	tt.Frame()
	if !tt.HasText(locales.CardCannotPlay()) {
		t.Errorf("unplayable, it shows %q", tt.Texts())
	}
	show = "f2"
	tt.Frame()
	if !tt.HasText("Speaker 1: Hello") {
		t.Errorf("video notes show %q", tt.Texts())
	}
}

// A long transcript's window moves on with the audio, and stays closed;
// opened, it shows every line.
func TestRollingLines(t *testing.T) {
	a := templatesApp(t)
	var notes []string
	for i := range 20 {
		notes = append(notes, doc.Clock(i*10)+" Line "+string(rune('A'+i)))
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Talk", Content: doc.FromBlocks([]*doc.Node{
		{ID: "f1", Type: "file", Data: map[string]any{"name": "talk.m4a", "notes": strings.Join(notes, "\n")}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ed := a.page.editor
	tt := ui.NewTester(func(c *ui.Context) { a.videoNotes(c, ed.Content.Find("f1"), false) }, 700, 500)
	tt.Frame()
	if !tt.HasText("Line A") || tt.HasText("Line T") {
		t.Fatalf("at first it shows %q", tt.Texts())
	}
	a.now = nowPlaying{block: "f1"}
	a.fileNow, a.fileNowAt = fileStatus{at: 120, length: 200, paused: true}, time.Now()
	tt.Frame()
	if tt.HasText("Line A") || !tt.HasText("Line L") || !tt.HasText("Line M") {
		t.Errorf("at 2:00 it shows %q", tt.Texts())
	}
	if a.notesOpen["f1"] {
		t.Error("following opened the transcript")
	}
	a.fileNow.at = 190
	tt.Frame()
	if !tt.HasText("Line T") {
		t.Errorf("at the end it shows %q", tt.Texts())
	}
	a.notesOpen = map[string]bool{"f1": true}
	tt.Frame()
	if !tt.HasText("Line A") || !tt.HasText("Line T") {
		t.Errorf("opened, it shows %q", tt.Texts())
	}
}

// The window keeps a line above the one being said, and stops short of
// the end, where the last lines still show.
func TestRollTop(t *testing.T) {
	for _, c := range []struct{ current, count, want int }{
		{-1, 20, 0}, {0, 20, 0}, {1, 20, 0}, {5, 20, 4}, {19, 20, 17}, {4, 20, 3},
	} {
		if got := rollTop(c.current, c.count); got != c.want {
			t.Errorf("rollTop(%d, %d) = %d", c.current, c.count, got)
		}
	}
}

// The system's media controls show what plays, say so when it moves, and
// drive it; once it stops, they let the app go.
func TestSystemAudio(t *testing.T) {
	a := templatesApp(t)
	rec := store.Recording{ID: "r1", PageID: "p", DurationMs: 60000, Status: "ready"}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Call", Content: doc.FromBlocks([]*doc.Node{
		{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": "Standup"}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	pl := audio.NewSound(8000, 1, make([]float32, 8000*60)).NewPlayer()
	a.players = map[string]*player{"r1": {p: pl}}
	a.setNowPlaying("", "r1")
	defer mygo.NowPlaying.Clear()

	if a.syncSystemAudio() {
		t.Error("a paused recording counts as playing")
	}
	info, ok := mygo.NowPlaying.Info()
	if !ok || info.Title != "Standup" || info.State != mygo.PlaybackPaused || info.Duration != time.Minute {
		t.Fatalf("shows %+v (%v)", info, ok)
	}
	if info.Commands&(mygo.MediaNext|mygo.MediaPrevious|mygo.MediaSeek) == 0 {
		t.Errorf("offers %v", info.Commands)
	}
	a.mediaCommand(mygo.MediaNext, 0)
	if pl.Position() != 15*time.Second {
		t.Errorf("a skip reached %v", pl.Position())
	}
	if info, _ := mygo.NowPlaying.Info(); info.Position != 15*time.Second {
		t.Errorf("the system thinks %v", info.Position)
	}
	a.mediaCommand(mygo.MediaSeek, 40*time.Second)
	a.mediaCommand(mygo.MediaPrevious, 0)
	if pl.Position() != 25*time.Second {
		t.Errorf("after a seek and a skip back, %v", pl.Position())
	}
	a.mediaCommand(mygo.MediaStop, 0)
	if _, ok := mygo.NowPlaying.Info(); ok || a.now != (nowPlaying{}) {
		t.Errorf("stopped, still shows it: %+v", a.now)
	}
}
