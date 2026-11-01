package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"leaflet/internal/doc"
)

func templates(t *testing.T, c *Core) []templateRow {
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

// closeEditors closes every editor content opened of page id, and so the
// page.
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
	for _, r := range templates(t, c) {
		if r.Key == "starter:meeting" {
			meeting = r
		}
	}
	if meeting.Hint == "" || meeting.Icon == "" {
		t.Fatalf("no meeting starter: %+v", meeting)
	}

	// An empty, untitled page takes a starter's blocks and title.
	page := create(t, c, `{}`)
	title, err := c.ApplyTemplate(page.ID, meeting.Key)
	if err != nil || title != meeting.Title {
		t.Fatalf("applied %q, %v", title, err)
	}
	if len(content(t, c, page.ID).Children) < 2 {
		t.Fatal("the page took no blocks")
	}
	// A page with something in it is left as it is.
	if title, _ := c.ApplyTemplate(page.ID, meeting.Key); title != "" {
		t.Fatalf("applied again: %q", title)
	}

	// Editing a starter makes it the user's own, in its place.
	id, err := c.EditTemplate(meeting.Key)
	if err != nil || id == "" {
		t.Fatal(err)
	}
	for _, r := range templates(t, c) {
		if r.Key == meeting.Key {
			t.Fatal("the starter still shows beside its template")
		}
	}

	// A folder's default template fills the pages started in it.
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

	// A named version comes back on restore, the page as it was kept too.
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
	if err := c.RestoreVersion(child.ID, versions[0].ID); err != nil {
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

	// The file is found by its ID, where it is kept here; gone from here
	// and never uploaded, it can't be had.
	local, err := c.LocalFile(n.Str("file_id"))
	if data, _ := os.ReadFile(local); err != nil || string(data) != "hi" {
		t.Fatalf("local file %q, %v", local, err)
	}
	_ = os.Remove(local)
	if _, err := c.LocalFile(n.Str("file_id")); err == nil {
		t.Fatal("a file neither here nor uploaded was found")
	}
}
