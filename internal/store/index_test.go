package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
)

func textPage(t *testing.T, s *Store, title, text string) Page {
	t.Helper()
	n := &doc.Node{Type: "paragraph"}
	n.SetDelta(doc.Delta{{Text: text}})
	p, err := s.CreatePage(NewPageOptions{Title: title, Content: doc.FromBlocks([]*doc.Node{n})})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func titlesOf(hits []SearchHit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Kind+":"+h.Title)
	}
	slices.Sort(out)
	return out
}

func searchFor(t *testing.T, s *Store, q string) []string {
	t.Helper()
	hits, err := s.Search(q, 20)
	if err != nil {
		t.Fatal(err)
	}
	return titlesOf(hits)
}

func TestSearchCandidatesWhileIndexing(t *testing.T) {
	s := open(t)
	const pages = 60
	for i := range pages {
		textPage(t, s, fmt.Sprintf("Page %d", i), "the quarterly budget")
	}
	ctx := context.Background()
	for range 5 {
		if _, err := s.db.Exec(`UPDATE text_writes SET indexed = NULL`); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			for {
				if all, err := s.indexFor(ctx, time.Millisecond); err != nil || all {
					done <- err
					return
				}
			}
		}()
		for indexing := true; indexing; {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
				indexing = false
			default:
			}
			found, err := s.searchCandidates(ctx, "page", "quarterly")
			if err != nil {
				t.Fatal(err)
			}
			if len(found.ids) != pages {
				t.Fatalf("found %d of the %d pages while indexing", len(found.ids), pages)
			}
		}
	}
}

func TestSearchFollowsWritesThroughItsIndex(t *testing.T) {
	s := open(t)
	p := textPage(t, s, "Notes", "the quarterly budget")
	if got := searchFor(t, s, "budget"); !slices.Equal(got, []string{"page:Notes"}) {
		t.Fatalf("found %v", got)
	}
	c := s.Content(p.ID)
	c.Children[0].SetDelta(doc.Delta{{Text: "the yearly plan"}})
	if _, _, err := s.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	if got := searchFor(t, s, "budget"); len(got) != 0 {
		t.Errorf("found the text gone: %v", got)
	}
	if got := searchFor(t, s, "yearly"); !slices.Equal(got, []string{"page:Notes"}) {
		t.Errorf("found %v", got)
	}
	other := doc.FromBlocks([]*doc.Node{{ID: "x", Type: "paragraph", Data: map[string]any{"delta": doc.Delta{{Text: "an offsite agenda"}}.JSON()}}})
	if _, err := s.DB().Exec(`UPDATE page_contents SET content = ?, plain_text = ? WHERE page_id = ?`, other.Encode(), other.PlainText(), p.ID); err != nil {
		t.Fatal(err)
	}
	if got := searchFor(t, s, "offsite"); !slices.Equal(got, []string{"page:Notes"}) {
		t.Errorf("found %v", got)
	}
	if _, err := s.AddComment(p.ID, "x", "a remark on the venue", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.PutRecording(Recording{ID: doc.NewRowID(), PageID: p.ID, BlockID: "r", Status: "ready",
		Transcript: []Segment{{Text: "we spoke of the venue"}}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("venue", 20)
	var kinds []string
	for _, h := range hits {
		kinds = append(kinds, h.Kind)
	}
	if err != nil || !slices.Equal(kinds, []string{"recording", "comment"}) {
		t.Errorf("found %+v, %v", hits, err)
	}
	var indexed int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM text_writes WHERE indexed = write_id`).Scan(&indexed); err != nil || indexed < 3 {
		t.Errorf("%d indexed, %v", indexed, err)
	}
	if err := s.Wipe(); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM search_index WHERE search_index MATCH '"ven"'`).Scan(&left); err != nil || left != 0 {
		t.Errorf("the index kept %d texts removed, %v", left, err)
	}
}

func TestSearchMatchesAsWritten(t *testing.T) {
	s := open(t)
	for title, text := range map[string]string{
		"JA": "会議の議事録を共有します", "ZH": "项目计划和预算", "DE": "Straße und ÜBER", "Quote": `he said "AND" or NEAR * -x`,
		"Spread": "bud get",
	} {
		textPage(t, s, title, text)
	}
	for q, want := range map[string][]string{
		"会議": {"page:JA"}, "議事録": {"page:JA"}, "计划": {"page:ZH"}, "über": {"page:DE"}, "STRASSE": nil, "straße": {"page:DE"},
		`"and"`: {"page:Quote"}, "near *": {"page:Quote"}, "-x": {"page:Quote"}, "budget": nil, "bud get": {"page:Spread"},
	} {
		if got := searchFor(t, s, q); !slices.Equal(got, want) {
			t.Errorf("%q found %v, want %v", q, got, want)
		}
	}
}

func TestSearchStopsWhenGivenUpOn(t *testing.T) {
	s := open(t)
	textPage(t, s, "Notes", "the quarterly budget")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if hits, err := s.SearchContext(ctx, "budget", 10); !errors.Is(err, context.Canceled) {
		t.Errorf("hits %v, %v", hits, err)
	}
}

func TestOldDatabaseIsSearchedAndIndexed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	before := strings.Split(schema, textWritesTable)[0]
	n := doc.FromBlocks([]*doc.Node{{ID: "a", Type: "todo_list", Data: map[string]any{"delta": doc.Delta{{Text: "call the venue"}}.JSON(), "due": "2026-12-01"}}})
	writeDB(t, path, before,
		`INSERT INTO pages (id, space_id, title, sort_key, created_at, updated_at) VALUES ('p1', 's', 'Trip', 'a', 1, 1)`,
		`INSERT INTO page_contents (page_id, content, plain_text, updated_at) VALUES ('p1', '`+n.Encode()+`', 'call the venue', 1)`,
		`PRAGMA user_version = 7`)
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.LoadSession(&User{ID: "u"}); err != nil && !errors.Is(err, ErrUnsynced) {
		t.Fatal(err)
	}
	if id, err := s.WriteID("p1"); err != nil || id == 0 {
		t.Errorf("write %d, %v", id, err)
	}
	if got := searchFor(t, s, "venue"); len(got) != 1 {
		t.Errorf("found %v", got)
	}
	var indexed int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM text_writes WHERE indexed = write_id`).Scan(&indexed); err != nil || indexed != 1 {
		t.Errorf("%d indexed, %v", indexed, err)
	}
}

func TestSaveOverReturnsWhatItStored(t *testing.T) {
	s := open(t)
	blocks := func(texts ...string) *doc.Content {
		var nodes []*doc.Node
		for i, text := range texts {
			n := &doc.Node{ID: string(rune('a' + i)), Type: "paragraph"}
			n.SetDelta(doc.Delta{{Text: text}})
			nodes = append(nodes, n)
		}
		return doc.FromBlocks(nodes)
	}
	p, err := s.CreatePage(NewPageOptions{Title: "Plan", Content: blocks("one", "two", "three")})
	if err != nil {
		t.Fatal(err)
	}
	row, _ := s.ContentRow(p.ID)
	if err := s.PutSyncedContentOver(p.ID, row.Content, doc.MustDecode(row.Content), row.Content, time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	row, _ = s.ContentRow(p.ID)
	c := doc.MustDecode(row.Content)
	c.Find("b").SetDelta(doc.Delta{{Text: "two, edited"}})
	saved, err := s.SaveOver(p.ID, row.Content, c)
	if err != nil || !saved.Changed {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	now, _ := s.ContentRow(p.ID)
	if now.Content != saved.Encoded || now.WriteID != saved.WriteID || now.WriteID == row.WriteID || now.BaseContent != row.BaseContent {
		t.Errorf("stored %q (write %d, was %d), saved %q (write %d)", now.Content, now.WriteID, row.WriteID, saved.Encoded, saved.WriteID)
	}
	if e := mustPending(t, s); len(e) == 0 || e[len(e)-1].Label != "1 block edited" {
		t.Errorf("queued %+v", e)
	}
	again, err := s.SaveOver(p.ID, now.Content, doc.MustDecode(now.Content))
	if err != nil || again.Changed || again.WriteID != now.WriteID || again.Encoded != now.Content {
		t.Errorf("saved again %+v, %v", again, err)
	}
	if _, err := s.SaveOver(p.ID, row.Content, c); !errors.Is(err, ErrContentChanged) {
		t.Errorf("saved over what was: %v", err)
	}
}

func TestDownloadedFileFromKeepsNothingCutShort(t *testing.T) {
	s := open(t)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE files SET local_path = NULL WHERE id = ?`, f.ID); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(s.Dir, "files", f.ID))
	cut := io.MultiReader(strings.NewReader("half of it"), errReader{io.ErrUnexpectedEOF})
	if _, err := s.DownloadedFileFrom(f.ID, cut); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("kept a file cut short: %v", err)
	}
	if got, _ := s.File(f.ID); got.LocalPath != "" {
		t.Errorf("the row names %q", got.LocalPath)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.Dir, "files", f.ID)); len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
	local, err := s.DownloadedFileFrom(f.ID, strings.NewReader("the whole file"))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(local); string(data) != "the whole file" { // #nosec G304
		t.Errorf("kept %q", data)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestTreeAnswersAsTheStore(t *testing.T) {
	s := open(t)
	own, _ := s.CreatePage(NewPageOptions{Title: "Mine"})
	shared := func(id, parent string) Page {
		at := time.Now().UTC().Format(time.RFC3339Nano)
		row := map[string]any{"id": id, "space_id": "other", "title": id, "sort_key": "a", "created_at": at, "updated_at": at}
		if parent != "" {
			row["parent_id"] = parent
		}
		if err := s.PutRemotePage(row); err != nil {
			t.Fatal(err)
		}
		p, err := s.Page(id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	top := shared("00000000-0000-0000-0000-0000000000a1", "")
	mid := shared("00000000-0000-0000-0000-0000000000a2", top.ID)
	leaf := shared("00000000-0000-0000-0000-0000000000a3", mid.ID)
	alone := shared("00000000-0000-0000-0000-0000000000a4", "")
	user := s.Session().UserID
	if err := s.SetMembers(top.ID, []Member{{PageID: top.ID, UserID: user, Role: "editor"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMembers(mid.ID, []Member{{PageID: mid.ID, UserID: user, Role: "viewer"}}); err != nil {
		t.Fatal(err)
	}
	tree, err := s.Tree()
	if err != nil {
		t.Fatal(err)
	}
	pages := []Page{own, top, mid, leaf, alone}
	for _, p := range pages {
		if got, want := tree.CanEdit(p), s.CanEdit(p); got != want {
			t.Errorf("%s: tree says %v, the store %v", p.Title, got, want)
		}
		for _, of := range pages {
			if got, want := tree.IsDescendant(p.ID, of.ID), s.IsDescendant(p.ID, of.ID); got != want {
				t.Errorf("%s in %s: tree says %v, the store %v", p.Title, of.Title, got, want)
			}
		}
	}
	if !tree.CanEdit(top) || tree.CanEdit(leaf) || tree.CanEdit(alone) || !tree.CanEdit(own) {
		t.Error("the tree's answers are not the members'")
	}
}

func TestSaveWithoutItsWriteRow(t *testing.T) {
	s := open(t)
	p := textPage(t, s, "A", "alpha")
	if _, err := s.DB().Exec(`DELETE FROM text_writes`); err != nil {
		t.Fatal(err)
	}
	row, _ := s.ContentRow(p.ID)
	if saved, err := s.SaveOver(p.ID, row.Content, doc.MustDecode(row.Content)); err != nil || saved.Changed || saved.WriteID != 0 {
		t.Errorf("saved as it was %+v, %v", saved, err)
	}
	c := doc.MustDecode(row.Content)
	c.Children[0].SetDelta(doc.Delta{{Text: "beta"}})
	if saved, err := s.SaveOver(p.ID, row.Content, c); err != nil || !saved.Changed || saved.WriteID == 0 {
		t.Errorf("saved %+v, %v", saved, err)
	}
}

func TestSearchReadsWhatARecordingSaid(t *testing.T) {
	st, err := OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePage(NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := st.CreateRecording(p.ID, "b1", "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRecording(r.ID, func(r *Recording) {
		r.Status = "ready"
		r.Transcript = []Segment{{EndMs: 1000, Text: `The budget for R&D is "fine"`, Speaker: "1"}}
	}); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{"budget": 1, "r&d": 1, `is "fine"`: 1, "start_ms": 0, "speaker": 0, "1000": 0} {
		hits, err := st.Search(q, 20)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, h := range hits {
			if h.Kind == "recording" {
				n++
			}
		}
		if n != want {
			t.Errorf("%q found %d recordings, want %d", q, n, want)
		}
	}
}
