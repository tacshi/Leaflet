package mobile

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/web"
)

// A recording's transcript is the blocks its page's editor puts after
// its block, a paragraph a line, each after its time in bold.
func TestInsertTranscript(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Standup"}`)
	var rec store.Recording
	var err error
	c.q.do(func() {
		if rec, err = c.core.Store.CreateRecording(page.ID, "rec", "", "", "", ""); err != nil {
			return
		}
		_, err = c.core.Store.UpdateRecording(rec.ID, func(r *store.Recording) {
			r.Status, r.Transcript = "ready", []store.Segment{{EndMs: 900, Text: "Hello"}, {StartMs: 61000, EndMs: 62000, Text: "Again"}}
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.TranscriptBlocks(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got transcript
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if texts(&doc.Content{Children: got.Blocks}) != "0:00  Hello|1:01  Again" {
		t.Fatalf("blocks %q", texts(&doc.Content{Children: got.Blocks}))
	}
	if d := got.Blocks[0].Delta(); len(d) != 2 || !d[0].Attrs.Bold || d[1].Attrs.Bold {
		t.Errorf("a line's time is not bold alone: %+v", d)
	}
}

// Settings' test of the microphone records a while into no page, and
// says what it heard.
func TestTestMicrophoneSaysWhatItHeard(t *testing.T) {
	c, _ := open(t)
	c.UseMicrophone(&fakeMic{closed: make(chan struct{}, 1)})
	c.UseTranscriber(&fakeTranscriber{heard: map[string]int{}, sinks: map[string]*TranscriptSink{}})
	if err := c.TestMicrophone(); err != nil {
		t.Fatal(err)
	}
	var st recordingState
	for deadline := time.Now().Add(15 * time.Second); st.Heard == ""; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never heard: %+v", st)
		}
		raw, _ := c.RecordingState()
		_ = json.Unmarshal([]byte(raw), &st)
	}
	if st.Testing || !strings.Contains(st.Heard, "heard 32000") || st.PageID != "" || st.ID != "" {
		t.Fatalf("state %+v", st)
	}
}

// Account › Devices lists the account's devices, this one marked, and
// signs another out by revoking it on the server.
func TestDevicesAndSigningOneOut(t *testing.T) {
	c, _ := open(t)
	me := c.core.Store.DeviceID()
	var mu sync.Mutex
	var revoked string
	signInRockyTo(t, c, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/v1/devices" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `[{"id":"`+me+`","name":"iPad","last_active_at":"2026-11-03T10:00:00Z"},
				{"id":"d2","name":"MacBook","last_active_at":"2026-11-02T10:00:00Z","revoked_at":null}]`)
		case r.URL.Path == "/rest/v1/rpc/sign_out_device" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			revoked = string(body)
			mu.Unlock()
			_, _ = io.WriteString(w, `null`)
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	})
	raw, err := c.Devices()
	if err != nil {
		t.Fatal(err)
	}
	var list []device
	_ = json.Unmarshal([]byte(raw), &list)
	if len(list) != 2 || !list[0].This || list[1].This || list[1].Name != "MacBook" || list[1].Revoked || list[0].LastActive == 0 {
		t.Fatalf("devices %+v", list)
	}
	if err := c.SignOutDevice("d2"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if revoked != `{"p_device_id":"d2"}` {
		t.Errorf("revoked with %s", revoked)
	}
}

// A starter deleted is hidden on this device, and back once Undo
// restores it.
func TestSetStarterDeleted(t *testing.T) {
	c, _ := open(t)
	keys := func() []string {
		raw, err := c.Templates()
		if err != nil {
			t.Fatal(err)
		}
		var rows []templateRow
		_ = json.Unmarshal([]byte(raw), &rows)
		var out []string
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out
	}
	before := keys()
	starter := ""
	for _, k := range before {
		if strings.HasPrefix(k, "starter:") {
			starter = k
			break
		}
	}
	if starter == "" {
		t.Fatalf("no starter among %v", before)
	}
	if err := c.SetStarterDeleted(starter, true); err != nil {
		t.Fatal(err)
	}
	if after := keys(); len(after) != len(before)-1 || strings.Contains(strings.Join(after, " "), starter) {
		t.Fatalf("deleted %s: %v", starter, after)
	}
	if err := c.SetStarterDeleted(starter, false); err != nil {
		t.Fatal(err)
	}
	if again := keys(); strings.Join(again, " ") != strings.Join(before, " ") {
		t.Fatalf("restored: %v, want %v", again, before)
	}
}

// A run undone is made again by Redo, its block back in the page.
func TestRedoRun(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	var err error
	c.q.do(func() {
		err = c.editContent(page.ID, func(content *doc.Content) bool {
			content.Children = append(content.Children, paragraph("x1", "by the agent"))
			return true
		})
		s := c.core.Hub.Session(page.ID)
		s.Runs = append(s.Runs, &agents.Run{ID: "r1", Status: "done", AgentName: "Claude",
			Ops: []doc.Op{doc.InsertOp(doc.Position{At: "end"}, paragraph("x1", "by the agent"))}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.UndoRun(page.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got := texts(c.core.Store.Content(page.ID)); strings.Contains(got, "by the agent") {
		t.Fatalf("undone: %q", got)
	}
	if err := c.RedoRun(page.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got := texts(c.core.Store.Content(page.ID)); !strings.HasSuffix(got, "by the agent") {
		t.Fatalf("redone: %q", got)
	}
	var undone bool
	c.q.do(func() { undone = c.core.Hub.Session(page.ID).Run("r1").Undone })
	if undone {
		t.Error("the run is still undone")
	}
}

// A page moves under a page of its space the user may change, but not
// under itself, a page inside it or a template.
func TestMoveTargets(t *testing.T) {
	c, _ := open(t)
	trip := create(t, c, `{"title":"Trip"}`)
	packing := create(t, c, `{"title":"Packing","parent_id":"`+trip.ID+`"}`)
	shoes := create(t, c, `{"title":"Shoes","parent_id":"`+packing.ID+`"}`)
	work := create(t, c, `{"title":"Work"}`)
	raw, err := c.SaveAsTemplate(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tpl pageRow
	_ = json.Unmarshal([]byte(raw), &tpl)
	raw, err = c.MoveTargets(packing.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	_ = json.Unmarshal([]byte(raw), &ids)
	has := func(id string) bool { return slices.Contains(ids, id) }
	if !has(trip.ID) || !has(work.ID) || has(packing.ID) || has(shoes.ID) || tpl.ID == "" || has(tpl.ID) {
		t.Fatalf("targets %v", ids)
	}
}

// A picture on this device or its network is not read, as on the
// desktop: a link in a shared page could reach a router's page.
func TestPictureReadsOnlyPublicAddresses(t *testing.T) {
	c, _ := open(t)
	if _, err := c.Picture("http://127.0.0.1:1/a.png"); !errors.Is(err, web.ErrNotPublic) {
		t.Fatalf("read a local picture: %v", err)
	}
	if _, err := c.Picture("file:///etc/hosts"); err == nil {
		t.Fatal("read a file")
	}
}

// The To-dos tab's sections come from the core in order, each by date, as
// the desktop sorts them.
func TestTodosComeInSections(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Chores"}`)
	todo := func(id, text string, due time.Time) *doc.Node {
		n := &doc.Node{ID: id, Type: "todo_list", Data: map[string]any{"checked": false}}
		n.SetDelta(doc.Delta{{Text: text}})
		n.SetDue(due, false)
		return n
	}
	now := time.Now()
	var err error
	c.q.do(func() {
		err = c.editContent(page.ID, func(content *doc.Content) bool {
			content.Children = []*doc.Node{
				todo("a", "later", now.AddDate(0, 0, 9)), todo("b", "late", now.AddDate(0, 0, -3)),
				todo("c", "soon", now.AddDate(0, 0, 2)), todo("d", "today", now),
			}
			return true
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Todos()
	if err != nil {
		t.Fatal(err)
	}
	var groups []todoGroup
	_ = json.Unmarshal([]byte(raw), &groups)
	var got []string
	for _, g := range groups {
		var texts []string
		for _, t := range g.Todos {
			texts = append(texts, t.Text)
		}
		got = append(got, g.Section+": "+strings.Join(texts, ", "))
	}
	if strings.Join(got, "; ") != "overdue: late; today: today; upcoming: soon, later" || groups[0].Title != "Overdue" {
		t.Fatalf("sections %s", raw)
	}
}
