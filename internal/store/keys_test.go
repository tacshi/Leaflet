package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// addFile adds a file of name to s, as a file the user picked.
func addFile(t *testing.T, s *Store, name string) File {
	t.Helper()
	source := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFile(source, "", "", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// queued is the entry of an outbox row, with its payload.
func queued(t *testing.T, s *Store, entity, id string) (OutboxEntry, map[string]any) {
	t.Helper()
	entries, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Entity == entity && e.EntityID == id {
			var payload map[string]any
			if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
				t.Fatal(err)
			}
			return e, payload
		}
	}
	t.Fatalf("no entry %s/%s", entity, id)
	return OutboxEntry{}, nil
}

// A file is stored under a key Storage takes, whatever its name, which
// stays as it was for people to read; a name Storage takes is its key.
func TestFileKeysAreOnesStorageTakes(t *testing.T) {
	s := open(t)
	for name, want := range map[string]string{
		"report (final).pdf": "report (final).pdf",
		"报告 #1.pdf":          "__ _1.pdf",
		"résumé [2026].pdf":  "r_sum_ _2026_.pdf",
		"100% done~.txt":     "100_ done_.txt",
		"at 9.41.22 AM.png":  "at 9.41.22_AM.png",
	} {
		f := addFile(t, s, name)
		if f.Name != name || f.StoragePath != f.SpaceID+"/"+f.ID+"/"+want || !ValidStorageKey(f.StoragePath) {
			t.Errorf("%q: named %q, kept at %q", name, f.Name, f.StoragePath)
		}
		if _, upload := queued(t, s, "storage", "files/"+f.ID); upload["path"] != f.StoragePath {
			t.Errorf("%q: uploads to %v", name, upload["path"])
		}
	}
	for key, want := range map[string]bool{"s/f/a b.pdf": true, "s/f/a&b=(1).pdf": true, "s/f/报告.pdf": false, "s/f/a#b": false, "": false} {
		if ValidStorageKey(key) != want {
			t.Errorf("ValidStorageKey(%q) = %v", key, !want)
		}
	}
}

// A file an earlier version queued under its name, which Storage refuses,
// gets a key of its own in its row, its row's entry and its upload, each
// as it was queued but for that, and goes at once. One Storage takes is
// left as it is.
func TestRekeyFile(t *testing.T) {
	s := open(t)
	f := addFile(t, s, "plan.pdf")
	refused := f.SpaceID + "/" + f.ID + "/报告.pdf"
	f.Name, f.StoragePath = "报告.pdf", refused
	if err := s.PutFile(f); err != nil {
		t.Fatal(err)
	}
	row := FileToRemote(f)
	if err := s.Queue("files", f.ID, row, "new file", ""); err != nil {
		t.Fatal(err)
	}
	upload := fileUpload(s.Dir, f)
	upload["from_path"] = "elsewhere/" + f.ID + "/original.pdf"
	if err := s.Queue("storage", "files/"+f.ID, upload, "file waiting to upload", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE outbox SET attempts = 3`); err != nil {
		t.Fatal(err)
	}

	key, err := s.RekeyFile(f.ID)
	want := f.SpaceID + "/" + f.ID + "/__.pdf"
	if err != nil || key != want {
		t.Fatalf("key %q, %v", key, err)
	}
	got, err := s.File(f.ID)
	if err != nil || got.StoragePath != want || got.Name != "报告.pdf" {
		t.Errorf("file %+v, %v", got, err)
	}
	if e, payload := queued(t, s, "files", f.ID); payload["storage_path"] != want || payload["name"] != "报告.pdf" || e.Attempts != 0 {
		t.Errorf("row goes as %v, %d attempts", payload, e.Attempts)
	}
	e, payload := queued(t, s, "storage", "files/"+f.ID)
	if payload["path"] != want || payload["from_path"] != upload["from_path"] || e.Attempts != 0 {
		t.Errorf("upload goes as %v, %d attempts", payload, e.Attempts)
	}

	before := e.Revision
	if key, err := s.RekeyFile(f.ID); err != nil || key != want {
		t.Errorf("again: %q, %v", key, err)
	}
	if e, _ := queued(t, s, "storage", "files/"+f.ID); e.Revision != before {
		t.Error("a key Storage takes was queued again")
	}
	if _, err := s.RekeyFile("00000000-0000-0000-0000-000000000000"); !IsNotFound(err) {
		t.Errorf("a file that is gone: %v", err)
	}
}

// A row with only some columns queued stays a change of those, with its
// key among them, and one with none queued, as already on the server,
// goes as a change of its key alone. One sent again as this device joined
// an account stays so.
func TestRekeyFileKeepsHowTheRowGoes(t *testing.T) {
	s := open(t)
	f := addFile(t, s, "plan.pdf")
	refused := f.SpaceID + "/" + f.ID + "/报告.pdf"
	f.Name, f.StoragePath = "报告.pdf", refused
	if err := s.PutFile(f); err != nil {
		t.Fatal(err)
	}
	e, _ := queued(t, s, "files", f.ID)
	if err := s.Dequeue(e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RekeyFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if _, payload := queued(t, s, "files", f.ID); jsonOf(t, payload[ChangedKey]) != `["storage_path"]` {
		t.Errorf("not queued goes as %v", payload)
	}

	if _, err := s.DB().Exec(`UPDATE files SET storage_path = ?`, refused); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RekeyFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if _, payload := queued(t, s, "files", f.ID); payload[ChangedKey] == nil || payload[AdoptKey] != nil {
		t.Errorf("some columns queued goes as %v", payload)
	}

	e, _ = queued(t, s, "files", f.ID)
	if err := s.Dequeue(e); err != nil {
		t.Fatal(err)
	}
	f, _ = s.File(f.ID)
	if err := s.Queue("files", f.ID, adopting(FileToRemote(f), f.UpdatedAt), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE files SET storage_path = ?`, refused); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RekeyFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if _, payload := queued(t, s, "files", f.ID); payload[AdoptKey] == nil || payload[ChangedKey] != nil {
		t.Errorf("sent again goes as %v", payload)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A file sent again goes up under a key Storage takes when it goes up from
// here. One that does not stays by the name it is stored under, where the
// other devices find it: only its space moves.
func TestRequeuedFileKeepsItsStoredName(t *testing.T) {
	s := open(t)
	f := addFile(t, s, "plan.pdf")
	f.Name, f.StoragePath = "报告.pdf", f.SpaceID+"/"+f.ID+"/报告.pdf"
	if err := s.PutFile(f); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueFile(f.ID, "other"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.File(f.ID)
	if want := "other/" + f.ID + "/__.pdf"; got.StoragePath != want {
		t.Errorf("on this device: kept at %q", got.StoragePath)
	}
	if _, upload := queued(t, s, "storage", "files/"+f.ID); upload["path"] != got.StoragePath {
		t.Errorf("uploads to %v", upload["path"])
	}

	if _, err := s.DB().Exec(`UPDATE files SET storage_path = ?, local_path = NULL`, "other/"+f.ID+"/报告.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueFile(f.ID, "third"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.File(f.ID); got.StoragePath != "third/"+f.ID+"/报告.pdf" {
		t.Errorf("not on this device: kept at %q", got.StoragePath)
	}
}

// A comment's body and quote are cut to what the server keeps, in
// characters, as a longer one would be refused each time it went up.
func TestCommentsAreCutToWhatTheServerKeeps(t *testing.T) {
	s := open(t)
	long := strings.Repeat("字", maxCommentLen+5)
	c, err := s.AddComment("p", "b", long, "", long, "", "")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.Comment(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(stored.Body)); n != maxCommentLen {
		t.Errorf("body of %d characters", n)
	}
	if n := len([]rune(stored.Quote)); n != maxCommentLen {
		t.Errorf("quote of %d characters", n)
	}
	if got := ClampComment("short"); got != "short" {
		t.Errorf("a short comment became %q", got)
	}
}

// dataVersion is what another connection sees of s's commits, on one that
// stays open.
func dataVersion(t *testing.T, s *Store) int64 {
	t.Helper()
	var v int64
	if err := s.DB().QueryRow(`PRAGMA data_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Opening a database up to date writes nothing, so a command that only
// reads does not tell the app the database changed (WatchOthers).
func TestOpeningADatabaseUpToDateWritesNothing(t *testing.T) {
	dir := t.TempDir()
	app, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	before := dataVersion(t, app)
	for range 3 {
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if after := dataVersion(t, app); after != before {
		t.Errorf("data_version went from %d to %d", before, after)
	}
}

// Opening a database up to date does not wait for the write lock another
// connection holds.
func TestOpeningADatabaseUpToDateTakesNoLock(t *testing.T) {
	dir := t.TempDir()
	app, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	tx, err := app.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	opened := make(chan error, 1)
	go func() {
		s, err := Open(dir)
		if err == nil {
			err = s.Close()
		}
		opened <- err
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the open waited for the write lock")
	}
}

// A database of this version that lacks what schema makes, as a table
// added since without a step of its own, still gets it.
func TestOpeningADatabaseMakesWhatItLacks(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP TABLE row_versions`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.RowVersion("pages", "p"); err != nil {
		t.Errorf("the table is still gone: %v", err)
	}
}

// What Postgres does not keep goes as U+FFFD, in text and in encoded JSON,
// and nothing else changes: not a NUL spelled out in text, as code shows
// one, nor a pair of surrogates.
func TestCleaningKeepsToWhatPostgresRefuses(t *testing.T) {
	for in, want := range map[string]string{
		"plain":      "plain",
		"a\x00b":     "a\uFFFDb",
		"a\xffb":     "a\uFFFDb",
		"\x00\x00":   "\uFFFD\uFFFD",
		"\U0001F600": "\U0001F600",
	} {
		if got := CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q", in, got)
		}
	}
	for in, want := range map[string]string{
		`{"a":"b"}`:               `{"a":"b"}`,
		`{"a":"b\u0000c"}`:        `{"a":"b\ufffdc"}`,
		`{"a\u0000":1}`:           `{"a\ufffd":1}`,
		`{"a":"\\u0000"}`:         `{"a":"\\u0000"}`,
		`{"a":"\\\u0000"}`:        `{"a":"\\\ufffd"}`,
		`{"a":"\u003cb\u003e\n"}`: `{"a":"\u003cb\u003e\n"}`,
		`{"a":"\ud83d\ude00"}`:    `{"a":"\ud83d\ude00"}`,
		`{"a":"\uD83D\uDE00"}`:    `{"a":"\uD83D\uDE00"}`,
		`{"a":"\ud83dx"}`:         `{"a":"\ufffdx"}`,
		`{"a":"\ude00"}`:          `{"a":"\ufffd"}`,
		`{"a":"\ud83d\u0041"}`:    `{"a":"\ufffd\u0041"}`,
		`{"a":"\ud83d`:            `{"a":"\ufffd`,
		"{\"a\":\"b\xffc\"}":      "{\"a\":\"b\uFFFDc\"}",
	} {
		if got := CleanJSON(in); got != want {
			t.Errorf("CleanJSON(%s) = %s", in, got)
		}
	}
	row := map[string]any{"title": "a\x00", "n": float64(1), "none": nil,
		"transcript": []any{map[string]any{"text": "b\x00"}}, "tags": []any{"c\x00"}}
	CleanRow(row)
	if got := jsonOf(t, row); strings.Contains(got, `\u0000`) {
		t.Errorf("CleanRow left %s", got)
	}
	if row["n"] != float64(1) || row["none"] != nil {
		t.Errorf("CleanRow changed %v", row)
	}
}

// A snapshot goes up with what Postgres keeps, as stored JSON, read by no
// decoder on its way, may hold what Postgres refuses.
func TestSnapshotsGoUpClean(t *testing.T) {
	r := HistoryToRemote(HistoryEntry{ID: "h", PageID: "p", Content: `{"a":"b\u0000\ud83d"}`, Reason: "autosave"}, "u")
	if got := jsonOf(t, r["content"]); got != `{"a":"b\ufffd\ufffd"}` {
		t.Errorf("content %s", got)
	}
}
