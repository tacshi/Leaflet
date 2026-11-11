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

type fakeProject struct {
	mu            sync.Mutex
	applied       map[string][]supabaseapi.Migration
	secrets       []supabaseapi.Secret
	queries       []string
	deployed      []string
	functions     []string
	deleteDown    bool
	status        []string
	created       string
	restored      []string
	members       string
	broken        string
	refused       bool
	meanwhile     []string
	signUps       supabaseapi.SignUps
	signUpPatches int
	authRefused   bool
	authDown      bool
	realtimeWaits bool
	heard         int
	deployDown    bool
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
			if body.Query == ensureSavesHeard {
				f.heard++
				if f.realtimeWaits {
					http.Error(w, `{"message":"Failed to run sql query: ERROR:  P0001: leaflet_realtime_waits\n"}`, http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusCreated)
				return
			}
			f.queries = append(f.queries, body.Query)
			for _, v := range f.meanwhile {
				if strings.Contains(body.Query, "where version = '"+v+"') then raise exception") {
					http.Error(w, `{"message":"Failed to run sql query: ERROR:  P0001: leaflet_migration_applied\n"}`, http.StatusBadRequest)
					return
				}
			}
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(path, "/config/auth") && f.authRefused:
			http.Error(w, `{"message":"Forbidden resource"}`, http.StatusForbidden)
		case strings.HasSuffix(path, "/config/auth") && f.authDown:
			http.Error(w, `{"message":"try again"}`, http.StatusServiceUnavailable)
		case strings.HasSuffix(path, "/config/auth") && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(f.signUps)
		case strings.HasSuffix(path, "/config/auth") && r.Method == "PATCH":
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
			var set []supabaseapi.Secret
			_ = json.NewDecoder(r.Body).Decode(&set)
			for _, s := range set {
				f.secrets = append(f.secrets, supabaseapi.Secret{Name: s.Name, Value: "digest"})
			}
		case strings.HasSuffix(path, "/functions/deploy") && f.deployDown:
			http.Error(w, `{"message":"try again"}`, http.StatusServiceUnavailable)
		case strings.HasSuffix(path, "/functions/deploy"):
			f.deployed = append(f.deployed, r.URL.Query().Get("slug"))
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(path, "/functions/") && r.Method == "DELETE" && f.deleteDown:
			http.Error(w, `{"message":"try again"}`, http.StatusServiceUnavailable)
		case strings.Contains(path, "/functions/") && r.Method == "DELETE":
			slug := path[strings.LastIndex(path, "/")+1:]
			if !slices.Contains(f.functions, slug) {
				http.Error(w, `{"message":"Function not found"}`, http.StatusNotFound)
				return
			}
			f.functions = slices.DeleteFunc(f.functions, func(s string) bool { return s == slug })
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

	ProjectName = DevProjectName
	t.Cleanup(func() { ProjectName = "Leaflet" })
	if found, err = Find(context.Background(), api); err != nil {
		t.Fatal(err)
	}
	if got := refs(found.Projects) + "|" + refs(found.Unfinished); got != "dev|" {
		t.Errorf("a development build found %s", got)
	}
}

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
		// stt was deleted by hand: the two left go, and the one gone is no failure.
		functions: []string{"account", "agent", "providers", "theirs"},
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
	if strings.Join(f.functions, ",") != "account,theirs" {
		t.Errorf("functions left %v", f.functions)
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

	f.applied["p"] = nil
	for _, m := range all {
		f.applied["p"] = append(f.applied["p"], supabaseapi.Migration{Version: m.Version})
	}
	f.queries, f.deployed = nil, nil
	if err := Setup(context.Background(), api, "p", nil); err != nil || len(f.queries) != 0 || len(f.deployed) != 0 {
		t.Errorf("set up again: %v, %d queries, deployed %v", err, len(f.queries), f.deployed)
	}
}

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

var functionsHashes = map[int]string{
	1:  "6c7a63f55f1a88ba9ced2e8085b35cf1e781c73438e276ef73709cefef78836b",
	2:  "2b34649fda4f2565e5de75e6772d25454ba74c8435a6a898f925ddd792acabf1",
	3:  "dfb182b6786ef1ea387d13f63c7a613ef98ed019b9e0527a337bf395c7a72cd9",
	4:  "dd527200d9329f6f96d2b17c4dbc0e5994e8ff69893fc44969b9e8edab8159a7",
	5:  "6e3994b6bd5f678c7dc07c19a459a932a066db4ef4c173c8d63eb59916a7b508",
	6:  "4c98a96cf1986b0c7ca862f4334410990186337f4fb56cad4c0c45585b3bd173",
	7:  "bf4593b9e4801d510595dbc68c3592010b81a98151c3819b2b4bb61e22c4fc48",
	8:  "dadff27ccd1171b4f5ee5b70318bc02118c36b3a2d0ce717a3fa6ce7878a47d4",
	9:  "1d347915d7cb4ee0886e09c50bc5c9c86ba8fd68d521d1f6e973be2f38d0bb91",
	10: "7f8fcfc6d8d75569bbf529cf648d778181c35849d23fc57752555bbf274b576f",
	11: "1b056001d96569c7dcdcbcbe7e4e5f638a7d4931f3f3b4e50c4a24e030a24dee",
	12: "c05888e1f0892591a0d2da9abcdbe302dc1f3d16a32c839977555584ea701547",
	13: "672313c3b28cb0455492789efb2f01d42cf95faefa0b3e85d5015aed408fbc3c",
	14: "8cfbf2333baeb23ac21c97868eb7a54f5df666eafe17f636f5e8f9b82e957483",
	15: "5351ceba3a2ac232e9347bb15a5d18f4f7d738d66bcf9b4d3a68ac7cb8c1e550",
	16: "4a931655fbd8ad611cd32114faffdc45fa478c870a006a8660e08b8b4d65d150",
	17: "6c5ee661b43c5e2fba84dcf1e8c4ffa2210c037dbfc97ad6873e6b57f03ce0e4",
	18: "bc9982d99067af9a8616cb61ebf6c856f9671890753d8eba8ee9cff68a8da3f0",
	19: "8e0fc74e251d22b92a210ce1e93e4012985fd24ab850ca4c1810d6e0d679f8c4",
	20: "8e0fc74e251d22b92a210ce1e93e4012985fd24ab850ca4c1810d6e0d679f8c4",
}

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

func TestFunctionsLeaveTestsOut(t *testing.T) {
	files, err := functionFiles("account")
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Path != "supabase/functions/account/index.ts" {
		t.Errorf("entrypoint %s", files[0].Path)
	}
	if !slices.ContainsFunc(files, func(f supabaseapi.File) bool { return f.Path == "supabase/functions/account/handler.ts" }) {
		t.Error("the account's handler stays behind")
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

	f = &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}, refused: true}
	if err := Setup(context.Background(), f.serve(t), "p", nil); err == nil {
		t.Error("a refused migration counted as applied")
	}
}

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

func TestSetupWaitsForRealtime(t *testing.T) {
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}, realtimeWaits: true}
	api := f.serve(t)
	if err := Setup(context.Background(), api, "p", nil); !errors.Is(err, ErrRealtimeWaits) || len(f.deployed) != len(Functions) || len(f.queries) == 0 {
		t.Errorf("with no messages: %v, deployed %v, %d queries", err, f.deployed, len(f.queries))
	}
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.realtimeWaits = false
	f.queries, f.deployed, f.heard = nil, nil, 0
	f.applied["p"] = nil
	for _, m := range all {
		f.applied["p"] = append(f.applied["p"], supabaseapi.Migration{Version: m.Version})
	}
	f.mu.Unlock()
	if err := Setup(context.Background(), api, "p", nil); err != nil || f.heard != 1 || len(f.queries) != 0 {
		t.Errorf("once they are there: %v, heard %d, %d queries", err, f.heard, len(f.queries))
	}
}

func TestSetupGoesOnPastARefusedPolicy(t *testing.T) {
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}}
	api := f.serve(t)
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		f.applied["p"] = append(f.applied["p"], supabaseapi.Migration{Version: m.Version})
	}
	f.refused = true
	if err := Setup(context.Background(), api, "p", nil); !errors.Is(err, ErrRealtimeWaits) || len(f.deployed) != len(Functions) {
		t.Errorf("refused: %v, deployed %v", err, f.deployed)
	}
}

func TestSetupSaysSignUpsLeftOpen(t *testing.T) {
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}, authDown: true}
	api := f.serve(t)
	if err := Setup(context.Background(), api, "p", nil); !errors.Is(err, ErrSignUpsOpen) || len(f.deployed) != len(Functions) || len(f.queries) == 0 {
		t.Errorf("with Auth down: %v, deployed %v, %d queries", err, f.deployed, len(f.queries))
	}
	f.mu.Lock()
	f.authDown = false
	f.mu.Unlock()
	if err := Setup(context.Background(), api, "p", nil); err != nil || f.signUps != (supabaseapi.SignUps{Disabled: true}) {
		t.Errorf("again: %v, sign-ups %+v", err, f.signUps)
	}
}

func TestSetupDeploysAndLeavesTheDatabase(t *testing.T) {
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProject{applied: map[string][]supabaseapi.Migration{"p": nil}, deployDown: true}
	for _, m := range all {
		f.applied["p"] = append(f.applied["p"], supabaseapi.Migration{Version: m.Version})
	}
	api := f.serve(t)
	if err := Setup(context.Background(), api, "p", nil); err == nil || len(f.secrets) != 0 {
		t.Errorf("with deploys failing: %v, secrets %v", err, f.secrets)
	}
	f.mu.Lock()
	f.deployDown = false
	f.mu.Unlock()
	if err := Setup(context.Background(), api, "p", nil); err != nil || !slices.Equal(f.deployed, Functions) || len(f.queries) != 0 {
		t.Errorf("deployed %v: %v, queries %q", f.deployed, err, f.queries)
	}
}

func TestSetupDeletesTheFunctionsOfEarlierBuilds(t *testing.T) {
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProject{
		applied:    map[string][]supabaseapi.Migration{"p": nil},
		functions:  []string{"account", "agent", "providers", "stt"},
		deleteDown: true,
	}
	for _, m := range all {
		f.applied["p"] = append(f.applied["p"], supabaseapi.Migration{Version: m.Version})
	}
	api := f.serve(t)
	if err := Setup(context.Background(), api, "p", nil); err == nil || len(f.secrets) != 0 || len(f.functions) != 4 {
		t.Errorf("with deletes failing: %v, secrets %v, functions %v", err, f.secrets, f.functions)
	}
	f.mu.Lock()
	f.deleteDown = false
	f.mu.Unlock()
	if err := Setup(context.Background(), api, "p", nil); err != nil || !slices.Equal(f.functions, []string{"account"}) || len(f.secrets) != 1 {
		t.Errorf("functions left %v: %v, secrets %v", f.functions, err, f.secrets)
	}
	if err := Setup(context.Background(), api, "p", nil); err != nil || !slices.Equal(f.functions, []string{"account"}) {
		t.Errorf("set up again: %v, functions %v", err, f.functions)
	}
}
