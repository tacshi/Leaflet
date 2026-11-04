package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/supabaseapi"
	"leaflet/locales"
)

// Signing in as another account than the one whose pages this device
// keeps, with changes of theirs not uploaded, asks first: Cancel keeps
// them, Sign in removes them.
func TestSignInAsAnotherAccountAsks(t *testing.T) {
	a := templatesApp(t)
	a.backend = remote.SignedInForTest("http://127.0.0.1:1", a.st, "u2")
	a.route = Route{Kind: "signin"}
	if err := a.st.SetValue("account", "u1"); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Not uploaded"})
	if err != nil {
		t.Fatal(err)
	}
	n := a.st.PendingCount()
	if n == 0 {
		t.Fatal("nothing waits to upload")
	}
	other := &store.User{ID: "u2", Email: "sam@example.com"}
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 900, 700)
	a.signedIn(other)
	tt.Frame()
	if !tt.HasText(locales.SigninRemovesUnsynced(n, "sam@example.com")) {
		t.Fatalf("no alert in %v", tt.Texts())
	}
	if err := tt.Click(locales.CommonCancel()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, err := a.st.Page(p.ID); err != nil || a.session.SignedIn || a.signIn.asking != nil {
		t.Fatalf("after Cancel: page %v, signed in %v", err, a.session.SignedIn)
	}

	a.settings.ICloudSync = true
	a.signedIn(other)
	tt.Frame()
	// Sign in, the alert's default button; the page has one of its own.
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if _, err := a.st.Page(p.ID); err == nil {
		t.Error("the previous account's page stayed")
	}
	if !a.session.SignedIn || a.session.UserID != "u2" {
		t.Errorf("session %+v", a.session)
	}
	// iCloud sync is off for the server's, and says so once signed out.
	if a.settings.ICloudSync {
		t.Error("iCloud sync still on in Settings")
	}
}

// A sign-in that finishes after the person has gone to a page asks there,
// and once done leaves them on the page.
func TestSignInFinishedOnAPage(t *testing.T) {
	a := templatesApp(t)
	a.backend = remote.SignedInForTest("http://127.0.0.1:1", a.st, "u2")
	if err := a.st.SetValue("account", "u1"); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Not uploaded"})
	if err != nil {
		t.Fatal(err)
	}
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.reloadAll()
	a.route = Route{Kind: "signin"}
	a.router = ui.NewRouter("/signin")
	a.routerAt = a.router.Path()
	a.navigate(Route{Kind: "page", ID: p.ID})
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1200, 800)
	tt.Frame()
	a.signedIn(&store.User{ID: "u2", Email: "sam@example.com"})
	tt.Frame()
	if !tt.HasText(locales.SigninRemovesUnsynced(a.st.PendingCount(), "sam@example.com")) {
		t.Fatalf("no alert on the page: %v", tt.Texts())
	}
	if err := tt.Click(locales.CommonCancel()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// Signing in with nothing to lose leaves the page open.
	if err := a.st.SetValue("account", ""); err != nil {
		t.Fatal(err)
	}
	a.signedIn(&store.User{ID: "u2", Email: "sam@example.com"})
	tt.Frame()
	if !a.session.SignedIn || a.route != (Route{Kind: "page", ID: p.ID}) {
		t.Errorf("signed in %v, at %+v", a.session.SignedIn, a.route)
	}
}

// Signing in as another account that fails to become the session, as when
// the previous account's pages cannot be removed, signs it out again.
func TestSignInThatFailsSignsOut(t *testing.T) {
	a := templatesApp(t)
	a.backend = remote.SignedInForTest("http://127.0.0.1:1", a.st, "u2")
	if err := a.st.SetValue("account", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Uploaded"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := a.st.Pending()
	for _, e := range entries {
		if err := a.st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.st.DB().Exec(`CREATE TRIGGER full BEFORE DELETE ON pages BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	a.signedIn(&store.User{ID: "u2", Email: "sam@example.com"})
	for deadline := time.Now().Add(5 * time.Second); a.backend.SignedIn(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the new account is still signed in")
		}
	}
	if a.session.SignedIn {
		t.Error("the session is the new account's")
	}
}

// A change stored while signing out, by leaflet mcp after the upload, is
// asked about rather than removed with the account's pages.
func TestSignOutAsksAboutAChangeStoredMeanwhile(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Uploaded"})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := a.st.Pending()
	for _, e := range entries {
		if err := a.st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	posted := make(chan func(), 4)
	a.post = func(fn func()) { posted <- fn }
	a.backend = remote.SignedInForTest("http://127.0.0.1:1", a.st, "u1")
	a.signOut()
	uploaded := <-posted
	c := a.st.Content(p.ID)
	late := &doc.Node{ID: "late", Type: "paragraph"}
	late.SetDelta(doc.FromMarkdown("edited meanwhile"))
	c.Children = append(c.Children, late)
	if _, _, err := a.st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	uploaded()
	(<-posted)()
	if _, err := a.st.Page(p.ID); err != nil || a.account.unsynced == 0 || !a.backend.SignedIn() {
		t.Fatalf("page %v, asked about %d, signed in %v", err, a.account.unsynced, a.backend.SignedIn())
	}
}

// A change stored as signing out removes the account's pages, which the
// removal finds and stops for, is asked about, not shown as an error, and
// leaves the device signed in, as a change found before does.
func TestSignOutAsksAboutAChangeFoundAsItRemoves(t *testing.T) {
	a := templatesApp(t)
	a.backend = remote.SignedInForTest("http://127.0.0.1:1", a.st, "u1")
	posted := make(chan func(), 1)
	a.post = func(fn func()) { posted <- fn }
	a.leave(func() error { return store.ErrUnsynced }, true)
	(<-posted)()
	if a.account.unsynced == 0 || len(a.toasts) != 0 || !a.backend.SignedIn() {
		t.Errorf("asked about %d, toasts %+v, signed in %v", a.account.unsynced, a.toasts, a.backend.SignedIn())
	}
}

// Deleting the account wipes the device's settings with it: those the app
// shows then are a new device's, not the ones gone.
func TestWipeResetsSettings(t *testing.T) {
	a := templatesApp(t)
	a.settings = a.st.Settings()
	a.settings.TextSize = 21
	a.saveSettings()
	posted := make(chan func(), 1)
	a.post = func(fn func()) { posted <- fn }
	a.leave(a.st.Wipe, false)
	(<-posted)()
	if a.settings.TextSize != store.DefaultSettings().TextSize {
		t.Errorf("text size %v after the wipe", a.settings.TextSize)
	}
}

// Signing in as another account while the open page's edits cannot be
// saved stops, as signing out does: the previous account's pages stay,
// with the page open and what was typed in it.
func TestSignInAsAnotherAccountStopsForAnUnsavedPage(t *testing.T) {
	a := templatesApp(t)
	a.backend = remote.SignedInForTest("http://127.0.0.1:1", a.st, "u2")
	if err := a.st.SetValue("account", "u1"); err != nil {
		t.Fatal(err)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := a.st.Pending()
	for _, e := range entries {
		if err := a.st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.page.title = "Typed title"
	if _, err := a.st.DB().Exec(`CREATE TRIGGER full BEFORE UPDATE ON pages BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	a.signedIn(&store.User{ID: "u2", Email: "sam@example.com"})
	if _, err := a.st.Page(p.ID); err != nil {
		t.Fatalf("the previous account's page went: %v", err)
	}
	if a.session.SignedIn || a.page == nil || a.page.title != "Typed title" {
		t.Errorf("signed in %v, page open %v", a.session.SignedIn, a.page != nil)
	}
}

// Pages sync one way at a time: with iCloud sync on, signing in with
// Supabase is disabled, and its tip says how to use it instead.
func TestICloudSyncDisablesSupabaseSignIn(t *testing.T) {
	a := templatesApp(t)
	if !a.hasServer() {
		t.Skip("no server to sign in to")
	}
	a.settings.ICloudSync = true
	tt := ui.NewTester(func(c *ui.Context) { a.signInView(c) }, 900, 700)
	r, ok := tt.Find(locales.SigninContinueSupabase())
	if !ok {
		t.Fatalf("no button in %v", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText(locales.SettingsSupabaseICloudOn()) {
		t.Errorf("no tip in %v", tt.Texts())
	}
	if err := tt.Click(locales.SigninContinueSupabase()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.supa.step != "" {
		a.supa.cancel()
		t.Fatalf("signing in with iCloud sync on: step %q", a.supa.step)
	}
}

// A second click on a project while the first one's setup starts does
// nothing: one setup runs at a time.
func TestChooseProjectOnce(t *testing.T) {
	auth, base := supabaseapi.Auth, supabaseapi.Base
	supabaseapi.Auth, supabaseapi.Base = "http://127.0.0.1:1", "http://127.0.0.1:1"
	t.Cleanup(func() {
		supabaseapi.Auth, supabaseapi.Base = auth, base
	})
	a := templatesApp(t)
	posted := make(chan func(), 4)
	a.post = func(fn func()) { posted <- fn }
	a.supa = supabaseState{run: a.nextRun(), step: stepChoose, api: supabaseapi.Resume(""), org: "org"}
	a.chooseProject(nil)
	run := a.supa.run
	a.chooseProject(nil)
	if a.supa.run != run || a.supa.step != stepCreating {
		t.Errorf("run %d, want %d; step %q", a.supa.run, run, a.supa.step)
	}
	a.supa.cancel()
	(<-posted)()
	if a.supa.step != "" || a.signIn.err == "" {
		t.Errorf("after the setup failed: step %q, error %q", a.supa.step, a.signIn.err)
	}
}

// An emailed code comes again, as one mistyped or too old, or goes to
// another email; a send that fails says so alone, not that a code went.
func TestEmailCodeSendsAgain(t *testing.T) {
	a := templatesApp(t)
	var sends atomic.Int32
	var fail atomic.Bool
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"msg":"email rate limit exceeded"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(hs.Close)
	a.backend = remote.New(remote.Settings{URL: hs.URL, PublishableKey: "test"}, a.st)
	tt := ui.NewTester(func(c *ui.Context) { a.emailCodeForm(c) }, 600, 400)
	tt.Frame()
	a.signIn.email = "sam@example.com"
	if err := tt.Click(locales.SigninSendCode()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.signIn.sent || a.signIn.info != locales.SigninCodeSent() {
		t.Fatalf("sent %v, info %q, error %q", a.signIn.sent, a.signIn.info, a.signIn.err)
	}
	fail.Store(true)
	if err := tt.Click(locales.SigninResendCode()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if sends.Load() != 2 || a.signIn.info != "" || a.signIn.err == "" || !a.signIn.sent {
		t.Fatalf("sends %d, info %q, error %q", sends.Load(), a.signIn.info, a.signIn.err)
	}
	if err := tt.Click(locales.SigninChangeEmail()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.signIn.sent || a.signIn.err != "" || !tt.HasText(locales.SigninSendCode()) {
		t.Errorf("sent %v, error %q, shows %v", a.signIn.sent, a.signIn.err, tt.Texts())
	}
}
