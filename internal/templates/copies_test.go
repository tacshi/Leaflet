package templates

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func TestDuplicate(t *testing.T) {
	st := openStore(t)
	mine, _ := st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if o := Duplicate(st, mine); o.AfterID != mine.ID || o.Title != "Notes copy" {
		t.Errorf("a copy of the user's page %+v", o)
	}
	const space, at = "00000000-0000-0000-0000-0000000000cc", "2026-01-01T00:00:00Z"
	theirs, kid := "00000000-0000-0000-0000-0000000000bb", "00000000-0000-0000-0000-0000000000be"
	share := func(role string) {
		for _, p := range []map[string]any{
			{"id": theirs, "space_id": space, "title": "Theirs", "sort_key": "V", "created_at": at, "updated_at": at},
			{"id": kid, "space_id": space, "parent_id": theirs, "title": "Kid", "sort_key": "V", "created_at": at, "updated_at": at},
		} {
			if err := st.PutRemotePage(p); err != nil {
				t.Fatal(err)
			}
			id := p["id"].(string)
			if err := st.SetMembers(id, []store.Member{{PageID: id, UserID: st.Session().UserID, Role: role}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	page := func(id string) store.Page {
		p, err := st.Page(id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	share("editor")
	if o := Duplicate(st, page(kid)); o.ParentID != theirs || o.AfterID != kid {
		t.Errorf("a copy of a subpage the user may add beside %+v", o)
	}
	if o := Duplicate(st, page(theirs)); o != (store.CopyOptions{Title: "Theirs copy"}) {
		t.Errorf("a copy of a shared page at the top %+v", o)
	}
	share("viewer")
	if o := Duplicate(st, page(kid)); o != (store.CopyOptions{Title: "Kid copy"}) {
		t.Errorf("a copy of a subpage the user can't add beside %+v", o)
	}
	if CopyTitle(" ") != "" {
		t.Error("an untitled page's copy is titled")
	}
}

func TestRecordingIn(t *testing.T) {
	st := openStore(t)
	notes, _ := st.CreatePage(store.NewPageOptions{Title: "Notes"})
	call, _ := st.CreatePage(store.NewPageOptions{ParentID: notes.ID, Title: "Call"})
	other, _ := st.CreatePage(store.NewPageOptions{Title: "Other"})
	under := func(id, other string) bool {
		for id != "" {
			if id == other {
				return true
			}
			p, err := st.Page(id)
			if err != nil {
				return false
			}
			id = p.ParentID
		}
		return false
	}
	rec := recording.NewController(st, remote.New(remote.Config(), st))
	if RecordingIn(st, rec, notes.ID, under) {
		t.Error("a recording where there is none")
	}
	rec.State.Active, rec.State.PageID = true, call.ID
	if !RecordingIn(st, rec, notes.ID, under) || RecordingIn(st, rec, other.ID, under) {
		t.Error("a recording being made")
	}
	rec.State.Active = false
	r, err := st.CreateRecording(call.ID, "b1", "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	rec.Transcribing[r.ID] = true
	if !RecordingIn(st, rec, notes.ID, under) || RecordingIn(st, rec, other.ID, under) {
		t.Error("a recording being transcribed")
	}
}

func TestCopyError(t *testing.T) {
	for _, err := range []error{store.ErrCopyNotHere, fs.ErrNotExist} {
		if got := CopyError(err); !errors.Is(got, ErrFilesUploading) || got.Error() != locales.TemplateFilesUploading() {
			t.Errorf("%v: %v", err, got)
		}
	}
	other := errors.New("disk full")
	if got := CopyError(other); got != other {
		t.Errorf("another error said %v", got)
	}
}

func fetching(t *testing.T, st *store.Store, served map[string]string) (store.Page, store.Recording, *syncsvc.Service) {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for prefix, body := range served {
			if strings.HasPrefix(r.URL.Path, "/storage/v1/object/authenticated/"+prefix+"/") {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/storage/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(hs.Close)
	p, _ := st.CreatePage(store.NewPageOptions{Title: "Trip"})
	f := store.File{ID: doc.NewRowID(), SpaceID: st.Session().SpaceID, PageID: p.ID, Name: "plan.pdf", MimeType: "application/pdf",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.StoragePath = f.SpaceID + "/" + f.ID + "/plan.pdf"
	if err := st.PutFile(f); err != nil {
		t.Fatal(err)
	}
	r, err := st.CreateRecording(p.ID, "r1", "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	r.AudioPath, r.Status, r.UploadedAt = st.Session().SpaceID+"/"+r.ID+".wav", "done", time.Now()
	if err := st.PutRecording(r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "r1", Type: "recording", Data: map[string]any{"recording_id": r.ID}},
		{ID: "b1", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	return p, r, syncsvc.New(st, remote.SignedInForTest(hs.URL, st, "u1"))
}

func TestNeedsAndFetch(t *testing.T) {
	st := openStore(t)
	p, r, sync := fetching(t, st, map[string]string{"files": "%PDF", "recordings": "RIFF"})
	if _, err := Needs(st, syncsvc.New(st, remote.New(remote.Config(), st)), p.ID); !errors.Is(err, ErrFilesUploading) {
		t.Errorf("needs without sync: %v", err)
	}
	needs, err := Needs(st, sync, p.ID)
	if err != nil || len(needs) != 2 {
		t.Fatalf("needs %+v, %v", needs, err)
	}
	audio, err := Fetch(st, sync, needs)
	if err != nil || len(audio) != 1 || audio[0] != r.ID {
		t.Fatalf("fetched audio %v, %v", audio, err)
	}
	if needs, err := Needs(st, sync, p.ID); err != nil || len(needs) != 0 {
		t.Errorf("needs once fetched %+v, %v", needs, err)
	}
	if _, err := st.CopyPage(p.ID, store.CopyOptions{Title: "Trip copy"}); err != nil {
		t.Errorf("copy once fetched: %v", err)
	}
}

func TestFetchFailsWithWhatCame(t *testing.T) {
	st := openStore(t)
	p, r, sync := fetching(t, st, map[string]string{"recordings": "RIFF"})
	needs, err := Needs(st, sync, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	audio, err := Fetch(st, sync, needs)
	if err == nil || len(audio) != 1 || audio[0] != r.ID {
		t.Errorf("fetched %v, %v", audio, err)
	}
}

func TestNeedsWaitsForUploads(t *testing.T) {
	st := openStore(t)
	p, r, sync := fetching(t, st, nil)
	if _, err := st.UpdateRecording(r.ID, func(r *store.Recording) { r.AudioPath = "" }); err != nil {
		t.Fatal(err)
	}
	if _, err := Needs(st, sync, p.ID); !errors.Is(err, ErrFilesUploading) {
		t.Errorf("needs %v", err)
	}
}

func TestDropAudio(t *testing.T) {
	st := openStore(t)
	p, _ := st.CreatePage(store.NewPageOptions{Title: "Call"})
	r, err := st.CreateRecording(p.ID, "r1", "soniox", "m", "", "en")
	if err != nil {
		t.Fatal(err)
	}
	local, err := st.DownloadedAudio(r.ID, []byte("RIFF"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRecording(r.ID, func(r *store.Recording) { r.AudioPath = "u1/" + r.ID + ".wav" }); err != nil {
		t.Fatal(err)
	}
	s := st.Settings()
	s.KeepAudioOnDevice = "always"
	DropAudio(st, s, []string{r.ID})
	if got, _ := st.Recording(r.ID); got.LocalPath == "" {
		t.Error("the audio went though it is kept")
	}
	s.KeepAudioOnDevice = "until_uploaded"
	DropAudio(st, s, []string{r.ID})
	if got, _ := st.Recording(r.ID); got.LocalPath != "" {
		t.Errorf("the audio stays at %q", got.LocalPath)
	}
	if _, err := os.Stat(local); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the audio's file stays: %v", err)
	}
}
