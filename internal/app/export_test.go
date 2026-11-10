package app

import (
	"archive/zip"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func TestExportOnlyOwnFiles(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret, pic := filepath.Join(outside, "id_rsa"), filepath.Join(outside, "pic.png")
	for _, p := range []string{secret, pic} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, err := st.AddFile(pic, "", "", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	blocks := []*doc.Node{
		{ID: "a", Type: "image", Data: map[string]any{"url": "file://" + f.LocalPath, "file_id": f.ID}},
		{ID: "b", Type: "file", Data: map[string]any{"url": "file://" + secret, "name": "id_rsa"}},
	}
	if _, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks(blocks)}); err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	out := filepath.Join(t.TempDir(), "export.zip")
	if _, err := a.writeExport(out, a.keeper(), a.folders, a.files); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	if !slices.Contains(names, "files/pic.png") || slices.Contains(names, "files/id_rsa") {
		t.Errorf("exported %q", names)
	}
}

func TestExportKeepsTranscripts(t *testing.T) {
	a := templatesApp(t)
	rec := store.Recording{ID: "r1", PageID: "p", Status: "ready", DurationMs: 9000,
		Transcript: []store.Segment{{StartMs: 0, EndMs: 4000, Text: "We ship on Friday."}}}
	if err := a.st.PutRecording(rec); err != nil {
		t.Fatal(err)
	}
	c := doc.Empty()
	c.Children = []*doc.Node{{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": "Standup"}}}
	md := doc.PageMarkdown("Notes", a.withTranscripts("p", c), doc.MarkdownOptions{})
	if !strings.Contains(md, "Standup") || !strings.Contains(md, "We ship on Friday.") {
		t.Errorf("markdown %q", md)
	}
	if len(c.Children) != 1 {
		t.Error("the page's own content changed")
	}
}

func TestExportSaysThePageIsNotHere(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Away"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.DB().Exec(`DELETE FROM page_contents WHERE page_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetKeepOffline(p.ID, false); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	for _, export := range []func(string){a.exportMarkdown, a.exportPDF} {
		a.toasts = nil
		export(p.ID)
		if len(a.toasts) != 1 || a.toasts[0].text != locales.HostErrorExport(locales.CardNotOnDevice()) {
			t.Errorf("toasts %+v", a.toasts)
		}
	}
}

func TestExportFallsBackToTheCopyHere(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(hs.Close)
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.SignedInForTest(hs.URL, st, "u1")
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	para := &doc.Node{ID: "p1", Type: "paragraph"}
	para.SetDelta(doc.Delta{{Text: "As it was here"}})
	p, err := st.CreatePage(store.NewPageOptions{Title: "Away", Content: doc.FromBlocks([]*doc.Node{para})})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKeepOffline(p.ID, false); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.session.SignedIn = true
	if a.keptHere(*a.pageByID(p.ID)) || !a.sync.CanSync() {
		t.Fatal("the page is kept here, or nothing syncs")
	}
	var got *doc.Content
	a.exportContent(p.ID, func(c *doc.Content) { got = c })
	if got == nil || got.Find("p1") == nil {
		t.Fatalf("exported %v", got)
	}
	if len(a.toasts) != 1 || a.toasts[0].text != locales.HostDownloading("Away") {
		t.Errorf("toasts %+v", a.toasts)
	}

	if _, err := st.DB().Exec(`DELETE FROM page_contents WHERE page_id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	a.toasts, got = nil, nil
	a.exportContent(p.ID, func(c *doc.Content) { got = c })
	if got != nil || len(a.toasts) != 2 || !strings.HasPrefix(a.toasts[1].text, strings.TrimSuffix(locales.HostErrorExport(""), " ")) ||
		a.toasts[1].text == locales.HostErrorExport(locales.CardNotOnDevice()) {
		t.Errorf("with no copy here: exported %v, toasts %+v", got, a.toasts)
	}
}

func TestExportBringsAPageNotKeptDown(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Away"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	if !a.keptHere(*a.pageByID(p.ID)) {
		t.Error("with nothing to sync with, a page is not kept here")
	}
	if err := a.st.SetKeepOffline(p.ID, false); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.session.SignedIn = true
	if a.keptHere(*a.pageByID(p.ID)) {
		t.Error("a page not kept, nor open, nor linked, is kept here")
	}
	a.settings.AllPagesOffline = true
	if !a.keptHere(*a.pageByID(p.ID)) {
		t.Error("with all pages kept, a page is not kept here")
	}
	a.settings.AllPagesOffline = false
	a.pub = &pubLink{page: p.ID, state: "live"}
	if !a.keptHere(*a.pageByID(p.ID)) {
		t.Error("the page a link serves is not kept here")
	}
}

func TestExportZipTakesAudio(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Application Support")
	if err := os.MkdirAll(filepath.Join(dir, "recordings"), 0o750); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session(), rec: recording.NewController(st, be)}
	p, _ := st.CreatePage(store.NewPageOptions{Title: "Standup"})
	wav := filepath.Join(dir, "recordings", "r1.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecording(store.Recording{ID: "r1", PageID: p.ID, BlockID: "b1", Status: "ready", LocalPath: wav,
		Transcript: []store.Segment{{EndMs: 900, Text: "We ship on Friday."}}}); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "my map.png")
	if err := os.WriteFile(src, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	pic, err := st.AddFile(src, "", p.ID, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": "r1", "title": "Standup call"}},
		{ID: "b2", Type: "image", Data: map[string]any{"url": "file://" + pic.LocalPath, "file_id": pic.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	files, _ := st.Files()
	out := filepath.Join(t.TempDir(), "export.zip")
	if _, err := a.writeExport(out, a.keeper(), nil, files); err != nil {
		t.Fatal(err)
	}
	got := readZip(t, out)
	if got["files/Standup call.wav"] != "RIFF" || got["files/my map.png"] != "png" {
		t.Fatalf("zip has %v", slices.Sorted(maps.Keys(got)))
	}
	md := got["Standup.md"]
	if !strings.Contains(md, "(files/Standup%20call.wav)") || !strings.Contains(md, "(files/my%20map.png)") ||
		strings.Contains(md, "file://") || !strings.Contains(md, "We ship on Friday.") {
		t.Errorf("Standup.md:\n%s", md)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "export-*")); len(left) != 0 {
		t.Errorf("the export left %v", left)
	}
}

func readZip(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	out := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = string(data)
	}
	return out
}

func TestEditMenuCopiesPlainText(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	head := &doc.Node{ID: "h", Type: "heading", Data: map[string]any{"level": 1}}
	head.SetDelta(doc.FromMarkdown("Trip **plan**"))
	item := &doc.Node{ID: "i", Type: "bulleted_list"}
	item.SetDelta(doc.FromMarkdown("[Kyoto](https://kyoto.travel)"))
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Trip", Content: doc.FromBlocks([]*doc.Node{head, item})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	tt.Frame()
	var copyPlain *mygo.MenuItem
	for _, it := range a.editMenu() {
		if it.Label == locales.EditorCopyPlainText() {
			copyPlain = it
		}
	}
	if copyPlain == nil {
		t.Fatal("no Copy as Plain Text in the Edit menu")
	}
	a.page.editor.Select(editor.Pos{ID: "h"}, editor.Pos{ID: "i", Off: 5})
	tt.Frame()
	copyPlain.Click(copyPlain, nil)
	if got := tt.Clipboard(); got != "" {
		t.Errorf("copied %q without the focus", got)
	}
	a.page.editor.Focus()
	tt.Frame()
	copyPlain.Click(copyPlain, nil)
	if formats := tt.ClipboardData().Formats(); len(formats) != 1 || formats[0] != transfer.Text {
		t.Errorf("copied as %v", formats)
	}
	if got := tt.Clipboard(); got != "Trip plan\nKyoto" {
		t.Errorf("copied %q", got)
	}
}

func TestExportMenus(t *testing.T) {
	a := templatesApp(t)
	var export *mygo.MenuItem
	for _, it := range a.menu(&mygo.MenuItem{Label: "Check for Updates…"})[1].Submenu {
		if it.Label == locales.AppMenuExport() {
			export = it
		}
	}
	if export == nil {
		t.Fatal("no Export in the File menu")
	}
	var labels []string
	for _, it := range export.Submenu {
		labels = append(labels, it.Label)
	}
	if !slices.Equal(labels, []string{locales.AppMenuExportMarkdown(), locales.AppMenuExportPDF()}) {
		t.Errorf("File › Export has %v", labels)
	}

	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	a.reloadAll()
	for _, header := range []bool{false, true} {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Button(c.Key("menu"), "Menu").Menu(func(m *ui.Menu) { a.pageMenu(m, *a.pageByID(p.ID), header) })
		}, 300, 200)
		tt.Frame()
		if err := tt.Click("Menu"); err != nil {
			t.Fatal(err)
		}
		menu := tt.Menu()
		if !slices.Contains(menu, locales.SidebarExport()) || slices.Contains(menu, locales.SidebarExportMarkdown()) {
			t.Errorf("header %v: menu %v", header, menu)
		}
		if err := tt.ChooseMenuItem(locales.SidebarExport()); err == nil || !strings.Contains(err.Error(), "opens a submenu") {
			t.Errorf("header %v: Export is no submenu: %v", header, err)
		}
	}
}
