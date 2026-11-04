package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Work started in a provider dialog, a test or a save, finds nowhere to go
// once that dialog is closed, or another one opened in its place.
func TestProviderResultsStayWithTheirDialog(t *testing.T) {
	a := &App{}
	a.openForm(providerForm{open: true, name: "A"})
	first := a.form.gen
	if !a.formIs(first) {
		t.Fatal("the open dialog is not itself")
	}
	a.form.open = false
	if a.formIs(first) {
		t.Error("a closed dialog took a result")
	}
	a.openForm(providerForm{open: true, name: "B"})
	if a.formIs(first) || !a.formIs(a.form.gen) {
		t.Error("dialog B took dialog A's result")
	}
}

// Models listed for one provider dialog don't land in another opened
// since, at the same address.
func TestModelsStayWithTheirDialog(t *testing.T) {
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
	a.openForm(providerForm{open: true, preset: "ollama", name: "A", baseURL: hs.URL})
	a.fetchModels()
	first := next()
	a.openForm(providerForm{open: true, preset: "ollama", name: "B", baseURL: hs.URL})
	a.fetchModels()
	second := next()
	first()
	if len(a.form.models) != 0 {
		t.Errorf("dialog B took dialog A's models %v", a.form.models)
	}
	second()
	if len(a.form.models) != 1 || a.form.models[0] != "llama-2" {
		t.Errorf("dialog B lists %v", a.form.models)
	}
}

// Models listed for one address don't land in the list once the address
// has changed, and a changed address lists its own.
func TestModelsFollowTheAddress(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"llama"}]}`)
	}))
	t.Cleanup(hs.Close)
	a := templatesApp(t)
	t.Cleanup(func() {
		if a.form.keyTimer != nil {
			a.form.keyTimer.Stop()
		}
	})
	posts := make(chan func(), 2)
	a.post = func(fn func()) { posts <- fn }
	a.openForm(providerForm{open: true, preset: "ollama", name: "A", baseURL: hs.URL})
	a.fetchModels()
	a.form.baseURL = "http://127.0.0.1:1"
	select {
	case fn := <-posts:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("no models came")
	}
	if len(a.form.models) != 0 || a.form.fetching {
		t.Errorf("the old address's models %v, fetching %v", a.form.models, a.form.fetching)
	}

	// Typing an address lists its models once it settles.
	a.form.baseURL = ""
	tt := ui.NewTester(func(c *ui.Context) { a.providerDialog(c) }, 900, 700)
	tt.Frame()
	if err := tt.Click(locales.SettingsBaseURL()); err != nil {
		t.Fatalf("no address field in %v: %v", tt.Texts(), err)
	}
	tt.Type("h")
	tt.Frame()
	if a.form.baseURL != "h" || a.form.keyTimer == nil {
		t.Errorf("address %q, fetch to come %v", a.form.baseURL, a.form.keyTimer != nil)
	}
}

// Switch account does nothing while a ChatGPT sign-in is under way: a
// second would wait in the browser beside the first.
func TestSwitchAccountWaitsForTheSignIn(t *testing.T) {
	a := templatesApp(t)
	a.openForm(providerForm{open: true, preset: "chatgpt", chatgpt: agents.NewChatGPT(agents.ChatGPTAccount{Email: "sam@example.com"}), signingIn: true})
	gen := a.form.gen
	// The app has no hub: a sign-in started would stop the test there.
	tt := ui.NewTester(func(c *ui.Context) { a.chatgptRow(c) }, 600, 200)
	tt.Frame()
	if err := tt.Click(locales.SettingsSwitchAccount()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.form.gen != gen || a.form.result != "" {
		t.Errorf("a second sign-in started: %+v", a.form)
	}
}

// A ChatGPT sign-in under way stops as its dialog closes, or another
// opens in its place: its page in the browser would sign in to nothing.
func TestChatGPTSignInStopsWithItsDialog(t *testing.T) {
	a := templatesApp(t)
	stopped := 0
	stop := func() { stopped++ }
	a.openForm(providerForm{open: true, preset: "chatgpt", signingIn: true, stopSignIn: stop})
	a.openForm(providerForm{open: true, preset: "chatgpt"})
	if stopped != 1 {
		t.Fatalf("stopped %d times as another dialog opened", stopped)
	}
	a.form.signingIn, a.form.stopSignIn = true, stop
	tt := ui.NewTester(func(c *ui.Context) { a.modelsSettings(c) }, 900, 700)
	tt.Frame()
	if stopped != 1 {
		t.Fatal("stopped while its dialog is open")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if a.form.open || stopped != 2 || a.form.stopSignIn != nil {
		t.Errorf("open %v, stopped %d times", a.form.open, stopped)
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
	// The answers come on the main thread, between frames: the key kept
	// here, read as the dialog opens, the models it lists then, and the
	// removal's.
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
	answer("the key")
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
	a.form.confirmRemove = false
	a.removeProvider(p.ID)
	answer("the removal")
	tt.Frame()
	if _, err := a.st.Provider(p.ID); err != nil {
		t.Error("removed here though the server kept it")
	}
	if !a.form.open || !strings.Contains(a.form.errorText, "down for maintenance") || !tt.HasText("down for maintenance") {
		t.Errorf("open %v, error %q, shows %v", a.form.open, a.form.errorText, tt.Texts())
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
	a := templatesApp(t)
	tt := ui.NewTester(func(c *ui.Context) { a.accountSettings(c) }, 800, 900)
	tt.Frame()
	if a.account.name != locales.CommonYou() {
		t.Fatalf("field shows %q", a.account.name)
	}
	if _, err := a.st.LoadSession(&store.User{ID: "u2", Email: "sam@example.com", DisplayName: "Sam"}); err != nil {
		t.Fatal(err)
	}
	a.session = a.st.Session()
	tt.Frame()
	tt.Frame()
	if a.account.name != "Sam" || a.st.Session().DisplayName != "Sam" {
		t.Errorf("field %q, account's name %q", a.account.name, a.st.Session().DisplayName)
	}

	// A name typed saves once the field is left.
	a.account.name, a.account.nameEdited = "Sam Lee", true
	tt.Frame()
	if got := a.st.Session().DisplayName; got != "Sam Lee" || a.account.nameEdited {
		t.Errorf("account's name %q, edited %v", got, a.account.nameEdited)
	}
}

// Devices that could not load say so, and load again on Retry.
func TestDevicesThatFailToLoad(t *testing.T) {
	a := templatesApp(t)
	a.backend = remote.SignedInForTest(failingServer(t).URL, a.st, "u1")
	a.loadDevices()
	if !strings.Contains(a.account.devicesErr, "down for maintenance") {
		t.Fatalf("error %q", a.account.devicesErr)
	}
	a.session.SignedIn = true
	tt := ui.NewTester(func(c *ui.Context) {
		a.accountSettings(c)
		a.keepName()
	}, 800, 900)
	tt.Frame()
	if tt.HasText(locales.SettingsDevicesLoading()) || !tt.HasText(locales.SettingsDevicesFailed(a.account.devicesErr)) {
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
	if !strings.Contains(a.account.devicesErr, "down for maintenance") {
		t.Errorf("error %q", a.account.devicesErr)
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

// Offline, agents run on a provider on this machine, even with settings
// saved while a switch turned that off.
func TestOfflineRunsALocalProvider(t *testing.T) {
	a := templatesApp(t)
	if err := a.st.SetValue("settings", `{"local_when_offline": false, "agents_read_other_pages": false}`); err != nil {
		t.Fatal(err)
	}
	a.settings = a.st.Settings()
	if a.settings.AgentsReadOtherPages {
		t.Error("saved settings did not load")
	}
	a.hub = agents.NewHub(a.st, a.backend, a)
	put := func(p store.Provider) {
		t.Helper()
		if err := a.st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	// A ChatGPT plan runs from this device but needs the network.
	put(store.Provider{ID: "plan", Preset: "chatgpt", Name: "ChatGPT", AuthKind: "account", DefaultModel: "gpt-6.1-sol", IsLocal: true})
	put(store.Provider{ID: "local", Preset: "ollama", Name: "Ollama", AuthKind: "none", DefaultModel: "llama3", IsLocal: true})
	a.reloadAll()
	a.settings.DefaultModel = store.ModelChoice{ProviderID: "plan"}.Encode()
	o, err := a.hub.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Provider.ID != "local" {
		t.Errorf("offline runs %s, want local", o.Provider.ID)
	}
}

// A name being typed saves as Settings closes, before the field is left.
func TestNameSavesAsSettingsCloses(t *testing.T) {
	a := templatesApp(t)
	a.settingsOpen = true
	tt := ui.NewTester(func(c *ui.Context) {
		if a.settingsOpen {
			a.accountSettings(c)
		}
		a.keepName()
	}, 800, 900)
	tt.Frame()
	// The field is at the right of its row.
	r, ok := tt.Find(locales.SettingsName())
	if !ok {
		t.Fatal("no name row")
	}
	tt.ClickAt(790-110, r.Y+r.H/2)
	tt.Key(0, ui.KeyEnd)
	tt.Type(" Lee")
	tt.Frame()
	want := locales.CommonYou() + " Lee"
	if got := a.st.Session().DisplayName; got == want || !a.account.nameEdited {
		t.Fatalf("saved %q while typing, edited %v", got, a.account.nameEdited)
	}
	a.settingsOpen = false
	tt.Frame()
	if got := a.st.Session().DisplayName; got != want || a.account.nameEdited {
		t.Errorf("account's name %q, edited %v", got, a.account.nameEdited)
	}
}

// keys is a keychain in memory, for tests that keep a provider's key.
type keys map[string][]byte

func (k keys) Get(name string) ([]byte, error) {
	v, ok := k[name]
	if !ok {
		return nil, keychain.ErrNotFound
	}
	return v, nil
}

func (k keys) Set(name string, value []byte) error {
	k[name] = value
	return nil
}

func (k keys) Delete(name string) error {
	delete(k, name)
	return nil
}

// Signed out, a cloud provider is kept on this device with its key, and
// saves; one kept on the server waits for the account.
func TestSignedOutSavesAProviderKeptHere(t *testing.T) {
	keychain.Use(keys{})
	// The package's own store comes back for the tests after this one.
	t.Cleanup(func() { keychain.Use(mygoSecrets{}) })
	a := templatesApp(t)
	if a.session.SignedIn {
		t.Fatal("the test app is signed in")
	}
	a.hub = agents.NewHub(a.st, a.backend, a)
	// The models listed as it is edited come from no server out there,
	// and are not waited for.
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(hs.Close)
	// What the keychain has, and how a save went, come on the main
	// thread, between frames.
	posted := make(chan func(), 8)
	a.post = func(fn func()) { posted <- fn }
	until := func(busy *bool, what string) {
		t.Helper()
		for *busy {
			select {
			case fn := <-posted:
				fn()
			case <-time.After(5 * time.Second):
				t.Fatalf("%s never came", what)
			}
		}
	}
	read := func() {
		t.Helper()
		until(&a.form.reading, "the key kept here")
	}
	tt := ui.NewTester(a.providerDialog, 900, 800)
	a.newProvider(agents.PresetByID("deepseek"))
	a.form.baseURL, a.form.apiKey = hs.URL, "sk-test"
	tt.Frame()
	if !tt.HasText(locales.SettingsProviderDescDevice()) {
		t.Errorf("the dialog does not say the provider stays here: %v", tt.Texts())
	}
	if err := tt.Click(locales.CommonSave()); err != nil {
		t.Fatal(err)
	}
	until(&a.form.saving, "the save")
	tt.Frame()
	ps, err := a.st.Providers()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || !ps[0].IsLocal || ps[0].Preset != "deepseek" {
		t.Fatalf("saved %+v", ps)
	}
	if a.form.open {
		t.Errorf("the dialog stayed open: %q", a.form.errorText)
	}
	if got := agents.LocalKey(ps[0].ID); got != "sk-test" {
		t.Errorf("key %q", got)
	}

	// Edited signed out, it saves again.
	a.editProvider(&ps[0])
	// Until its key has been read, it does not save, which would drop it.
	tt.Frame()
	if err := tt.Click(locales.CommonSave()); err != nil {
		t.Fatal(err)
	}
	if !a.form.open || agents.LocalKey(ps[0].ID) != "sk-test" {
		t.Fatalf("saved before its key was read: open %v", a.form.open)
	}
	read()
	if a.form.apiKey != "sk-test" {
		t.Errorf("edited with key %q", a.form.apiKey)
	}
	a.form.model = "deepseek-v4-flash"
	tt.Frame()
	if err := tt.Click(locales.CommonSave()); err != nil {
		t.Fatal(err)
	}
	until(&a.form.saving, "the save")
	tt.Frame()
	if p, err := a.st.Provider(ps[0].ID); err != nil || p.DefaultModel != "deepseek-v4-flash" {
		t.Errorf("edited to %+v, %v", p, err)
	}

	// One on the server is not saved signed out.
	server := store.Provider{ID: "00000000-0000-0000-0000-0000000000cd", Preset: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1",
		AuthKind: "api_key", DefaultModel: "gpt-6-astra"}
	if err := a.st.PutProvider(server); err != nil {
		t.Fatal(err)
	}
	a.editProvider(&server)
	read()
	a.form.model = "gpt-6-luna"
	tt.Frame()
	if err := tt.Click(locales.CommonSave()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.form.saving || !a.form.open {
		t.Errorf("a server's provider saved signed out: saving %v, open %v", a.form.saving, a.form.open)
	}
}

// Sync errors are in words for the user, as a pass hands them on: the
// session ended, the pass ran out of time, or the server refused it; and
// iCloud sync failing to turn on says what failed.
func TestSyncMessages(t *testing.T) {
	a := templatesApp(t)
	done := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/v1/slow" {
			select {
			case <-r.Context().Done():
			case <-done:
			}
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"new row violates row-level security policy"}`))
	}))
	t.Cleanup(hs.Close)
	t.Cleanup(func() { close(done) })
	be := remote.SignedInForTest(hs.URL, a.st, "u1")
	_, refused := be.Insert(t.Context(), "pages", map[string]any{"id": "p"})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, late := be.Insert(ctx, "slow", map[string]any{"id": "p"})
	_, out := remote.New(remote.Settings{URL: hs.URL, PublishableKey: "test"}, a.st).Insert(t.Context(), "pages", map[string]any{"id": "p"})
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("push: %w", out), locales.SettingsSyncSignedOut()},
		{fmt.Errorf("push: %w", late), locales.SettingsSyncTimedOut()},
		{fmt.Errorf("push: %w", refused), locales.SettingsSyncRefused(http.StatusForbidden, "new row violates row-level security policy")},
	} {
		// Each is one a pass hands on, not one of the network it waits out.
		if c.err == nil || remote.IsNetwork(c.err) {
			t.Fatalf("not an error a pass hands on: %v", c.err)
		}
		if got := a.syncMessage(c.err); got != c.want {
			t.Errorf("syncMessage(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	disk := errors.New("mkdir Leaflet: permission denied")
	for err, want := range map[error]string{
		disk:                  locales.SettingsICloudTurnOnFailed(disk.Error()),
		icloud.ErrUnavailable: locales.SettingsICloudUnavailable(),
		icloud.ErrNewerFormat: locales.SettingsICloudNewer(),
	} {
		if got := a.iCloudFailed(err); got != want {
			t.Errorf("iCloudFailed(%v) = %q, want %q", err, got, want)
		}
	}
}

// A provider removed on another device takes the default with it: the
// app reads the default as none, as the store does.
func TestDefaultGoesWithItsProvider(t *testing.T) {
	a := templatesApp(t)
	if err := a.st.PutProvider(store.Provider{ID: "p1", Preset: "openai", Name: "OpenAI", AuthKind: "api_key"}); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "model_providers"})
	a.settings.DefaultModel = store.ModelChoice{ProviderID: "p1"}.Encode()
	if err := a.st.DeleteProvider("p1"); err != nil {
		t.Fatal(err)
	}
	a.reload(store.Change{Table: "model_providers"})
	if _, ok := a.settings.Default(); ok {
		t.Errorf("default %q after its provider went", a.settings.DefaultModel)
	}
}

// The devices shown are the account's signed in now, as one signed out
// elsewhere and another signed in, though there are none; and they load
// again as the section opens.
func TestDevicesFollowTheAccount(t *testing.T) {
	a := templatesApp(t)
	var rows atomic.Value
	rows.Store(`[{"id":"d1","name":"Old Mac","last_active_at":"2026-11-01T10:00:00Z"}]`)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rows.Load().(string)))
	}))
	t.Cleanup(hs.Close)
	a.backend = remote.SignedInForTest(hs.URL, a.st, "u1")
	signIn := func(id string) { a.session = store.Session{UserID: id, Email: id + "@example.com", SignedIn: true} }
	signIn("u1")
	shown := true
	tt := ui.NewTester(func(c *ui.Context) {
		if shown {
			a.accountSettings(c)
		}
		a.keepName()
	}, 800, 900)
	tt.Frame()
	if !tt.HasText("Old Mac") {
		t.Fatalf("devices show %v", tt.Texts())
	}

	rows.Store(`[]`)
	signIn("u2")
	tt.Frame()
	if tt.HasText("Old Mac") || tt.HasText(locales.SettingsDevicesLoading()) || !tt.HasText(locales.SettingsDevicesNone()) {
		t.Fatalf("another account's devices show %v", tt.Texts())
	}

	rows.Store(`[{"id":"d2","name":"New iPad","last_active_at":"2026-11-02T10:00:00Z"}]`)
	tt.Frame()
	if tt.HasText("New iPad") {
		t.Fatal("loaded again in every frame")
	}
	shown = false
	tt.Frame()
	shown = true
	tt.Frame()
	if !tt.HasText("New iPad") {
		t.Errorf("reopened, devices show %v", tt.Texts())
	}
}

// Deleting the account takes DELETE typed, then its button or Return,
// and waits while the server deletes it.
func TestDeleteAccountAsksForDELETE(t *testing.T) {
	a := templatesApp(t)
	var calls atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"down for maintenance"}`))
	}))
	t.Cleanup(hs.Close)
	a.backend = remote.SignedInForTest(hs.URL, a.st, "u1")
	a.account.deleteOpen = true
	tt := ui.NewTester(func(c *ui.Context) { a.deleteAccountDialog(c) }, 800, 600)
	tt.Frame()
	a.account.confirm = "DELET"
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if calls.Load() != 0 || a.account.busy {
		t.Fatal("deleted before DELETE was typed")
	}
	a.account.confirm = "DELETE"
	tt.Frame()
	posts := make(chan func(), 1)
	a.post = func(fn func()) { posts <- fn }
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !a.account.busy || !tt.HasText(locales.SettingsDeletingAccount()) {
		t.Fatalf("busy %v, shows %v", a.account.busy, tt.Texts())
	}
	select {
	case fn := <-posts:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("the server was not asked")
	}
	if calls.Load() != 1 || a.account.busy || !a.account.deleteOpen ||
		len(a.toasts) != 1 || !strings.Contains(a.toasts[0].text, "down for maintenance") {
		t.Errorf("calls %d, busy %v, open %v, toasts %+v", calls.Load(), a.account.busy, a.account.deleteOpen, a.toasts)
	}
}

// A conflict whose version kept is this device's, the newer, offers the
// other device's, and puts it back; one whose kept version is the other
// device's offers this one's instead.
func TestConflictOffersTheVersionNotKept(t *testing.T) {
	a := templatesApp(t)
	para := func(text string) *doc.Node {
		n := &doc.Node{ID: "b1", Type: "paragraph"}
		n.SetDelta(doc.FromMarkdown(text))
		return n
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Plans", Content: doc.FromBlocks([]*doc.Node{para("Mine")})})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := a.st.AddConflict(p.ID, doc.Conflict{BlockID: "b1", Local: para("Mine"), Remote: para("Theirs"),
		LocalEditedAt: now, RemoteEditedAt: now - 1000}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) { a.offlineSettings(c) }, 900, 900)
	tt.Frame()
	if err := tt.Click(locales.SettingsResolve()); err != nil {
		t.Fatal(err)
	}
	if got := tt.Menu(); !slices.Contains(got, locales.SettingsUseOtherDevice()) || slices.Contains(got, locales.SettingsUseThisDevice()) {
		t.Fatalf("menu %q", got)
	}
	if err := tt.ChooseMenuItem(locales.SettingsUseOtherDevice()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.st.Content(p.ID).PlainText(); !strings.Contains(got, "Theirs") || strings.Contains(got, "Mine") {
		t.Errorf("put back %q", got)
	}
	a.reloadAll()
	if len(a.conflicts) != 0 {
		t.Errorf("conflicts %+v", a.conflicts)
	}

	if err := a.st.AddConflict(p.ID, doc.Conflict{BlockID: "b1", Local: para("Mine"), Remote: para("Theirs"),
		LocalEditedAt: now - 1000, RemoteEditedAt: now}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	tt.Frame()
	if err := tt.Click(locales.SettingsResolve()); err != nil {
		t.Fatal(err)
	}
	if got := tt.Menu(); !slices.Contains(got, locales.SettingsUseThisDevice()) || slices.Contains(got, locales.SettingsUseOtherDevice()) {
		t.Errorf("menu %q", got)
	}
}
