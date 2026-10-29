package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

// invitesApp is an app signed in against a fake server that takes the
// answers to invites; what update runs waits for the next frame, as
// Window.Update runs it on the main thread.
func invitesApp(t *testing.T) (a *App, answers func() []string, frame func()) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PageID string `json:"p_page_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, strings.TrimPrefix(r.URL.Path, "/rest/v1/rpc/")+" "+body.PageID)
		mu.Unlock()
		_, _ = w.Write([]byte("null"))
	}))
	t.Cleanup(hs.Close)
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.SignedInForTest(hs.URL, st, "u1")
	a = &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.route = Route{Kind: "home"}
	a.reloadAll()
	a.session.SignedIn = true
	var posted []func()
	a.post = func(fn func()) {
		mu.Lock()
		posted = append(posted, fn)
		mu.Unlock()
	}
	answers = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
	frame = func() {
		mu.Lock()
		fns := posted
		posted = nil
		mu.Unlock()
		for _, fn := range fns {
			fn()
		}
	}
	return a, answers, frame
}

// waitFor runs frames until ok, or fails.
func waitFor(t *testing.T, tt *ui.Tester, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
}

// The sidebar has a button for the pages shared with the user that wait
// for them, only while some do; it lists them, and accepting or declining
// one answers the server and takes it off.
func TestSidebarListsInvites(t *testing.T) {
	a, answers, frame := invitesApp(t)
	a.settings.SidebarOpen = true
	sidebar := func(c *ui.Context) {
		frame()
		ui.Column(c).Width(280).FillHeight().Children(func() { a.sidebar(c) })
	}
	none := ui.NewTester(sidebar, 900, 600)
	none.Frame()
	if _, ok := none.Find(locales.SidebarInvites(0)); ok || none.HasText("shared with you") {
		t.Errorf("the sidebar without invites shows %v", none.Texts())
	}

	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	a.invites = []syncsvc.Invite{
		{PageID: "p1", Title: "Roadmap", Role: "editor", Inviter: "Sam", At: at},
		{PageID: "p2", Title: "", Role: "viewer", Inviter: "", At: at},
	}
	tt := ui.NewTester(sidebar, 900, 600)
	tt.Frame()
	if tt.HasText("Sam shared") {
		t.Fatal("the invites show before the button is clicked")
	}
	if err := tt.Click(locales.SidebarInvites(2)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"Sam shared “Roadmap” with you", "Can edit", "Someone shared “Untitled” with you", "Can view"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %v", want, tt.Texts())
		}
	}

	r, ok := tt.Find("Sam shared “Roadmap” with you")
	if !ok {
		t.Fatal("no first invite")
	}
	accept, _ := tt.Find("Accept")
	if accept.Y > r.Y+r.H || accept.Y+accept.H < r.Y {
		t.Fatalf("the first Accept %+v is not beside its invite %+v", accept, r)
	}
	if err := tt.Click("Accept"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(a.inviteBusy) == 0 && len(answers()) == 1 })
	if got := answers(); got[0] != "accept_page_invite p1" {
		t.Errorf("answered %v", got)
	}
	// The list is sync's, which had none.
	if len(a.invites) != 0 {
		t.Errorf("invites after accepting %+v", a.invites)
	}

	a.invites = []syncsvc.Invite{{PageID: "p2", Title: "Notes", Role: "viewer", Inviter: "Kim", At: at}}
	a.invitesOpen = true
	tt.Frame()
	if err := tt.Click("Decline"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(a.inviteBusy) == 0 && len(answers()) == 2 })
	if got := answers(); got[1] != "decline_page_invite p2" {
		t.Errorf("answered %v", got)
	}
	tt.Frame()
	if tt.HasText("Kim shared") {
		t.Error("a declined invite is still listed")
	}
	if _, ok := tt.Find(locales.SidebarInvites(0)); ok || a.invitesOpen {
		t.Error("the button stays with no invites")
	}
}

// An invite's notification shows the invites, the sidebar with them.
func TestShowInvites(t *testing.T) {
	a, _, _ := invitesApp(t)
	a.settings.SidebarOpen = false
	a.showInvites()
	if a.settings.SidebarOpen || a.invitesOpen {
		t.Error("showed invites with none")
	}
	a.invites = []syncsvc.Invite{{PageID: "p1", Title: "Roadmap", Role: "editor"}}
	a.showInvites()
	if !a.settings.SidebarOpen || !a.invitesOpen {
		t.Errorf("sidebar %v, invites %v", a.settings.SidebarOpen, a.invitesOpen)
	}
}

// The Share dialog marks the people who have not accepted yet.
func TestShareDialogMarksInvited(t *testing.T) {
	a, _, frame := invitesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.session.SignedIn = true
	d := &a.dialogs
	d.shareOpen, d.sharePage, d.shareRole = true, p.ID, "editor"
	d.shareMember = []store.Member{
		{PageID: p.ID, UserID: "u2", Role: "editor", DisplayName: "Sam", Accepted: true},
		{PageID: p.ID, UserID: "u3", Role: "viewer", Email: "kim@example.com"},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		frame()
		a.shareDialog(c)
	}, 900, 700)
	tt.Frame()
	invited := 0
	for _, s := range tt.Texts() {
		if s == "Invited" {
			invited++
		}
	}
	if invited != 1 || !tt.HasText("kim@example.com") || !tt.HasText("Sam") {
		t.Fatalf("%d marked invited in %v", invited, tt.Texts())
	}
	kim, _ := tt.Find("kim@example.com")
	mark, _ := tt.Find("Invited")
	if mark.Y > kim.Y+kim.H || mark.Y+mark.H < kim.Y {
		t.Errorf("Invited %+v is not on Kim's row %+v", mark, kim)
	}
}

// The server's reasons for refusing an invite read as sentences.
func TestShareFailure(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&remote.Error{Status: 400, Message: "invalid_email"}, locales.DialogShareInvalidEmail()},
		{&remote.Error{Status: 400, Message: "cannot_share_with_self"}, locales.DialogShareWithSelf()},
		{fmt.Errorf("share: %w", &remote.Error{Status: 400, Message: "invalid_email"}), locales.DialogShareInvalidEmail()},
		{&remote.Error{Status: 500, Message: "boom"}, "boom"},
	} {
		if got := shareFailure(c.err); got != c.want {
			t.Errorf("shareFailure(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
