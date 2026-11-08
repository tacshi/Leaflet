package sync

import (
	"errors"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// typeWhileStoring saves an edit to block b once, between a merge and its
// store, standing in for the user typing then.
func typeWhileStoring(t *testing.T, st *store.Store, pageID string) *bool {
	t.Helper()
	typed := false
	was := beforeStore
	beforeStore = func(id string) {
		if typed || id != pageID {
			return
		}
		typed = true
		now := st.Content(id)
		now.Find("b").SetDelta(doc.Delta{{Text: "two, typed meanwhile"}})
		if _, _, err := st.SaveContent(id, now); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeStore = was })
	return &typed
}

// A merge stored while the user typed goes again over what they typed, and
// the app hears of the content once, as it is stored.
func TestStoreMergeGoesAgainOverASave(t *testing.T) {
	st := openStore(t)
	svc := New(st, nil)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan", Content: &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")}}})
	if err != nil {
		t.Fatal(err)
	}
	told := 0
	svc.OnPageContent = func(string) { told++ }
	typed := typeWhileStoring(t, st, p.ID)
	merges := 0
	before, stored, err := svc.storeMerge(p.ID, func(row store.ContentRow, kept bool) (*doc.Content, error) {
		merges++
		c := doc.MustDecode(row.Content)
		c.Find("a").SetDelta(doc.Delta{{Text: "one, merged"}})
		return c, nil
	}, func(over string, c *doc.Content) error {
		return st.PutSyncedContentOver(p.ID, over, c, c.Encode(), time.Now(), 1)
	})
	if err != nil || !*typed || merges != 2 {
		t.Fatalf("typed %v, %d merges: %v", *typed, merges, err)
	}
	c := st.Content(p.ID)
	if textOf(c, "a") != "one, merged" || textOf(c, "b") != "two, typed meanwhile" || stored.Encode() != c.Encode() {
		t.Errorf("stored %q, %q", textOf(c, "a"), textOf(c, "b"))
	}
	if textOf(doc.MustDecode(before), "b") != "two, typed meanwhile" || told != 1 {
		t.Errorf("before %q; told %d times", before, told)
	}
}

// A merge of a page that keeps changing while it merges is never stored:
// it gives up after three tries, and the app hears of nothing.
func TestStoreMergeGivesUpOnAPageThatKeepsChanging(t *testing.T) {
	st := openStore(t)
	svc := New(st, nil)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan", Content: &doc.Content{Children: []*doc.Node{para("a", "one")}}})
	if err != nil {
		t.Fatal(err)
	}
	told := 0
	svc.OnPageContent = func(string) { told++ }
	was := beforeStore
	beforeStore = func(id string) {
		now := st.Content(id)
		now.Find("a").SetDelta(doc.Delta{{Text: textOf(now, "a") + "!"}})
		if _, _, err := st.SaveContent(id, now); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeStore = was })
	merges := 0
	_, stored, err := svc.storeMerge(p.ID, func(row store.ContentRow, kept bool) (*doc.Content, error) {
		merges++
		return doc.MustDecode(row.Content), nil
	}, func(over string, c *doc.Content) error {
		return st.PutSyncedContentOver(p.ID, over, c, c.Encode(), time.Now(), 1)
	})
	if !errors.Is(err, errKeptChanging) || stored != nil || merges != 3 || told != 0 {
		t.Errorf("%d merges, stored %v, told %d times: %v", merges, stored, told, err)
	}
	if got := textOf(st.Content(p.ID), "a"); got != "one!!!" {
		t.Errorf("content %q", got)
	}
}

// A merge with nothing to store, or that stores the content as it is,
// leaves the page and tells the app nothing; a page with no content here
// merges as none.
func TestStoreMergeOfNothing(t *testing.T) {
	st := openStore(t)
	svc := New(st, nil)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan", Content: &doc.Content{Children: []*doc.Node{para("a", "one")}}})
	if err != nil {
		t.Fatal(err)
	}
	told := 0
	svc.OnPageContent = func(string) { told++ }
	put := func(over string, c *doc.Content) error {
		return st.PutSyncedContentOver(p.ID, over, c, c.Encode(), time.Now(), 1)
	}
	row, _ := st.ContentRow(p.ID)
	before, stored, err := svc.storeMerge(p.ID, func(store.ContentRow, bool) (*doc.Content, error) { return nil, nil }, put)
	if err != nil || stored != nil || before != row.Content {
		t.Errorf("stored %v over %q: %v", stored, before, err)
	}
	if _, _, err := svc.storeMerge(p.ID, func(row store.ContentRow, _ bool) (*doc.Content, error) {
		return doc.MustDecode(row.Content), nil
	}, put); err != nil || told != 0 {
		t.Errorf("told %d times: %v", told, err)
	}
	kept := true
	if _, _, err := svc.storeMerge("00000000-0000-0000-0000-0000000000ab", func(_ store.ContentRow, k bool) (*doc.Content, error) {
		kept = k
		return nil, nil
	}, put); err != nil || kept {
		t.Errorf("a page with no content kept %v: %v", kept, err)
	}
}

// unreadableValue has the value kept under key fail to read, as a disk
// that fails part way does, though it can still be written.
func unreadableValue(t *testing.T, st *store.Store, key string) {
	t.Helper()
	for _, q := range []string{
		`ALTER TABLE key_values RENAME TO key_values_kept`,
		`CREATE TABLE key_values (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO key_values SELECT key, value FROM key_values_kept`,
		`DROP TABLE key_values_kept`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().Exec(`INSERT INTO key_values (key, value) VALUES (?, NULL)
		ON CONFLICT(key) DO UPDATE SET value = NULL`, key); err != nil {
		t.Fatal(err)
	}
}

// A set of pages that wait for their content, which cannot be read, is
// not written over with only the pages added to it: they would stop
// waiting.
func TestWaitingPagesUnreadAreNotWrittenOver(t *testing.T) {
	st := openStore(t)
	svc := New(st, remote.New(remote.Settings{}, st))
	unreadableValue(t, st, "sync.content_waiting")
	if err := svc.waitForContent([]string{"p1"}); err == nil {
		t.Error("pages added to a set not read")
	}
	if _, _, err := st.Lookup("sync.content_waiting"); err == nil {
		t.Error("the set was written over")
	}
}
