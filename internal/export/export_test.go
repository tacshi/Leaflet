package export

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
)

func TestExportZip(t *testing.T) {
	at := time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC)
	pages := []store.Page{
		{ID: "trip", Title: "Trip planning", FolderID: "f", UpdatedAt: at},
		{ID: "hotels", Title: "Hotels", ParentID: "trip", UpdatedAt: at},
		{ID: "notes", Title: "Notes", UpdatedAt: at},
		{ID: "notes2", Title: "Notes", UpdatedAt: at},
	}
	img := &doc.Node{ID: "i", Type: "image", Data: map[string]any{"url": "file:///data/files/x/dawn.jpg", "alt": "dawn"}}
	link := &doc.Node{ID: "s", Type: "subpage", Data: map[string]any{"page_id": "hotels"}}
	mention := &doc.Node{ID: "m", Type: "paragraph"}
	mention.SetDelta(doc.Delta{{Text: "See "}, {Text: "Notes", Attrs: doc.PageMention("notes", "Notes")},
		{Text: " and "}, {Text: "its end", Attrs: doc.Attrs{Href: doc.BlockLink("notes", "b1")}}})
	contents := map[string]*doc.Content{
		"trip":   doc.FromBlocks([]*doc.Node{img, link, mention}),
		"hotels": doc.FromBlocks([]*doc.Node{img.Clone()}),
		"notes":  doc.Empty(),
		"notes2": doc.FromBlocks([]*doc.Node{{ID: "s", Type: "image", Data: map[string]any{
			"url": "file:///elsewhere/files/sun/sun.jpg", "alt": "sun", "file_id": "sun"}}}),
	}
	var buf bytes.Buffer
	err := Zip(&buf, pages, []store.Folder{{ID: "f", Name: "Travel"}}, func(id string) *doc.Content { return contents[id] },
		func(id string) string {
			if id == "sun" {
				return "/data/files/sun/sun.jpg"
			}
			return ""
		},
		func(p string) (io.ReadCloser, error) {
			if p == "/data/files/x/dawn.jpg" || p == "/data/files/sun/sun.jpg" {
				return io.NopCloser(strings.NewReader("jpeg")), nil
			}
			return nil, os.ErrNotExist
		})
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range r.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		got[f.Name] = string(b)
		if !f.Modified.Equal(at) {
			t.Errorf("%s modified %v", f.Name, f.Modified)
		}
	}
	for _, name := range []string{"Travel/Trip planning.md", "Travel/Trip planning/Hotels.md", "Notes.md", "Notes (2).md", "files/dawn.jpg", "files/sun.jpg"} {
		if _, ok := got[name]; !ok {
			t.Errorf("no %s in %v", name, keys(got))
		}
	}
	if len(got) != 6 {
		t.Errorf("files %v", keys(got))
	}
	trip := got["Travel/Trip planning.md"]
	if !strings.Contains(trip, "](../files/dawn.jpg)") || !strings.Contains(trip, "](Trip%20planning/Hotels.md)") ||
		!strings.Contains(trip, "See [Notes](../Notes.md) and [its end](../Notes.md#b1)") {
		t.Errorf("trip links:\n%s", trip)
	}
	if !strings.Contains(got["Travel/Trip planning/Hotels.md"], "](../../files/dawn.jpg)") {
		t.Errorf("hotels:\n%s", got["Travel/Trip planning/Hotels.md"])
	}
	if !strings.Contains(got["Notes (2).md"], "](files/sun.jpg)") {
		t.Errorf("notes 2:\n%s", got["Notes (2).md"])
	}
}

func TestExportZipDotNames(t *testing.T) {
	pages := []store.Page{
		{ID: "up", Title: ".."},
		{ID: "notes", Title: "Notes", ParentID: "up"},
		{ID: "here", Title: ".", FolderID: "f"},
	}
	var buf bytes.Buffer
	err := Zip(&buf, pages, []store.Folder{{ID: "f", Name: ".."}}, func(string) *doc.Content { return doc.Empty() },
		func(string) string { return "" }, openFile)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	if want := []string{"-- (2).md", "-- (2)/Notes.md", "--/-.md"}; !slices.Equal(names, want) {
		t.Errorf("names %q, want %q", names, want)
	}
}

func TestExportZipFilesBesidePages(t *testing.T) {
	pages := []store.Page{
		{ID: "notes", Title: "notes", FolderID: "f"},
		{ID: "trip", Title: "Trip", FolderID: "f"},
		{ID: "hotels", Title: "Hotels", ParentID: "trip"},
		{ID: "other", Title: "Other"},
	}
	md := &doc.Node{ID: "m", Type: "file", Data: map[string]any{"url": "file:///data/files/m/notes.md", "name": "notes.md"}}
	dir := &doc.Node{ID: "d", Type: "file", Data: map[string]any{"url": "file:///data/files/d/Trip", "name": "Trip"}}
	contents := map[string]*doc.Content{"other": doc.FromBlocks([]*doc.Node{md, dir})}
	var buf bytes.Buffer
	err := Zip(&buf, pages, []store.Folder{{ID: "f", Name: "files"}}, func(id string) *doc.Content {
		if c := contents[id]; c != nil {
			return c
		}
		return doc.Empty()
	}, func(string) string { return "" }, func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("attached")), nil })
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range r.File {
		if _, ok := got[f.Name]; ok {
			t.Errorf("%s is in the zip twice", f.Name)
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		got[f.Name] = string(b)
	}
	if got["files/notes.md"] == "attached" || got["files/notes (2).md"] != "attached" || got["files/Trip (2)"] != "attached" {
		t.Errorf("files %v", keys(got))
	}
	if other := got["Other.md"]; !strings.Contains(other, "](files/notes%20%282%29.md)") {
		t.Errorf("other:\n%s", other)
	}
}

func openFile(path string) (io.ReadCloser, error) { return os.Open(path) } // #nosec G304

func unzip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range r.File {
		if _, ok := got[f.Name]; ok {
			t.Errorf("%s is in the zip twice", f.Name)
		}
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		got[f.Name] = string(data)
	}
	return got
}

func TestExportZipNoFileNamedAsADirectory(t *testing.T) {
	for _, pages := range [][]store.Page{
		{{ID: "x", Title: "X"}, {ID: "xmd", Title: "X.md"}, {ID: "child", Title: "Child", ParentID: "xmd"}},
		{{ID: "xmd", Title: "x.MD"}, {ID: "child", Title: "Child", ParentID: "xmd"}, {ID: "x", Title: "X"}},
	} {
		var buf bytes.Buffer
		if err := Zip(&buf, pages, nil, func(string) *doc.Content { return doc.Empty() }, func(string) string { return "" }, openFile); err != nil {
			t.Fatal(err)
		}
		got := unzip(t, buf.Bytes())
		dirs := map[string]bool{}
		for name := range got {
			for d := path.Dir(name); d != "."; d = path.Dir(d) {
				dirs[strings.ToLower(d)] = true
			}
		}
		for name := range got {
			if dirs[strings.ToLower(name)] {
				t.Errorf("%s is a file and a directory in %v", name, keys(got))
			}
		}
		if len(got) != 3 {
			t.Errorf("files %v", keys(got))
		}
	}
}

func TestExportZipLinksOnlyLinks(t *testing.T) {
	pages := []store.Page{{ID: "e", Title: "Notes"}, {ID: "p", Title: "Plan"}}
	code := doc.CodeNode("see (leaflet://page/e) and (file:///data/files/x/dawn.jpg) here", "")
	code.ID = "c"
	text := &doc.Node{ID: "t", Type: "paragraph"}
	text.SetDelta(doc.Delta{{Text: "Notes", Attrs: doc.Attrs{Href: doc.PageLink("e")}}, {Text: " or (leaflet://page/e#b)"}})
	img := &doc.Node{ID: "i", Type: "image", Data: map[string]any{"url": "file:///data/files/x/dawn.jpg", "alt": "dawn"}}
	contents := map[string]*doc.Content{"e": doc.Empty(), "p": doc.FromBlocks([]*doc.Node{code, text, img})}
	var buf bytes.Buffer
	err := Zip(&buf, pages, nil, func(id string) *doc.Content { return contents[id] }, func(string) string { return "" },
		func(p string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("jpeg")), nil })
	if err != nil {
		t.Fatal(err)
	}
	plan := unzip(t, buf.Bytes())["Plan.md"]
	for _, want := range []string{"see (leaflet://page/e) and (file:///data/files/x/dawn.jpg) here", "[Notes](Notes.md) or (leaflet://page/e#b)", "![dawn](files/dawn.jpg)"} {
		if !strings.Contains(plan, want) {
			t.Errorf("no %q in\n%s", want, plan)
		}
	}
}

func TestOwnFilesOpensOnlyTheStoresFiles(t *testing.T) {
	dir := t.TempDir()
	own, db := filepath.Join(dir, "files", "f1", "a.png"), filepath.Join(dir, "leaflet.db")
	if err := os.MkdirAll(filepath.Dir(own), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{own, db} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	localPath, open := OwnFiles([]store.File{{ID: "f1", LocalPath: own}, {ID: "dir", LocalPath: dir}})
	if localPath("f1") != own {
		t.Errorf("f1 is at %q", localPath("f1"))
	}
	f, err := open(own)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for _, p := range []string{db, dir} {
		if f, err := open(p); !errors.Is(err, ErrNotOwnFile) {
			t.Errorf("opened %s: %v", p, err)
			if f != nil {
				_ = f.Close()
			}
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestExportZipNestedFolders(t *testing.T) {
	folders := []store.Folder{
		{ID: "work", Name: "Work"},
		{ID: "home", Name: "Home"},
		{ID: "work-notes", Name: "Notes", ParentFolderID: "work"},
		{ID: "home-notes", Name: "Notes", ParentFolderID: "home"},
		{ID: "home-notes2", Name: "notes", ParentFolderID: "home"},
		{ID: "deep", Name: "a/b", ParentFolderID: "work-notes"},
	}
	pages := []store.Page{
		{ID: "1", Title: "Plan", FolderID: "work-notes"},
		{ID: "2", Title: "Plan", FolderID: "home-notes"},
		{ID: "3", Title: "Plan", FolderID: "home-notes2"},
		{ID: "4", Title: "Plan", FolderID: "deep"},
		{ID: "5", Title: "Work"},
	}
	var buf bytes.Buffer
	err := Zip(&buf, pages, folders, func(string) *doc.Content { return doc.Empty() },
		func(string) string { return "" }, openFile)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	want := []string{"Home/Notes/Plan.md", "Home/notes (2)/Plan.md", "Work (2).md", "Work/Notes/Plan.md", "Work/Notes/a-b/Plan.md"}
	if !slices.Equal(names, want) {
		t.Errorf("names %q, want %q", names, want)
	}
}

func TestSafeName(t *testing.T) {
	long := strings.Repeat("旅", 100)
	for in, want := range map[string]string{
		"Trip: day 1":        "Trip- day 1",
		"tab\there\n":        "tab-here-",
		"Notes. ":            "Notes",
		"CON":                "CON-",
		"nul.txt":            "nul-.txt",
		"Com1 .md":           "Com1 -.md",
		"Console":            "Console",
		"..":                 "--",
		long + ".jpeg":       strings.Repeat("旅", 65) + ".jpeg",
		long + ".Next steps": strings.Repeat("旅", 66),
	} {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SafeName(long); len(got) > 200 || !utf8.ValidString(got) {
		t.Errorf("SafeName of a long name is %d bytes: %q", len(got), got)
	}
}

func TestWithAudioOneRecordingTwice(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	wav := filepath.Join(dir, "r1.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecording(store.Recording{ID: "r1", PageID: "p", BlockID: "b1", Status: "ready", LocalPath: wav}); err != nil {
		t.Fatal(err)
	}
	c := doc.FromBlocks([]*doc.Node{
		{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": "Standup"}},
		{ID: "b2", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": "Standup"}},
	})
	src := Source{Store: st, Rec: recording.NewController(st, nil)}
	got := withAudio(src, "p", c, t.TempDir())
	var files []string
	got.Walk(func(n, _ *doc.Node) {
		if n.Type == "file" {
			files = append(files, n.Str("url"))
		}
	})
	if len(files) != 2 || files[0] != files[1] {
		t.Errorf("files %q", files)
	}
	if data, err := os.ReadFile(wav); err != nil || string(data) != "RIFF" { // #nosec G304
		t.Errorf("the recording's audio is now %q, %v", data, err)
	}
}

func TestCopyAudioKeepsALink(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a.wav"), filepath.Join(dir, "b.wav")
	if err := os.WriteFile(from, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(from, to); err != nil {
		t.Skip(err)
	}
	if err := copyAudio(from, to); err == nil {
		t.Error("copied over a file there")
	}
	if data, _ := os.ReadFile(from); string(data) != "RIFF" { // #nosec G304
		t.Errorf("the audio is now %q", data)
	}
}

func TestCopyAudioCutShortGoes(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "b.wav")
	if err := copyAudio(dir, to); err == nil {
		t.Fatal("copied a folder")
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Errorf("the copy cut short stayed: %v", err)
	}
}

func TestExportZipMoments(t *testing.T) {
	first := &doc.Node{ID: "l", Type: "paragraph"}
	first.SetDelta(doc.Delta{{Text: "1:23", Attrs: doc.Attrs{Href: doc.MomentLink("second", "r", 83)}},
		{Text: " and "}, {Text: "0:10", Attrs: doc.Attrs{Href: doc.MomentLink("second", "v", 10)}}})
	contents := map[string]*doc.Content{
		"first": doc.FromBlocks([]*doc.Node{first}),
		"second": doc.FromBlocks([]*doc.Node{
			{ID: "r", Type: "recording", Data: map[string]any{"recording_id": "x"}},
			{ID: "a", Type: "file", Data: map[string]any{"url": "file:///tmp/audio/Call.wav", "name": "Call.wav", audioOf: "r"}},
			{ID: "v", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"}},
		}),
	}
	var buf bytes.Buffer
	err := Zip(&buf, []store.Page{{ID: "first", Title: "First"}, {ID: "second", Title: "Second"}}, nil,
		func(id string) *doc.Content { return contents[id] }, func(string) string { return "" },
		func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("RIFF")), nil })
	if err != nil {
		t.Fatal(err)
	}
	md := unzip(t, buf.Bytes())["First.md"]
	for _, want := range []string{"[1:23](files/Call.wav#t=83)", "[0:10](https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10s)"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in\n%s", want, md)
		}
	}
}

func TestMoments(t *testing.T) {
	c := doc.FromBlocks([]*doc.Node{
		{ID: "r", Type: "recording"}, {ID: "f", Type: "file"}, {ID: "p", Type: "paragraph"},
		{ID: "v", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"}},
	})
	link := Moments(c)
	for href, want := range map[string]string{
		doc.MomentLink("pg", "r", 83):    "",
		doc.MomentLink("pg", "f", 83):    "",
		doc.MomentLink("pg", "v", 83):    "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=83s",
		doc.MomentLink("pg", "p", 83):    doc.MomentLink("pg", "p", 83),
		doc.MomentLink("pg", "gone", 83): doc.MomentLink("pg", "gone", 83),
		doc.BlockLink("pg", "r"):         doc.BlockLink("pg", "r"),
		"https://example.com":            "https://example.com",
	} {
		if got := link(href); got != want {
			t.Errorf("link(%q) = %q, want %q", href, got, want)
		}
	}
	if got := Moments(nil)(doc.MomentLink("pg", "r", 1)); got != doc.MomentLink("pg", "r", 1) {
		t.Errorf("without content: %q", got)
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Call.wav")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, iotest.ErrReader(errors.New("cut"))); err == nil {
		t.Error("a cut save reported nothing")
	}
	entries, _ := os.ReadDir(dir)
	if data, _ := os.ReadFile(path); string(data) != "old" || len(entries) != 1 { // #nosec G304
		t.Errorf("after a cut save: %q, %d entries", data, len(entries))
	}
	if err := Save(path, strings.NewReader("RIFF")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "RIFF" { // #nosec G304
		t.Errorf("saved %q", data)
	}
}

func TestWithTranscriptsOwnRecordingsOnly(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	wav := filepath.Join(dir, "r1.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := store.Recording{ID: "r1", PageID: "p", BlockID: "b1", Status: "ready", LocalPath: wav,
		Transcript: []store.Segment{{StartMs: 0, EndMs: 4000, Text: "We ship on Friday."}}}
	if err := st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	c := doc.FromBlocks([]*doc.Node{{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": "Standup"}}})
	md := func(pageID string) string {
		return doc.PageMarkdown("Notes", WithTranscripts(st, pageID, c), doc.MarkdownOptions{})
	}
	if !strings.Contains(md("p"), "We ship on Friday.") {
		t.Errorf("the recording's own page misses its transcript:\n%s", md("p"))
	}
	if strings.Contains(md("q"), "We ship on Friday.") {
		t.Errorf("another page has the recording's transcript:\n%s", md("q"))
	}
	src := Source{Store: st, Rec: recording.NewController(st, nil)}
	files := func(pageID string) int {
		n := 0
		withAudio(src, pageID, c.Copy(), t.TempDir()).Walk(func(b, _ *doc.Node) {
			if b.Type == "file" {
				n++
			}
		})
		return n
	}
	if got := files("p"); got != 1 {
		t.Errorf("the recording's own page has %d audio files", got)
	}
	if got := files("q"); got != 0 {
		t.Errorf("another page has %d of the recording's audio files", got)
	}
}

type fakeSync struct {
	content map[string]*doc.Content
	asked   []string
}

func (f *fakeSync) Hold(fn func()) { fn() }
func (f *fakeSync) CanSync() bool  { return true }
func (f *fakeSync) RefreshContent(_ context.Context, id string) (*doc.Content, error) {
	f.asked = append(f.asked, id)
	if c, ok := f.content[id]; ok {
		return c, nil
	}
	return nil, errors.New("offline")
}

func text(s string) *doc.Content {
	n := &doc.Node{ID: "t", Type: "paragraph"}
	n.SetDelta(doc.Delta{{Text: s}})
	return doc.FromBlocks([]*doc.Node{n})
}

func TestAllRefreshesPagesNotKept(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	page := func(title string, keep bool) store.Page {
		p, err := st.CreatePage(store.NewPageOptions{Title: title, Content: text(title + " here")})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetKeepOffline(p.ID, keep); err != nil {
			t.Fatal(err)
		}
		return p
	}
	closed, away, kept, open := page("Closed", false), page("Away", false), page("Kept", true), page("Open", false)
	sync := &fakeSync{content: map[string]*doc.Content{closed.ID: text("Closed elsewhere")}}
	out := filepath.Join(t.TempDir(), "export.zip")
	report, err := All(out, Source{Store: st, Sync: sync, Kept: Keeper{SyncOn: true, Pulled: []string{open.ID}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report != (Report{Stale: 1}) {
		t.Errorf("report %+v", report)
	}
	if slices.Contains(sync.asked, kept.ID) || slices.Contains(sync.asked, open.ID) || !slices.Contains(sync.asked, away.ID) {
		t.Errorf("brought down %q", sync.asked)
	}
	data, err := os.ReadFile(out) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	got := unzip(t, data)
	for name, want := range map[string]string{"Closed.md": "Closed elsewhere", "Away.md": "Away here", "Kept.md": "Kept here", "Open.md": "Open here"} {
		if !strings.Contains(got[name], want) {
			t.Errorf("%s is %q, want %q in it", name, got[name], want)
		}
	}
}

func TestAllFailedKeepsTheFileThere(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	st.Dir = filepath.Join(t.TempDir(), "gone")
	out := filepath.Join(t.TempDir(), "export.zip")
	if err := os.WriteFile(out, []byte("last week's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := All(out, Source{Store: st}, nil, nil); err == nil {
		t.Fatal("the export reported nothing")
	}
	if data, err := os.ReadFile(out); err != nil || string(data) != "last week's" { // #nosec G304
		t.Errorf("the file there is now %q, %v", data, err)
	}
}

func TestWriteCutShort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.zip")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Write(path, func(w io.Writer) error {
		_, _ = io.WriteString(w, "half")
		return errors.New("cut")
	})
	if err == nil {
		t.Error("a cut write reported nothing")
	}
	entries, _ := os.ReadDir(dir)
	if data, _ := os.ReadFile(path); string(data) != "old" || len(entries) != 1 { // #nosec G304
		t.Errorf("after a cut write: %q, %d entries", data, len(entries))
	}
}
