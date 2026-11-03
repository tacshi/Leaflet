package sync

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"leaflet/internal/store"
)

// A row of each synced table, read as the other devices have it, is stored
// on another device as it was: a comment with its author's name.
func TestSyncedRowsGoAsTheyAre(t *testing.T) {
	from, to := New(openStore(t), nil), New(openStore(t), nil)
	p, err := from.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	folder, err := from.st.CreateFolder("Folder")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := from.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	c, err := from.st.AddComment(p.ID, "blk", "Look here", "", "", "", "Sam")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := from.st.CreateRecording(p.ID, "blk", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{"pages": p.ID, "folders": folder.ID, "files": f.ID, "comments": c.ID, "recordings": rec.ID}
	if len(syncedTables) != len(ids) {
		t.Fatalf("synced tables %v", slices.Sorted(maps.Keys(syncedTables)))
	}
	for table, id := range ids {
		row, ok, err := from.localRow(table, id)
		if err != nil || !ok {
			t.Fatalf("%s: %v, %v", table, ok, err)
		}
		if err := to.putRow(table, row, to.st.PutRemotePage); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		got, ok, err := to.localRow(table, id)
		if err != nil || !ok || !reflect.DeepEqual(got, row) {
			t.Errorf("%s stored as %v, %v, %v; sent %v", table, got, ok, err, row)
		}
	}
	if got, _, _ := to.localRow("comments", c.ID); got["author_name"] != "Sam" {
		t.Errorf("comment by %v", got["author_name"])
	}
}

// A table whose rows stay on each device has no row to send, and stores
// none.
func TestUnsyncedTableHasNoRows(t *testing.T) {
	svc := New(openStore(t), nil)
	if synced("page_history") {
		t.Error("page history syncs")
	}
	if row, ok, err := svc.localRow("page_history", "h1"); row != nil || ok || err != nil {
		t.Errorf("row %v, %v, %v", row, ok, err)
	}
	stored := false
	put := func(map[string]any) error { stored = true; return nil }
	if err := svc.putRow("page_history", map[string]any{"id": "h1"}, put); err != nil || stored {
		t.Errorf("stored %v, %v", stored, err)
	}
	if _, ok, err := svc.localRow("pages", "00000000-0000-0000-0000-0000000000ff"); ok || err != nil {
		t.Errorf("a page that is not here: %v, %v", ok, err)
	}
}
