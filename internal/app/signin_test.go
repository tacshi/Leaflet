package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/supabaseapi"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

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
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if _, err := a.st.Page(p.ID); err == nil {
		t.Error("the previous account's page stayed")
	}
	if !a.session.SignedIn || a.session.UserID != "u2" {
		t.Errorf("session %+v", a.session)
	}
	if a.settings.ICloudSync {
		t.Error("iCloud sync still on in Settings")
	}
}

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

	if err := a.st.SetValue("account", ""); err != nil {
		t.Fatal(err)
	}
	a.signedIn(&store.User{ID: "u2", Email: "sam@example.com"})
	tt.Frame()
	if !a.session.SignedIn || a.route != (Route{Kind: "page", ID: p.ID}) {
		t.Errorf("signed in %v, at %+v", a.session.SignedIn, a.route)
	}
}

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

func TestSignOutWaitsForAnUpload(t *testing.T) {
	defer syncsvc.UploadWaitForTest(10 * time.Millisecond)()
	a := templatesApp(t)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/storage/") {
			_, _ = io.Copy(io.Discard, r.Body)
			time.Sleep(200 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "[]")
	}))
	defer hs.Close()
	a.backend = remote.SignedInForTest(hs.URL, a.st, "u1")
	a.sync = syncsvc.New(a.st, a.backend)
	if err := a.st.SetValue("space.u1", a.st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "film.mov")
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddFile(source, "", "", "video/quicktime"); err != nil {
		t.Fatal(err)
	}
	entries, _ := a.st.Pending()
	for _, e := range entries {
		if e.Entity != "files" && e.Entity != "storage" {
			if err := a.st.Dequeue(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	posted := make(chan func(), 4)
	a.post = func(fn func()) { posted <- fn }
	a.signOut()
	(<-posted)()
	<-posted
	if a.account.unsynced != 0 || a.backend.SignedIn() {
		t.Fatalf("asked about %d, signed in %v", a.account.unsynced, a.backend.SignedIn())
	}
}

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

func TestSignOutKeepsThePageReadOnlyUntilItEnds(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.backend = remote.SignedInForTest("http://127.0.0.1:1", a.st, "u1")
	posted := make(chan func(), 1)
	a.post = func(fn func()) { posted <- fn }
	proceed := make(chan struct{})
	a.leave(func() error { <-proceed; return store.ErrUnsynced }, true)
	if !a.page.editor.ReadOnly {
		t.Error("the page takes typing as signing out removes it")
	}
	a.reload(store.Change{Table: "pages", PageID: p.ID})
	if !a.page.editor.ReadOnly {
		t.Error("a change made the page take typing as signing out removes it")
	}
	close(proceed)
	(<-posted)()
	if a.account.unsynced == 0 || a.page == nil || a.page.editor.ReadOnly {
		t.Errorf("asked about %d, page %v, read-only after", a.account.unsynced, a.page != nil)
	}
}

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
