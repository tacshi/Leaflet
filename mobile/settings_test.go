package mobile

import (
	"archive/zip"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
)

// Settings › Account, Sync and storage, the Tags tab and the byline, as
// the phone has them.

// The name is saved on this device, without the spaces around it; none
// changes nothing. Signed out, there is no account to delete.
func TestDisplayName(t *testing.T) {
	c, _ := open(t)
	if err := c.SetDisplayName("  Ada  "); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDisplayName("   "); err != nil {
		t.Fatal(err)
	}
	if got := account0(t, c).DisplayName; got != "Ada" {
		t.Errorf("name %q, want Ada", got)
	}
	if err := c.DeleteAccount(); err != nil {
		t.Errorf("deleting no account: %v", err)
	}
}

// Export all writes a zip of every page, the open page's edits saved
// first, and its templates in a folder of their own.
func TestExportAll(t *testing.T) {
	c, _ := open(t)
	trip := create(t, c, `{"title":"Trip"}`)
	raw, err := c.OpenPage(trip.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pg openedPage
	_ = json.Unmarshal([]byte(raw), &pg)
	pg.Content.Children = []*doc.Node{paragraph("p", "Socks")}
	if err := c.EditPage(trip.ID, pg.Editor, pg.Content.Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SaveAsTemplate(trip.ID); err != nil {
		t.Fatal(err)
	}
	raw, err = c.ExportAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var out exportedAll
	_ = json.Unmarshal([]byte(raw), &out)
	if out.Name != "Leaflet pages.zip" || filepath.Base(out.Path) != out.Name || out.Missing != 0 {
		t.Fatalf("export %+v", out)
	}
	z, err := zip.OpenReader(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
		if f.Name == "Trip.md" {
			r, _ := f.Open()
			b := make([]byte, 256)
			n, _ := r.Read(b)
			_ = r.Close()
			if !strings.Contains(string(b[:n]), "Socks") {
				t.Errorf("Trip.md misses the edit: %q", b[:n])
			}
		}
	}
	if !slices.Contains(names, "Trip.md") || !slices.Contains(names, "Templates/Trip.md") {
		t.Errorf("zip has %q", names)
	}
}

// A tag renamed is renamed on its pages, merging into one of that name; a
// tag deleted comes back on Undo.
func TestRenameAndDeleteTags(t *testing.T) {
	c, _ := open(t)
	a := create(t, c, `{"title":"A"}`)
	b := create(t, c, `{"title":"B"}`)
	_ = c.SetPageTags(a.ID, `["work"]`)
	_ = c.SetPageTags(b.ID, `["work","home"]`)
	tagsOf := func() map[string]int {
		raw, err := c.Library()
		if err != nil {
			t.Fatal(err)
		}
		var lib library
		_ = json.Unmarshal([]byte(raw), &lib)
		out := map[string]int{}
		for _, tg := range lib.Tags {
			out[tg.Tag] = tg.Count
		}
		return out
	}
	name, err := c.RenameTag("work", " #home ")
	if err != nil || name != "home" {
		t.Fatalf("renamed to %q, %v", name, err)
	}
	if got := tagsOf(); got["home"] != 2 || got["work"] != 0 {
		t.Fatalf("tags %v", got)
	}
	if name, err := c.RenameTag("home", "  "); err != nil || name != "" {
		t.Fatalf("renamed to nothing: %q, %v", name, err)
	}
	before, err := c.DeleteTag("home")
	if err != nil {
		t.Fatal(err)
	}
	if got := tagsOf(); got["home"] != 0 {
		t.Fatalf("tags after deleting %v", got)
	}
	if err := c.RestoreTags(before); err != nil {
		t.Fatal(err)
	}
	if got := tagsOf(); got["home"] != 2 {
		t.Fatalf("tags after Undo %v", got)
	}
}

func conflicts(t *testing.T, c *Core) []conflictRow {
	t.Helper()
	raw, err := c.Conflicts()
	if err != nil {
		t.Fatal(err)
	}
	var out []conflictRow
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// A conflict lists the text kept over the other side's; this device's
// version can be put back, the page as it was kept in its history, and a
// block one device deleted can be deleted again.
func TestResolveConflicts(t *testing.T) {
	c, _ := open(t)
	p := create(t, c, `{"title":"Plans"}`)
	if err := c.editContent(p.ID, func(content *doc.Content) bool {
		content.Children = []*doc.Node{paragraph("b1", "Theirs"), paragraph("b2", "Edited there")}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	st := c.core.Store
	now := time.Now().UnixMilli()
	if err := st.AddConflict(p.ID, doc.Conflict{BlockID: "b1", Local: paragraph("b1", "Mine"), Remote: paragraph("b1", "Theirs"),
		LocalEditedAt: now - 1000, RemoteEditedAt: now}); err != nil {
		t.Fatal(err)
	}
	list := conflicts(t, c)
	if len(list) != 1 || list[0].Kept != "Theirs" || list[0].Other != "Mine" || list[0].Deleted != "" || list[0].PageTitle != "Plans" {
		t.Fatalf("conflicts %+v", list)
	}
	if err := c.ResolveConflict(list[0].ID, resolveThisDevice); err != nil {
		t.Fatal(err)
	}
	if got := texts(content(t, c, p.ID)); !strings.Contains(got, "Mine") || strings.Contains(got, "Theirs") {
		t.Fatalf("put back %q", got)
	}
	if raw, _ := c.History(p.ID); !strings.Contains(raw, "conflict") && !strings.Contains(raw, "resolving") {
		t.Errorf("history %s", raw)
	}
	if list := conflicts(t, c); len(list) != 0 {
		t.Fatalf("resolved, still %+v", list)
	}

	if err := st.AddConflict(p.ID, doc.Conflict{BlockID: "b2", Local: paragraph("b2", "Edited there")}); err != nil {
		t.Fatal(err)
	}
	list = conflicts(t, c)
	if len(list) != 1 || list[0].Deleted != "there" || list[0].Kept != "Edited there" {
		t.Fatalf("conflicts %+v", list)
	}
	if err := c.ResolveConflict(list[0].ID, resolveDelete); err != nil {
		t.Fatal(err)
	}
	if got := texts(content(t, c, p.ID)); strings.Contains(got, "Edited there") {
		t.Fatalf("deleted again, still %q", got)
	}
}

// An edit says the page is saving, and the save shortly after says it no
// longer is; a page no one shares has no members.
func TestPageSavingAndMembers(t *testing.T) {
	c, ev := open(t)
	p := create(t, c, `{"title":"Notes"}`)
	raw, err := c.OpenPage(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pg openedPage
	_ = json.Unmarshal([]byte(raw), &pg)
	pg.Content.Children = []*doc.Node{paragraph("p", "Hello")}
	if err := c.EditPage(p.ID, pg.Editor, pg.Content.Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, false} {
		select {
		case got := <-ev.saving:
			if got != want {
				t.Fatalf("saving %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no PageSaving(%v)", want)
		}
	}
	if raw, err := c.Members(p.ID); err != nil || raw != "[]" {
		t.Errorf("members %s, %v", raw, err)
	}
}
