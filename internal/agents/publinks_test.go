package agents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

func TestPublicLinksRecordedForAgentsOutsideTheApp(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	var asked atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		switch {
		case fail.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
		case strings.Contains(r.URL.Path, "/pages") && strings.Contains(r.URL.RawQuery, "public_link_until=gt.now"):
			_, _ = w.Write([]byte(`[{"id":"p1","public_link_until":"` + time.Now().Add(time.Hour).Format(time.RFC3339Nano) + `"},{"id":"p2","public_link_until":"soon"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(hs.Close)
	h := NewHub(st, remote.SignedInForTest(hs.URL, st, "u1"), onlineHost{})
	if _, known := st.PublicLink("p3"); known {
		t.Fatal("known before the app read the links")
	}
	if err := h.recordPublicLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"p1": true, "p2": true, "p3": false} {
		if linked, known := st.PublicLink(id); linked != want || !known {
			t.Errorf("%s: linked %v, known %v", id, linked, known)
		}
	}

	// A read that fails keeps what was read before, which runs stale.
	fail.Store(true)
	if err := h.recordPublicLinks(context.Background()); err == nil {
		t.Error("a failed read recorded")
	}
	if linked, known := st.PublicLink("p1"); !linked || !known {
		t.Errorf("after a failed read: linked %v, known %v", linked, known)
	}
	fail.Store(false)

	// Signed out, syncing through iCloud Drive, the links come from the other
	// devices' files.
	signedOut := NewHub(st, nil, onlineHost{})
	signedOut.ICloudLinks = func() (map[string]time.Time, error) {
		return map[string]time.Time{"p3": time.Now().Add(-time.Minute)}, nil
	}
	if err := signedOut.recordPublicLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if linked, known := st.PublicLink("p3"); !linked || !known {
		t.Errorf("an iCloud lease within its drift: linked %v, known %v", linked, known)
	}
	signedOut.ICloudLinks = func() (map[string]time.Time, error) { return nil, errors.New("unreadable") }
	if err := signedOut.recordPublicLinks(context.Background()); err == nil {
		t.Error("unreadable device files recorded")
	}

	// The watch reads at once, as it is kicked, and only while pages sync.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var can atomic.Bool
	asked.Store(0)
	go func() {
		defer close(done)
		h.WatchPublicLinks(ctx, can.Load)
	}()
	time.Sleep(50 * time.Millisecond)
	if n := asked.Swap(0); n != 0 {
		t.Errorf("asked the server %d times while pages do not sync", n)
	}
	can.Store(true)
	h.KickPublicLinks()
	for deadline := time.Now().Add(5 * time.Second); asked.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if asked.Load() == 0 {
		t.Error("a kick read nothing")
	}
	cancel()
	<-done
	var nobody *Hub
	nobody.KickPublicLinks()
}
