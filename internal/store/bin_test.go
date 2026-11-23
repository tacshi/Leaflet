package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"leaflet/internal/doc"
)

func binFile(t *testing.T, s *Store, folderID, pageID string) File {
	t.Helper()
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFile(src, folderID, pageID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func queuedOf(t *testing.T, s *Store, entity, id string) (map[string]any, bool) {
	t.Helper()
	entries, err := s.pending(`payload`)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Entity == entity && e.EntityID == id {
			var payload map[string]any
			if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
				t.Fatal(err)
			}
			return payload, true
		}
	}
	return nil, false
}

func TestBinHoldsWhatWasDeleted(t *testing.T) {
	s := open(t)
	parent, _ := s.CreatePage(NewPageOptions{Title: "Parent"})
	kid, _ := s.CreatePage(NewPageOptions{Title: "Kid", ParentID: parent.ID})
	early, _ := s.CreatePage(NewPageOptions{Title: "Early", ParentID: parent.ID})
	theirs, _ := s.CreatePage(NewPageOptions{Title: "Theirs", SpaceID: "space-other"})
	f := binFile(t, s, "", "")
	if _, err := s.DeletePage(early.ID); err != nil {
		t.Fatal(err)
	}
	nextStamp()
	if err := s.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	nextStamp()
	if _, err := s.DeletePage(parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeletePage(theirs.ID); err != nil {
		t.Fatal(err)
	}
	items, err := s.Bin()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, string(it.Kind)+":"+it.Title)
	}
	if want := []string{"page:Parent", "file:notes.txt", "page:Early"}; !slices.Equal(got, want) {
		t.Fatalf("bin %v, want %v", got, want)
	}
	if items[0].Pages != 2 {
		t.Errorf("parent counts %d pages, want it and the kid", items[0].Pages)
	}
	if !items[0].GoesAt.After(time.Now().Add(BinKept - time.Hour)) {
		t.Errorf("parent goes at %v", items[0].GoesAt)
	}
	if ids, _ := s.InBin(kid.ID); !slices.Equal(ids, []string{parent.ID, kid.ID}) {
		t.Errorf("the kid goes with %v", ids)
	}
	if ids, _ := s.InBin(early.ID); !slices.Equal(ids, []string{early.ID}) {
		t.Errorf("a page deleted before its parent goes on its own, not with %v", ids)
	}
	if ids, _ := s.InBin(theirs.ID); len(ids) != 1 {
		t.Errorf("a page of another space is in no bin here, but goes with %v", ids)
	}
	if files, _ := s.Files(); len(files) != 0 {
		t.Errorf("a file in the bin is listed: %+v", files)
	}
	if _, err := os.Stat(f.LocalPath); err != nil {
		t.Errorf("a file in the bin lost its copy: %v", err)
	}
	if _, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); queued {
		t.Error("a file in the bin has its contents removed")
	}
}

func TestRestoreFileFromTheBin(t *testing.T) {
	s := open(t)
	folder, _ := s.CreateFolder("Docs")
	f := binFile(t, s, folder.ID, "")
	if err := s.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFolder(folder); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreFile(f.ID); err != nil {
		t.Fatal(err)
	}
	files, _ := s.Files()
	if len(files) != 1 || files[0].FolderID != "" {
		t.Fatalf("restored as %+v, want at the top level, its folder gone", files)
	}
	if items, _ := s.Bin(); len(items) != 0 {
		t.Errorf("still in the bin: %+v", items)
	}
}

func TestPurgeLeavesNothingButTheRow(t *testing.T) {
	s := open(t)
	parent, _ := s.CreatePage(NewPageOptions{Title: "Secret", Icon: "🔒", Tags: []string{"work"}})
	kid, _ := s.CreatePage(NewPageOptions{Title: "Kid", ParentID: parent.ID})
	c := doc.Empty()
	if err := c.Insert(doc.Position{At: "end"}, &doc.Node{ID: "b1", Type: "paragraph", Data: map[string]any{"delta": []any{map[string]any{"insert": "hello"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveContent(kid.ID, c); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHistory(kid.ID, c, "manual", "", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment(kid.ID, "b1", "a note", "", "", "user", "Me"); err != nil {
		t.Fatal(err)
	}
	f := binFile(t, s, "", kid.ID)
	if err := s.PutRecording(Recording{ID: "rec1", PageID: kid.ID, BlockID: "b2", AudioPath: "me/rec1.wav", Status: "ready",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeletePage(parent.ID); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Bin()
	if err := s.Purge(refs(items)...); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.Bin(); len(items) != 0 {
		t.Errorf("still in the bin: %+v", items)
	}
	for _, id := range []string{parent.ID, kid.ID} {
		p, err := s.Page(id)
		if err != nil {
			t.Fatal(err)
		}
		if p.PurgedAt.IsZero() || p.DeletedAt.IsZero() || p.Title != "" || p.Icon != "" || len(p.Tags) != 0 {
			t.Errorf("purged as %+v", p)
		}
		row, queued := queuedOf(t, s, "pages", id)
		if !queued || row["purged_at"] == nil || row["deleted_at"] == nil || row["title"] != "" {
			t.Errorf("goes up as %v", row)
		}
		if !s.IsPurged(id) {
			t.Errorf("%s is not purged", id)
		}
	}
	if got := s.Content(kid.ID); got != nil {
		t.Errorf("content kept: %v", got.Blocks)
	}
	if h, _ := s.History(kid.ID); len(h) != 0 {
		t.Errorf("history kept: %d", len(h))
	}
	if comments, _ := s.AllComments(); len(comments) != 0 {
		t.Errorf("comments kept: %+v", comments)
	}
	if _, err := s.File(f.ID); !IsNotFound(err) {
		t.Errorf("file row kept: %v", err)
	}
	if _, err := os.Stat(f.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file copy kept: %v", err)
	}
	if _, err := s.Recording("rec1"); !IsNotFound(err) {
		t.Errorf("recording kept: %v", err)
	}
	for _, key := range []string{"files/" + f.ID, "recordings/rec1"} {
		if _, queued := queuedOf(t, s, "storage_delete", key); !queued {
			t.Errorf("%s is not removed where it is stored", key)
		}
	}
	for _, entity := range []string{"page_content", "page_history", "comments", "files"} {
		entries, _ := s.Queued()
		for _, e := range entries {
			if e.Entity == entity {
				t.Errorf("%s still goes up: %+v", entity, e)
			}
		}
	}
	if _, _, err := s.SaveContent(kid.ID, c); !errors.Is(err, ErrPurged) {
		t.Errorf("saved over a page deleted for good: %v", err)
	}
	if err := s.RestorePages([]string{parent.ID}); !errors.Is(err, ErrPurged) {
		t.Errorf("restored a page deleted for good: %v", err)
	}
	if p, _ := s.Page(parent.ID); p.PurgedAt.IsZero() || p.DeletedAt.IsZero() {
		t.Errorf("restored a page deleted for good: %+v", p)
	}
	if pages, _ := s.Pages(); len(pages) != 0 {
		t.Errorf("purged page returned to active pages: %+v", pages)
	}
}

func TestPurgeFileFromTheBin(t *testing.T) {
	s := open(t)
	f := binFile(t, s, "", "")
	if err := s.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EmptyBin(); err != nil {
		t.Fatal(err)
	}
	got, err := s.File(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PurgedAt.IsZero() || got.LocalPath != "" {
		t.Errorf("purged as %+v", got)
	}
	if _, err := os.Stat(f.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("copy kept: %v", err)
	}
	if row, queued := queuedOf(t, s, "files", f.ID); !queued || row["purged_at"] == nil {
		t.Errorf("goes up as %v", row)
	}
	if _, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); !queued {
		t.Error("its contents stay where they are stored")
	}
	if err := s.RestoreFile(f.ID); !errors.Is(err, ErrPurged) {
		t.Errorf("restored a file deleted for good: %v", err)
	}
	// A stale row from a device that had not heard of the purge must not bring the file back or drop its storage_delete.
	stale := FileToRemote(f)
	stale["deleted_at"] = nil
	stale["updated_at"] = iso(time.Now().Add(time.Second))
	if err := s.PutRemoteFile(stale); err != nil {
		t.Fatal(err)
	}
	if got, err := s.File(f.ID); err != nil || got.PurgedAt.IsZero() || got.DeletedAt.IsZero() {
		t.Errorf("a stale row brought a purged file back: %+v, %v", got, err)
	}
	if files, _ := s.Files(); len(files) != 0 {
		t.Errorf("purged file returned to active files: %+v", files)
	}
	if _, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); !queued {
		t.Error("a stale row cancelled storage_delete of a purged file")
	}
}

func TestPurgeOldCountsFromTheFirstBin(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Long gone"})
	if _, err := s.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE pages SET deleted_at = ? WHERE id = ?`, time.Now().Add(-90*24*time.Hour).UnixMilli(), p.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if gone, err := s.PurgeOld(now); err != nil || gone {
		t.Fatalf("purged what was deleted before the bin, at once: %v %v", gone, err)
	}
	if gone, err := s.PurgeOld(now.Add(BinKept - time.Hour)); err != nil || gone {
		t.Fatalf("purged before its days in the bin: %v %v", gone, err)
	}
	if gone, err := s.PurgeOld(now.Add(BinKept + time.Hour)); err != nil || !gone {
		t.Fatalf("kept past its days in the bin: %v %v", gone, err)
	}
	if !s.IsPurged(p.ID) {
		t.Error("not purged")
	}
}

func TestPulledPurgeTakesThePageOff(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Shared out"})
	c := doc.Empty()
	_ = c.Insert(doc.Position{At: "end"}, &doc.Node{ID: "b1", Type: "paragraph", Data: map[string]any{}})
	if _, _, err := s.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	row := PageToRemote(p)
	row["updated_at"] = iso(time.Now())
	row["deleted_at"] = iso(time.Now())
	row["purged_at"] = iso(time.Now())
	row["title"] = ""
	if err := s.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	if !s.IsPurged(p.ID) {
		t.Fatal("not purged")
	}
	if got := s.Content(p.ID); got != nil {
		t.Errorf("content kept: %v", got.Blocks)
	}
	stale := PageToRemote(p)
	stale["updated_at"] = iso(time.Now())
	if err := s.PutRemotePage(stale); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Page(p.ID); got.Title != "" || got.DeletedAt.IsZero() {
		t.Errorf("a stale row brought it back: %+v", got)
	}
	comment := map[string]any{"id": "c1", "page_id": p.ID, "block_id": "b1", "body": "late", "author_kind": "user",
		"created_at": iso(time.Now()), "updated_at": iso(time.Now())}
	if err := s.PutRemoteComment(comment, "Them"); err != nil {
		t.Fatal(err)
	}
	if comments, _ := s.AllComments(); len(comments) != 0 {
		t.Errorf("kept a comment that came late: %+v", comments)
	}
	if err := s.PutSyncedContentOver(p.ID, "", c, "", time.Now(), 3); err != nil {
		t.Fatal(err)
	}
	if got := s.Content(p.ID); got != nil {
		t.Errorf("kept content that came late: %v", got.Blocks)
	}
}

func TestPulledFileDeletionKeepsItsCopyForTheBin(t *testing.T) {
	s := open(t)
	f := binFile(t, s, "", "")
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	row := FileToRemote(f)
	row["updated_at"] = iso(time.Now())
	row["deleted_at"] = iso(time.Now())
	row["kept"] = true
	if err := s.PutRemoteFile(row); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.LocalPath); err != nil {
		t.Errorf("a file in the bin lost its copy: %v", err)
	}
	row["purged_at"] = iso(time.Now())
	row["updated_at"] = iso(time.Now().Add(time.Second))
	if err := s.PutRemoteFile(row); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file deleted for good kept its copy: %v", err)
	}
}

func TestBinKeepsWhatAnotherDeviceDeletedBeforeThisOneHadIt(t *testing.T) {
	s := open(t)
	kept := binFile(t, s, "", "")
	gone := binFile(t, s, "", "")
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	// Another device put one in its bin before this one had the bin; a Leaflet from before the bin deleted the other.
	before := s.binSince().Add(-48 * time.Hour)
	for _, f := range []File{kept, gone} {
		row := FileToRemote(f)
		row["deleted_at"], row["updated_at"] = iso(before), iso(time.Now())
		if f.ID == kept.ID {
			row["kept"] = true
		}
		if err := s.PutRemoteFile(row); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.Bin()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != kept.ID {
		t.Fatalf("bin %+v, want only the file kept", items)
	}
	if want := before.Add(BinKept); !items[0].GoesAt.Equal(want) {
		t.Errorf("goes at %v, want %v, as on the device that deleted it", items[0].GoesAt, want)
	}
	if _, err := os.Stat(kept.LocalPath); err != nil {
		t.Errorf("the file kept lost its copy: %v", err)
	}
	if _, err := os.Stat(gone.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file deleted without the bin kept its copy: %v", err)
	}
	if err := s.RestoreFile(gone.ID); !errors.Is(err, ErrPurged) {
		t.Errorf("restored a file whose contents went: %v", err)
	}
}

func TestTrashAndRestoreSayWhetherTheFileWasKept(t *testing.T) {
	s := open(t)
	f := binFile(t, s, "", "")
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if err := s.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	row, queued := queuedOf(t, s, "files", f.ID)
	if !queued || row["kept"] != true || !slices.Contains(row[ChangedKey].([]any), any("kept")) {
		t.Fatalf("a file put in the bin goes up as %v", row)
	}
	if err := s.RestoreFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.File(f.ID); got.Kept || !got.DeletedAt.IsZero() {
		t.Errorf("restored as %+v", got)
	}
}

func TestPurgeLeavesPagesOfOtherSpaces(t *testing.T) {
	s := open(t)
	theirs, _ := s.CreatePage(NewPageOptions{Title: "Theirs", SpaceID: "space-other"})
	if _, err := s.DeletePage(theirs.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(BinRef{Kind: BinPage, ID: theirs.ID}); err != nil {
		t.Fatal(err)
	}
	if s.IsPurged(theirs.ID) {
		t.Error("deleted for good a page only its owner may")
	}
	if n := s.PendingCount(); n != 0 {
		t.Errorf("%d waiting to go up", n)
	}
}

func TestPulledPurgeOfASharedPageRemovesWhatThisAccountStored(t *testing.T) {
	s := open(t)
	sess := s.Session()
	p, _ := s.CreatePage(NewPageOptions{Title: "Shared with me", SpaceID: "space-other"})
	mine := binFile(t, s, "", p.ID)
	theirs := File{ID: doc.NewRowID(), SpaceID: "space-other", PageID: p.ID, Name: "theirs.txt", StoragePath: "space-other/x/theirs.txt",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.PutFile(theirs); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Recording{
		{ID: "mine", PageID: p.ID, BlockID: "b1", AudioPath: sess.UserID + "/mine.wav", Status: "ready"},
		{ID: "theirs", PageID: p.ID, BlockID: "b2", AudioPath: "someone/theirs.wav", Status: "ready"},
	} {
		if err := s.PutRecording(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	row := PageToRemote(p)
	now := iso(time.Now())
	row["updated_at"], row["deleted_at"], row["purged_at"], row["title"] = now, now, now, ""
	if err := s.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	var keys []string
	entries, _ := s.Queued()
	for _, e := range entries {
		keys = append(keys, e.Entity+":"+e.EntityID)
	}
	slices.Sort(keys)
	if want := []string{"storage_delete:files/" + mine.ID, "storage_delete:recordings/mine"}; !slices.Equal(keys, want) {
		t.Errorf("queued %v, want %v", keys, want)
	}
}
