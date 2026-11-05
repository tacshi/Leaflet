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
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"leaflet/internal/guard"
	"leaflet/internal/supabaseapi"
	"leaflet/locales"
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

// FunctionsVersion orders the functions builds deploy: it goes up by one
// with each change to them, as TestFunctionsVersion holds it to, so a build
// deploys over older functions only, never over newer ones.
const FunctionsVersion = 12

// hashSecret begins the name of the project secret that says which
// functions are deployed: the rest of the name is their FunctionsVersion
// and FunctionsHash's start. The name, not the value, since the API lists
// values only as digests.
const hashSecret = "LEAFLET_FUNCTIONS_"

// hashName is the secret's name for the functions hash at version.
func hashName(version int, hash string) string {
	return fmt.Sprintf("%sV%d_%s", hashSecret, version, strings.ToUpper(hash[:16]))
}

var hashNameRe = regexp.MustCompile(`^` + hashSecret + `(?:V(\d+)_)?[0-9A-F]{16}$`)

// deployedVersion is the FunctionsVersion of the functions a project has,
// by its secrets, and whether one says: 0 for one from before the secret
// named it, which is older than any that does.
func deployedVersion(secrets []supabaseapi.Secret) (version int, found bool) {
	for _, s := range secrets {
		m := hashNameRe.FindStringSubmatch(s.Name)
		if m == nil {
			continue
		}
		found = true
		if n, err := strconv.Atoi(m[1]); err == nil && n > version {
			version = n
		}
	}
	return version, found
}

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
// project's root: its index.ts first, the rest of its folder, and the
// shared modules, without tests.
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
	entry := path.Join("functions", slug, "index.ts")
	if err := add(entry); err != nil {
		return nil, err
	}
	own, err := fs.Glob(supabase.Backend, path.Join("functions", slug, "*.ts"))
	if err != nil {
		return nil, err
	}
	shared, err := fs.Glob(supabase.Backend, "functions/_shared/*.ts")
	if err != nil {
		return nil, err
	}
	for _, name := range append(own, shared...) {
		if name == entry || strings.HasSuffix(name, "_test.ts") {
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

// Signature names what Setup brings a project to, this build's: its
// migrations, and its functions' FunctionsVersion and FunctionsHash. A
// project set up by a build of the same signature has nothing to gain
// from another Setup by this one.
var Signature = sync.OnceValues(func() (string, error) {
	all, err := Migrations()
	if err != nil {
		return "", err
	}
	hash, err := FunctionsHash()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, m := range all {
		_, _ = fmt.Fprintf(h, "%s\x00", m.Version)
	}
	_, _ = fmt.Fprintf(h, "%d\x00%s", FunctionsVersion, hash)
	return hex.EncodeToString(h.Sum(nil)), nil
})

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
	failed := make([]error, len(projects))
	var wg sync.WaitGroup
	for i, p := range projects {
		if !p.Healthy() {
			continue
		}
		wg.Go(func() {
			defer guard.RecoverAs(&failed[i])
			applied[i], failed[i] = api.Migrations(ctx, p.Ref)
		})
	}
	wg.Wait()
	for i, p := range projects {
		switch {
		case failed[i] != nil && IsLeafletName(p.Name) && ownName(p.Name):
			// Named as Leaflet names its own, it may be the project:
			// leaving it out would offer to make another.
			return f, failed[i]
		case failed[i] != nil:
			// Another of the account's, as one in an organization that
			// keeps it out.
		case !p.Healthy():
			// A paused or starting project can't say what it holds.
			if IsLeafletName(p.Name) {
				f.Unfinished = append(f.Unfinished, p)
			}
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

// Ready waits until p is up, restoring it when it is paused, or once it
// is when it was pausing.
func Ready(ctx context.Context, api *supabaseapi.Client, p supabaseapi.Project) (supabaseapi.Project, error) {
	var err error
	restored := false
	for !p.Healthy() {
		switch p.Status {
		case "INIT_FAILED", "REMOVED", "RESTORE_FAILED":
			return p, errors.New(locales.ProvisionProjectFailed())
		case "INACTIVE":
			if !restored {
				if err := api.Restore(ctx, p.Ref); err != nil {
					return p, err
				}
				restored = true
			}
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
// migrations it lacks, closes its sign-ups, and deploys the functions
// when the project's are older, by FunctionsVersion. Ones of the same
// version stay, whatever their hash, so two builds never take turns
// deploying; only a development build deploys its own over them, as they
// change while it is worked on. A migration another device applied
// meanwhile, setting the project up at the same time, counts as done.
// Sign-ups it could not close for now leave it ErrSignUpsOpen, and a
// Realtime that has not made its messages yet ErrRealtimeWaits, once the
// rest is done: it runs again until neither does. progress reports steps
// done of the steps there are.
func Setup(ctx context.Context, api *supabaseapi.Client, ref string, progress func(done, total int)) error {
	all, err := Migrations()
	if err != nil {
		return err
	}
	applied, err := api.Migrations(ctx, ref)
	if err != nil {
		return err
	}
	var missing []Migration
	for _, m := range all {
		if !slices.ContainsFunc(applied, func(a supabaseapi.Migration) bool { return a.Version == m.Version }) {
			missing = append(missing, m)
		}
	}
	hash, err := FunctionsHash()
	if err != nil {
		return err
	}
	secrets, err := api.Secrets(ctx, ref)
	if err != nil {
		return err
	}
	name := hashName(FunctionsVersion, hash)
	var deploy bool
	switch theirs, found := deployedVersion(secrets); {
	case !found || theirs < FunctionsVersion:
		deploy = true
	case theirs == FunctionsVersion:
		deploy = ProjectName == DevProjectName && !slices.ContainsFunc(secrets, func(s supabaseapi.Secret) bool { return s.Name == name })
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
		if err := api.Query(ctx, ref, migrationSQL(m)); err != nil && !appliedMeanwhile(err) {
			return &failed{locales.ProvisionMigrationFailed(m.Name, err.Error()), err}
		}
		step()
	}
	// Sign-ups that could not be closed for now, and a Realtime not ready,
	// leave the rest to do, and Setup says so at the end, to be run again.
	open := errors.Join(closeSignUps(ctx, api, ref), hearSaves(ctx, api, ref))
	if !deploy {
		return open
	}
	for _, slug := range Functions {
		files, err := functionFiles(slug)
		if err != nil {
			return err
		}
		if err := api.DeployFunction(ctx, ref, slug, "supabase/functions/"+slug+"/index.ts", true, files); err != nil {
			return &failed{locales.ProvisionDeployFailed(slug, err.Error()), err}
		}
		step()
	}
	var old []string
	for _, s := range secrets {
		if strings.HasPrefix(s.Name, hashSecret) && s.Name != name {
			old = append(old, s.Name)
		}
	}
	if err := api.SetSecrets(ctx, ref, []supabaseapi.Secret{{Name: name, Value: hash}}); err != nil {
		return err
	}
	if len(old) > 0 {
		_ = api.DeleteSecrets(ctx, ref, old)
	}
	// Best effort: a plan with a lower cap refuses it, and long
	// recordings then transcribe on the device.
	_ = api.SetUploadLimit(ctx, ref, uploadLimit)
	return open
}

// ErrSignUpsOpen is Setup's when it did all else, but the project's
// sign-ups could not be closed for now, as with the network failing as
// it asked: the project works, and Setup is to run again to close them.
var ErrSignUpsOpen = errors.New("provision: the project's sign-ups could not be closed yet")

// closeSignUps keeps a project's Auth to the owner's account, which
// Leaflet makes with the secret key (remote.SignInAsOwner): no one else
// may make an account, and an address counts only once confirmed, as
// shares and invites go to whoever confirms it. The settings are read
// first and changed only when they differ. A refusal that asking again
// would not change leaves them, and is no error: Auth settings need the
// OAuth app's Auth scope, which a Supabase sign-in from before the app
// had it lacks, and the project then keeps what Supabase made it with,
// sign-ups open and addresses confirmed. Any other failure, as the
// network's, is ErrSignUpsOpen.
func closeSignUps(ctx context.Context, api *supabaseapi.Client, ref string) error {
	closed := supabaseapi.SignUps{Disabled: true}
	s, err := api.SignUps(ctx, ref)
	if err == nil && s != closed {
		err = api.SetSignUps(ctx, ref, closed)
	}
	if err == nil || refusedForGood(err) {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrSignUpsOpen, err)
}

// ErrRealtimeWaits is Setup's when it did all else, but the project's
// Realtime has not made realtime.messages yet, as it may only once a
// device first connects: the policy that lets devices hear a page's
// saves goes on that table, and Setup is to run again to make it. Until
// then the project works, and its devices check their open pages.
var ErrRealtimeWaits = errors.New("provision: Realtime has not made its messages yet")

// realtimeMark is what ensureSavesHeard raises while there is no table.
const realtimeMark = "leaflet_realtime_waits"

// ensureSavesHeard has the project make the policy, which it can only once
// the table is there, and raises until it is.
const ensureSavesHeard = "do $leaflet$ begin if not public.ensure_page_saves_heard() then raise exception '" + realtimeMark + "'; end if; end $leaflet$;"

// hearSaves makes the Realtime policy for a page's saves, when it can, and
// says why not, as ErrRealtimeWaits, when it can't.
func hearSaves(ctx context.Context, api *supabaseapi.Client, ref string) error {
	err := api.Query(ctx, ref, ensureSavesHeard)
	if e, ok := errors.AsType[*supabaseapi.Error](err); ok && strings.Contains(e.Message, realtimeMark) {
		return ErrRealtimeWaits
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRealtimeWaits, err)
	}
	return nil
}

// refusedForGood reports whether the API refused a request as it would
// again, whenever it is asked: a 4xx but a timeout or a wait asked for.
func refusedForGood(err error) bool {
	e, ok := errors.AsType[*supabaseapi.Error](err)
	return ok && e.Status >= 400 && e.Status < 500 && e.Status != http.StatusRequestTimeout && e.Status != http.StatusTooManyRequests
}

// appliedMark is what migrationSQL raises when the migration was applied
// while it waited for the lock.
const appliedMark = "leaflet_migration_applied"

// migrationSQL applies m and records it in the migration history the
// Supabase CLI keeps, so `supabase db push` agrees. The statements run as
// one transaction. Two devices may set a project up at once, as a Mac
// and an iPhone signing in together: each migration first takes a lock
// the other's waits for, and one recorded meanwhile raises appliedMark,
// changing nothing, rather than being applied twice.
func migrationSQL(m Migration) string {
	// Version and name are digits and [a-z0-9_] (migrationRe): safe to
	// quote as is.
	return "select pg_advisory_xact_lock(hashtext('supabase_migrations'));\n" +
		"create schema if not exists supabase_migrations;\n" +
		"create table if not exists supabase_migrations.schema_migrations (version text not null primary key, statements text[], name text);\n" +
		fmt.Sprintf("do $leaflet$ begin if exists (select 1 from supabase_migrations.schema_migrations where version = '%s') then raise exception '%s'; end if; end $leaflet$;\n", m.Version, appliedMark) +
		m.SQL + "\n;\n" +
		fmt.Sprintf("insert into supabase_migrations.schema_migrations (version, name) values ('%s', '%s');\n", m.Version, m.Name)
}

// appliedMeanwhile reports whether a migration failed because another
// device applied it while this one waited (appliedMark).
func appliedMeanwhile(err error) bool {
	var e *supabaseapi.Error
	return errors.As(err, &e) && strings.Contains(e.Message, appliedMark)
}

// failed is an error said as a sentence that holds err, in the app's
// language, and still err to errors.Is and errors.As.
type failed struct {
	msg string
	err error
}

func (f *failed) Error() string { return f.msg }

func (f *failed) Unwrap() error { return f.err }

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
		return "", "", errors.New(locales.ProvisionWhichMember())
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
		return "", "", errors.New(locales.ProvisionNoKeys())
	}
	return publishable, secret, nil
}
