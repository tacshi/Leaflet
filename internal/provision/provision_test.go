package provision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/supabaseapi"
	"leaflet/supabase"
)

// fakeProject is a Management API with projects whose migrations and
// secrets the test sets, recording what Setup does.
type fakeProject struct {
	mu       sync.Mutex
	applied  map[string][]supabaseapi.Migration
	secrets  []supabaseapi.Secret
	queries  []string
	deployed []string
	status   []string
	created  string
	restored []string
	members  string
	// broken is a project whose migrations can't be listed.
	broken string
	// refused refuses every query.
	refused bool
	// meanwhile are the versions another device applies while this one
	// waits for its lock.
	meanwhile []string
	// signUps are the project's Auth settings, and signUpPatches how
	// often they were changed; authRefused refuses both, as a sign-in
	// without the Auth scope is.
	signUps       supabaseapi.SignUps
	signUpPatches int
	authRefused   bool
}

func (f *fakeProject) serve(t *testing.T) *supabaseapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		switch {
		case path == "/v1/oauth/token":
			_, _ = io.WriteString(w, `{"access_token":"a","expires_in":3600,"token_type":"Bearer"}`)
		case strings.HasPrefix(path, "/v1/organizations/") && strings.HasSuffix(path, "/members"):
			_, _ = io.WriteString(w, f.members)
		case path == "/v1/organizations":
			_, _ = io.WriteString(w, `[{"id":"1","slug":"me","name":"Me"}]`)
		case path == "/v1/projects" && r.Method == "GET":
			_, _ = io.WriteString(w, `[
				{"ref":"new","name":"Later","status":"ACTIVE_HEALTHY","created_at":"2026-10-02","organization_slug":"me"},
				{"ref":"old","name":"Leaflet","status":"ACTIVE_HEALTHY","created_at":"2026-10-01","organization_slug":"me"},
				{"ref":"other","name":"Shop","status":"ACTIVE_HEALTHY","created_at":"2026-09-01","organization_slug":"me"},
				{"ref":"paused","name":"Leaflet 2","status":"INACTIVE","created_at":"2026-08-01","organization_slug":"me"},
				{"ref":"empty","name":"Leaflet 3","status":"ACTIVE_HEALTHY","created_at":"2026-08-02","organization_slug":"me"},
				{"ref":"mine","name":"Leaflet 4","status":"ACTIVE_HEALTHY","created_at":"2026-08-03","organization_slug":"me"},
				{"ref":"gone","name":"Leaflet 5","status":"REMOVED","created_at":"2026-08-04","organization_slug":"me"},
				{"ref":"dev","name":"Leaflet-dev","status":"ACTIVE_HEALTHY","created_at":"2026-07-01","organization_slug":"me"}]`)
		case path == "/v1/projects" && r.Method == "POST":
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.created = body.Name
			_, _ = io.WriteString(w, `{"ref":"made","name":"Leaflet","status":"COMING_UP"}`)
		case strings.HasSuffix(path, "/restore"):
			f.restored = append(f.restored, strings.Split(path, "/")[3])
		case path == "/v1/projects/paused":
			_, _ = io.WriteString(w, `{"ref":"paused","name":"Leaflet 2","status":"ACTIVE_HEALTHY"}`)
		case path == "/v1/projects/made":
			st := "COMING_UP"
			if len(f.status) > 0 {
				st, f.status = f.status[0], f.status[1:]
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"ref": "made", "name": "Leaflet", "status": st})
		case strings.HasSuffix(path, "/database/migrations"):
			ref := strings.Split(path, "/")[3]
			if ref == f.broken {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(f.applied[ref])
		case strings.HasSuffix(path, "/database/query") && f.refused:
			http.Error(w, `{"message":"permission denied"}`, http.StatusBadRequest)
		case strings.HasSuffix(path, "/database/query"):
			var body struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.queries = append(f.queries, body.Query)
			for _, v := range f.meanwhile {
				if strings.Contains(body.Query, "where version = '"+v+"') then raise exception") {
					// As Postgres answers the check that raises.
					http.Error(w, `{"message":"Failed to run sql query: ERROR:  P0001: leaflet_migration_applied\n"}`, http.StatusBadRequest)
					return
				}
			}
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(path, "/config/auth") && f.authRefused:
			http.Error(w, `{"message":"Forbidden resource"}`, http.StatusForbidden)
		case strings.HasSuffix(path, "/config/auth") && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(f.signUps)
		case strings.HasSuffix(path, "/config/auth") && r.Method == "PATCH":
			// Only the fields sent change, as the API's PATCH does.
			var set map[string]bool
			_ = json.NewDecoder(r.Body).Decode(&set)
			if v, ok := set["disable_signup"]; ok {
				f.signUps.Disabled = v
			}
			if v, ok := set["mailer_autoconfirm"]; ok {
				f.signUps.AutoConfirm = v
			}
			f.signUpPatches++
			_ = json.NewEncoder(w).Encode(f.signUps)
		case strings.HasSuffix(path, "/secrets") && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(f.secrets)
		case strings.HasSuffix(path, "/secrets") && r.Method == "DELETE":
			var names []string
			_ = json.NewDecoder(r.Body).Decode(&names)
			f.secrets = slices.DeleteFunc(f.secrets, func(s supabaseapi.Secret) bool { return slices.Contains(names, s.Name) })
		case strings.HasSuffix(path, "/secrets"):
			// Listed with a digest the app can't count on.
			var set []supabaseapi.Secret
			_ = json.NewDecoder(r.Body).Decode(&set)
			for _, s := range set {
				f.secrets = append(f.secrets, supabaseapi.Secret{Name: s.Name, Value: "digest"})
			}
		case strings.HasSuffix(path, "/functions/deploy"):
			f.deployed = append(f.deployed, r.URL.Query().Get("slug"))
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(path, "/config/storage"):
		case strings.HasSuffix(path, "/api-keys"):
			_, _ = io.WriteString(w, `[{"name":"anon","type":"legacy","api_key":"eyJanon"},{"name":"service_role","type":"legacy","api_key":"eyJservice"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	old, auth := supabaseapi.Base, supabaseapi.Auth
	supabaseapi.Base, supabaseapi.Auth = srv.URL, ""
	t.Cleanup(func() { supabaseapi.Base, supabaseapi.Auth = old, auth; srv.Close() })
	return supabaseapi.Resume("r")
}

// Find tells the Leaflet projects from those Leaflet made and did not
// finish, or that are paused, and leaves out what holds something else.
func TestFindTellsProjectsApart(t *testing.T) {
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{
		"old":   {{Version: FirstMigration, Name: "init"}},
		"new":   {{Version: FirstMigration, Name: "init"}},
		"other": {{Version: "20200101000000", Name: "shop"}},
		"mine":  {{Version: "20200101000000", Name: "notes"}},
		"dev":   {{Version: FirstMigration, Name: "init"}},
	}}
	api := f.serve(t)
	found, err := Find(context.Background(), api)
	if err != nil {
		t.Fatal(err)
	}
	refs := func(ps []supabaseapi.Project) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Ref)
		}
		return strings.Join(out, ",")
	}
	if got := refs(found.Projects); got != "old,new" {
		t.Errorf("Leaflet projects %s", got)
	}
	if got := refs(found.Unfinished); got != "paused,empty" {
		t.Errorf("unfinished %s", got)
	}
	if len(found.Orgs) != 1 {
		t.Errorf("orgs %v", found.Orgs)
	}

	// A development build sees its own projects only.
	ProjectName = DevProjectName
	t.Cleanup(func() { ProjectName = "Leaflet" })
	if found, err = Find(context.Background(), api); err != nil {
		t.Fatal(err)
	}
	if got := refs(found.Projects) + "|" + refs(found.Unfinished); got != "dev|" {
		t.Errorf("a development build found %s", got)
	}
}

// A new project takes a name the organization has free.
func TestCreateNamesItFree(t *testing.T) {
	old := pollEvery
	pollEvery = time.Millisecond
	t.Cleanup(func() { pollEvery = old })
	f := &fakeProject{status: []string{"ACTIVE_HEALTHY"}}
	api := f.serve(t)
	if _, err := Create(context.Background(), api, "me"); err != nil {
		t.Fatal(err)
	}
	if f.created != "Leaflet 6" {
		t.Errorf("named %q", f.created)
	}
}

// A paused project restarts before it is set up.
func TestReadyRestoresPaused(t *testing.T) {
	f := &fakeProject{}
	api := f.serve(t)
	p, err := Ready(context.Background(), api, supabaseapi.Project{Ref: "paused", Name: "Leaflet 2", Status: "INACTIVE"})
	if err != nil || !p.Healthy() || len(f.restored) != 1 {
		t.Errorf("ready %+v, %v, restored %v", p, err, f.restored)
	}
}

func TestIsLeafletName(t *testing.T) {
	for name, want := range map[string]bool{"Leaflet": true, "Leaflet 2": true, "Leaflet 12": true, "Leaflet 1": false, "Leaflets": false, "Leaflet x": false, "My Leaflet": false} {
		if IsLeafletName(name) != want {
			t.Errorf("%q: %v", name, !want)
		}
	}
}

func TestSetupAppliesWhatIsMissing(t *testing.T) {
	all, err := Migrations()
	if err != nil || len(all) < 2 || all[0].Version != FirstMigration {
		t.Fatalf("migrations %d, %v", len(all), err)
	}
	f := &fakeProject{
		applied: map[string][]supabaseapi.Migration{"p": {{Version: all[0].Version, Name: all[0].Name}}},
		secrets: []supabaseapi.Secret{{Name: "LEAFLET_FUNCTIONS_0123456789ABCDEF", Value: "digest"}, {Name: "OTHER", Value: "digest"}},
	}
	api := f.serve(t)
	var steps [][2]int
	if err := Setup(context.Background(), api, "p", func(done, total int) { steps = append(steps, [2]int{done, total}) }); err != nil {
		t.Fatal(err)
	}
	hash, err := FunctionsHash()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != len(all)-1 {
		t.Fatalf("applied %d migrations, want %d", len(f.queries), len(all)-1)
	}
	last := all[len(all)-1]
	if q := f.queries[len(f.queries)-1]; !strings.Contains(q, last.SQL) ||
		!strings.Contains(q, "values ('"+last.Version+"', '"+last.Name+"')") {
		t.Errorf("the last migration is not recorded as the CLI does:\n%s", q[max(0, len(q)-300):])
	}
	if strings.Join(f.deployed, ",") != strings.Join(Functions, ",") {
		t.Errorf("deployed %v", f.deployed)
	}
	if want := len(all) - 1 + len(Functions); len(steps) != want || steps[len(steps)-1] != [2]int{want, want} {
		t.Errorf("progress %v", steps)
	}

	var names []string
	for _, s := range f.secrets {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "OTHER,"+hashName(FunctionsVersion, hash) {
		t.Errorf("secrets %v", names)
	}

	// Up to date, from another device: nothing more, by the project's
	// secret.
	f.applied["p"] = nil
	for _, m := range all {
		f.applied["p"] = append(f.applied["p"], supabaseapi.Migration{Version: m.Version})
	}
	f.queries, f.deployed = nil, nil
	if err := Setup(context.Background(), api, "p", nil); err != nil || len(f.queries) != 0 || len(f.deployed) != 0 {
		t.Errorf("set up again: %v, %d queries, deployed %v", err, len(f.queries), f.deployed)
	}
}

// An older build never deploys over newer functions, and a newer one
// replaces older ones, the one from before the secret named its version
// too. Functions of the same version stay, whatever their hash, so two
// builds never take turns; only a development build deploys over them.
func TestSetupNeverDowngrades(t *testing.T) {
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := FunctionsHash()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}}
	for _, m := range all {
		f.applied["p"] = append(f.applied["p"], supabaseapi.Migration{Version: m.Version})
	}
	api := f.serve(t)
	setup := func(secret string) []string {
		t.Helper()
		f.secrets, f.deployed = []supabaseapi.Secret{{Name: secret, Value: "digest"}}, nil
		if err := Setup(context.Background(), api, "p", nil); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range f.secrets {
			names = append(names, s.Name)
		}
		return names
	}
	other := "FEDCBA9876543210"
	for _, newer := range []string{hashName(FunctionsVersion+1, other), hashName(FunctionsVersion, other)} {
		if names := setup(newer); len(f.deployed) != 0 || len(names) != 1 || names[0] != newer {
			t.Errorf("over %s: deployed %v, secrets %v", newer, f.deployed, names)
		}
	}
	for _, older := range []string{hashName(FunctionsVersion-1, other), hashSecret + other} {
		if names := setup(older); len(f.deployed) != len(Functions) || len(names) != 1 || names[0] != hashName(FunctionsVersion, hash) {
			t.Errorf("over %s: deployed %v, secrets %v", older, f.deployed, names)
		}
	}

	ProjectName = DevProjectName
	t.Cleanup(func() { ProjectName = "Leaflet" })
	if setup(hashName(FunctionsVersion, other)); len(f.deployed) != len(Functions) {
		t.Errorf("a development build left other functions of its version: deployed %v", f.deployed)
	}
	if setup(hashName(FunctionsVersion, hash)); len(f.deployed) != 0 {
		t.Errorf("a development build deployed its own again: %v", f.deployed)
	}
}

// functionsHashes are FunctionsHash at each FunctionsVersion. Changing
// supabase/functions raises FunctionsVersion by one and adds its hash
// here; a released version's hash never changes.
var functionsHashes = map[int]string{
	1: "6c7a63f55f1a88ba9ced2e8085b35cf1e781c73438e276ef73709cefef78836b",
	2: "2b34649fda4f2565e5de75e6772d25454ba74c8435a6a898f925ddd792acabf1",
	3: "dfb182b6786ef1ea387d13f63c7a613ef98ed019b9e0527a337bf395c7a72cd9",
	4: "dd527200d9329f6f96d2b17c4dbc0e5994e8ff69893fc44969b9e8edab8159a7",
}

// The functions a project gets import their packages at exact versions:
// their lock does not go with them, and "@2" would be whatever 2.x is the
// newest as each project deploys, untested, and differ between projects of
// one FunctionsVersion.
func TestFunctionsImportExactVersions(t *testing.T) {
	spec := regexp.MustCompile(`"((?:jsr|npm):@?[^"@]+@[^"]*)"`)
	exact := regexp.MustCompile(`@\d+\.\d+\.\d+$`)
	err := fs.WalkDir(supabase.Backend, "functions", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.ts") {
			return err
		}
		src, err := fs.ReadFile(supabase.Backend, path)
		if err != nil {
			return err
		}
		for _, m := range spec.FindAllStringSubmatch(string(src), -1) {
			if !exact.MatchString(m[1]) {
				t.Errorf("%s imports %s, not at an exact version", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// FunctionsVersion goes up with every change to the functions: a build
// with other functions at the same version would neither deploy them nor
// have them deployed.
func TestFunctionsVersion(t *testing.T) {
	hash, err := FunctionsHash()
	if err != nil {
		t.Fatal(err)
	}
	if functionsHashes[FunctionsVersion] != hash {
		t.Errorf("supabase/functions changed: raise FunctionsVersion to %d and add %d: %q to functionsHashes", FunctionsVersion+1, FunctionsVersion+1, hash)
	}
	for v := 1; v <= FunctionsVersion; v++ {
		if functionsHashes[v] == "" {
			t.Errorf("no hash for FunctionsVersion %d", v)
		}
	}
}

// A project named as Leaflet's whose migrations can't be listed fails
// Find, rather than being left out, which would offer to make another;
// another of the account's is left out.
func TestFindFailsOnAProjectItCantCheck(t *testing.T) {
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{"new": {{Version: FirstMigration}}}, broken: "old"}
	api := f.serve(t)
	if found, err := Find(context.Background(), api); err == nil {
		t.Errorf("found %+v", found)
	}
	f.broken = "other"
	if found, err := Find(context.Background(), api); err != nil || len(found.Projects) != 1 || found.Projects[0].Ref != "new" {
		t.Errorf("found %+v, %v", found, err)
	}
}

// A project that was pausing is restored once it has paused.
func TestReadyRestoresOncePaused(t *testing.T) {
	old := pollEvery
	pollEvery = time.Millisecond
	t.Cleanup(func() { pollEvery = old })
	f := &fakeProject{status: []string{"PAUSING", "INACTIVE", "INACTIVE", "RESTORING", "ACTIVE_HEALTHY"}}
	api := f.serve(t)
	p, err := Ready(context.Background(), api, supabaseapi.Project{Ref: "made", Status: "PAUSING"})
	if err != nil || !p.Healthy() || strings.Join(f.restored, ",") != "made" {
		t.Errorf("ready %+v, %v, restored %v", p, err, f.restored)
	}
}

// A function goes up with its entrypoint first, the rest of its folder and
// the shared modules, and none of their tests.
func TestFunctionsLeaveTestsOut(t *testing.T) {
	files, err := functionFiles("agent")
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Path != "supabase/functions/agent/index.ts" {
		t.Errorf("entrypoint %s", files[0].Path)
	}
	// What index.ts imports from its own folder goes with it.
	if !slices.ContainsFunc(files, func(f supabaseapi.File) bool { return f.Path == "supabase/functions/agent/handler.ts" }) {
		t.Error("the agent's handler stays behind")
	}
	for _, f := range files {
		if strings.HasSuffix(f.Path, "_test.ts") || !strings.HasPrefix(f.Path, "supabase/functions/") {
			t.Errorf("uploads %s", f.Path)
		}
	}
}

func TestCreateWaitsUntilUp(t *testing.T) {
	old := pollEvery
	pollEvery = time.Millisecond
	t.Cleanup(func() { pollEvery = old })
	f := &fakeProject{status: []string{"COMING_UP", "ACTIVE_HEALTHY"}}
	api := f.serve(t)
	p, err := Create(context.Background(), api, "me")
	if err != nil || !p.Healthy() || p.Ref != "made" {
		t.Errorf("created %+v, %v", p, err)
	}
	f.status = []string{"INIT_FAILED"}
	if _, err := Create(context.Background(), api, "me"); err == nil {
		t.Error("a project that failed to start came up")
	}
}

func TestKeysFallBackToLegacy(t *testing.T) {
	api := (&fakeProject{}).serve(t)
	pub, sec, err := Keys(context.Background(), api, "p")
	if err != nil || pub != "eyJanon" || sec != "eyJservice" {
		t.Errorf("keys %q %q %v", pub, sec, err)
	}
}

func TestRegionGroup(t *testing.T) {
	for zone, want := range map[int]string{-8: "americas", -3: "americas", 0: "emea", 3: "emea", 8: "apac", 10: "apac"} {
		now := time.Now().In(time.FixedZone("", zone*3600))
		if got := RegionGroup(now); got != want {
			t.Errorf("UTC%+d: %s, want %s", zone, got, want)
		}
	}
}

// A development build's projects go by their own name.
func TestDevProjectName(t *testing.T) {
	old := ProjectName
	ProjectName = "Leaflet-dev"
	t.Cleanup(func() { ProjectName = old })
	for name, want := range map[string]bool{"Leaflet-dev": true, "Leaflet-dev 2": true, "Leaflet": false, "Leaflet 2": false} {
		if IsLeafletName(name) != want {
			t.Errorf("%q: %v", name, !want)
		}
	}
}

// The person signed in is the organization's one member; with more, it
// can't be told which.
func TestOwnerFromMembers(t *testing.T) {
	for members, want := range map[string]string{
		`[{"user_name":"me","email":"me@example.com","role_name":"Developer"}]`:                                                                   "me@example.com",
		`[{"user_name":"me","email":"me@example.com","role_name":"Owner"},{"user_name":"you","email":"you@example.com","role_name":"Developer"}]`: "",
		`[{"user_name":"me","email":"me@example.com","role_name":"Owner"},{"user_name":"bot","role_name":"Developer"}]`:                           "me@example.com",
		`[{"user_name":"me","email":"me@example.com","role_name":"Owner"},{"user_name":"you","email":"you@example.com","role_name":"Owner"}]`:     "",
	} {
		f := &fakeProject{members: members}
		api := f.serve(t)
		email, _, err := Owner(context.Background(), api, "me")
		if email != want || (want == "") != (err != nil) {
			t.Errorf("%s: %q, %v", members, email, err)
		}
	}
}

// A setup that fails says so in the app's language, the API's error still
// in it, to errors.As too.
func TestSetupFailsInTheAppsLanguage(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "ja" })
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}, refused: true}
	err := Setup(context.Background(), f.serve(t), "p", nil)
	var api *supabaseapi.Error
	if err == nil || !strings.HasPrefix(err.Error(), "Leaflet でデータベースを設定できませんでした（") || !strings.Contains(err.Error(), "permission denied") || !errors.As(err, &api) {
		t.Errorf("setup: %v", err)
	}
}

// Two devices setting a project up at once take turns, migration by
// migration: each takes the lock first, then checks the version is not
// recorded, before any of the migration. One the other device applied
// while this one waited is done, and the rest go on.
func TestSetupSkipsWhatAnotherDeviceApplied(t *testing.T) {
	all, err := Migrations()
	if err != nil || len(all) < 3 {
		t.Fatalf("migrations %d, %v", len(all), err)
	}
	q := migrationSQL(all[1])
	check := strings.Index(q, "where version = '"+all[1].Version+"'")
	body := strings.Index(q, all[1].SQL)
	record := strings.LastIndex(q, "insert into supabase_migrations.schema_migrations")
	if !strings.HasPrefix(q, "select pg_advisory_xact_lock(") || check < 0 || body < check || record < body {
		t.Errorf("check at %d, migration at %d, record at %d:\n%s", check, body, record, q[:min(len(q), 400)])
	}

	f := &fakeProject{
		applied:   map[string][]supabaseapi.Migration{"p": {{Version: all[0].Version}}},
		meanwhile: []string{all[1].Version},
	}
	var steps [][2]int
	if err := Setup(context.Background(), f.serve(t), "p", func(done, total int) { steps = append(steps, [2]int{done, total}) }); err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != len(all)-1 {
		t.Errorf("sent %d migrations, want %d", len(f.queries), len(all)-1)
	}
	if want := len(all) - 1 + len(Functions); len(steps) != want {
		t.Errorf("progress %v", steps)
	}

	// Any other failure still stops it.
	f = &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}, refused: true}
	if err := Setup(context.Background(), f.serve(t), "p", nil); err == nil {
		t.Error("a refused migration counted as applied")
	}
}

// Setup keeps the project's Auth to the owner's account: sign-ups off and
// addresses confirmed, changed only when they differ. A sign-in that may
// not touch Auth leaves them, and the setup goes on.
func TestSetupClosesSignUps(t *testing.T) {
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}, signUps: supabaseapi.SignUps{AutoConfirm: true}}
	api := f.serve(t)
	if err := Setup(context.Background(), api, "p", nil); err != nil {
		t.Fatal(err)
	}
	if f.signUps != (supabaseapi.SignUps{Disabled: true}) || f.signUpPatches != 1 {
		t.Errorf("sign-ups %+v after %d changes", f.signUps, f.signUpPatches)
	}
	if err := Setup(context.Background(), api, "p", nil); err != nil || f.signUpPatches != 1 {
		t.Errorf("set up again: %v, %d changes", err, f.signUpPatches)
	}

	f = &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}, authRefused: true}
	if err := Setup(context.Background(), f.serve(t), "p", nil); err != nil || len(f.deployed) != len(Functions) {
		t.Errorf("with Auth refused: %v, deployed %v", err, f.deployed)
	}
}
