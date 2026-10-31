// Package provision finds, creates and sets up the Supabase project
// Leaflet syncs through, in the person's own Supabase account.
package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"leaflet/internal/supabaseapi"
	"leaflet/supabase"
)

// FirstMigration is the version of Leaflet's first migration: a project
// that has it is a Leaflet project.
const FirstMigration = "20261025000000"

// ProjectName is the name of a project Leaflet creates; development
// builds set it to DevProjectName.
var ProjectName = "Leaflet"

// DevProjectName is the name of a development build's project, which keeps
// its pages apart from the real ones.
const DevProjectName = "Leaflet-dev"

// ownName reports whether a Leaflet project named name is this build's to
// use: a development build uses its own projects only, and other builds
// any but those.
func ownName(name string) bool {
	if ProjectName == DevProjectName {
		return IsLeafletName(name)
	}
	_, dev := strings.CutPrefix(name, DevProjectName)
	return !dev
}

// Functions are the Edge Functions Leaflet deploys.
var Functions = []string{"agent", "providers", "stt", "account"}

// hashSecret begins the name of the project secret that says which
// functions are deployed: the rest of the name is FunctionsHash's start.
// The name, not the value, since the API lists values only as digests.
const hashSecret = "LEAFLET_FUNCTIONS_"

// hashName is the secret's name for the functions hash.
func hashName(hash string) string { return hashSecret + strings.ToUpper(hash[:16]) }

// uploadLimit is the largest upload the buckets take (a long recording);
// a plan with a lower cap keeps its own.
const uploadLimit = 500 << 20

// Migration is one of Leaflet's migrations.
type Migration struct {
	Version, Name, SQL string
}

var migrationRe = regexp.MustCompile(`^(\d{14})_([a-z0-9_]+)\.sql$`)

// Migrations are Leaflet's migrations, oldest first.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(supabase.Backend, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		m := migrationRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration %s is not named version_name.sql", e.Name())
		}
		sql, err := fs.ReadFile(supabase.Backend, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: m[1], Name: m[2], SQL: string(sql)})
	}
	slices.SortFunc(out, func(a, b Migration) int { return strings.Compare(a.Version, b.Version) })
	return out, nil
}

// functionFiles are each function's sources, at their paths from the
// project's root: its index.ts and the shared modules, without tests.
func functionFiles(slug string) ([]supabaseapi.File, error) {
	var out []supabaseapi.File
	add := func(name string) error {
		data, err := fs.ReadFile(supabase.Backend, name)
		if err != nil {
			return err
		}
		out = append(out, supabaseapi.File{Path: "supabase/" + name, Data: data})
		return nil
	}
	if err := add(path.Join("functions", slug, "index.ts")); err != nil {
		return nil, err
	}
	shared, err := fs.Glob(supabase.Backend, "functions/_shared/*.ts")
	if err != nil {
		return nil, err
	}
	for _, name := range shared {
		if strings.HasSuffix(name, "_test.ts") {
			continue
		}
		if err := add(name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FunctionsHash names the functions' sources: it changes when any of them
// does.
func FunctionsHash() (string, error) {
	h := sha256.New()
	for _, slug := range Functions {
		files, err := functionFiles(slug)
		if err != nil {
			return "", err
		}
		for _, f := range files {
			_, _ = fmt.Fprintf(h, "%s\x00%d\x00", f.Path, len(f.Data))
			_, _ = h.Write(f.Data)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// IsLeaflet reports whether a project's applied migrations are Leaflet's.
func IsLeaflet(applied []supabaseapi.Migration) bool {
	return slices.ContainsFunc(applied, func(m supabaseapi.Migration) bool { return m.Version == FirstMigration })
}

// Found is what an account has for Leaflet.
type Found struct {
	// Projects are its Leaflet projects that are up and this build's to
	// use (ownName), oldest first.
	Projects []supabaseapi.Project
	// Unfinished are the account's projects named Leaflet with no
	// migration history, or that can't say what they hold, being paused
	// or starting: ones Leaflet made and did not finish, or the person's
	// own, so they are set up only when the person chooses one.
	Unfinished []supabaseapi.Project
	Orgs       []supabaseapi.Organization
}

// IsLeafletName reports whether name is one Create gives: Leaflet, or
// Leaflet 2 and on.
func IsLeafletName(name string) bool {
	rest, ok := strings.CutPrefix(name, ProjectName)
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	n, err := strconv.Atoi(strings.TrimPrefix(rest, " "))
	return strings.HasPrefix(rest, " ") && err == nil && n >= 2
}

// Find looks for the account's Leaflet projects.
func Find(ctx context.Context, api *supabaseapi.Client) (Found, error) {
	var f Found
	var err error
	if f.Orgs, err = api.Organizations(ctx); err != nil {
		return f, err
	}
	projects, err := api.Projects(ctx)
	if err != nil {
		return f, err
	}
	projects = slices.DeleteFunc(projects, func(p supabaseapi.Project) bool {
		switch p.Status {
		case "REMOVED", "GOING_DOWN", "INIT_FAILED", "RESTORE_FAILED":
			return true
		}
		return false
	})
	// Each project's migrations at once: an account may have many.
	applied := make([][]supabaseapi.Migration, len(projects))
	failed := make([]bool, len(projects))
	var wg sync.WaitGroup
	for i, p := range projects {
		if !p.Healthy() {
			continue
		}
		wg.Go(func() {
			var err error
			applied[i], err = api.Migrations(ctx, p.Ref)
			failed[i] = err != nil
		})
	}
	wg.Wait()
	for i, p := range projects {
		switch {
		case !p.Healthy():
			// A paused or starting project can't say what it holds.
			if IsLeafletName(p.Name) {
				f.Unfinished = append(f.Unfinished, p)
			}
		case failed[i]:
		case IsLeaflet(applied[i]) && ownName(p.Name):
			f.Projects = append(f.Projects, p)
		case len(applied[i]) == 0 && IsLeafletName(p.Name):
			f.Unfinished = append(f.Unfinished, p)
		}
	}
	byAge := func(a, b supabaseapi.Project) int { return strings.Compare(a.CreatedAt, b.CreatedAt) }
	slices.SortFunc(f.Projects, byAge)
	slices.SortFunc(f.Unfinished, byAge)
	return f, nil
}

// RegionGroup is the region group nearest this device, by its time zone.
func RegionGroup(now time.Time) string {
	_, offset := now.Zone()
	switch h := offset / 3600; {
	case h <= -2:
		return "americas"
	case h <= 4:
		return "emea"
	default:
		return "apac"
	}
}

// pollEvery is how often Ready asks whether a project is up.
var pollEvery = 5 * time.Second

// Create creates a Leaflet project in the organization org, named Leaflet,
// or Leaflet 2 and on when the organization has one, and waits until it is
// up, which takes a minute or two.
func Create(ctx context.Context, api *supabaseapi.Client, org string) (supabaseapi.Project, error) {
	projects, err := api.Projects(ctx)
	if err != nil {
		return supabaseapi.Project{}, err
	}
	taken := map[string]bool{}
	for _, p := range projects {
		if p.OrganizationSlug == org {
			taken[p.Name] = true
		}
	}
	name := ProjectName
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s %d", ProjectName, n)
	}
	p, err := api.CreateProject(ctx, name, org, RegionGroup(time.Now()))
	if err != nil {
		return p, err
	}
	return Ready(ctx, api, p)
}

// Ready waits until p is up, restoring it first when it is paused.
func Ready(ctx context.Context, api *supabaseapi.Client, p supabaseapi.Project) (supabaseapi.Project, error) {
	if p.Status == "INACTIVE" {
		if err := api.Restore(ctx, p.Ref); err != nil {
			return p, err
		}
	}
	var err error
	for !p.Healthy() {
		switch p.Status {
		case "INIT_FAILED", "REMOVED", "RESTORE_FAILED":
			return p, errors.New("Supabase couldn't start the project. Try again, or check it in the Supabase dashboard.") //nolint:staticcheck // shown to people as a sentence.
		}
		select {
		case <-ctx.Done():
			return p, ctx.Err()
		case <-time.After(pollEvery):
		}
		if p, err = api.Project(ctx, p.Ref); err != nil {
			return p, err
		}
	}
	return p, nil
}

// Setup brings a project up to this version of Leaflet: it applies the
// migrations it lacks and deploys the functions unless deployed, the hash
// this device deployed last, is current. progress reports steps done of
// the steps there are. It returns the functions' hash, to keep as
// deployed.
func Setup(ctx context.Context, api *supabaseapi.Client, ref, deployed string, progress func(done, total int)) (string, error) {
	all, err := Migrations()
	if err != nil {
		return "", err
	}
	applied, err := api.Migrations(ctx, ref)
	if err != nil {
		return "", err
	}
	var missing []Migration
	for _, m := range all {
		if !slices.ContainsFunc(applied, func(a supabaseapi.Migration) bool { return a.Version == m.Version }) {
			missing = append(missing, m)
		}
	}
	hash, err := FunctionsHash()
	if err != nil {
		return "", err
	}
	deploy := hash != deployed
	if deploy {
		secrets, err := api.Secrets(ctx, ref)
		if err != nil {
			return "", err
		}
		deploy = !hasHash(secrets, hash)
	}
	total, done := len(missing), 0
	if deploy {
		total += len(Functions)
	}
	step := func() {
		done++
		if progress != nil {
			progress(done, total)
		}
	}
	for _, m := range missing {
		if err := api.Query(ctx, ref, migrationSQL(m)); err != nil {
			return "", fmt.Errorf("Leaflet couldn't set up the database (%s): %w", m.Name, err) //nolint:staticcheck // shown to people as a sentence.
		}
		step()
	}
	if deploy {
		for _, slug := range Functions {
			files, err := functionFiles(slug)
			if err != nil {
				return "", err
			}
			if err := api.DeployFunction(ctx, ref, slug, "supabase/functions/"+slug+"/index.ts", true, files); err != nil {
				return "", fmt.Errorf("Leaflet couldn't deploy its %s function: %w", slug, err) //nolint:staticcheck // shown to people as a sentence.
			}
			step()
		}
		var old []string
		if secrets, err := api.Secrets(ctx, ref); err == nil {
			for _, s := range secrets {
				if strings.HasPrefix(s.Name, hashSecret) && s.Name != hashName(hash) {
					old = append(old, s.Name)
				}
			}
		}
		if err := api.SetSecrets(ctx, ref, []supabaseapi.Secret{{Name: hashName(hash), Value: hash}}); err != nil {
			return "", err
		}
		if len(old) > 0 {
			_ = api.DeleteSecrets(ctx, ref, old)
		}
		// Best effort: a plan with a lower cap refuses it, and long
		// recordings then transcribe on the device.
		_ = api.SetUploadLimit(ctx, ref, uploadLimit)
	}
	return hash, nil
}

// hasHash reports whether the project's functions are hash.
func hasHash(secrets []supabaseapi.Secret, hash string) bool {
	return slices.ContainsFunc(secrets, func(s supabaseapi.Secret) bool { return s.Name == hashName(hash) })
}

// migrationSQL applies m and records it in the migration history the
// Supabase CLI keeps, so `supabase db push` agrees. The statements run as
// one transaction.
func migrationSQL(m Migration) string {
	// Version and name are digits and [a-z0-9_] (migrationRe): safe to
	// quote as is.
	return m.SQL + "\n;\n" +
		"create schema if not exists supabase_migrations;\n" +
		"create table if not exists supabase_migrations.schema_migrations (version text not null primary key, statements text[], name text);\n" +
		fmt.Sprintf("insert into supabase_migrations.schema_migrations (version, name) values ('%s', '%s');\n", m.Version, m.Name)
}

// Owner returns the email and name of the person signed in, by the
// members of the project's organization: the API tells no account its own
// email, so only an organization of one says who it is.
func Owner(ctx context.Context, api *supabaseapi.Client, org string) (email, name string, err error) {
	members, err := api.Members(ctx, org)
	if err != nil {
		return "", "", err
	}
	members = slices.DeleteFunc(members, func(m supabaseapi.Member) bool { return m.Email == "" })
	if len(members) != 1 {
		return "", "", errors.New("Leaflet couldn't tell which member of the organization you are. Use a project in an organization of your own.") //nolint:staticcheck // shown to people as a sentence.
	}
	return members[0].Email, members[0].Name, nil
}

// Keys returns a project's publishable and secret keys, or its legacy anon
// and service_role keys when it has no new ones.
func Keys(ctx context.Context, api *supabaseapi.Client, ref string) (publishable, secret string, err error) {
	keys, err := api.APIKeys(ctx, ref)
	if err != nil {
		return "", "", err
	}
	var anon, service string
	for _, k := range keys {
		switch {
		case k.Type == "publishable" && publishable == "":
			publishable = k.APIKey
		case k.Type == "secret" && secret == "":
			secret = k.APIKey
		case k.Name == "anon":
			anon = k.APIKey
		case k.Name == "service_role":
			service = k.APIKey
		}
	}
	if publishable == "" || secret == "" {
		publishable, secret = anon, service
	}
	if publishable == "" || secret == "" {
		return "", "", errors.New("Supabase didn't give Leaflet the project's API keys.") //nolint:staticcheck // shown to people as a sentence.
	}
	return publishable, secret, nil
}
