package sync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// realtimePage is a page synced at revision 1, on a server whose
// page_content srv answers and whose Realtime rt stands in for, and a
// service signed in to it with the page open.
func realtimePage(t *testing.T) (*Service, *contentServer, *remote.RealtimeForTest, string) {
	t.Helper()
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	mine := &doc.Content{Children: []*doc.Node{para("a", "as it was")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	putSynced(t, st, p.ID, mine)
	dequeueAll(t, st)
	srv := &contentServer{content: mine.Encode(), revision: 1}
	rt := &remote.RealtimeForTest{}
	mux := http.NewServeMux()
	mux.Handle("/realtime/v1/websocket", rt)
	mux.Handle("/rest/v1/page_content", srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	svc.mu.Lock()
	svc.open[p.ID] = true
	svc.mu.Unlock()
	return svc, srv, rt, p.ID
}

// listening runs svc's listen until the test ends.
func listening(t *testing.T, svc *Service) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.listen(ctx, 20*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ctx
}

// heard waits for the loop to be told to check, and checks as it would.
func heard(t *testing.T, ctx context.Context, svc *Service) {
	t.Helper()
	select {
	case <-svc.hear:
		svc.checkHeard(ctx)
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was heard")
	}
}

func (s *Service) isLive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id] != nil
}

// An open page's channel hears what another device saves, and the page
// takes it at once; the checks every few seconds leave it out meanwhile.
// A save of a revision the page has is not checked, and the channel goes
// with the page.
func TestAnOpenPageHearsItsSaves(t *testing.T) {
	svc, srv, rt, id := realtimePage(t)
	got := make(chan string, 4)
	svc.OnPageContent = func(id string) { got <- id }
	ctx := listening(t, svc)
	if !rt.Await(5*time.Second, func() bool { return slices.Equal(rt.Joined(), []string{pageTopic(id)}) }) {
		t.Fatalf("joined %q", rt.Joined())
	}
	// Once joined, the page is checked for a save made before.
	heard(t, ctx, svc)
	if !svc.isLive(id) || len(got) != 0 {
		t.Fatalf("live %v, told of %d changes", svc.isLive(id), len(got))
	}

	theirs := &doc.Content{Children: []*doc.Node{para("a", "as an agent wrote it")}, Blocks: map[string]int64{"a": 2000}, Deleted: map[string]int64{}}
	srv.mu.Lock()
	srv.content, srv.revision = theirs.Encode(), 2
	srv.mu.Unlock()
	rt.Broadcast(pageTopic(id), "saved", map[string]any{"page_id": id, "revision": 2})
	heard(t, ctx, svc)
	select {
	case <-got:
	default:
		t.Fatal("the save heard did not come")
	}
	if c := svc.st.Content(id); c == nil || textOf(c, "a") != "as an agent wrote it" {
		t.Errorf("stored %v", c)
	}

	rt.Broadcast(pageTopic(id), "saved", map[string]any{"page_id": id, "revision": 2})
	select {
	case <-svc.hear:
		t.Error("a revision the page has was checked")
	case <-time.After(200 * time.Millisecond):
	}

	svc.SetOpen(id, false)
	if !rt.Await(5*time.Second, func() bool { return rt.Connected() == 0 }) {
		t.Fatalf("still joined to %q", rt.Joined())
	}
	if svc.isLive(id) {
		t.Error("a closed page is still live")
	}
}

// A page whose channel Realtime refuses the person, as on a project that
// does not say who may hear it, is not live, so the checks every few
// seconds keep to it, and is not asked for again for a while.
func TestAPageRealtimeRefusesIsChecked(t *testing.T) {
	old := listenRetry
	listenRetry = 50 * time.Millisecond
	t.Cleanup(func() { listenRetry = old })
	svc, _, rt, id := realtimePage(t)
	rt.Refused = map[string]string{pageTopic(id): "Unauthorized: You do not have permissions to read from this Channel topic: " + pageTopic(id)}
	listening(t, svc)
	if !rt.Await(5*time.Second, func() bool { return len(rt.Sent("phx_join")) > 0 }) {
		t.Fatal("no join was asked for")
	}
	time.Sleep(10 * listenRetry)
	if svc.isLive(id) || len(rt.Sent("phx_join")) != 1 {
		t.Fatalf("refused: live %v, %d joins", svc.isLive(id), len(rt.Sent("phx_join")))
	}
}

// A refusal for the moment, as by a project's Realtime still waking, is
// asked again soon; one of the person, a while after.
func TestRefusedFor(t *testing.T) {
	if d := refusedFor(&remote.RealtimeError{Reason: "MissingPartition: Realtime was unable to find the expected messages partition"}); d != listenRetry {
		t.Errorf("for the moment: %v", d)
	}
	if d := refusedFor(&remote.RealtimeError{Reason: "Unauthorized: You do not have permissions to read from this Channel topic: page:x"}); d != unauthorizedRetry {
		t.Errorf("unauthorized: %v", d)
	}
}

// A connection that drops leaves the pages to the checks, and is made
// again soon, joining them again.
func TestRealtimeConnectsAgain(t *testing.T) {
	old := listenRetry
	listenRetry = 50 * time.Millisecond
	t.Cleanup(func() { listenRetry = old })
	svc, _, rt, id := realtimePage(t)
	ctx := listening(t, svc)
	if !rt.Await(5*time.Second, func() bool { return len(rt.Joined()) == 1 }) {
		t.Fatal("never joined")
	}
	heard(t, ctx, svc)
	rt.Drop()
	if !rt.Await(5*time.Second, func() bool { return len(rt.Sent("phx_join")) == 2 && len(rt.Joined()) == 1 }) {
		t.Fatalf("joined again %d times", len(rt.Sent("phx_join"))-1)
	}
	heard(t, ctx, svc)
	if !svc.isLive(id) {
		t.Error("the page joined again is not live")
	}
	// The server closes its channel: it is joined again too.
	rt.CloseChannel(pageTopic(id))
	if !rt.Await(5*time.Second, func() bool { return len(rt.Sent("phx_join")) == 3 && len(rt.Joined()) == 1 }) {
		t.Fatalf("%d joins once the channel closed", len(rt.Sent("phx_join")))
	}
}

// The loop checks a page as Realtime tells of its saves, with no check
// every few seconds to bring it.
func TestTheLoopHearsSaves(t *testing.T) {
	old := watchEvery
	watchEvery = time.Hour
	t.Cleanup(func() { watchEvery = old })
	svc, srv, rt, id := realtimePage(t)
	got := make(chan string, 4)
	svc.OnPageContent = func(id string) { got <- id }
	svc.Start()
	defer svc.Stop()
	if !rt.Await(5*time.Second, func() bool { return len(rt.Joined()) == 1 }) {
		t.Fatal("never joined")
	}
	theirs := &doc.Content{Children: []*doc.Node{para("a", "from another device")}, Blocks: map[string]int64{"a": 2000}, Deleted: map[string]int64{}}
	srv.mu.Lock()
	srv.content, srv.revision = theirs.Encode(), 2
	srv.mu.Unlock()
	rt.Broadcast(pageTopic(id), "saved", map[string]any{"page_id": id, "revision": 2})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the save heard never came to the open page")
	}
	if c := svc.st.Content(id); c == nil || textOf(c, "a") != "from another device" {
		t.Errorf("stored %v", c)
	}
}

// A save heard while the server rests after failing a check is left to the
// passes, as a check every few seconds is: heard during an outage, each
// would ask it again.
func TestAHeardSaveWaitsWhileTheServerRests(t *testing.T) {
	svc, srv, _, id := realtimePage(t)
	gets := 0
	srv.between = nil
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gets++
		_, _ = w.Write([]byte("[]"))
	}))
	defer be.Close()
	svc.be = remote.SignedInForTest(be.URL, svc.st, "u1")
	svc.rest(errors.New("the server failed"))
	svc.hearPage(id)
	<-svc.hear
	svc.checkHeard(t.Context())
	if gets != 0 {
		t.Errorf("asked the server %d times", gets)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.heard) != 0 {
		t.Errorf("still heard %v", svc.heard)
	}
}
