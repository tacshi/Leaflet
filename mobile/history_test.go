package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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
