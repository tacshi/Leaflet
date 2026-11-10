package sync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

type storageServer struct {
	mu       gosync.Mutex
	objects  []string
	requests []string
	rows     map[string][]map[string]any
	hold     chan struct{}
	unread   string
	closing  chan struct{}
	refuse   bool
	reads    chan struct{}
	began    bool
}

var validKey = regexp.MustCompile(`^[A-Za-z0-9_/!.*'() &$=@;:+,?-]*$`)

func (s *storageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	hold, unread := s.hold, s.unread
	s.mu.Unlock()
	if key, ok := strings.CutPrefix(r.URL.Path, "/storage/v1/object/files/"); ok && r.Method == http.MethodPost {
		s.mu.Lock()
		s.began = true
		s.mu.Unlock()
		if unread != "" && strings.Contains(key, unread) {
			<-s.closing
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if s.refuse || !validKey.MatchString(key) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"statusCode":"400","error":"InvalidKey","message":"Invalid key: `+key+`"}`)
			return
		}
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		s.mu.Lock()
		s.objects = append(s.objects, key)
		s.mu.Unlock()
		_, _ = io.WriteString(w, "{}")
		return
	}
	if table, ok := strings.CutPrefix(r.URL.Path, "/rest/v1/"); ok && r.Method != http.MethodGet {
		var row map[string]any
		_ = json.NewDecoder(r.Body).Decode(&row)
		s.mu.Lock()
		if s.rows == nil {
			s.rows = map[string][]map[string]any{}
		}
		s.rows[table] = append(s.rows[table], row)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode([]map[string]any{row})
		return
	}
	s.mu.Lock()
	reads := s.reads
	if !s.began {
		reads = nil
	}
	s.mu.Unlock()
	if reads != nil {
		<-reads
	}
	_, _ = io.WriteString(w, "[]")
}

func (s *storageServer) asked(request string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r == request {
			n++
		}
	}
	return n
}

func (s *storageServer) stored() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.objects...)
}

func (s *storageServer) rowsOf(table string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[table]
}

func uploading(t *testing.T, st *store.Store) (*Service, *storageServer) {
	t.Helper()
	srv := &storageServer{closing: make(chan struct{})}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	t.Cleanup(func() { close(srv.closing) })
	if err := st.SetValue("space.u1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	return New(st, remote.SignedInForTest(hs.URL, st, "u1")), srv
}

func shortened(t *testing.T, wait, stall time.Duration) {
	t.Helper()
	oldWait, oldStall := uploadWait, uploadStall
	uploadWait, uploadStall = wait, stall
	t.Cleanup(func() { uploadWait, uploadStall = oldWait, oldStall })
}

func addedFile(t *testing.T, st *store.Store, name string, size int64) store.File {
	t.Helper()
	source := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if err := os.Truncate(source, size); err != nil {
			t.Fatal(err)
		}
	}
	f, err := st.AddFile(source, "", "", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func awaitUpload(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.mu.Lock()
		up := svc.uploading
		svc.mu.Unlock()
		if up == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the upload never ended")
		}
		time.Sleep(time.Millisecond)
	}
}

func awaitRuns(t *testing.T, svc *Service, entity string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !svc.uploadRuns(entity) {
		if time.Now().After(deadline) {
			t.Fatal("the upload never started")
		}
		time.Sleep(time.Millisecond)
	}
}

func attempts(t *testing.T, st *store.Store, entity, id string) int {
	t.Helper()
	entries, err := st.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Entity == entity && e.EntityID == id {
			return e.Attempts
		}
	}
	t.Fatalf("%s/%s is not queued", entity, id)
	return 0
}

func TestAFileQueuedUnderARefusedKeyGoesUp(t *testing.T) {
	st := openStore(t)
	f := addedFile(t, st, "plan.pdf", 0)
	f.Name, f.StoragePath = "报告.pdf", f.SpaceID+"/"+f.ID+"/报告.pdf"
	if err := st.PutFile(f); err != nil {
		t.Fatal(err)
	}
	if err := st.Queue("files", f.ID, store.FileToRemote(f), "new file", ""); err != nil {
		t.Fatal(err)
	}
	upload := map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": f.LocalPath, "content_type": "application/pdf", "file_id": f.ID}
	if err := st.Queue("storage", "files/"+f.ID, upload, "file waiting to upload", ""); err != nil {
		t.Fatal(err)
	}
	svc, srv := uploading(t, st)
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := f.SpaceID + "/" + f.ID + "/__.pdf"
	if got := srv.stored(); len(got) != 1 || got[0] != want {
		t.Errorf("stored %q", got)
	}
	rows := srv.rowsOf("files")
	if len(rows) != 1 || rows[0]["storage_path"] != want || rows[0]["name"] != "报告.pdf" {
		t.Errorf("rows %v", rows)
	}
	if st.PendingCount() != 0 {
		t.Errorf("%d still queued", st.PendingCount())
	}
	if got, _ := st.File(f.ID); got.StoragePath != want || got.Name != "报告.pdf" {
		t.Errorf("file %+v", got)
	}
}

func TestAFileNamedBeyondASCIIGoesUp(t *testing.T) {
	st := openStore(t)
	f := addedFile(t, st, "报告 #1.pdf", 0)
	svc, srv := uploading(t, st)
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := srv.stored(); len(got) != 1 || got[0] != f.StoragePath {
		t.Errorf("stored %q", got)
	}
	if rows := srv.rowsOf("files"); len(rows) != 1 || rows[0]["name"] != "报告 #1.pdf" {
		t.Errorf("rows %v", rows)
	}
	if st.PendingCount() != 0 {
		t.Errorf("%d still queued", st.PendingCount())
	}
}

func TestARefusedKeyWaitsTheLongest(t *testing.T) {
	st := openStore(t)
	f := addedFile(t, st, "plan.pdf", 0)
	svc, srv := uploading(t, st)
	srv.refuse = true
	if err := svc.push(context.Background()); err != nil {
		t.Fatalf("the push stopped: %v", err)
	}
	if n := attempts(t, st, "storage", "files/"+f.ID); n != 1 {
		t.Fatalf("%d failures", n)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		if e.Entity != "storage" {
			continue
		}
		svc.mu.Lock()
		wait := time.Until(svc.retryAt[outboxKey{e.ID, e.Revision}])
		svc.mu.Unlock()
		if wait < retryMax-time.Minute {
			t.Errorf("waits %v", wait)
		}
	}
}

func TestRemovingAnObjectUnderARefusedKeyIsDone(t *testing.T) {
	st := openStore(t)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"statusCode":"400","error":"InvalidKey","message":"Invalid key"}`)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	done, err := svc.removeStored(context.Background(), map[string]any{"bucket": "files", "path": "s/f/报告.pdf", "file_id": "f"})
	if !done || err != nil {
		t.Errorf("done %v, %v", done, err)
	}
}

func TestASlowUploadHoldsUpNothing(t *testing.T) {
	shortened(t, 20*time.Millisecond, time.Minute)
	st := openStore(t)
	f := addedFile(t, st, "film.mov", 0)
	later, err := st.CreatePage(store.NewPageOptions{Title: "Later"})
	if err != nil {
		t.Fatal(err)
	}
	svc, srv := uploading(t, st)
	release := make(chan struct{})
	srv.hold = release
	ctx := context.Background()
	for range 3 {
		if err := svc.syncNow(ctx, false, true); err != nil {
			t.Fatalf("the pass stopped: %v", err)
		}
	}
	if rows := srv.rowsOf("pages"); len(rows) != 1 || rows[0]["id"] != later.ID {
		t.Errorf("pages %v", rows)
	}
	if srv.asked("GET /rest/v1/folders") != 3 {
		t.Errorf("%d pulls of 3", srv.asked("GET /rest/v1/folders"))
	}
	if n := srv.asked("POST /storage/v1/object/files/" + f.StoragePath); n != 1 {
		t.Errorf("uploaded %d times", n)
	}
	if rows := srv.rowsOf("files"); len(rows) != 0 {
		t.Errorf("the row went before its file: %v", rows)
	}

	close(release)
	awaitUpload(t, svc)
	select {
	case <-svc.kick:
	case <-time.After(5 * time.Second):
		t.Fatal("no pass was asked for")
	}
	if st.HasPending("storage", "files/"+f.ID) {
		t.Error("the upload stayed queued")
	}
	if err := svc.syncNow(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	if rows := srv.rowsOf("files"); len(rows) != 1 || st.PendingCount() != 0 {
		t.Errorf("rows %v, %d queued", rows, st.PendingCount())
	}
}

func TestAStalledUploadGivesWay(t *testing.T) {
	shortened(t, 10*time.Millisecond, 200*time.Millisecond)
	retryBase = time.Minute
	t.Cleanup(func() { retryBase = 0 })
	st := openStore(t)
	stuck := addedFile(t, st, "stuck.mov", 64<<20)
	next := addedFile(t, st, "next.pdf", 0)
	svc, srv := uploading(t, st)
	srv.unread = "stuck.mov"
	ctx := context.Background()
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	awaitRuns(t, svc, "files/"+stuck.ID)
	awaitUpload(t, svc)
	if n := attempts(t, st, "storage", "files/"+stuck.ID); n != 1 {
		t.Errorf("%d failures", n)
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	awaitUpload(t, svc)
	if got := srv.stored(); len(got) != 1 || got[0] != next.StoragePath {
		t.Errorf("stored %q", got)
	}
}

func TestRemovalWaitsForAnUploadUnderWay(t *testing.T) {
	shortened(t, 10*time.Millisecond, time.Minute)
	st := openStore(t)
	f := addedFile(t, st, "film.mov", 0)
	svc, srv := uploading(t, st)
	release := make(chan struct{})
	srv.hold = release
	ctx := context.Background()
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	const remove = "DELETE /storage/v1/object/files"
	if srv.asked(remove) != 0 {
		t.Fatal("removed before the upload ended")
	}
	close(release)
	awaitUpload(t, svc)
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if srv.asked(remove) != 1 || st.PendingCount() != 0 {
		t.Errorf("%d removals, %d queued", srv.asked(remove), st.PendingCount())
	}
}

func TestStopEndsAnUpload(t *testing.T) {
	shortened(t, 10*time.Millisecond, time.Minute)
	st := openStore(t)
	f := addedFile(t, st, "film.mov", 0)
	svc, srv := uploading(t, st)
	srv.hold = make(chan struct{})
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc.Stop()
	awaitUpload(t, svc)
	if n := attempts(t, st, "storage", "files/"+f.ID); n != 0 {
		t.Errorf("%d failures", n)
	}
}

func TestAnotherSessionEndsAnUpload(t *testing.T) {
	shortened(t, 10*time.Millisecond, 2*time.Second)
	st := openStore(t)
	f := addedFile(t, st, "film.mov", 0)
	svc, srv := uploading(t, st)
	srv.hold = make(chan struct{})
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc.be.SignInForTest("u2")
	awaitUpload(t, svc)
	if n := attempts(t, st, "storage", "files/"+f.ID); n != 0 {
		t.Errorf("%d failures", n)
	}
}

func TestDrainWaitsForAnUploadToEnd(t *testing.T) {
	shortened(t, 10*time.Millisecond, time.Minute)
	st := openStore(t)
	addedFile(t, st, "film.mov", 0)
	svc, srv := uploading(t, st)
	release := make(chan struct{})
	srv.hold = release
	time.AfterFunc(100*time.Millisecond, func() { close(release) })
	if err := svc.Drain(); err != nil {
		t.Fatal(err)
	}
	if st.PendingCount() != 0 || len(srv.rowsOf("files")) != 1 {
		t.Errorf("%d queued, rows %v", st.PendingCount(), srv.rowsOf("files"))
	}
}

func TestDrainTakesARowAfterAnUploadThatEndedInThePass(t *testing.T) {
	shortened(t, 10*time.Millisecond, time.Minute)
	st := openStore(t)
	f := addedFile(t, st, "film.mov", 0)
	svc, srv := uploading(t, st)
	release, reads := make(chan struct{}), make(chan struct{})
	srv.hold, srv.reads = release, reads
	entity, deadline := "files/"+f.ID, time.Now().Add(5*time.Second)
	go func() {
		defer close(reads)
		for !svc.uploadRuns(entity) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		close(release)
		for svc.uploadRuns(entity) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}()
	if err := svc.Drain(); err != nil {
		t.Fatal(err)
	}
	if st.PendingCount() != 0 || len(srv.rowsOf("files")) != 1 {
		t.Errorf("%d queued, rows %v", st.PendingCount(), srv.rowsOf("files"))
	}
}

func TestDrainTakesUploadsInTurn(t *testing.T) {
	shortened(t, 10*time.Millisecond, time.Minute)
	st := openStore(t)
	addedFile(t, st, "first.mov", 0)
	addedFile(t, st, "second.mov", 0)
	svc, srv := uploading(t, st)
	release := make(chan struct{})
	srv.hold = release
	time.AfterFunc(100*time.Millisecond, func() { close(release) })
	if err := svc.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := srv.stored(); len(got) != 2 || st.PendingCount() != 0 {
		t.Errorf("stored %q, %d queued", got, st.PendingCount())
	}
}

func TestDrainEndsWithAStalledUpload(t *testing.T) {
	shortened(t, 10*time.Millisecond, 200*time.Millisecond)
	st := openStore(t)
	addedFile(t, st, "stuck.mov", 64<<20)
	svc, srv := uploading(t, st)
	srv.unread = "stuck.mov"
	start := time.Now()
	if err := svc.Drain(); err != nil {
		t.Fatal(err)
	}
	if wait := time.Since(start); wait > 3*time.Second {
		t.Errorf("waited %v", wait)
	}
	if st.PendingCount() == 0 {
		t.Error("the stalled upload left the queue")
	}
}

func TestDrainWaitsNoLongerThanItsLimit(t *testing.T) {
	shortened(t, 10*time.Millisecond, time.Minute)
	old := drainWait
	drainWait = 100 * time.Millisecond
	defer func() { drainWait = old }()
	st := openStore(t)
	f := addedFile(t, st, "film.mov", 0)
	svc, srv := uploading(t, st)
	srv.hold = make(chan struct{})
	if err := svc.Drain(); err != nil {
		t.Fatal(err)
	}
	if !st.HasPending("storage", "files/"+f.ID) || !svc.uploadRuns("files/"+f.ID) {
		t.Error("the upload should go on, still queued")
	}
	close(srv.hold)
	awaitUpload(t, svc)
}
