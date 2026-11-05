package sync

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// A file that is there but cannot be read now waits to go up again; only
// one that is gone leaves the queue.
func TestUploadOfAFileItCannotReadWaits(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no mode that keeps a file from being read")
	}
	st := openStore(t)
	local := filepath.Join(t.TempDir(), "locked.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o000); err != nil {
		t.Fatal(err)
	}
	svc := New(st, remote.SignedInForTest("http://127.0.0.1:1", st, "u1"))
	payload := map[string]any{"bucket": "files", "path": "s/f/locked.txt", "local_path": local, "content_type": "text/plain"}
	if done, err := svc.pushFile(context.Background(), "u1", payload, new(atomic.Int64)); done || err == nil {
		t.Errorf("unreadable: done %v, %v", done, err)
	}
	payload["local_path"] = filepath.Join(t.TempDir(), "gone.txt")
	if done, err := svc.pushFile(context.Background(), "u1", payload, new(atomic.Int64)); !done || err != nil {
		t.Errorf("gone: done %v, %v", done, err)
	}
}

// Resolving someone else's comment updates only what may change, and a
// comment the server does not have yet goes in whole.
func TestCommentsPushAsUpdates(t *testing.T) {
	st := openStore(t)
	var requests []string
	var patched map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Query().Get("id"))
		out := []map[string]any{}
		if r.Method == "PATCH" {
			_ = json.NewDecoder(r.Body).Decode(&patched)
			if r.URL.Query().Get("id") == "eq.theirs" {
				out = append(out, map[string]any{"id": "theirs"})
			}
		} else {
			out = append(out, map[string]any{"id": "new"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()

	done, err := svc.pushComment(ctx, "u1", store.OutboxEntry{Entity: "comments", EntityID: "theirs"}, map[string]any{"id": "theirs", "author_id": "u2", "body": "Look", "resolved": true, "deleted_at": nil})
	if err != nil || !done {
		t.Fatalf("done %v, %v", done, err)
	}
	if len(patched) != 1 || patched["resolved"] != true {
		t.Errorf("sent %v", patched)
	}
	done, err = svc.pushComment(ctx, "u1", store.OutboxEntry{Entity: "comments", EntityID: "new"}, map[string]any{"id": "new", "body": "Mine", "resolved": false})
	if err != nil || !done {
		t.Fatalf("done %v, %v", done, err)
	}
	if strings.Join(requests, ",") != "PATCH eq.theirs,PATCH eq.new,POST " {
		t.Errorf("requests %v", requests)
	}
	// A resolve made here, of a comment deleted elsewhere, sends only the
	// resolve, not this device's deleted_at.
	patched = nil
	done, err = svc.pushComment(ctx, "u1", store.OutboxEntry{Entity: "comments", EntityID: "theirs"}, map[string]any{"id": "theirs", "author_id": "u1", "body": "Look", "resolved": true,
		"deleted_at": nil, store.ChangedKey: []any{"resolved", "updated_at"}})
	if err != nil || !done {
		t.Fatalf("done %v, %v", done, err)
	}
	if len(patched) != 1 || patched["resolved"] != true {
		t.Errorf("sent %v", patched)
	}
}

// A page changed here sends only what changed, with when it changed, so an
// offline rename does not undo a move or a deletion made on another device
// meanwhile; one the server does not have goes up whole.
func TestPagePushSendsOnlyItsChanges(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	clear := func() {
		entries, _ := st.Pending()
		for _, e := range entries {
			_ = st.Dequeue(e)
		}
	}
	clear() // the server has the page now
	if err := st.RenamePage(p.ID, "Plan for Q3"); err != nil {
		t.Fatal(err)
	}
	if err := st.RenamePage(p.ID, "Plan for Q4"); err != nil {
		t.Fatal(err)
	}

	onServer := true
	var methods []string
	var sent map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		sent = nil
		_ = json.NewDecoder(r.Body).Decode(&sent)
		out := []map[string]any{}
		if r.Method != "PATCH" || onServer {
			out = append(out, map[string]any{"id": p.ID})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()

	renamed, _ := st.Page(p.ID)
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "PATCH" || len(sent) != 2 || sent["title"] != "Plan for Q4" ||
		store.Time(sent["changed_at"]).UnixMilli() != renamed.UpdatedAt.UnixMilli() {
		t.Errorf("requests %v, sent %v", methods, sent)
	}
	if st.PendingCount() != 0 {
		t.Errorf("%d still queued", st.PendingCount())
	}

	// Nothing changed: nothing queued.
	if err := st.RenamePage(p.ID, "Plan for Q4"); err != nil {
		t.Fatal(err)
	}
	if st.PendingCount() != 0 {
		t.Errorf("an unchanged page queued")
	}

	onServer, methods = false, nil
	if err := st.MovePage(p.ID, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.RenamePage(p.ID, "Plan"); err != nil {
		t.Fatal(err)
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "PATCH,POST" || sent["space_id"] == nil || sent[store.ChangedKey] != nil {
		t.Errorf("requests %v, sent %v", methods, sent)
	}
}

// A change to a page the user may no longer change, as one shared with
// them to view since, gives way to the server's page, rather than wait
// forever with every change made to the page elsewhere passed over. One
// the server does not show stays queued.
func TestRefusedPageChangeTakesTheServers(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	if err := st.RenamePage(p.ID, "My plan"); err != nil {
		t.Fatal(err)
	}

	server := store.PageToRemote(p)
	server["title"], server["deleted_at"] = "Their plan", time.Now().UTC().Format(time.RFC3339Nano)
	visible := false
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PATCH":
			_, _ = w.Write([]byte("[]")) // not the user's to change
		case "POST":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"42501","message":"new row violates row-level security policy"}`))
		case "GET":
			out := []map[string]any{}
			if visible {
				out = append(out, server)
			}
			_ = json.NewEncoder(w).Encode(out)
		}
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()

	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Page(p.ID); st.PendingCount() != 1 || got.Title != "My plan" {
		t.Errorf("a page the server doesn't show: %d queued, title %q", st.PendingCount(), got.Title)
	}

	visible = true
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Page(p.ID)
	if st.PendingCount() != 0 || got.Title != "Their plan" || got.DeletedAt.IsZero() {
		t.Errorf("%d queued, page %q deleted %v", st.PendingCount(), got.Title, got.DeletedAt)
	}
}

// A file's move goes up as only that, so it does not undo a deletion made
// elsewhere meanwhile.
func TestFilePushSendsOnlyItsChanges(t *testing.T) {
	st := openStore(t)
	folder, err := st.CreateFolder("Folder")
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir() + "/notes.txt"
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, folder.ID, "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	if err := st.DeleteFolder(folder); err != nil {
		t.Fatal(err)
	}
	sent := map[string]map[string]any{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent[r.Method+" "+r.URL.Path] = body
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": f.ID}})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := sent["PATCH /rest/v1/files"]; !ok || len(got) != 2 || got["folder_id"] != nil || got["changed_at"] == nil {
		t.Errorf("sent %v", sent)
	}
	if _, ok := sent["POST /rest/v1/files"]; ok {
		t.Errorf("the whole file went: %v", sent)
	}
}

// A deleted file's object leaves Storage once the server has the row that
// says so, and not before: while the row cannot go up, the removal waits.
// An object the server does not have counts as removed.
func TestDeletedFileLeavesStorageAfterItsRow(t *testing.T) {
	st := openStore(t)
	src := t.TempDir() + "/notes.txt"
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var requests []string
	var removed []string
	refuseRow, missing := true, false
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/rest/v1/files" && refuseRow:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "refused"})
			return
		case r.URL.Path == "/storage/v1/object/files":
			var body struct {
				Prefixes []string `json:"prefixes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			removed = append(removed, body.Prefixes...)
			if missing {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "not found"})
				return
			}
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()

	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 || st.PendingCount() != 2 {
		t.Fatalf("removed %v before the row went up; %d waiting", removed, st.PendingCount())
	}
	refuseRow = false
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != f.StoragePath || st.PendingCount() != 0 {
		t.Errorf("removed %v, %d waiting", removed, st.PendingCount())
	}
	if requests[len(requests)-1] != "DELETE /storage/v1/object/files" || requests[len(requests)-2] != "POST /rest/v1/files" {
		t.Errorf("requests %v", requests)
	}

	// Gone from the server already.
	g, err := st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFile(g); err != nil {
		t.Fatal(err)
	}
	missing = true
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if st.PendingCount() != 0 {
		t.Errorf("%d waiting after a removal of what was not there", st.PendingCount())
	}
}

// An upload stays queued while the recording cannot take its path here:
// else nothing would send the audio again, and the other devices could
// not play it.
func TestUploadWaitsForTheRecordingsPath(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Standup"})
	if err != nil {
		t.Fatal(err)
	}
	local := t.TempDir() + "/a.wav"
	if err := os.WriteFile(local, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := st.CreateRecording(p.ID, "b1", "", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if r, err = st.UpdateRecording(r.ID, func(x *store.Recording) { x.Status = "ready" }); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	if err := st.QueueUpload(r); err != nil {
		t.Fatal(err)
	}
	// The disk refuses the recording's path, as a full one would.
	if _, err := st.DB().Exec(`CREATE TRIGGER refuse BEFORE UPDATE ON recordings
		BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	uploads := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if req.URL.Path == "/storage/v1/object/recordings/u1/"+r.ID+".wav" {
			uploads++
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()

	// A failure here is the upload's own: it stays queued, and the rest of
	// the queue goes on.
	if err := svc.push(ctx); err != nil {
		t.Errorf("the pass stopped: %v", err)
	}
	if entries, _ := st.Pending(); len(entries) != 1 || entries[0].Attempts != 1 {
		t.Fatalf("the upload left the queue without the recording's path: %+v", entries)
	}
	if _, err := st.DB().Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Recording(r.ID)
	if uploads != 2 || got.AudioPath != "u1/"+r.ID+".wav" || st.HasPending("storage", "recordings/"+r.ID) {
		t.Errorf("%d uploads, path %q, still queued %v", uploads, got.AudioPath, st.HasPending("storage", "recordings/"+r.ID))
	}
}

// A copy of a file and a recording queued by an earlier version, from
// what this device had only stored, goes up as the copy's own, from the
// originals' stored copies; a copy whose original is gone has nothing to
// send.
func TestCopiesGoUpFromTheOriginals(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	space := st.Session().SpaceID
	f := store.File{ID: "00000000-0000-0000-0000-0000000000f1", SpaceID: space, PageID: p.ID, Name: "plan.pdf",
		MimeType: "application/pdf", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.StoragePath = space + "/" + f.ID + "/plan.pdf"
	gone := f
	gone.ID, gone.Name, gone.StoragePath = "00000000-0000-0000-0000-0000000000f2", "gone.pdf", space+"/00000000-0000-0000-0000-0000000000f2/gone.pdf"
	for _, x := range []store.File{f, gone} {
		if err := st.PutFile(x); err != nil {
			t.Fatal(err)
		}
	}
	r, err := st.CreateRecording(p.ID, "b1", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if r, err = st.UpdateRecording(r.ID, func(x *store.Recording) { x.Status, x.AudioPath = "ready", "u0/"+r.ID+".wav" }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": r.ID}},
		{ID: "b2", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
		{ID: "b3", Type: "file", Data: map[string]any{"name": gone.Name, "file_id": gone.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	cp, err := st.CreatePage(store.NewPageOptions{Title: "Meeting copy"})
	if err != nil {
		t.Fatal(err)
	}
	recID := oldRecordingCopy(t, st, cp.ID, r).ID
	fileID, goneID := oldFileCopy(t, st, cp.ID, f).ID, oldFileCopy(t, st, cp.ID, gone).ID

	var mu gosync.Mutex
	uploaded := map[string]string{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch req.URL.Path {
		case "/storage/v1/object/authenticated/recordings/u0/" + r.ID + ".wav":
			_, _ = w.Write([]byte("RIFF"))
			return
		case "/storage/v1/object/authenticated/files/" + f.StoragePath:
			_, _ = w.Write([]byte("%PDF"))
			return
		case "/storage/v1/object/authenticated/files/" + gone.StoragePath:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "not found"})
			return
		}
		if rest, ok := strings.CutPrefix(req.URL.Path, "/storage/v1/object/"); ok && req.Method == http.MethodPost {
			body, _ := io.ReadAll(req.Body)
			uploaded[rest] = string(body)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	cf, _ := st.File(fileID)
	if uploaded["recordings/u1/"+recID+".wav"] != "RIFF" || uploaded["files/"+cf.StoragePath] != "%PDF" || len(uploaded) != 2 {
		t.Errorf("uploaded %v", uploaded)
	}
	if c, _ := st.Recording(recID); c.AudioPath != "u1/"+recID+".wav" {
		t.Errorf("the copy plays %q", c.AudioPath)
	}
	if data, err := os.ReadFile(cf.LocalPath); err != nil || string(data) != "%PDF" {
		t.Errorf("the copy here: %q, %v", data, err)
	}
	for _, id := range []string{"recordings/" + recID, "files/" + fileID, "files/" + goneID} {
		if st.HasPending("storage", id) {
			t.Errorf("%s is still queued", id)
		}
	}
}

// A file deleted while a copy of it an earlier version queued waits to go
// up from it stays in Storage until the copy has gone up.
func TestDeletedFileStaysForACopyThatWaits(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	space := st.Session().SpaceID
	f := store.File{ID: "00000000-0000-0000-0000-0000000000f1", SpaceID: space, PageID: p.ID, Name: "plan.pdf",
		MimeType: "application/pdf", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.StoragePath = space + "/" + f.ID + "/plan.pdf"
	if err := st.PutFile(f); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "b1", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	cp, err := st.CreatePage(store.NewPageOptions{Title: "Meeting copy"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := oldFileCopy(t, st, cp.ID, f).ID
	if err := st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}

	var mu gosync.Mutex
	var removed []string
	refuse := true
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case req.URL.Path == "/storage/v1/object/authenticated/files/"+f.StoragePath:
			if refuse {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "try again"})
				return
			}
			_, _ = w.Write([]byte("%PDF"))
			return
		case req.URL.Path == "/storage/v1/object/files" && req.Method == http.MethodDelete:
			var body struct {
				Prefixes []string `json:"prefixes"`
			}
			_ = json.NewDecoder(req.Body).Decode(&body)
			removed = append(removed, body.Prefixes...)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	// The server failing stops the push, the copy waiting.
	if err := svc.push(context.Background()); !remote.IsBusy(err) {
		t.Fatalf("push: %v", err)
	}
	if len(removed) != 0 || !st.HasPending("storage", "files/"+fileID) {
		t.Fatalf("removed %v before the copy went up", removed)
	}
	refuse = false
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != f.StoragePath || st.PendingCount() != 0 {
		t.Errorf("removed %v, %d waiting", removed, st.PendingCount())
	}
}

// A recording a quit left transcribing, settled at startup, does not go up
// over the transcript the server finished meanwhile: it takes the
// server's. One still transcribing there is settled there too, and one
// the server does not have goes up whole.
func TestSettledRecordingTakesAFinishedTranscript(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := st.CreateRecording(p.ID, "b1", "soniox", "", "", "auto")
	if _, err := st.UpdateRecording(r.ID, func(x *store.Recording) { x.Status, x.AudioPath = "transcribing", "u1/"+r.ID+".wav" }); err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	settle := func() {
		t.Helper()
		if _, err := st.SettleRecording(r.ID, "transcribing", false, func(x *store.Recording) { x.Status, x.Error = "failed", "quit" }); err != nil {
			t.Fatal(err)
		}
	}
	settle()

	finished := store.RecordingToRemote(r)
	finished["status"], finished["transcript"] = "ready", []map[string]any{{"start_ms": 0, "end_ms": 900, "text": "Hello"}}
	finished["audio_path"], finished["updated_at"] = "u1/"+r.ID+".wav", time.Now().UTC().Format(time.RFC3339Nano)
	var patched []string
	srv := &refusing{rows: map[string]map[string]any{"recordings": finished}}
	srv.patch = func(_ string, r *http.Request) []map[string]any {
		patched = append(patched, r.URL.Query().Get("status"))
		return nil // no longer transcribing there
	}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()

	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Recording(r.ID)
	if got.Status != "ready" || len(got.Transcript) != 1 || got.Transcript[0].Text != "Hello" || st.PendingCount() != 0 {
		t.Errorf("recording %s %v, %d queued", got.Status, got.Transcript, st.PendingCount())
	}
	if len(patched) != 1 || patched[0] != "eq.transcribing" || slices.Contains(srv.requests, "POST /rest/v1/recordings") {
		t.Errorf("patched where %v; requests %v", patched, srv.requests)
	}

	// Still transcribing there: it is settled there too.
	if _, err := st.UpdateRecording(r.ID, func(x *store.Recording) { x.Status = "transcribing" }); err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	settle()
	srv.patch = func(string, *http.Request) []map[string]any { return []map[string]any{{"id": r.ID}} }
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Recording(r.ID); got.Status != "failed" || st.PendingCount() != 0 {
		t.Errorf("recording %s, %d queued", got.Status, st.PendingCount())
	}
}

// A recording's change the server refuses, as on a page the user may no
// longer edit, gives way to the server's recording, as a page's does.
func TestRefusedRecordingTakesTheServers(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := st.CreateRecording(p.ID, "b1", "", "", "", "auto")
	if _, err := st.UpdateRecording(r.ID, func(x *store.Recording) { x.Status = "failed" }); err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	if _, err := st.UpdateRecording(r.ID, func(x *store.Recording) { x.Transcript = []store.Segment{{Text: "Mine"}} }); err != nil {
		t.Fatal(err)
	}
	server := store.RecordingToRemote(r)
	server["status"], server["transcript"] = "ready", []map[string]any{{"text": "Theirs"}}
	server["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	hs := httptest.NewServer(&refusing{rows: map[string]map[string]any{"recordings": server}})
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Recording(r.ID)
	if st.PendingCount() != 0 || got.Status != "ready" || len(got.Transcript) != 1 || got.Transcript[0].Text != "Theirs" {
		t.Errorf("%d queued; recording %s %v", st.PendingCount(), got.Status, got.Transcript)
	}
}

// A change to someone else's comment the server refuses, as resolving it
// on a page shared to view, gives way to the server's comment: waiting, it
// would keep the author's later edits from this device.
func TestRefusedCommentTakesTheServers(t *testing.T) {
	st := openStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	theirs := map[string]any{"id": "00000000-0000-0000-0000-0000000000c1", "page_id": "00000000-0000-0000-0000-0000000000a1",
		"block_id": "b1", "author_id": "u2", "author_kind": "user", "body": "Look", "resolved": false, "created_at": now, "updated_at": now}
	if err := st.PutRemoteComment(theirs, "Ann"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetResolved(store.S(theirs, "id"), true); err != nil {
		t.Fatal(err)
	}
	edited := maps.Clone(theirs)
	edited["body"] = "Look again"
	hs := httptest.NewServer(&refusing{rows: map[string]map[string]any{"comments": edited}})
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Comment(store.S(theirs, "id"))
	if st.PendingCount() != 0 || got.Body != "Look again" || got.Resolved {
		t.Errorf("%d queued; comment %q resolved %v", st.PendingCount(), got.Body, got.Resolved)
	}
}

// A file's deletion the server refuses gives way to the server's file,
// which keeps its stored copy: the removal queued with the deletion does
// not take it.
func TestRefusedFileDeletionKeepsItsStoredCopy(t *testing.T) {
	st := openStore(t)
	src := t.TempDir() + "/notes.txt"
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	if err := st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	server := store.FileToRemote(f)
	server["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	srv := &refusing{rows: map[string]map[string]any{"files": server}}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(srv.requests, "DELETE /storage/v1/object/files") || st.PendingCount() != 0 {
		t.Errorf("requests %v; %d queued", srv.requests, st.PendingCount())
	}
	if got, _ := st.File(f.ID); !got.DeletedAt.IsZero() {
		t.Error("the file is still deleted here")
	}
}

// A copy an earlier version queued, whose original is only in the Leaflet
// folder of iCloud Drive this device synced through, and not downloaded
// yet, waits for a later pass without holding this one up or counting as
// a failure: the copy's row waits with it. It goes up once iCloud has it.
func TestCopyWaitsForAnOriginalICloudHasNotDownloaded(t *testing.T) {
	root := cloudRoot(t)
	d := newCloudDevice(t, root)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := d.page("Attachments")
	f, err := d.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	d.sync()
	if f, err = d.st.File(f.ID); err != nil {
		t.Fatal(err)
	}
	rel, _ := blobPath("files", f.StoragePath)
	path := filepath.Join(root, filepath.FromSlash(rel))
	hidden := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".icloud")
	if err := os.Rename(path, hidden); err != nil {
		t.Fatal(err)
	}
	cp, err := d.st.CreatePage(store.NewPageOptions{Title: "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	c := oldFileCopy(t, d.st, cp.ID, f)

	var mu gosync.Mutex
	uploaded := map[string]string{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(req.URL.Path, "/storage/v1/object/authenticated/") {
			// The server never had the original: it went through iCloud Drive.
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "not found"})
			return
		}
		if rest, ok := strings.CutPrefix(req.URL.Path, "/storage/v1/object/"); ok && req.Method == http.MethodPost {
			body, _ := io.ReadAll(req.Body)
			uploaded[rest] = string(body)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	d.svc.SetICloud(false)
	svc := New(d.st, remote.SignedInForTest(hs.URL, d.st, "u1"))
	svc.ICloud = d.svc.ICloudFolder()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the pass waited %v for iCloud", took)
	}
	e := pendingOf(t, d.st, "storage", "files/"+c.ID)
	if e == nil || e.Attempts != 0 {
		t.Fatalf("the copy's upload: %+v", e)
	}
	if !d.st.HasPending("files", c.ID) || len(uploaded) != 0 {
		t.Errorf("went before its contents: uploaded %v", uploaded)
	}

	if err := os.Rename(hidden, path); err != nil {
		t.Fatal(err)
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if uploaded["files/"+c.StoragePath] != "hello" || d.st.HasPending("storage", "files/"+c.ID) || d.st.HasPending("files", c.ID) {
		t.Errorf("uploaded %v", uploaded)
	}
}

// oldRecordingCopy is oldFileCopy for a recording whose audio is stored:
// the copy plays the original's until its own has gone up.
func oldRecordingCopy(t *testing.T, st *store.Store, pageID string, orig store.Recording) store.Recording {
	t.Helper()
	c := orig
	c.ID, c.PageID, c.LocalPath, c.UpdatedAt = doc.NewRowID(), pageID, "", time.Now()
	if err := st.PutRecording(c); err != nil {
		t.Fatal(err)
	}
	if err := st.Queue("recordings", c.ID, store.RecordingToRemote(c), "", pageID); err != nil {
		t.Fatal(err)
	}
	if err := st.Queue("storage", "recordings/"+c.ID, map[string]any{"bucket": "recordings", "from_path": orig.AudioPath,
		"content_type": "audio/wav", "recording_id": c.ID}, "audio waiting to upload", pageID); err != nil {
		t.Fatal(err)
	}
	return c
}

// A file's row goes up after its contents, in the same pass: a device
// that pulls the row can fetch them, and never takes a file still
// uploading for one that is gone.
func TestFileRowGoesAfterItsContents(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Trip"})
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "map.png")
	if err := os.WriteFile(src, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", p.ID, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var order []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case req.URL.Path == "/storage/v1/object/files/"+f.StoragePath && req.Method == http.MethodPost:
			order = append(order, "contents")
		case req.URL.Path == "/rest/v1/files" && req.Method == http.MethodPost:
			order = append(order, "row")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "contents,row" {
		t.Errorf("sent %v", order)
	}
	if st.HasPending("files", f.ID) || st.HasPending("storage", "files/"+f.ID) {
		t.Error("the file still waits after the pass")
	}
}

// A cloud agent's job refers to the page, the pages it is inside, and the
// comment its thread starts with: flushing the page sends them all first,
// and leaves another page's changes queued.
func TestFlushSendsWhatTheJobRefersTo(t *testing.T) {
	st := openStore(t)
	parent, err := st.CreatePage(store.NewPageOptions{Title: "Projects"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreatePage(store.NewPageOptions{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := st.AddComment(p.ID, "b1", "@Agent sum this up", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	aside, err := st.AddComment(other.ID, "b1", "later", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := fullPass(t, st, nil)

	if err := svc.FlushPage(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	for _, e := range [][2]string{{"pages", parent.ID}, {"pages", p.ID}, {"page_content", p.ID}, {"comments", thread.ID}} {
		if st.HasPending(e[0], e[1]) {
			t.Errorf("%s %s still waits", e[0], e[1])
		}
	}
	if !st.HasPending("pages", other.ID) || !st.HasPending("comments", aside.ID) {
		t.Error("another page's changes went")
	}
}

// A flush of no page, as a workspace agent's run asks for, sends every
// change: its agent reads all the user's pages.
func TestFlushOfNoPageSendsEverything(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreatePage(store.NewPageOptions{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := fullPass(t, st, nil)

	if err := svc.FlushPage(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{p.ID, other.ID} {
		if st.HasPending("pages", id) || st.HasPending("page_content", id) {
			t.Errorf("page %s still waits", id)
		}
	}
}

// A page in a folder not up yet goes after the folder: the server refuses
// a page in a folder it does not have, so the job would refer to a page
// that is not there. Another folder stays queued.
func TestFlushSendsThePagesFolderFirst(t *testing.T) {
	st := openStore(t)
	folder, err := st.CreateFolder("Work")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateFolder("Home")
	if err != nil {
		t.Fatal(err)
	}
	top, err := st.CreatePage(store.NewPageOptions{Title: "Projects", FolderID: folder.ID})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan", ParentID: top.ID})
	if err != nil {
		t.Fatal(err)
	}
	svc, requests := fullPass(t, st, nil)

	if err := svc.FlushPage(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	for _, e := range [][2]string{{"folders", folder.ID}, {"pages", top.ID}, {"pages", p.ID}} {
		if st.HasPending(e[0], e[1]) {
			t.Errorf("%s %s still waits", e[0], e[1])
		}
	}
	if !st.HasPending("folders", other.ID) {
		t.Error("another folder went")
	}
	folders := slices.IndexFunc(*requests, func(r string) bool { return strings.HasSuffix(r, "/rest/v1/folders") })
	pages := slices.IndexFunc(*requests, func(r string) bool { return strings.HasSuffix(r, "/rest/v1/pages") })
	if folders < 0 || pages < 0 || folders > pages {
		t.Errorf("sent %v", *requests)
	}
}

// Rows sent again whole as a device's pages join an account go over the
// server's only when they changed here later than there, by when each
// change was made, not when it reached the server: one another device
// renamed since, which this device has from iCloud Drive as it was, takes
// the server's; one this device renamed after the copy another device
// uploaded late, as it joined the account first, goes up over that copy;
// and one the server does not have goes up. A server that keeps no change
// time has only its own.
func TestAdoptedRowsKeepNewerServerRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		// changed and uploaded are when the server's copy changed and when
		// it reached the server, from the change here; no changed leaves
		// the server's row without one.
		changed, uploaded time.Duration
		noChanged         bool
		serverNewer       bool
	}{
		{name: "renamed there since", changed: time.Hour, uploaded: time.Hour, serverNewer: true},
		{name: "an older copy uploaded late", changed: -time.Hour, uploaded: time.Hour},
		{name: "no change time there", uploaded: time.Hour, noChanged: true, serverNewer: true},
		{name: "no change time, uploaded before", uploaded: -time.Hour, noChanged: true},
	} {
		serverNewer := tc.serverNewer
		st := openStore(t)
		shared, err := st.CreatePage(store.NewPageOptions{Title: "Renamed here"})
		if err != nil {
			t.Fatal(err)
		}
		own, err := st.CreatePage(store.NewPageOptions{Title: "Mine"})
		if err != nil {
			t.Fatal(err)
		}
		entries, _ := st.Pending()
		for _, e := range entries {
			if err := st.Dequeue(e); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range []store.Page{shared, own} {
			if err := st.Requeue("pages", p.ID); err != nil {
				t.Fatal(err)
			}
		}
		stamp := func(d time.Duration) string { return shared.UpdatedAt.Add(d).UTC().Format(time.RFC3339Nano) }
		there := store.PageToRemote(shared)
		there["title"], there["created_at"], there["updated_at"] = "As it is there", stamp(-2*time.Hour), stamp(tc.uploaded)
		there["changed_at"] = stamp(tc.changed)
		if tc.noChanged {
			delete(there, "changed_at")
		}
		var mu gosync.Mutex
		sent := map[string]map[string]any{}
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			out := []map[string]any{}
			if r.URL.Path == "/rest/v1/pages" {
				switch r.Method {
				case http.MethodGet:
					if r.URL.Query().Get("id") == "eq."+shared.ID {
						out = append(out, there)
					}
				case http.MethodPost:
					var row map[string]any
					_ = json.NewDecoder(r.Body).Decode(&row)
					mu.Lock()
					sent[store.S(row, "id")] = row
					mu.Unlock()
					out = append(out, row)
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		}))
		svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
		if err := svc.push(context.Background()); err != nil {
			t.Fatal(err)
		}
		hs.Close()

		got, err := st.Page(shared.ID)
		if err != nil {
			t.Fatal(err)
		}
		row, ok := sent[shared.ID]
		if ok == serverNewer {
			t.Errorf("%s: the page was sent %v (%v)", tc.name, ok, row)
		}
		if ok && (row["title"] != "Renamed here" || store.Time(row["changed_at"]).UnixMilli() != shared.UpdatedAt.UnixMilli()) {
			t.Errorf("%s: sent %v, not as changed here", tc.name, row)
		}
		if want := map[bool]string{true: "As it is there", false: "Renamed here"}[serverNewer]; got.Title != want {
			t.Errorf("%s: title %q, want %q", tc.name, got.Title, want)
		}
		if row, ok := sent[own.ID]; !ok || row[store.AdoptKey] != nil || row["title"] != "Mine" {
			t.Errorf("%s: the device's own page went as %v (sent %v)", tc.name, row, ok)
		}
		if st.HasPending("pages", shared.ID) || st.HasPending("pages", own.ID) {
			t.Errorf("%s: pages still queued", tc.name)
		}
	}
}

// A row this device pulled from the server, and has not changed since,
// sends nothing as its pages join the account again, as after a while of
// syncing through iCloud Drive: the server's row stays, also when another
// device changed it there since, in a change it made offline before this
// one took the row, which by their times alone would lose to this copy.
// One changed here since, or by a device through iCloud Drive, goes up,
// as changed there.
func TestAdoptedPulledRowsGoOnlyWhenChangedHere(t *testing.T) {
	const id = "00000000-0000-0000-0000-0000000000f1"
	for _, tc := range []struct {
		name string
		// renamedThere is the title another device gave the row on the
		// server since, in a change made before this device took it; ""
		// for none.
		renamedThere, renamedHere, fromPeer string
		want                                string
	}{
		{name: "unchanged", want: "Plan"},
		{name: "renamed there since, offline before", renamedThere: "Plan, renamed offline", want: "Plan, renamed offline"},
		{name: "renamed here", renamedHere: "Plan, renamed here", want: "Plan, renamed here"},
		{name: "renamed by a device through iCloud Drive", fromPeer: "Plan, renamed on the iPad", want: "Plan, renamed on the iPad"},
	} {
		st := openStore(t)
		written := time.Now().UTC().Add(-time.Hour)
		server := func(at time.Time) string { return at.Format("2006-01-02T15:04:05.999999-07:00") }
		there := map[string]any{
			"id": id, "space_id": st.Session().SpaceID, "parent_id": nil, "folder_id": nil, "title": "Plan", "icon": nil,
			"sort_key": "a0", "created_by": "u1", "created_at": server(written.Add(-time.Hour)), "deleted_at": nil,
			"is_template": false, "tags": []any{"work"}, "default_template_id": nil, "placed_at": server(written),
			"updated_at": server(written), "changed_at": server(written.Add(-time.Minute)),
		}
		if err := st.PutRemotePage(there); err != nil {
			t.Fatal(err)
		}
		switch {
		case tc.renamedHere != "":
			if err := st.RenamePage(id, tc.renamedHere); err != nil {
				t.Fatal(err)
			}
			dequeueAll(t, st) // gone through iCloud Drive
		case tc.fromPeer != "":
			// As applyRow stores it: the change is the other device's, made
			// before this one took the row, so the row's time stays.
			peer := maps.Clone(there)
			peer["title"], peer[store.PeerKey] = tc.fromPeer, true
			if err := st.PutRemotePage(peer); err != nil {
				t.Fatal(err)
			}
		}
		if tc.renamedThere != "" {
			there = maps.Clone(there)
			there["title"], there["changed_at"], there["updated_at"] = tc.renamedThere, server(written.Add(-2*time.Hour)), server(time.Now())
		}
		if err := st.Requeue("pages", id); err != nil {
			t.Fatal(err)
		}
		local, err := st.Page(id)
		if err != nil {
			t.Fatal(err)
		}
		var mu gosync.Mutex
		var sent []map[string]any
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			out := []map[string]any{}
			if r.URL.Path == "/rest/v1/pages" {
				if r.Method == http.MethodGet {
					out = append(out, there)
				} else {
					var row map[string]any
					_ = json.NewDecoder(r.Body).Decode(&row)
					mu.Lock()
					sent = append(sent, row)
					mu.Unlock()
					out = append(out, row)
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		}))
		svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
		if err := svc.push(context.Background()); err != nil {
			t.Fatal(err)
		}
		hs.Close()

		goesUp := tc.renamedHere != "" || tc.fromPeer != ""
		switch {
		case !goesUp && len(sent) != 0:
			t.Errorf("%s: sent %v", tc.name, sent)
		case goesUp && (len(sent) != 1 || sent[0]["title"] != tc.want ||
			store.Time(sent[0]["changed_at"]).UnixMilli() != local.UpdatedAt.UnixMilli()):
			t.Errorf("%s: sent %v", tc.name, sent)
		}
		if got, _ := st.Page(id); got.Title != tc.want {
			t.Errorf("%s: title %q, want %q", tc.name, got.Title, tc.want)
		}
		if st.HasPending("pages", id) {
			t.Errorf("%s: still queued", tc.name)
		}
	}
}

// A page renamed, then moved under a page made after, keeps its place in
// the queue, before that page: the server refuses the move under a page it
// does not have yet, and the page goes once that one has, in the same
// push, with its rename, counting no failure.
func TestMoveUnderANewPageWaitsForIt(t *testing.T) {
	st := openStore(t)
	a, err := st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RenamePage(a.ID, "Meeting notes"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreatePage(store.NewPageOptions{Title: "Project"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MovePage(a.ID, b.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	known := map[string]bool{a.ID: true}
	var patched map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		out := []map[string]any{}
		if r.URL.Path == "/rest/v1/pages" {
			var row map[string]any
			_ = json.NewDecoder(r.Body).Decode(&row)
			switch r.Method {
			case http.MethodPost:
				known[store.S(row, "id")] = true
				out = append(out, row)
			case http.MethodPatch:
				// As pages_guard raises it, which PostgREST answers 500.
				if parent := store.S(row, "parent_id"); parent != "" && !known[parent] {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"code":"P0002","message":"parent_not_found"}`)
					return
				}
				patched = row
				row["id"] = a.ID
				out = append(out, row)
			case http.MethodGet:
				if r.URL.Query().Get("id") == "eq."+a.ID {
					out = append(out, map[string]any{"id": a.ID, "space_id": a.SpaceID, "title": "Notes", "sort_key": a.SortKey,
						"created_at": time.Now().UTC().Format(time.RFC3339Nano), "updated_at": time.Now().UTC().Format(time.RFC3339Nano)})
				}
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if patched["parent_id"] != b.ID || patched["title"] != "Meeting notes" {
		t.Errorf("the page went as %v", patched)
	}
	got, err := st.Page(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != b.ID || got.Title != "Meeting notes" {
		t.Errorf("the page is %q under %q here: the move and rename were undone", got.Title, got.ParentID)
	}
	if st.HasPending("pages", a.ID) || st.HasPending("pages", b.ID) {
		t.Error("pages still queued")
	}
}

// Two devices moving pages under each other at once: the server takes the
// first move and refuses the second (page_cycle) for good. This device's
// pull stores the first while its own waits, which would leave both pages
// out of the tree in a loop: its own page goes to the top level meanwhile,
// and its refused move gives way to the server's row, with no retries.
func TestMovesUnderEachOtherSettleOnTheServers(t *testing.T) {
	st := openStore(t)
	a, err := st.CreatePage(store.NewPageOptions{Title: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreatePage(store.NewPageOptions{Title: "B"})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.MovePage(a.ID, b.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	row := func(p store.Page, parent any) map[string]any {
		r := store.PageToRemote(p)
		r["parent_id"], r["created_at"], r["updated_at"] = parent, now, now
		return r
	}
	var patches int
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if r.URL.Path == "/rest/v1/pages" {
			switch r.Method {
			case http.MethodPatch:
				patches++
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"code":"23514","message":"page_cycle"}`)
				return
			case http.MethodGet:
				if r.URL.Query().Get("id") == "eq."+a.ID {
					out = append(out, row(a, nil))
				}
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	// The pull brings B under A, as the other device moved it.
	if err := svc.putServerPage(row(b, a.ID)); err != nil {
		t.Fatal(err)
	}
	gotA, _ := st.Page(a.ID)
	gotB, _ := st.Page(b.ID)
	if gotA.ParentID != "" || gotB.ParentID != a.ID {
		t.Fatalf("A under %q, B under %q: want A at the top, B under it", gotA.ParentID, gotB.ParentID)
	}

	for range 2 {
		if err := svc.push(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if st.HasPending("pages", a.ID) {
		t.Error("the refused move stays queued")
	}
	if patches != 1 {
		t.Errorf("the move went %d times", patches)
	}
	if gotA, _ = st.Page(a.ID); gotA.ParentID != "" {
		t.Errorf("A under %q: want the server's top level", gotA.ParentID)
	}
}

// A snapshot goes up as an insert the server keeps if it has it, without
// reading it back: sent again, as when the answer to its upload was lost,
// it is done, though no one may change a snapshot, and the server refuses
// an upsert of one it has.
func TestHistorySentAgainIsDone(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	if err := st.AddHistory(p.ID, doc.Empty(), "manual", "", "v1"); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	have := map[string]bool{}
	var prefers []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/rest/v1/page_history" || r.Method != http.MethodPost {
			_, _ = w.Write([]byte("[]"))
			return
		}
		prefers = append(prefers, r.Header.Get("Prefer"))
		var row map[string]any
		_ = json.NewDecoder(r.Body).Decode(&row)
		id := store.S(row, "id")
		ignore := strings.Contains(r.Header.Get("Prefer"), "ignore-duplicates")
		if have[id] && !ignore {
			// As page_history's policies have it: no one may update one.
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"42501","message":"new row violates row-level security policy"}`)
			return
		}
		have[id] = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	// Its first upload went, but its answer was lost: it is still queued.
	h, _ := st.History(p.ID)
	have[h[0].ID] = true
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.HasPending("page_history", h[0].ID) {
		t.Errorf("the snapshot stays queued (%v)", prefers)
	}
}

// A server failing, or asking for a wait, stops the push without counting
// against the entries, which go as soon as it is back; the loop leaves it
// alone for the wait it asks.
func TestBusyServerCountsNoFailure(t *testing.T) {
	st := openStore(t)
	if _, err := st.CreatePage(store.NewPageOptions{Title: "Plan"}); err != nil {
		t.Fatal(err)
	}
	status := http.StatusInternalServerError
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "120")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "busy"})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	for _, s := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		status = s
		if err := svc.push(context.Background()); !remote.IsBusy(err) {
			t.Fatalf("%d: push %v", s, err)
		}
		entries, _ := st.Pending()
		for _, e := range entries {
			if e.Attempts != 0 {
				t.Errorf("%d: %s/%s counted %d failures", s, e.Entity, e.EntityID, e.Attempts)
			}
		}
	}
	svc.mu.Lock()
	wait := time.Until(svc.busyUntil)
	svc.mu.Unlock()
	if wait < time.Minute || wait > 2*time.Minute {
		t.Errorf("waits %v for a Retry-After of 2 minutes", wait)
	}
	ran := false
	svc.OnState = func(running bool, _ error) { ran = ran || running }
	loop, stop := context.WithCancel(context.Background())
	defer stop()
	_ = svc.syncNow(loop, true, true)
	if ran {
		t.Error("the loop's pass ran in the wait the server asked for")
	}
}

// A page whose parent neither waits here nor is on the server, as one that
// came through iCloud Drive before its parent did, is refused with a 500
// (parent_not_found, P0002), which is that page's own: the rest of the
// queue and the pull go on, and it waits as a failure does.
func TestAPageWithoutItsParentHoldsUpNothing(t *testing.T) {
	st := openStore(t)
	const child = "00000000-0000-0000-0000-0000000000c1"
	if err := st.PutRemotePage(map[string]any{"id": child, "space_id": st.Session().SpaceID, "parent_id": "00000000-0000-0000-0000-0000000000a9",
		"title": "Child", "sort_key": "a1", "created_at": "2026-11-01T00:00:00Z", "updated_at": "2026-11-01T00:00:00Z", store.PeerKey: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Requeue("pages", child); err != nil {
		t.Fatal(err)
	}
	later, err := st.CreatePage(store.NewPageOptions{Title: "Later"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("space.u1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var went []string
	pulled := false
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		pulled = pulled || r.Method == http.MethodGet && r.URL.Path == "/rest/v1/folders"
		if r.Method == http.MethodPost && r.URL.Path == "/rest/v1/pages" {
			var row map[string]any
			_ = json.NewDecoder(r.Body).Decode(&row)
			if store.S(row, "parent_id") != "" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"code":"P0002","message":"parent_not_found"}`)
				return
			}
			went = append(went, store.S(row, "id"))
		}
		_, _ = io.WriteString(w, "[]")
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.syncNow(context.Background(), false, true); err != nil {
		t.Fatalf("the pass stopped: %v", err)
	}
	if !slices.Equal(went, []string{later.ID}) || !pulled {
		t.Errorf("went %q, pulled %v", went, pulled)
	}
	if n := attempts(t, st, "pages", child); n != 1 {
		t.Errorf("%d failures", n)
	}
}

// A comment queued before comments were cut to what the server keeps goes
// up cut: whole, it would be refused every time.
func TestACommentTooLongGoesUpCut(t *testing.T) {
	st := openStore(t)
	long := strings.Repeat("字", 100005)
	c := store.Comment{ID: "00000000-0000-0000-0000-0000000000c2", PageID: "00000000-0000-0000-0000-0000000000a1", BlockID: "b1", AuthorKind: "user",
		Body: long, Quote: long, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.PutComment(c); err != nil {
		t.Fatal(err)
	}
	if err := st.Queue("comments", c.ID, store.CommentToRemote(c), "new comment", c.PageID); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var sent map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			var row map[string]any
			_ = json.NewDecoder(r.Body).Decode(&row)
			if len([]rune(store.S(row, "body"))) > 100000 || len([]rune(store.S(row, "quote"))) > 100000 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"code":"23514","message":"comments_body_length"}`)
				return
			}
			sent = row
		}
		_, _ = io.WriteString(w, "[]")
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.PendingCount() != 0 || sent == nil {
		t.Errorf("%d queued, sent %v", st.PendingCount(), sent != nil)
	}
}

// The account having as many devices as it may leaves this one out of its
// list, and it syncs all the same.
func TestTooManyDevicesStopsNoPass(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("space.u1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	asked, pages := 0, 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/rest/v1/devices":
			asked++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":"23514","message":"too_many_devices"}`)
			return
		case "/rest/v1/pages":
			if r.Method == http.MethodPost {
				pages++
			}
		}
		_, _ = io.WriteString(w, "[]")
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()
	for range 2 {
		if err := svc.pass(ctx, false); err != nil {
			t.Fatalf("the pass stopped: %v", err)
		}
	}
	if pages == 0 || asked != 1 || st.HasPending("pages", p.ID) {
		t.Errorf("%d pages up, asked %d times, still queued %v", pages, asked, st.HasPending("pages", p.ID))
	}
}

// refusesNUL is a server that refuses what Postgres does, in text or jsonb,
// and keeps the rows of the writes it takes.
type refusesNUL struct {
	mu      gosync.Mutex
	refused int
	writes  map[string][]string
}

func (s *refusesNUL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, "[]")
		return
	}
	var sent any
	_ = json.Unmarshal(body, &sent)
	if holdsNUL(sent) {
		s.refused++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":"22P05","message":"unsupported Unicode escape sequence"}`)
		return
	}
	if s.writes == nil {
		s.writes = map[string][]string{}
	}
	s.writes[r.URL.Path] = append(s.writes[r.URL.Path], string(body))
	row := map[string]any{}
	_ = json.Unmarshal(body, &row)
	row["revision"], row["updated_at"] = 0, time.Now().UTC().Format(time.RFC3339Nano)
	_ = json.NewEncoder(w).Encode([]map[string]any{row})
}

// holdsNUL reports whether a NUL is in a value decoded from JSON.
func holdsNUL(v any) bool {
	switch v := v.(type) {
	case string:
		return strings.ContainsRune(v, 0)
	case []any:
		return slices.ContainsFunc(v, holdsNUL)
	case map[string]any:
		for k, e := range v {
			if strings.ContainsRune(k, 0) || holdsNUL(e) {
				return true
			}
		}
	}
	return false
}

// A page, its title and its comment holding a NUL, which Postgres refuses
// whole, go up with U+FFFD for it, a new page and an entry queued long ago
// alike, and the page here takes what went: it and the server agree.
func TestWhatPostgresRefusesGoesUpClean(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	c := &doc.Content{Children: []*doc.Node{para("a", "before\x00after"), para("b", `shown as \u0000 in code`)}, Blocks: map[string]int64{}, Deleted: map[string]int64{}}
	if _, _, err := st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	if err := st.RenamePage(p.ID, "Plan\x00B"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddComment(p.ID, "a", "Look\x00", "", "quote\x00", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.AddHistory(p.ID, c, "autosave", "", "Autosave"); err != nil {
		t.Fatal(err)
	}
	srv := &refusesNUL{}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	got := 0
	svc.OnPageContent = func(string) { got++ }
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.refused != 0 || st.PendingCount() != 0 {
		t.Fatalf("%d refused, %d queued", srv.refused, st.PendingCount())
	}
	for _, path := range []string{"/rest/v1/pages", "/rest/v1/page_content", "/rest/v1/comments", "/rest/v1/page_history"} {
		if n := len(srv.writes[path]); n == 0 {
			t.Errorf("nothing went to %s", path)
		}
	}
	row, err := st.ContentRow(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := doc.Decode(row.Content)
	if err != nil {
		t.Fatal(err)
	}
	if textOf(stored, "a") != "before\uFFFDafter" || textOf(stored, "b") != `shown as \u0000 in code` {
		t.Errorf("stored %q and %q", textOf(stored, "a"), textOf(stored, "b"))
	}
	if strings.Contains(row.PlainText, "\x00") || row.BaseContent != row.Content {
		t.Errorf("plain text %q, base as the content %v", row.PlainText, row.BaseContent == row.Content)
	}
	if got == 0 {
		t.Error("the page open here was not told of its content")
	}
}

// timingOut is a server that ends statements as out of time: every one, or
// with heavy, those heavy says, a light read of spaces answered all the
// same. It counts the requests.
type timingOut struct {
	mu       gosync.Mutex
	requests int
	heavy    func(r *http.Request, body string) bool
	bodies   []string
}

func (s *timingOut) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if s.heavy == nil || r.URL.Path != "/rest/v1/spaces" && s.heavy(r, string(raw)) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"code":"57014","message":"canceling statement due to statement timeout"}`)
		return
	}
	if r.Method != http.MethodGet {
		s.bodies = append(s.bodies, string(raw))
		_, _ = io.WriteString(w, `[{"id":"x","revision":0,"updated_at":"2026-11-06T00:00:00Z"}]`)
		return
	}
	_, _ = io.WriteString(w, "[]")
}

// A database whose statements all run out of time is the server in
// trouble: the push stops at once, counting against no entry, or each
// would wait long after the server is back.
func TestADatabaseTimingOutCountsAgainstNoEntry(t *testing.T) {
	st := openStore(t)
	for range 10 {
		if _, err := st.CreatePage(store.NewPageOptions{Title: "Plan"}); err != nil {
			t.Fatal(err)
		}
	}
	srv := &timingOut{}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); !remote.IsBusy(err) {
		t.Fatalf("push: %v", err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		if e.Attempts != 0 {
			t.Errorf("%s/%s counted %d failures", e.Entity, e.EntityID, e.Attempts)
		}
	}
	if srv.requests > 2 {
		t.Errorf("%d requests to a server in trouble", srv.requests)
	}
}

// A statement too heavy for a server that answers otherwise is its entry's
// own: it waits, and the queue behind it goes, which it would stop each
// pass for good.
func TestAPageTooHeavyToSaveHoldsUpNothing(t *testing.T) {
	st := openStore(t)
	heavy, err := st.CreatePage(store.NewPageOptions{Title: "Heavy"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := st.CreatePage(store.NewPageOptions{Title: "Later"})
	if err != nil {
		t.Fatal(err)
	}
	srv := &timingOut{heavy: func(r *http.Request, body string) bool {
		return r.Method != http.MethodGet && strings.Contains(body, heavy.ID)
	}}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatalf("the push stopped: %v", err)
	}
	if n := attempts(t, st, "pages", heavy.ID); n != 1 {
		t.Errorf("%d failures", n)
	}
	if st.HasPending("pages", later.ID) {
		t.Error("the page behind it did not go")
	}
}

// A server that answers reads and times out every write is in trouble
// however it reads: past a few, the push stops counting them against their
// entries.
func TestAServerTimingOutEveryWriteStopsCounting(t *testing.T) {
	st := openStore(t)
	for range 10 {
		if _, err := st.CreatePage(store.NewPageOptions{Title: "Plan"}); err != nil {
			t.Fatal(err)
		}
	}
	srv := &timingOut{heavy: func(r *http.Request, _ string) bool { return r.Method != http.MethodGet }}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); !remote.IsBusy(err) {
		t.Fatalf("push: %v", err)
	}
	counted := 0
	entries, _ := st.Pending()
	for _, e := range entries {
		if e.Attempts > 0 {
			counted++
		}
	}
	if counted != ownTimeouts {
		t.Errorf("%d of %d entries counted a failure", counted, len(entries))
	}
}
