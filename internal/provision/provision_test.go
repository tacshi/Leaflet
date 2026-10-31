package provision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/supabaseapi"
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
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.created = body.Name
			_, _ = io.WriteString(w, `{"ref":"made","name":"Leaflet","status":"COMING_UP"}`)
		case path == "/v1/projects/paused/restore":
			f.restored = append(f.restored, "paused")
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
			_ = json.NewEncoder(w).Encode(f.applied[ref])
		case strings.HasSuffix(path, "/database/query"):
			var body struct{ Query string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.queries = append(f.queries, body.Query)
			w.WriteHeader(http.StatusCreated)
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
	hash, err := Setup(context.Background(), api, "p", "", func(done, total int) { steps = append(steps, [2]int{done, total}) })
	if err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != len(all)-1 {
		t.Fatalf("applied %d migrations, want %d", len(f.queries), len(all)-1)
	}
	last := all[len(all)-1]
	if q := f.queries[len(f.queries)-1]; !strings.HasPrefix(q, last.SQL) ||
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
	if strings.Join(names, ",") != "OTHER,"+hashName(hash) {
		t.Errorf("secrets %v", names)
	}

	// Up to date, from another device: nothing more, by the project's
	// secret.
	f.applied["p"] = nil
	for _, m := range all {
		f.applied["p"] = append(f.applied["p"], supabaseapi.Migration{Version: m.Version})
	}
	f.queries, f.deployed = nil, nil
	again, err := Setup(context.Background(), api, "p", "", nil)
	if err != nil || again != hash || len(f.queries) != 0 || len(f.deployed) != 0 {
		t.Errorf("set up again: %v, %d queries, deployed %v", err, len(f.queries), f.deployed)
	}
}

func TestFunctionsLeaveTestsOut(t *testing.T) {
	files, err := functionFiles("agent")
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Path != "supabase/functions/agent/index.ts" {
		t.Errorf("entrypoint %s", files[0].Path)
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
