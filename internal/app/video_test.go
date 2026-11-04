package app

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
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
