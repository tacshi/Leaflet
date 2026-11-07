package store

import (
	"os"
	"path/filepath"
	gosync "sync"
	"testing"

	"leaflet/internal/doc"
)

// The app and `leaflet mcp` making folders at the same time give them keys
// of their own, each after the last.
func TestCreateFolderKeysAreTheirOwn(t *testing.T) {
	dir := t.TempDir()
	var stores []*Store
	for range 2 {
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		stores = append(stores, s)
	}
	var wg gosync.WaitGroup
	for _, s := range stores {
		wg.Go(func() {
			for range 20 {
				if _, err := s.CreateFolder("f"); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	folders, err := stores[0].Folders()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, f := range folders {
		if keys[f.SortKey] {
			t.Errorf("two folders keyed %q", f.SortKey)
		}
		keys[f.SortKey] = true
	}
	if len(folders) != 40 {
		t.Errorf("%d folders", len(folders))
	}
}

// fail makes the database refuse a write to a table, as a full disk or
// a locked database would, by a trigger raising an error.
func fail(t *testing.T, s *Store, when, table string) {
	t.Helper()
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_` + table + ` BEFORE ` + when + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
}

// A Wipe that fails part way leaves everything as it was: a queue left
// without its pages, or pages without their queue, would go up as the
// next account's.
func TestWipeIsAllOrNothing(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{Title: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetValue("k", "v"); err != nil {
		t.Fatal(err)
	}
	fail(t, s, "DELETE", "key_values")
	if err := s.Wipe(); err == nil {
		t.Fatal("wiped past a refused delete")
	}
	if got, err := s.Page(p.ID); err != nil || got.Title != "kept" {
		t.Errorf("page %+v, %v", got, err)
	}
	var queued int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&queued); err != nil || queued == 0 {
		t.Errorf("queue of %d, %v", queued, err)
	}
}

// A write that fails tells no one of a change.
func TestFailedWritesNotifyNoOne(t *testing.T) {
	s := open(t)
	p, err := s.CreatePage(NewPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutProvider(Provider{ID: "ollama", Preset: "ollama", Name: "Ollama"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddConflict(p.ID, doc.Conflict{BlockID: "b"}); err != nil {
		t.Fatal(err)
	}
	conflicts, err := s.Conflicts()
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts %+v, %v", conflicts, err)
	}
	if err := s.PutSpace("a", "Old", "me"); err != nil {
		t.Fatal(err)
	}
	fail(t, s, "UPDATE", "pages")
	fail(t, s, "DELETE", "model_providers")
	fail(t, s, "DELETE", "conflicts")
	fail(t, s, "DELETE", "spaces")
	var told []string
	s.Subscribe(func(c Change) { told = append(told, c.Table) })
	for name, write := range map[string]func() error{
		"SetKeepOffline":  func() error { return s.SetKeepOffline(p.ID, true) },
		"DeleteProvider":  func() error { return s.DeleteProvider("ollama") },
		"ResolveConflict": func() error { return s.ResolveConflict(conflicts[0].ID) },
		"MoveSpace":       func() error { return s.MoveSpace("a", "b") },
	} {
		told = nil
		if err := write(); err == nil {
			t.Errorf("%s: no error", name)
		}
		if len(told) > 0 {
			t.Errorf("%s told of %v", name, told)
		}
	}
}

// A search whose rows fail part way says so, rather than answer with the
// hits it read before.
func TestSearchFailsWithItsRows(t *testing.T) {
	s := open(t)
	for _, text := range []string{"apple one", "apple two"} {
		p, err := s.CreatePage(NewPageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		c := doc.FromBlocks([]*doc.Node{{ID: doc.NewBlockID(), Type: "paragraph", Data: map[string]any{"delta": doc.Delta{{Text: text}}.JSON()}}})
		if _, _, err := s.SaveContent(p.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	if hits, err := s.Search("apple", 10); err != nil || len(hits) != 2 {
		t.Fatalf("hits %+v, %v", hits, err)
	}
	// A row the database fails to read, as json_extract of what is not
	// JSON fails, as the second is stepped to.
	for _, q := range []string{
		`ALTER TABLE page_contents RENAME TO contents`,
		`CREATE VIEW page_contents AS SELECT page_id, CASE WHEN plain_text LIKE '%two%' THEN json_extract('not json', '$') ELSE plain_text END AS plain_text FROM contents`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if hits, err := s.Search("apple", 10); err == nil {
		t.Errorf("hits %+v, no error", hits)
	}
}

// A default model whose provider is gone, as one removed on another
// device, reads as none, so the next provider added becomes the default;
// it reads as set again once the provider is back.
func TestDefaultModelOfAProviderGoneIsNone(t *testing.T) {
	s := open(t)
	settings := s.Settings()
	settings.DefaultModel = ModelChoice{ProviderID: "p1"}.Encode()
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Settings().Default(); ok {
		t.Error("a default with no provider")
	}
	if err := s.PutProvider(Provider{ID: "p1", Preset: "openai", Name: "OpenAI", AuthKind: "api_key"}); err != nil {
		t.Fatal(err)
	}
	if def, ok := s.Settings().Default(); !ok || def.ProviderID != "p1" {
		t.Errorf("default %v, %v", def, ok)
	}
}

// A file copied into the app is whole or not there: one that cannot be
// read to the end leaves nothing, and one that can leaves only itself, as
// does one downloaded over it.
func TestFileCopiesAreWholeOrNone(t *testing.T) {
	s := open(t)
	// A folder opens as a file does, and fails as it is read.
	if _, err := s.AddFile(t.TempDir(), "", "", ""); err == nil {
		t.Fatal("added a folder")
	}
	if left, _ := os.ReadDir(filepath.Join(s.Dir, "files")); len(left) != 0 {
		t.Errorf("left %v", left)
	}
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DownloadedFile(f.ID, []byte("hello again")); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(filepath.Dir(f.LocalPath))
	data, _ := os.ReadFile(f.LocalPath)
	if len(left) != 1 || string(data) != "hello again" || f.SizeBytes != 5 {
		t.Errorf("left %v, %q, size %d", left, data, f.SizeBytes)
	}
}

// A conflict met again, as a merge tried again meets it, is recorded once;
// one taken back goes.
func TestHoldConflictsOnce(t *testing.T) {
	st := open(t)
	p, err := st.CreatePage(NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	c := doc.Conflict{BlockID: "a"}
	first, err := st.HoldConflicts(p.ID, []doc.Conflict{c})
	if err != nil || len(first) != 1 {
		t.Fatalf("first hold: %v, %v", first, err)
	}
	if again, err := st.HoldConflicts(p.ID, []doc.Conflict{c}); err != nil || len(again) != 0 {
		t.Fatalf("held again: %v, %v", again, err)
	}
	if rows, _ := st.Conflicts(); len(rows) != 1 {
		t.Fatalf("%d conflicts, want 1", len(rows))
	}
	if err := st.DropConflicts(first); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.Conflicts(); len(rows) != 0 {
		t.Errorf("%d conflicts after dropping, want 0", len(rows))
	}
}
