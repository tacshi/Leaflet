package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// binnedPage is the page as the bin lists it, by its deletion.
func binnedPage(t *testing.T, s *Store, id string) BinRef {
	t.Helper()
	p, err := s.Page(id)
	if err != nil {
		t.Fatal(err)
	}
	return BinRef{Kind: BinPage, ID: id, DeletedAt: p.DeletedAt}
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
	if err := s.Purge(WipeNow, refs(items)...); err != nil {
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
	if err := s.EmptyBin(WipeNow); err != nil {
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
	if gone, err := s.PurgeOld(now, WipeNow); err != nil || gone {
		t.Fatalf("purged what was deleted before the bin, at once: %v %v", gone, err)
	}
	if gone, err := s.PurgeOld(now.Add(BinKept-time.Hour), WipeNow); err != nil || gone {
		t.Fatalf("purged before its days in the bin: %v %v", gone, err)
	}
	if gone, err := s.PurgeOld(now.Add(BinKept+time.Hour), WipeNow); err != nil || !gone {
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
	if err := s.Purge(WipeNow, binnedPage(t, s, theirs.ID)); err != nil {
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

// TestAPurgeOnceTakenKeepsThePageUntilTheServerTakesIt deletes a page for good on a device syncing through the server:
// the page leaves the bin at once, and what was on it stays here until the server takes the deletion, then goes,
// here and where it is stored, what the server listed too.
func TestAPurgeOnceTakenKeepsThePageUntilTheServerTakesIt(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Plans"})
	c := doc.Empty()
	_ = c.Insert(doc.Position{At: "end"}, &doc.Node{ID: "b1", Type: "paragraph", Data: map[string]any{}})
	if _, _, err := s.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	f := binFile(t, s, "", p.ID)
	if _, err := s.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(WipeOnceTaken, binnedPage(t, s, p.ID)); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.Bin(); len(items) != 0 || !s.IsPurged(p.ID) {
		t.Fatalf("still in the bin: %+v", items)
	}
	if s.Content(p.ID) == nil {
		t.Error("the content went before the server took the purge")
	}
	if _, err := os.Stat(f.LocalPath); err != nil {
		t.Errorf("the file's copy went before the server took the purge: %v", err)
	}
	if _, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); queued {
		t.Error("the file is removed where it is stored before the server took the purge")
	}
	if err := s.WipePurged(p.ID, []StoredObject{{"files", f.StoragePath}, {"files", "space/other/photo.png"}}); err != nil {
		t.Fatal(err)
	}
	if s.Content(p.ID) != nil {
		t.Error("the content stayed once the server took the purge")
	}
	if _, err := os.Stat(f.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file's copy stayed: %v", err)
	}
	var keys []string
	entries, _ := s.Queued()
	for _, e := range entries {
		keys = append(keys, e.Entity+":"+e.EntityID)
	}
	slices.Sort(keys)
	if want := []string{"storage_delete:files/" + f.ID, "storage_delete:files/space/other/photo.png"}; !slices.Equal(keys, want) {
		t.Errorf("queued %v, want %v", keys, want)
	}
}

// TestARefusedPurgeBringsThePageBack takes the server's row over a purge it refused, of a page another device restored
// since: the page is as the server has it, with what was on it, which waited here.
func TestARefusedPurgeBringsThePageBack(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Plans", Icon: "x"})
	c := doc.Empty()
	_ = c.Insert(doc.Position{At: "end"}, &doc.Node{ID: "b1", Type: "paragraph", Data: map[string]any{}})
	if _, _, err := s.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(WipeOnceTaken, binnedPage(t, s, p.ID)); err != nil {
		t.Fatal(err)
	}
	var entry OutboxEntry
	entries, _ := s.Queued()
	for _, e := range entries {
		if e.Entity == "pages" && e.EntityID == p.ID {
			entry = e
		}
	}
	if entry.ID == 0 {
		t.Fatal("the purge is not queued")
	}
	there := PageToRemote(p)
	there["updated_at"] = iso(time.Now())
	if err := s.TakeRemote(entry, there); err != nil {
		t.Fatal(err)
	}
	got, err := s.Page(p.ID)
	if err != nil || s.IsPurged(p.ID) || !got.DeletedAt.IsZero() || got.Title != "Plans" || got.Icon != "x" {
		t.Fatalf("the page is %+v, %v", got, err)
	}
	if content := s.Content(p.ID); content == nil || content.Find("b1") == nil {
		t.Errorf("what was on the page went: %v", content)
	}
	if n := s.PendingCount(); n != 0 {
		t.Errorf("%d waiting to go up", n)
	}
	// A row of the page as it was, from a device that had not heard of a purge, still brings nothing back.
	if _, err := s.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(WipeNow, binnedPage(t, s, p.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRemotePage(there); err != nil {
		t.Fatal(err)
	}
	if !s.IsPurged(p.ID) {
		t.Error("a stale row brought a page deleted for good back")
	}
}

// TestAPulledPurgeRemovesWhatThisDeviceKnewOf has a page of the space deleted for good on another device, which had
// not heard of a file put on it here: this device removes it where it is stored.
func TestAPulledPurgeRemovesWhatThisDeviceKnewOf(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Plans"})
	f := binFile(t, s, "", p.ID)
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	row := PageToRemote(p)
	now := iso(time.Now())
	row["updated_at"], row["deleted_at"], row["purged_at"], row["title"] = now, now, now, ""
	if err := s.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	if _, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); !queued {
		t.Error("the file is not removed where it is stored")
	}
}

func TestDropUnsentLeavesWhatWentUp(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{})
	if dropped, err := s.DropUnsent(p.ID); err != nil || !dropped {
		t.Fatalf("a page that went nowhere was not dropped: %v %v", dropped, err)
	}
	if _, err := s.Page(p.ID); !IsNotFound(err) {
		t.Errorf("the page stayed: %v", err)
	}
	if n := s.PendingCount(); n != 0 {
		t.Errorf("%d waiting to go up", n)
	}
	sent, _ := s.CreatePage(NewPageOptions{})
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if dropped, err := s.DropUnsent(sent.ID); err != nil || dropped {
		t.Fatalf("a page that went up was dropped: %v %v", dropped, err)
	}
	if _, err := s.Page(sent.ID); err != nil {
		t.Errorf("the page that went up went: %v", err)
	}
}

func TestTombstoneRowKeepsWhatAPurgedPageKeeps(t *testing.T) {
	p := Page{ID: "p1", SpaceID: "s1", Title: "Secret", Icon: "x", Tags: []string{"work"}, DefaultTemplateID: "t1",
		DeletedAt: time.Now(), PurgedAt: time.Now()}
	want := PageToRemote(tombstone(p))
	got := TombstoneRow(PageToRemote(p))
	for _, k := range purgedCols {
		if g, w := got[k], want[k]; !sameJSON(g, w) {
			t.Errorf("%s is %v in the log, %v in the row", k, g, w)
		}
	}
}

// TestPurgeTakesOnlyTheDeletionTheBinListed lists the bin, then has a file restored, another restored and put in the
// bin again, and a page restored, as by another device whose changes came in meanwhile: deleting for good what the bin
// listed leaves each as it is.
func TestPurgeTakesOnlyTheDeletionTheBinListed(t *testing.T) {
	s := open(t)
	restored := binFile(t, s, "", "")
	again := binFile(t, s, "", "")
	page, _ := s.CreatePage(NewPageOptions{Title: "Plans"})
	for _, id := range []string{restored.ID, again.ID} {
		if err := s.TrashFile(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeletePage(page.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := s.Bin()
	if err != nil || len(listed) != 3 {
		t.Fatalf("bin %+v, %v", listed, err)
	}
	nextStamp()
	for _, id := range []string{restored.ID, again.ID} {
		if err := s.RestoreFile(id); err != nil {
			t.Fatal(err)
		}
	}
	nextStamp()
	if err := s.TrashFile(again.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RestorePages([]string{page.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(WipeNow, refs(listed)...); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.File(restored.ID); !f.PurgedAt.IsZero() || !f.DeletedAt.IsZero() || f.Name != "notes.txt" {
		t.Errorf("a file restored since the bin was listed is %+v", f)
	}
	if _, err := os.Stat(restored.LocalPath); err != nil {
		t.Errorf("a file restored since the bin was listed lost its copy: %v", err)
	}
	if f, _ := s.File(again.ID); !f.PurgedAt.IsZero() || f.DeletedAt.IsZero() {
		t.Errorf("a file deleted again since the bin was listed is %+v", f)
	}
	if s.IsPurged(page.ID) {
		t.Error("a page restored since the bin was listed was deleted for good")
	}
	if items, _ := s.Bin(); len(items) != 1 || items[0].ID != again.ID {
		t.Fatalf("bin %+v, want the file deleted again", items)
	}
	if err := s.EmptyBin(WipeNow); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.File(again.ID); f.PurgedAt.IsZero() {
		t.Errorf("the bin as it lists now was not deleted for good: %+v", f)
	}
}

// TestARefusedFilePurgeKeepsTheFileWhole deletes a file of the bin for good on a device syncing through the server: its
// copy stays until the server takes the deletion. The server refuses it, of a file another device put in the bin again
// since: the file is back in the bin as the server has it, with its contents, here and where they are stored.
func TestARefusedFilePurgeKeepsTheFileWhole(t *testing.T) {
	s := open(t)
	f := binFile(t, s, "", "")
	if err := s.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	listed, _ := s.Bin()
	if err := s.Purge(WipeOnceTaken, refs(listed)...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.LocalPath); err != nil {
		t.Errorf("the copy went before the server took the deletion: %v", err)
	}
	if removal, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); !queued || removal["path"] != f.StoragePath {
		t.Fatalf("its stored copy is removed as %v", removal)
	}
	var entry OutboxEntry
	entries, _ := s.Queued()
	for _, e := range entries {
		if e.Entity == "files" && e.EntityID == f.ID {
			entry = e
		}
	}
	there := FileToRemote(f)
	later := time.Now().Add(time.Minute)
	there["deleted_at"], there["kept"], there["updated_at"] = iso(later), true, iso(later)
	if err := s.TakeRemote(entry, there); err != nil {
		t.Fatal(err)
	}
	got, err := s.File(f.ID)
	if err != nil || !got.PurgedAt.IsZero() || !got.Kept || got.Name != "notes.txt" || got.LocalPath == "" {
		t.Fatalf("the file is %+v, %v", got, err)
	}
	if _, err := os.Stat(f.LocalPath); err != nil {
		t.Errorf("the file back in the bin lost its copy: %v", err)
	}
	if _, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); queued {
		t.Error("the file back in the bin is still removed where it is stored")
	}
	if items, _ := s.Bin(); len(items) != 1 || items[0].ID != f.ID {
		t.Fatalf("bin %+v", items)
	}
	listed, _ = s.Bin()
	if err := s.Purge(WipeOnceTaken, refs(listed)...); err != nil {
		t.Fatal(err)
	}
	if err := s.WipePurgedFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy stayed once the server took the deletion: %v", err)
	}
	if got, _ := s.File(f.ID); got.LocalPath != "" {
		t.Errorf("the file keeps a copy at %q", got.LocalPath)
	}
}

// TestAPagePurgeKeepsTheRemovalOfAFileDeletedForGoodOnIt deletes a file for good, then its page, before the removal of
// the file's stored copy went: the removal stays, though the page's purge lists the file no more.
func TestAPagePurgeKeepsTheRemovalOfAFileDeletedForGoodOnIt(t *testing.T) {
	for _, wipe := range []Wipe{WipeNow, WipeOnceTaken} {
		s := open(t)
		p, _ := s.CreatePage(NewPageOptions{Title: "Plans"})
		f := binFile(t, s, "", p.ID)
		if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteFile(f); err != nil {
			t.Fatal(err)
		}
		if removal, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); !queued || removal["page_id"] != nil {
			t.Fatalf("the file's stored copy is removed as %v", removal)
		}
		if _, err := s.DeletePage(p.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Purge(wipe, binnedPage(t, s, p.ID)); err != nil {
			t.Fatal(err)
		}
		if wipe == WipeOnceTaken {
			if err := s.WipePurged(p.ID, nil); err != nil {
				t.Fatal(err)
			}
		}
		if removal, queued := queuedOf(t, s, "storage_delete", "files/"+f.ID); !queued || removal["path"] != f.StoragePath {
			t.Errorf("wipe %d: the removal of the file deleted for good went with its page: %v", wipe, removal)
		}
	}
}

// TestAFileRestoredAcrossItsPurgeIsNoFileToList takes the row of a file deleted for good on one device while another
// restored it, which says it was deleted for good and not that it was deleted: it is not listed, and keeps nothing.
func TestAFileRestoredAcrossItsPurgeIsNoFileToList(t *testing.T) {
	s := open(t)
	f := binFile(t, s, "", "")
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	row := FileToRemote(f)
	now := time.Now()
	row["deleted_at"], row["purged_at"], row["updated_at"] = nil, iso(now), iso(now)
	if err := s.PutRemoteFile(row); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Files(); len(files) != 0 {
		t.Errorf("a file deleted for good is listed: %+v", files)
	}
	got, err := s.File(f.ID)
	if err != nil || got.DeletedAt.UnixMilli() != now.UnixMilli() || got.Name != "" || got.StoragePath != "" {
		t.Errorf("the file deleted for good is %+v, %v", got, err)
	}
	if _, err := os.Stat(f.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file deleted for good kept its copy: %v", err)
	}
}

// TestAFileDeletedForGoodKeepsNothingOfWhatItWas deletes a file of the bin for good: its row keeps no name, type, size
// or path, here or as it goes up with its deletion, and only the removal of its stored copy says where that was.
func TestAFileDeletedForGoodKeepsNothingOfWhatItWas(t *testing.T) {
	s := open(t)
	f := binFile(t, s, "", "")
	if err := s.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if err := s.EmptyBin(WipeNow); err != nil {
		t.Fatal(err)
	}
	got, _ := s.File(f.ID)
	if got.Name != "" || got.MimeType != "" || got.SizeBytes != 0 || got.StoragePath != "" || got.PurgedAt.IsZero() {
		t.Errorf("a file deleted for good keeps %+v", got)
	}
	row, queued := queuedOf(t, s, "files", f.ID)
	cols, _ := row[ChangedKey].([]any)
	if !queued || row["name"] != "" || row["storage_path"] != nil || !slices.Contains(cols, any("deleted_at")) ||
		!slices.Contains(cols, any("purged_at")) || !slices.Contains(cols, any("name")) {
		t.Errorf("goes up as %v", row)
	}
	if removal, _ := queuedOf(t, s, "storage_delete", "files/"+f.ID); removal["path"] != f.StoragePath {
		t.Errorf("its stored copy is removed as %v", removal)
	}
	other := binFile(t, s, "", "")
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	pulled := FileToRemote(other)
	now := iso(time.Now())
	pulled["deleted_at"], pulled["purged_at"], pulled["updated_at"] = now, now, now
	if err := s.PutRemoteFile(pulled); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.File(other.ID); got.Name != "" || got.StoragePath != "" || got.SizeBytes != 0 {
		t.Errorf("a file deleted for good elsewhere keeps %+v here", got)
	}
}

func TestFileTombstoneRowKeepsWhatAPurgedFileKeeps(t *testing.T) {
	f := File{ID: "f1", SpaceID: "s1", Name: "secret.txt", StoragePath: "s1/f1/secret.txt", MimeType: "text/plain",
		SizeBytes: 9, DeletedAt: time.Now(), Kept: true, PurgedAt: time.Now()}
	want := FileToRemote(fileTombstone(f))
	got := FileTombstoneRow(FileToRemote(f))
	for k := range purgedFileBlank {
		if g, w := got[k], want[k]; !sameJSON(g, w) {
			t.Errorf("%s is %v in the log, %v in the row", k, g, w)
		}
	}
}

// TestAPurgeRemovesOnlyWhatItsRowsStore has on a page deleted for good a file row naming another file's object and a
// recording naming another's audio, as a page's writer could put there, and the server listing objects this device has
// on another page: none of those is removed.
func TestAPurgeRemovesOnlyWhatItsRowsStore(t *testing.T) {
	s := open(t)
	sess := s.Session()
	p, _ := s.CreatePage(NewPageOptions{Title: "Plans"})
	other, _ := s.CreatePage(NewPageOptions{Title: "Other"})
	own := binFile(t, s, "", p.ID)
	elsewhere := binFile(t, s, "", other.ID)
	now := time.Now()
	planted := File{ID: doc.NewRowID(), SpaceID: sess.SpaceID, PageID: p.ID, Name: "x.txt", StoragePath: elsewhere.StoragePath,
		CreatedAt: now, UpdatedAt: now}
	if err := s.PutFile(planted); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Recording{
		{ID: "mine", PageID: p.ID, BlockID: "b1", AudioPath: sess.UserID + "/mine.wav", Status: "ready"},
		{ID: "planted", PageID: p.ID, BlockID: "b2", AudioPath: "someone/theirs.wav", Status: "ready"},
		{ID: "theirs", PageID: other.ID, BlockID: "b3", AudioPath: "someone/theirs.wav", Status: "ready"},
	} {
		if err := s.PutRecording(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(WipeOnceTaken, binnedPage(t, s, p.ID)); err != nil {
		t.Fatal(err)
	}
	listed := []StoredObject{{"files", elsewhere.StoragePath}, {"recordings", "someone/theirs.wav"}, {"files", "space/gone/photo.png"}}
	if err := s.WipePurged(p.ID, listed); err != nil {
		t.Fatal(err)
	}
	var keys []string
	entries, _ := s.Queued()
	for _, e := range entries {
		if e.Entity == "storage_delete" {
			keys = append(keys, e.EntityID)
		}
	}
	slices.Sort(keys)
	if want := []string{"files/" + own.ID, "files/space/gone/photo.png", "recordings/mine"}; !slices.Equal(keys, want) {
		t.Errorf("removes %v, want %v", keys, want)
	}
}

// TestLivePagesUnderAPageDeletedForGoodGoToTheTop deletes for good pages with a live page under each, as a device that
// had not heard of their deletion made: the live pages go to the top level of the space, as the server puts them and
// the sidebar shows them, here as the deletion is made, heard of, or taken by the server. A page shared with this
// account stays where the server says.
func TestLivePagesUnderAPageDeletedForGoodGoToTheTop(t *testing.T) {
	s := open(t)
	space := s.Session().SpaceID
	under := func(parent Page) Page {
		t.Helper()
		kid := Page{ID: doc.NewRowID(), SpaceID: parent.SpaceID, ParentID: parent.ID, Title: "Kid", SortKey: "V",
			CreatedAt: time.Now(), ChangedAt: time.Now()}
		row := PageToRemote(kid)
		row["updated_at"] = iso(time.Now())
		if err := s.PutRemotePage(row); err != nil {
			t.Fatal(err)
		}
		return kid
	}
	purgedRow := func(p Page) map[string]any {
		row := PageToRemote(p)
		now := iso(time.Now())
		row["updated_at"], row["deleted_at"], row["purged_at"], row["title"] = now, now, now, ""
		return row
	}
	here, _ := s.CreatePage(NewPageOptions{Title: "Here"})
	if _, err := s.DeletePage(here.ID); err != nil {
		t.Fatal(err)
	}
	hereKid := under(here)
	if err := s.Purge(WipeNow, binnedPage(t, s, here.ID)); err != nil {
		t.Fatal(err)
	}
	heard, _ := s.CreatePage(NewPageOptions{Title: "Heard"})
	heardKid := under(heard)
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRemotePage(purgedRow(heard)); err != nil {
		t.Fatal(err)
	}
	taken, _ := s.CreatePage(NewPageOptions{Title: "Taken"})
	if _, err := s.DeletePage(taken.ID); err != nil {
		t.Fatal(err)
	}
	takenKid := under(taken)
	if err := s.Purge(WipeOnceTaken, binnedPage(t, s, taken.ID)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Page(takenKid.ID); got.ParentID != taken.ID {
		t.Errorf("a page moved out before the server took the deletion, to %q", got.ParentID)
	}
	if err := s.WipePurged(taken.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, kid := range []Page{hereKid, heardKid, takenKid} {
		if got, err := s.Page(kid.ID); err != nil || got.ParentID != "" || !got.DeletedAt.IsZero() || got.SpaceID != space {
			t.Errorf("a live page under one deleted for good is %+v, %v", got, err)
		}
		if _, queued := queuedOf(t, s, "pages", kid.ID); queued {
			t.Errorf("the move of %s goes up", kid.ID)
		}
	}
	theirs, _ := s.CreatePage(NewPageOptions{Title: "Theirs", SpaceID: "space-other"})
	theirKid := under(theirs)
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRemotePage(purgedRow(theirs)); err != nil {
		t.Fatal(err)
	}
	if !s.IsPurged(theirs.ID) {
		t.Fatal("the shared page was not deleted for good")
	}
	if got, _ := s.Page(theirKid.ID); got.ParentID != theirs.ID {
		t.Errorf("a page shared with this account moved to %q", got.ParentID)
	}
}

// TestTheServersTimeOfARowOutlastsChangesMadeHere keeps the server's updated_at of a page and a file as they came, or
// as the server took their writes, past changes made here since, which pulled_at does not outlast: a deletion for good
// sends it.
func TestTheServersTimeOfARowOutlastsChangesMadeHere(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Plans"})
	f := binFile(t, s, "", "")
	if _, err := s.db.Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SeenAt("pages", p.ID); ok {
		t.Error("a page made here has the server's time")
	}
	at := time.UnixMilli(time.Now().Add(-time.Hour).UnixMilli())
	row := PageToRemote(p)
	row["updated_at"] = iso(at)
	if err := s.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	fileRow := FileToRemote(f)
	fileRow["updated_at"] = iso(at)
	if err := s.PutRemoteFile(fileRow); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPageTags(p.ID, []string{"work"}); err != nil {
		t.Fatal(err)
	}
	if err := s.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"pages", "files"} {
		id := map[string]string{"pages": p.ID, "files": f.ID}[table]
		if got, ok := s.SeenAt(table, id); !ok || !got.Equal(at) {
			t.Errorf("%s: the server's time is %v, %v, want %v", table, got, ok, at)
		}
		if _, pulled := s.RowPulledAt(table, id); pulled {
			t.Errorf("%s: changed here, and still pulled", table)
		}
	}
	if err := s.NoteSeen("pages", p.ID, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteSeen("pages", p.ID, at); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SeenAt("pages", p.ID); !got.Equal(at.Add(time.Second)) {
		t.Errorf("the server's time went back to %v", got)
	}
	peer, _ := s.CreatePage(NewPageOptions{Title: "From a peer"})
	row = PageToRemote(peer)
	row["updated_at"], row[PeerKey] = iso(at), true
	if err := s.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SeenAt("pages", peer.ID); ok {
		t.Error("a row from another device through iCloud Drive has the server's time")
	}
}

// TestWipingAPageForgetsWhatThisDeviceKeptOfIt deletes a page for good: the chats, an agent's last edit and the
// reminders given of it go with it, and the reminders of every page with the account.
func TestWipingAPageForgetsWhatThisDeviceKeptOfIt(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{Title: "Plans"})
	other, _ := s.CreatePage(NewPageOptions{Title: "Other"})
	keys := []string{"chat." + p.ID, "mcp_edited:" + p.ID, "reminded:" + p.ID + "#b1", "reminded:" + p.ID + "#b2"}
	for _, k := range append(keys, "reminded:"+other.ID+"#b1") {
		if err := s.SetValue(k, "1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(WipeNow, binnedPage(t, s, p.ID)); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if _, ok := s.Value(k); ok {
			t.Errorf("%s stayed", k)
		}
	}
	if _, ok := s.Value("reminded:" + other.ID + "#b1"); !ok {
		t.Error("another page's reminder went")
	}
	if err := s.ForgetAccount(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Value("reminded:" + other.ID + "#b1"); ok {
		t.Error("a reminder stayed past the account")
	}
}

func TestDropUnsentRemovesTheCopiesOfItsFiles(t *testing.T) {
	s := open(t)
	p, _ := s.CreatePage(NewPageOptions{})
	f := binFile(t, s, "", p.ID)
	if dropped, err := s.DropUnsent(p.ID); err != nil || !dropped {
		t.Fatalf("not dropped: %v %v", dropped, err)
	}
	if _, err := os.Stat(f.LocalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy of a file on the page dropped stayed: %v", err)
	}
}

// TestADatabaseKeepsTheServersTimesAndScrubsFilesDeletedForGood opens a database from before server_at: a row pulled
// keeps the server's time it came with, a file deleted for good keeps nothing of what it was, and a removal of where a
// file is stored no longer goes with a page.
func TestADatabaseKeepsTheServersTimesAndScrubsFilesDeletedForGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leaflet.db")
	before := strings.ReplaceAll(strings.ReplaceAll(schema, ",\n  server_at INTEGER", ""), ", server_at INTEGER", "")
	if strings.Contains(before, "server_at") {
		t.Fatal("the schema keeps server_at")
	}
	writeDB(t, path, before,
		`INSERT INTO pages (id, space_id, title, sort_key, created_at, updated_at, pulled_at) VALUES ('p1', 's1', 'Plan', 'a', 1, 9000, 5000)`,
		`INSERT INTO files (id, space_id, name, storage_path, mime_type, size_bytes, created_at, updated_at, purged_at)
			VALUES ('f1', 's1', 'secret.txt', 's1/f1/secret.txt', 'text/plain', 9, 1, 9000, 8000)`,
		`INSERT INTO outbox (entity, entity_id, op, payload, page_id, created_at)
			VALUES ('storage_delete', 'files/f1', 'upsert', '{}', 'p1', 1)`,
		fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)-1))
	s, err := OpenPath(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if at, ok := s.SeenAt("pages", "p1"); !ok || at.UnixMilli() != 5000 {
		t.Errorf("the page has the server's time %v, %v", at, ok)
	}
	f, err := s.File("f1")
	if err != nil || f.Name != "" || f.StoragePath != "" || f.SizeBytes != 0 || f.DeletedAt.UnixMilli() != 8000 {
		t.Errorf("the file deleted for good is %+v, %v", f, err)
	}
	if _, err := s.DeletePage("p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(WipeNow, binnedPage(t, s, "p1")); err != nil {
		t.Fatal(err)
	}
	if _, queued := queuedOf(t, s, "storage_delete", "files/f1"); !queued {
		t.Error("the removal went with the page")
	}
}

func TestForgetAllKeepsWhenTheDeviceFirstHadTheBin(t *testing.T) {
	s := open(t)
	since, ok := s.Value(binSinceKey)
	if !ok {
		t.Fatal("the device never had the bin")
	}
	if err := s.SetValue("theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetAll(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Value(binSinceKey); got != since {
		t.Errorf("the device had the bin since %q, now %q", since, got)
	}
	if _, ok := s.Value("theme"); ok {
		t.Error("a setting stayed")
	}
}
