package remote

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func realtimeTest(t *testing.T) (*Client, *RealtimeForTest, *Realtime) {
	t.Helper()
	f := &RealtimeForTest{}
	hs := httptest.NewServer(f)
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	r, err := c.DialRealtime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return c, f, r
}

func next(t *testing.T, r *Realtime) RealtimeEvent {
	t.Helper()
	select {
	case e, ok := <-r.Events():
		if !ok {
			t.Fatalf("the connection ended: %v", r.Err())
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event came")
	}
	return RealtimeEvent{}
}

func TestRealtimeURL(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"https://abc.supabase.co", "wss://abc.supabase.co/realtime/v1/websocket?apikey=k&vsn=1.0.0"},
		{"http://127.0.0.1:54321/", "ws://127.0.0.1:54321/realtime/v1/websocket?apikey=k&vsn=1.0.0"},
	} {
		if got, err := realtimeURL(Settings{URL: tc.url, PublishableKey: "k"}); err != nil || got != tc.want {
			t.Errorf("%s: %q, %v", tc.url, got, err)
		}
	}
	if _, err := realtimeURL(Settings{URL: "ftp://abc", PublishableKey: "k"}); err == nil {
		t.Error("an address neither http nor https was taken")
	}
}

func TestRealtimeHearsTheChannelsJoined(t *testing.T) {
	_, f, r := realtimeTest(t)
	if err := r.Join(t.Context(), "page:p1"); err != nil {
		t.Fatal(err)
	}
	if q := f.Queries[0]; q.Get("apikey") != "test" || q.Get("vsn") != "1.0.0" {
		t.Errorf("dialled with %v", q)
	}
	var join struct {
		Config struct {
			Private   bool `json:"private"`
			Broadcast struct {
				Self bool `json:"self"`
			} `json:"broadcast"`
		} `json:"config"`
		AccessToken string `json:"access_token"`
	}
	if sent := f.Sent("phx_join"); len(sent) != 1 || sent[0].Topic != "page:p1" || json.Unmarshal(sent[0].Payload, &join) != nil ||
		!join.Config.Private || join.Config.Broadcast.Self || join.AccessToken != "test" {
		t.Errorf("joined with %+v", f.Sent("phx_join"))
	}
	f.Broadcast("page:p2", "saved", map[string]any{"page_id": "p2"})
	f.Broadcast("page:p1", "saved", map[string]any{"page_id": "p1", "revision": 2})
	e := next(t, r)
	var saved struct {
		PageID   string `json:"page_id"`
		Revision int64  `json:"revision"`
	}
	if e.Topic != "page:p1" || e.Event != "saved" || e.Closed || json.Unmarshal(e.Payload, &saved) != nil || saved.PageID != "p1" || saved.Revision != 2 {
		t.Errorf("heard %+v", e)
	}
}

func TestRealtimeRefusesAJoin(t *testing.T) {
	_, f, r := realtimeTest(t)
	const reason = "Unauthorized: You do not have permissions to read from this Channel topic: page:p1"
	f.mu.Lock()
	f.Refused = map[string]string{"page:p1": reason}
	f.mu.Unlock()
	var refused *RealtimeError
	if err := r.Join(t.Context(), "page:p1"); !errors.As(err, &refused) || refused.Reason != reason {
		t.Errorf("join: %v", err)
	}
}

func TestRealtimeTellsOfAChannelClosed(t *testing.T) {
	_, f, r := realtimeTest(t)
	for _, topic := range []string{"page:p1", "page:p2"} {
		if err := r.Join(t.Context(), topic); err != nil {
			t.Fatal(err)
		}
	}
	r.Leave("page:p2")
	if !f.Await(5*time.Second, func() bool { return len(f.Joined()) == 1 }) {
		t.Fatalf("joined %q after leaving one", f.Joined())
	}
	f.CloseChannel("page:p2")
	f.CloseChannel("page:p1")
	if e := next(t, r); e.Topic != "page:p1" || !e.Closed {
		t.Errorf("heard %+v", e)
	}
}

func TestRealtimeBeatsAndRenewsItsToken(t *testing.T) {
	old := realtimeBeat
	realtimeBeat = 200 * time.Millisecond
	t.Cleanup(func() { realtimeBeat = old })
	c, f, r := realtimeTest(t)
	if err := r.Join(t.Context(), "page:p1"); err != nil {
		t.Fatal(err)
	}
	if !f.Await(5*time.Second, func() bool { return len(f.Sent("heartbeat")) >= 3 }) {
		t.Fatalf("%d heartbeats", len(f.Sent("heartbeat")))
	}
	if sent := f.Sent("access_token"); len(sent) != 0 {
		t.Errorf("the same token went again: %+v", sent)
	}
	c.mu.Lock()
	was := c.sess
	c.mu.Unlock()
	c.replace(was, &session{AccessToken: "renewed", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: was.User}, false)
	if !f.Await(5*time.Second, func() bool { return len(f.Sent("access_token")) > 0 }) {
		t.Fatal("the renewed token never went")
	}
	var got struct {
		AccessToken string `json:"access_token"`
	}
	if sent := f.Sent("access_token")[0]; sent.Topic != "page:p1" || json.Unmarshal(sent.Payload, &got) != nil || got.AccessToken != "renewed" {
		t.Errorf("sent %+v", sent)
	}
	select {
	case <-r.Done():
		t.Fatalf("the connection ended: %v", r.Err())
	default:
	}
}

func TestRealtimeEnds(t *testing.T) {
	old := realtimeBeat
	realtimeBeat = 200 * time.Millisecond
	t.Cleanup(func() { realtimeBeat = old })
	_, f, r := realtimeTest(t)
	f.mu.Lock()
	f.Silent = true
	f.mu.Unlock()
	select {
	case <-r.Done():
		if !errors.Is(r.Err(), errRealtimeSilent) {
			t.Errorf("ended for %v", r.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a silent server's connection went on")
	}
	if _, ok := <-r.Events(); ok {
		t.Error("events went on")
	}

	c, _, r := realtimeTest(t)
	c.keep(nil)
	select {
	case <-r.Done():
		if !errors.Is(r.Err(), ErrSignedOut) {
			t.Errorf("ended for %v", r.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection outlived the session")
	}
}

func TestRealtimeHeartbeatAnsweredAtOnce(t *testing.T) {
	beat, beaten := realtimeBeat, realtimeBeaten
	realtimeBeat = 30 * time.Millisecond
	realtimeBeaten = func() { time.Sleep(15 * time.Millisecond) }
	t.Cleanup(func() { realtimeBeat, realtimeBeaten = beat, beaten })
	_, f, r := realtimeTest(t)
	if !f.Await(5*time.Second, func() bool { return len(f.Sent("heartbeat")) >= 4 }) {
		t.Fatalf("%d heartbeats", len(f.Sent("heartbeat")))
	}
	select {
	case <-r.Done():
		t.Fatalf("the connection ended: %v", r.Err())
	default:
	}
}
