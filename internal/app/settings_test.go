package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Work started in a provider dialog, a test or a save, finds nowhere to go
// once that dialog is closed, or another one opened in its place.
func TestProviderResultsStayWithTheirDialog(t *testing.T) {
	was := form
	t.Cleanup(func() { form = was })
	openForm(providerForm{open: true, name: "A"})
	a := form.gen
	if !formIs(a) {
		t.Fatal("the open dialog is not itself")
	}
	form.open = false
	if formIs(a) {
		t.Error("a closed dialog took a result")
	}
	openForm(providerForm{open: true, name: "B"})
	if formIs(a) || !formIs(form.gen) {
		t.Error("dialog B took dialog A's result")
	}
}

// Models listed for one provider dialog don't land in another opened
// since, at the same address.
func TestModelsStayWithTheirDialog(t *testing.T) {
	was := form
	t.Cleanup(func() { form = was })
	var n atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"data":[{"id":"llama-%d"}]}`, n.Add(1))
	}))
	t.Cleanup(hs.Close)
	a := templatesApp(t)
	posts := make(chan func(), 2)
	a.post = func(fn func()) { posts <- fn }
	next := func() func() {
		select {
		case fn := <-posts:
			return fn
		case <-time.After(5 * time.Second):
			t.Fatal("no models came")
			return nil
		}
	}
	openForm(providerForm{open: true, preset: "ollama", name: "A", baseURL: hs.URL})
	a.fetchModels()
	first := next()
	openForm(providerForm{open: true, preset: "ollama", name: "B", baseURL: hs.URL})
	a.fetchModels()
	second := next()
	first()
	if len(form.models) != 0 {
		t.Errorf("dialog B took dialog A's models %v", form.models)
	}
	second()
	if len(form.models) != 1 || form.models[0] != "llama-2" {
		t.Errorf("dialog B lists %v", form.models)
	}
}

// iCloud sync turns on or off one turn at a time: one taken while the
// last still goes is not, so the switch and the syncing agree.
func TestICloudSwitchWaitsForItsLastTurn(t *testing.T) {
	a := templatesApp(t)
	a.sync.ICloud = icloud.Folder{Root: filepath.Join(t.TempDir(), "Leaflet"), Temp: t.TempDir()}
	t.Cleanup(func() { a.watchICloud(false) })
	posts := make(chan func(), 2)
	a.post = func(fn func()) { posts <- fn }
	next := func() func() {
		select {
		case fn := <-posts:
			return fn
		case <-time.After(5 * time.Second):
			t.Fatal("the turn did not finish")
			return nil
		}
	}
	a.setICloud(true)
	a.setICloud(false)
	if !a.settings.ICloudSync {
		t.Error("turned off while still turning on")
	}
	next()()
	if a.icloudBusy || !a.settings.ICloudSync || !a.sync.ICloudOn() {
		t.Fatalf("busy %v, setting %v, syncing %v", a.icloudBusy, a.settings.ICloudSync, a.sync.ICloudOn())
	}
	a.setICloud(false)
	next()()
	if a.settings.ICloudSync || a.sync.ICloudOn() {
		t.Errorf("setting %v, syncing %v", a.settings.ICloudSync, a.sync.ICloudOn())
	}
}

// failingServer is a server that refuses everything, as one that is down.
func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"down for maintenance"}`))
	}))
	t.Cleanup(hs.Close)
	return hs
}

// A provider kept on the server is removed here only once the server has
// deleted it, and asked about first; otherwise the dialog says why not.
func TestRemoveProviderAsksAndWaitsForTheServer(t *testing.T) {
	was := form
	t.Cleanup(func() { form = was })
	a := templatesApp(t)
	a.backend = remote.SignedInForTest(failingServer(t).URL, a.st, "u1")
	a.session.SignedIn = true
	a.hub = agents.NewHub(a.st, a.backend, a)
	p := store.Provider{ID: "00000000-0000-0000-0000-0000000000ab", Preset: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1",
		AuthKind: "api_key", DefaultModel: "gpt-5"}
	if err := a.st.PutProvider(p); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	// The server's answers come on the main thread, between frames: the
	// models the dialog lists as it opens, then the removal's.
	posts := make(chan func(), 1)
	a.post = func(fn func()) { posts <- fn }
	answer := func(what string) {
		t.Helper()
		select {
		case fn := <-posts:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatalf("the server never answered %s", what)
		}
	}
	a.editProvider(&p)
	tt := ui.NewTester(func(c *ui.Context) {
		a.providerDialog(c)
		a.removeAlert(c)
	}, 900, 800)
	// Its models' failure shows before Remove is clicked, not as it is,
	// moving it from under the click.
	answer("the models")
	tt.Frame()
	if err := tt.Click(locales.CommonRemove()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(locales.SettingsRemoveProviderTitle("OpenAI")) {
		t.Fatalf("no alert in %v", tt.Texts())
	}
	if _, err := a.st.Provider(p.ID); err != nil {
		t.Fatal("removed before the alert was answered")
	}
	form.confirmRemove = false
	a.removeProvider(p.ID)
	answer("the removal")
	tt.Frame()
	if _, err := a.st.Provider(p.ID); err != nil {
		t.Error("removed here though the server kept it")
	}
	if !form.open || !strings.Contains(form.errorText, "down for maintenance") || !tt.HasText("down for maintenance") {
		t.Errorf("open %v, error %q, shows %v", form.open, form.errorText, tt.Texts())
	}
}

// Settings close for the sign-in page they open, which would be behind
// them.
func TestSignInClosesSettings(t *testing.T) {
	a := templatesApp(t)
	a.openSettings("account")
	a.navigate(Route{Kind: "signin"})
	if a.settingsOpen || a.route.Kind != "signin" {
		t.Errorf("settings %v, route %+v", a.settingsOpen, a.route)
	}
}

// The name field shows the account's name until the user types in it:
// signing in shows the account's, and does not save the name shown before
// over it.
func TestNameFieldFollowsTheAccount(t *testing.T) {
	was := account
	t.Cleanup(func() { account = was })
	account = accountState{loaded: true}
	a := templatesApp(t)
	tt := ui.NewTester(func(c *ui.Context) { a.accountSettings(c) }, 800, 900)
	tt.Frame()
	if account.name != locales.CommonYou() {
		t.Fatalf("field shows %q", account.name)
	}
	if _, err := a.st.LoadSession(&store.User{ID: "u2", Email: "sam@example.com", DisplayName: "Sam"}); err != nil {
		t.Fatal(err)
	}
	a.session = a.st.Session()
	tt.Frame()
	tt.Frame()
	if account.name != "Sam" || a.st.Session().DisplayName != "Sam" {
		t.Errorf("field %q, account's name %q", account.name, a.st.Session().DisplayName)
	}

	// A name typed saves once the field is left.
	account.name, account.nameEdited = "Sam Lee", true
	tt.Frame()
	if got := a.st.Session().DisplayName; got != "Sam Lee" || account.nameEdited {
		t.Errorf("account's name %q, edited %v", got, account.nameEdited)
	}
}

// Devices that could not load say so, and load again on Retry.
func TestDevicesThatFailToLoad(t *testing.T) {
	was := account
	t.Cleanup(func() { account = was })
	account = accountState{loaded: true}
	a := templatesApp(t)
	a.backend = remote.SignedInForTest(failingServer(t).URL, a.st, "u1")
	a.loadDevices()
	if !strings.Contains(account.devicesErr, "down for maintenance") {
		t.Fatalf("error %q", account.devicesErr)
	}
	a.session.SignedIn = true
	tt := ui.NewTester(func(c *ui.Context) { a.accountSettings(c) }, 800, 900)
	tt.Frame()
	if tt.HasText(locales.SettingsDevicesLoading()) || !tt.HasText(locales.SettingsDevicesFailed(account.devicesErr)) {
		t.Fatalf("devices show %v", tt.Texts())
	}
	// The load runs off the main thread and posts its result to it.
	posts := make(chan func(), 1)
	a.post = func(fn func()) { posts <- fn }
	if err := tt.Click(locales.CommonRetry()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(locales.SettingsDevicesLoading()) {
		t.Fatalf("retrying shows %v", tt.Texts())
	}
	select {
	case fn := <-posts:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("Retry loaded nothing")
	}
	if !strings.Contains(account.devicesErr, "down for maintenance") {
		t.Errorf("error %q", account.devicesErr)
	}
}

// A provider's model is chosen in its own dialog; the default agent picks
// only the provider, and only once there are two to pick from.
func TestDefaultAgentPicksAProvider(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	put := func(p store.Provider) {
		t.Helper()
		if err := a.st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	put(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true})
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) { a.modelsSettings(c) }, 900, 800)
	tt.Frame()
	if tt.HasText(locales.SettingsDefaultAgent()) {
		t.Error("a default to pick with one provider")
	}

	put(store.Provider{ID: "studio", Preset: "ollama", Name: "Studio", AuthKind: "none", DefaultModel: "qwen", IsLocal: true})
	a.reloadAll()
	// A default from before names a model, which the provider's own
	// model now overrides.
	a.settings.DefaultModel = store.ModelChoice{ProviderID: "studio", Model: "old"}.Encode()
	tt.Frame()
	if !tt.HasText(locales.SettingsDefaultAgent()) || !tt.HasText("Studio") {
		t.Fatalf("no default to pick in %v", tt.Texts())
	}
	if tt.HasText("Studio · old") {
		t.Error("the default still names a model")
	}
	o, err := a.hub.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Provider.ID != "studio" || o.Model != "qwen" {
		t.Errorf("default runs %s · %s, want studio · qwen", o.Provider.ID, o.Model)
	}
}

// The offline switch is off and can't be turned on until a provider runs
// on this machine, and says so.
func TestOfflineSwitchNeedsALocalProvider(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.LocalWhenOffline = true
	put := func(p store.Provider) {
		t.Helper()
		if err := a.st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	put(store.Provider{ID: "cloud", Preset: "openai", Name: "OpenAI", AuthKind: "api_key", DefaultModel: "gpt-5"})
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) { a.modelsSettings(c) }, 900, 800)
	tt.Frame()
	if !tt.HasText(locales.SettingsLocalWhenOfflineNone()) || tt.HasText(locales.SettingsLocalWhenOfflineDesc()) {
		t.Errorf("no hint to add a local provider in %v", tt.Texts())
	}
	if !a.settings.LocalWhenOffline {
		t.Error("the setting was cleared while there was nothing to switch to")
	}

	put(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true})
	a.reloadAll()
	tt.Frame()
	if !tt.HasText(locales.SettingsLocalWhenOfflineDesc()) || tt.HasText(locales.SettingsLocalWhenOfflineNone()) {
		t.Errorf("the hint stays with a local provider in %v", tt.Texts())
	}
}
