package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Signing in as another account than the one whose pages this device
// keeps, with changes of theirs not uploaded, asks first: Cancel keeps
// them, Sign in removes them.
func TestSignInAsAnotherAccountAsks(t *testing.T) {
	t.Cleanup(func() { signIn = signInState{} })
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
	tt := ui.NewTester(func(c *ui.Context) { a.signInView(c) }, 900, 700)
	a.signedIn(other)
	tt.Frame()
	if !tt.HasText(locales.SigninRemovesUnsynced(n, "sam@example.com")) {
		t.Fatalf("no alert in %v", tt.Texts())
	}
	if err := tt.Click(locales.CommonCancel()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, err := a.st.Page(p.ID); err != nil || a.session.SignedIn || signIn.asking != nil {
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

// A change stored while signing out, by leaflet mcp after the upload, is
// asked about rather than removed with the account's pages.
func TestSignOutAsksAboutAChangeStoredMeanwhile(t *testing.T) {
	t.Cleanup(func() { account = accountState{} })
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
	if _, err := a.st.Page(p.ID); err != nil || account.unsynced == 0 || !a.backend.SignedIn() {
		t.Fatalf("page %v, asked about %d, signed in %v", err, account.unsynced, a.backend.SignedIn())
	}
}
