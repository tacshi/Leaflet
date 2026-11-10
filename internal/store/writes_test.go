package store

import (
	"os"
	"path/filepath"
	gosync "sync"
	"testing"

	"leaflet/internal/doc"
)

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

func fail(t *testing.T, s *Store, when, table string) {
	t.Helper()
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_` + table + ` BEFORE ` + when + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
}

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
	fail(t, s, "UPDATE", "conflicts")
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

func TestSpeechProviders(t *testing.T) {
	s := open(t)
	for provider, want := range map[string]string{
		"moss": "moss", "soniox": "soniox", "on-device": "on-device", "whisperd": "openai-compatible",
	} {
		settings := s.Settings()
		settings.Stt.Provider = provider
		if err := s.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
		if got := s.Settings().Stt.Provider; got != want {
			t.Errorf("%s reads as %s, want %s", provider, got, want)
		}
	}
}

func TestOldSonioxModelsMoveToTheCurrentOne(t *testing.T) {
	s := open(t)
	for model, want := range map[string]string{
		"stt-rt-v5": SonioxModel, "stt-rt-v3": SonioxModel, "stt-rt-preview-v2": SonioxModel,
		"stt-rt-v6": "stt-rt-v6", "stt-rt-custom": "stt-rt-custom", "": SonioxModel,
	} {
		settings := s.Settings()
		settings.Stt.SonioxModel = model
		if err := s.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
		if got := s.Settings().Stt.SonioxModel; got != want {
			t.Errorf("%q reads as %q, want %q", model, got, want)
		}
	}
}

func TestFileCopiesAreWholeOrNone(t *testing.T) {
	s := open(t)
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
