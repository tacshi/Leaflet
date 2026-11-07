package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
)

// versions returns page id's kept versions, newest first.
func versions(t *testing.T, c *Core, id string) []version {
	t.Helper()
	raw, err := c.History(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []version
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// A version of no blocks restores as one to type in, the page open or
// not, as on the desktop; the version kept before restoring brings the
// page back, for Undo.
func TestRestoringAnEmptyVersionLeavesABlock(t *testing.T) {
	c, ev := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	var err error
	c.q.do(func() { err = c.core.Store.AddHistory(page.ID, &doc.Content{}, "manual", "", "Empty") })
	if err != nil {
		t.Fatal(err)
	}
	empty := versions(t, c, page.ID)[0]
	c.q.do(func() {
		err = c.editContent(page.ID, func(content *doc.Content) bool {
			content.Children = []*doc.Node{paragraph("p", "Milk")}
			return true
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	kept, err := c.RestoreVersion(page.ID, empty.ID)
	if err != nil || kept == "" {
		t.Fatalf("restore: kept %q, %v", kept, err)
	}
	if got := c.core.Store.Content(page.ID); len(got.Children) != 1 || got.Children[0].Type != "paragraph" || texts(got) != "" {
		t.Fatalf("restored to %s", got.Encode())
	}
	if _, err := c.RestoreVersion(page.ID, kept); err != nil {
		t.Fatal(err)
	}
	if got := texts(c.core.Store.Content(page.ID)); got != "Milk" {
		t.Fatalf("undone to %q", got)
	}

	// Open, the editor is given the block.
	openEditor(t, c, page.ID)
	if _, err := c.RestoreVersion(page.ID, empty.ID); err != nil {
		t.Fatal(err)
	}
	u := wait(t, ev, "external")
	if got := doc.MustDecode(u.content); len(got.Children) != 1 || texts(got) != "" {
		t.Fatalf("the editor shows %s", u.content)
	}
	if _, err := c.RestoreVersion(page.ID, "gone"); err == nil {
		t.Error("restored a version not kept")
	}
}

// Restoring a version settles the page's last agent run: its Undo shows
// no more outside the chat, as the desktop's run bar hides, until a run
// changes the page again.
func TestRestoringAVersionSettlesTheLastRun(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	var err error
	c.q.do(func() {
		s := c.core.Hub.Session(page.ID)
		s.Runs = append(s.Runs, &agents.Run{ID: "r1", Status: "done", Ops: []doc.Op{{}}})
		err = c.core.Store.AddHistory(page.ID, c.content(page.ID), "agent_run", "", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RestoreVersion(page.ID, versions(t, c, page.ID)[0].ID); err != nil {
		t.Fatal(err)
	}
	settled := func() bool {
		raw, err := c.AgentState(page.ID)
		if err != nil {
			t.Fatal(err)
		}
		var st agentState
		_ = json.Unmarshal([]byte(raw), &st)
		return len(st.Runs) == 1 && st.Runs[0].Settled
	}
	if !settled() {
		t.Fatal("the run still offers Undo")
	}
	host{c}.Tint(page.ID, nil)
	if settled() {
		t.Error("a run that changed the page again stays settled")
	}
}

// A file another device uploaded downloads as a block asks for it, to the
// disk as it comes, and is found there from then on.
func TestLocalFileDownloadsAFileFromElsewhere(t *testing.T) {
	c, _ := open(t)
	data := strings.Repeat("a long video's bytes ", 50000)
	var asked atomic.Int32
	signInRockyTo(t, c, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/storage/v1/object/authenticated/files/space/clip.mp4" {
			asked.Add(1)
			_, _ = io.WriteString(w, data)
			return
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := c.core.Store.PutRemoteFile(map[string]any{"id": "f1", "space_id": c.core.Store.Session().SpaceID, "name": "clip.mp4",
		"storage_path": "space/clip.mp4", "mime_type": "video/mp4", "size_bytes": float64(len(data)), "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		path, err := c.LocalFile("f1", 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path) // #nosec G304 -- the file the core just kept.
		if err != nil || string(got) != data {
			t.Fatalf("downloaded %d bytes, %v", len(got), err)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("downloaded %d times", n)
	}
}

// A picture the app gives up on, as its page closes, stops waiting on
// its site at once.
func TestPictureEndsAsItIsGivenUp(t *testing.T) {
	c, _ := open(t)
	started := make(chan struct{})
	was := fetchPicture
	fetchPicture = func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { fetchPicture = was })
	done := make(chan error, 1)
	go func() {
		_, err := c.PictureCancellable("https://example.com/a.png", 7)
		done <- err
	}()
	<-started
	c.Cancel(7)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a picture given up on: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a picture given up on went on waiting")
	}
}

// A file whose page left the window as it was copied goes at the end of
// the page, closed or open in another window: no editor of it has a caret
// to put it at.
func TestAppendBlockGoesAtThePagesEnd(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Notes"}`)
	if err := c.AppendBlock(page.ID, `{"id":"closed","type":"file","data":{"name":"a.pdf"}}`); err != nil {
		t.Fatal(err)
	}
	if got := content(t, c, page.ID); got.Find("closed") == nil {
		t.Fatalf("a closed page has none of it: %s", got.Encode())
	}
	if err := c.AppendBlock(page.ID, `{"id":"open","type":"file","data":{"name":"b.pdf"}}`); err != nil {
		t.Fatal(err)
	}
	got := content(t, c, page.ID)
	if n := len(got.Children); n < 2 || got.Children[n-2].ID != "closed" || got.Children[n-1].ID != "open" {
		t.Fatalf("blocks of an open page: %s", got.Encode())
	}
	if err := c.AppendBlock(page.ID, `not a block`); err == nil {
		t.Error("a block that can't be read was taken")
	}
}

// The page as it is now is there to compare a version with, with what is
// being typed.
func TestCurrentMarkdown(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	if err := c.AppendBlock(page.ID, `{"id":"p","type":"paragraph","data":{"delta":[{"insert":"Hello"}]}}`); err != nil {
		t.Fatal(err)
	}
	got, err := c.CurrentMarkdown(page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Hello") {
		t.Errorf("markdown %q", got)
	}
}

// The sidebar has a folder's files, and the top level those of a folder
// gone; the files pages hold are the pages'.
func TestFolderFiles(t *testing.T) {
	c, _ := open(t)
	raw, err := c.CreateFolder("Docs")
	if err != nil {
		t.Fatal(err)
	}
	var folder struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &folder); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "budget.txt")
	if err := os.WriteFile(path, []byte("sums"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.AddFolderFile(folder.ID, path); err != nil {
		t.Fatal(err)
	}
	page := create(t, c, `{"title":"Plan"}`)
	if _, err := c.AddFile(page.ID, path); err != nil {
		t.Fatal(err)
	}
	lib := func() library {
		t.Helper()
		raw, err := c.Library()
		if err != nil {
			t.Fatal(err)
		}
		var out library
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	files := lib().Files
	if len(files) != 1 || files[0].Name != "budget.txt" || files[0].FolderID != folder.ID || files[0].Size != 4 {
		t.Fatalf("files %+v", files)
	}
	if local, err := c.LocalFile(files[0].ID, 0); err != nil || local == "" {
		t.Errorf("local file %q, %v", local, err)
	}
	if err := c.DeleteFile(files[0].ID); err != nil {
		t.Fatal(err)
	}
	if files := lib().Files; len(files) != 0 {
		t.Errorf("after deleting, files %+v", files)
	}
}

// A page goes before another, or first, as well as after one.
func TestMovePageBefore(t *testing.T) {
	c, _ := open(t)
	a := create(t, c, `{"title":"A"}`)
	b := create(t, c, `{"title":"B"}`)
	first := func() string {
		t.Helper()
		raw, err := c.Library()
		if err != nil {
			t.Fatal(err)
		}
		var lib library
		if err := json.Unmarshal([]byte(raw), &lib); err != nil {
			t.Fatal(err)
		}
		return lib.Top[0]
	}
	for _, id := range []string{a.ID, b.ID} {
		if err := c.MovePageBefore(id, "", "", ""); err != nil {
			t.Fatal(err)
		}
		if got := first(); got != id {
			t.Errorf("first %s, want %s", got, id)
		}
	}
	if err := c.MovePageBefore(a.ID, "", "", b.ID); err != nil {
		t.Fatal(err)
	}
	if got := first(); got != a.ID {
		t.Errorf("before: first %s, want %s", got, a.ID)
	}
}

// How long audio stays is a choice of three, kept among the settings.
func TestKeepAudio(t *testing.T) {
	c, _ := open(t)
	if err := c.SetKeepAudio("30_days"); err != nil {
		t.Fatal(err)
	}
	raw, err := c.SpeechSettings()
	if err != nil {
		t.Fatal(err)
	}
	var s speechSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if s.KeepAudio != "30_days" {
		t.Errorf("keep audio %q", s.KeepAudio)
	}
	if err := c.SetKeepAudio("forever"); err == nil {
		t.Error("no way to keep audio was kept")
	}
}

// A page's earlier conversations are there to go back to, and the one left
// joins them.
func TestPastConversations(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	state := func() agentState {
		t.Helper()
		raw, err := c.AgentState(page.ID)
		if err != nil {
			t.Fatal(err)
		}
		var out agentState
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if s := state(); s.Past == nil || len(s.Past) != 0 {
		t.Fatalf("past %v", s.Past)
	}
	c.q.do(func() {
		s := c.core.Hub.Session(page.ID)
		s.Messages = []*agents.Message{{Mine: true, Text: "First question"}}
	})
	c.NewConversation(page.ID)
	s := state()
	if len(s.Past) != 1 || s.Past[0].Title != "First question" || len(s.Messages) != 0 {
		t.Fatalf("state %+v", s)
	}
	c.OpenConversation(page.ID, s.Past[0].ID)
	if s := state(); len(s.Past) != 0 || len(s.Messages) != 1 {
		t.Errorf("gone back: %+v", s)
	}
}
