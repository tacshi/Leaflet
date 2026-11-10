package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/store"
	"leaflet/locales"
)

func templateRows(t *testing.T, c *Core) []templateRow {
	t.Helper()
	raw, err := c.Templates()
	if err != nil {
		t.Fatal(err)
	}
	var out []templateRow
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func content(t *testing.T, c *Core, id string) *doc.Content {
	t.Helper()
	raw, err := c.OpenPage(id)
	if err != nil {
		t.Fatal(err)
	}
	var pg openedPage
	_ = json.Unmarshal([]byte(raw), &pg)
	return pg.Content
}

func closeEditors(c *Core, id string) {
	var editors []int64
	c.q.do(func() {
		if p := c.pages[id]; p != nil {
			for e := range p.editors {
				editors = append(editors, e)
			}
		}
	})
	for _, e := range editors {
		c.ClosePage(id, e)
	}
}

func TestTemplatesStartPages(t *testing.T) {
	c, _ := open(t)
	var meeting templateRow
	for _, r := range templateRows(t, c) {
		if r.Key == "starter:meeting" {
			meeting = r
		}
	}
	if meeting.Hint == "" || meeting.Icon == "" {
		t.Fatalf("no meeting starter: %+v", meeting)
	}

	page := create(t, c, `{}`)
	title, err := c.ApplyTemplate(page.ID, meeting.Key)
	if err != nil || title != meeting.Title {
		t.Fatalf("applied %q, %v", title, err)
	}
	if len(content(t, c, page.ID).Children) < 2 {
		t.Fatal("the page took no blocks")
	}
	if title, _ := c.ApplyTemplate(page.ID, meeting.Key); title != "" {
		t.Fatalf("applied again: %q", title)
	}

	id, err := c.EditTemplate(meeting.Key)
	if err != nil || id == "" {
		t.Fatal(err)
	}
	for _, r := range templateRows(t, c) {
		if r.Key == meeting.Key {
			t.Fatal("the starter still shows beside its template")
		}
	}

	folder, _ := c.CreateFolder("Meetings")
	var f folderRow
	_ = json.Unmarshal([]byte(folder), &f)
	if _, err := c.SetDefaultTemplate("", f.ID, "template:"+id); err != nil {
		t.Fatal(err)
	}
	started := create(t, c, `{"start":true,"folder_id":"`+f.ID+`"}`)
	if started.Title != meeting.Title || len(content(t, c, started.ID).Children) < 2 {
		t.Fatalf("started %+v", started)
	}
}

func TestCopiesHistoryAndExport(t *testing.T) {
	c, _ := open(t)
	parent := create(t, c, `{"title":"Trip"}`)
	child := create(t, c, `{"title":"Packing","link":true,"parent_id":"`+parent.ID+`"}`)

	raw, err := c.DuplicatePage(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cp pageRow
	_ = json.Unmarshal([]byte(raw), &cp)
	if cp.ParentID != parent.ID || cp.Title == child.Title {
		t.Fatalf("copy %+v", cp)
	}
	if got := links(t, c, parent.ID); len(got) != 2 || got[1] != cp.ID {
		t.Fatalf("links %v", got)
	}

	raw, err = c.SaveAsTemplate(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tpl pageRow
	_ = json.Unmarshal([]byte(raw), &tpl)
	if !tpl.Template || tpl.ParentID != "" {
		t.Fatalf("template %+v", tpl)
	}

	closeEditors(c, child.ID)
	if err := c.NameVersion(child.ID, "Empty"); err != nil {
		t.Fatal(err)
	}
	raw, _ = c.History(child.ID)
	var versions []version
	_ = json.Unmarshal([]byte(raw), &versions)
	if len(versions) == 0 || versions[0].Label != "Empty" {
		t.Fatalf("history %s", raw)
	}
	if err := c.editContent(child.ID, func(content *doc.Content) bool {
		content.Children = []*doc.Node{paragraph("p", "Socks")}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RestoreVersion(child.ID, versions[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := texts(content(t, c, child.ID)); strings.Contains(got, "Socks") {
		t.Fatalf("restored %q", got)
	}

	raw, err = c.ExportMarkdown(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var md exported
	_ = json.Unmarshal([]byte(raw), &md)
	if md.Name != "Trip.md" || !strings.HasPrefix(md.Markdown, "# Trip") {
		t.Fatalf("export %+v", md)
	}
	dir := t.TempDir()
	for region, width := range map[string]float64{"US": 8.5, "DE": 8.27, "": 8.27} {
		raw, err = c.ExportPDFPage(parent.ID, region, dir)
		if err != nil {
			t.Fatal(err)
		}
		var pdf exportedPDF
		_ = json.Unmarshal([]byte(raw), &pdf)
		data, err := os.ReadFile(pdf.Path) // #nosec G304
		html := string(data)
		if err != nil || filepath.Dir(pdf.Path) != dir || pdf.Name != "Trip.pdf" || pdf.Width != width ||
			!strings.Contains(html, `<h1 class="title">Trip</h1>`) || !strings.Contains(html, ">Packing</p>") ||
			strings.Contains(html, "prefers-color-scheme") {
			t.Fatalf("PDF for %q: %+v, %v", region, pdf, err)
		}
	}
	if pages, _ := filepath.Glob(filepath.Join(dir, "*.html")); len(pages) != 3 {
		t.Fatalf("%d pages written for 3 PDFs", len(pages))
	}

	path := filepath.Join(t.TempDir(), "notes.txt")
	_ = os.WriteFile(path, []byte("hi"), 0o600)
	raw, err = c.AddFile(parent.ID, path)
	if err != nil {
		t.Fatal(err)
	}
	var n doc.Node
	_ = json.Unmarshal([]byte(raw), &n)
	if n.Type != "file" || n.Str("name") != "notes.txt" {
		t.Fatalf("file block %s", raw)
	}

	local, err := c.LocalFile(n.Str("file_id"), 0)
	if data, _ := os.ReadFile(local); err != nil || string(data) != "hi" { // #nosec G304
		t.Fatalf("local file %q, %v", local, err)
	}
	_ = os.Remove(local)
	if _, err := c.LocalFile(n.Str("file_id"), 0); err == nil {
		t.Fatal("a file neither here nor uploaded was found")
	}
}

func TestNoCopyWhileRecording(t *testing.T) {
	c, _ := open(t)
	p := create(t, c, `{"title":"Interview"}`)
	recording := func(on bool, id string) {
		c.q.do(func() { c.core.Rec.State.Active, c.core.Rec.State.PageID = on, id })
	}
	wait := locales.TemplateWaitForRecording()
	recording(true, p.ID)
	if _, err := c.DuplicatePage(p.ID); err == nil || err.Error() != wait {
		t.Fatalf("duplicated while recording: %v", err)
	}
	if _, err := c.SaveAsTemplate(p.ID); err == nil || err.Error() != wait {
		t.Fatalf("saved as a template while recording: %v", err)
	}
	recording(false, "")
	raw, err := c.SaveAsTemplate(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tpl pageRow
	_ = json.Unmarshal([]byte(raw), &tpl)

	recording(true, tpl.ID)
	page := create(t, c, `{}`)
	if _, err := c.ApplyTemplate(page.ID, "template:"+tpl.ID); err == nil || err.Error() != wait {
		t.Fatalf("used while recording: %v", err)
	}
	recording(false, "")
	if _, err := c.ApplyTemplate(page.ID, "template:"+tpl.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOneCopyAtATime(t *testing.T) {
	c, _ := open(t)
	p := create(t, c, `{"title":"Notes"}`)
	raw, err := c.SaveAsTemplate(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tpl pageRow
	_ = json.Unmarshal([]byte(raw), &tpl)
	page := create(t, c, `{}`)

	c.copying.Store(true)
	other := locales.TemplateFetchingOther()
	if _, err := c.DuplicatePage(p.ID); err == nil || err.Error() != other {
		t.Errorf("duplicated while another copy is made: %v", err)
	}
	if _, err := c.SaveAsTemplate(p.ID); err == nil || err.Error() != other {
		t.Errorf("saved as a template while another copy is made: %v", err)
	}
	if _, err := c.ApplyTemplate(page.ID, "template:"+tpl.ID); err == nil || err.Error() != other {
		t.Errorf("used a template while another copy is made: %v", err)
	}
	if !content(t, c, page.ID).IsEmpty() {
		t.Fatal("the page took the template while another copy was made")
	}
	if title, err := c.ApplyTemplate(page.ID, "starter:meeting"); err != nil || title == "" {
		t.Errorf("a starter while a copy is made: %q, %v", title, err)
	}

	c.copying.Store(false)
	if _, err := c.DuplicatePage(p.ID); err != nil {
		t.Errorf("not duplicated once the other copy is made: %v", err)
	}
}

func TestFailedCopyDropsTheAudioItFetched(t *testing.T) {
	c, _ := open(t)
	row := create(t, c, `{"title":"Standup"}`)
	root := t.TempDir()
	var ids []string
	var err error
	c.q.do(func() {
		st := c.core.Store
		s := st.Settings()
		s.KeepAudioOnDevice = "until_uploaded"
		if err = st.SaveSettings(s); err != nil {
			return
		}
		if err = st.SetValue("icloud.space", "s1"); err != nil {
			return
		}
		c.core.Sync.SetICloudRoot(root)
		var blocks []*doc.Node
		for range 2 {
			blockID := doc.NewBlockID()
			var rec store.Recording
			if rec, err = st.CreateRecording(row.ID, blockID, "", "", filepath.Join(root, "gone.wav"), "auto"); err != nil {
				return
			}
			if _, err = st.UpdateRecording(rec.ID, func(r *store.Recording) {
				r.Status, r.AudioPath = "ready", "u1/"+rec.ID+".wav"
			}); err != nil {
				return
			}
			ids = append(ids, rec.ID)
			blocks = append(blocks, &doc.Node{ID: blockID, Type: "recording", Data: map[string]any{"recording_id": rec.ID}})
		}
		_, _, err = st.SaveContent(row.ID, &doc.Content{Children: blocks})
	})
	if err != nil {
		t.Fatal(err)
	}
	rel, _ := icloud.RecordingBlobPath(ids[0])
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DuplicatePage(row.ID); err == nil {
		t.Fatal("copied without the second recording's audio")
	}
	var rec store.Recording
	c.q.do(func() { rec, err = c.core.Store.Recording(ids[0]) })
	if err != nil || rec.LocalPath != "" {
		t.Errorf("the first recording's audio stays at %q, %v", rec.LocalPath, err)
	}
}

func TestCancel(t *testing.T) {
	c, _ := open(t)
	ctx, done := c.cancellable(1, time.Minute)
	defer done()
	c.Cancel(1)
	if ctx.Err() == nil {
		t.Error("a call running was not given up on")
	}
	c.Cancel(2)
	ctx, done2 := c.cancellable(2, time.Minute)
	defer done2()
	if ctx.Err() == nil {
		t.Error("a call given up on before it began runs")
	}
	ctx, done3 := c.cancellable(3, time.Minute)
	defer done3()
	if ctx.Err() != nil {
		t.Error("a call no one gave up on was cancelled")
	}
}

func TestDuplicateKeepsTheCopyWithoutItsCard(t *testing.T) {
	c, ev := open(t)
	parent := create(t, c, `{"title":"Trip"}`)
	child := create(t, c, `{"title":"Packing","link":true,"parent_id":"`+parent.ID+`"}`)
	if _, err := c.core.Store.DB().Exec(`UPDATE page_contents SET content = '{"children": [' WHERE page_id = ?`, parent.ID); err != nil {
		t.Fatal(err)
	}
	c.ev.q.do(func() {})
	for len(ev.notices) > 0 {
		<-ev.notices
	}
	raw, err := c.DuplicatePage(child.ID)
	if err != nil {
		t.Fatalf("the copy was made and said it wasn't: %v", err)
	}
	c.ev.q.do(func() {})
	var cp pageRow
	_ = json.Unmarshal([]byte(raw), &cp)
	if cp.ID == "" || cp.ParentID != parent.ID {
		t.Fatalf("copy %+v", cp)
	}
	select {
	case n := <-ev.notices:
		if n == "" {
			t.Error("an empty notice")
		}
	default:
		t.Error("no notice of the card missing")
	}
}
