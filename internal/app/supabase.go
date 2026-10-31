package app

import (
	"context"
	_ "embed"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/keychain"
	"leaflet/internal/provision"
	"leaflet/internal/remote"
	"leaflet/internal/supabaseapi"
	"leaflet/locales"
)

// Signing in with Supabase: the person's own Supabase account holds the
// project Leaflet syncs through, which this finds, or creates, and sets up.

// managementKey keeps the Supabase account's sign-in, so a newer Leaflet
// can bring the project up to date.
const managementKey = "supabase.management"

//go:embed supabase.svg
var supabaseLogoSVG []byte

// supabaseLogo is Supabase's mark, over the sign-in page.
var supabaseLogo = sync.OnceValue(func() *ui.SVG { return ui.MustParseSVG(supabaseLogoSVG) })

func init() {
	// A development build's project is apart from the real one.
	if mygo.IsDev() {
		provision.ProjectName = provision.DevProjectName
	}
}

// deployedKey keeps the functions' hash deployed last to a project.
func deployedKey(ref string) string { return "supabase.functions." + ref }

// hasServer reports whether Leaflet can sync through a server here: the
// project chosen, or one to choose by signing in with Supabase, unless the
// environment fixes a server it gives no key for.
func (a *App) hasServer() bool { return a.backend.Configured() || !a.backend.Fixed() }

// Steps of signing in with Supabase, shown on the sign-in page.
const (
	stepBrowser  = "browser"
	stepFinding  = "finding"
	stepChoose   = "choose"
	stepCreating = "creating"
	stepStarting = "starting"
	stepSetup    = "setup"
	stepSigning  = "signing"
)

// supabaseState is a sign-in with Supabase under way.
type supabaseState struct {
	// run tells this sign-in from one cancelled or started again, whose
	// steps still to come are ignored.
	run       int
	step      string
	api       *supabaseapi.Client
	found     provision.Found
	org       string
	done, all int
	cancel    context.CancelFunc
	// link is Supabase's sign-in page, to open again: a new account makes
	// its first organization there, and Supabase leaves the sign-in for
	// that organization's dashboard.
	link string
}

var (
	supa     supabaseState
	supaRuns int
)

// nextRun starts a sign-in's run, on the main thread.
func nextRun() int {
	supaRuns++
	return supaRuns
}

// supaStep runs fn on the main thread while run is the sign-in under way.
func (a *App) supaStep(run int, fn func()) {
	a.update(func() {
		if supa.run == run {
			fn()
		}
	})
}

// keepManagement keeps the Supabase account's sign-in as it rotates.
func keepManagement(refresh string) { _ = keychain.Set(managementKey, []byte(refresh)) }

// signInWithSupabase opens Supabase's sign-in in the browser, then finds
// the account's Leaflet project: one is used at once; otherwise the person
// chooses on the page.
func (a *App) signInWithSupabase() {
	ctx, cancel := context.WithCancel(context.Background())
	run := nextRun()
	supa = supabaseState{run: run, step: stepBrowser, cancel: cancel}
	signIn.err, signIn.info = "", ""
	go func() {
		wait, stop := context.WithTimeout(ctx, 5*time.Minute)
		api, err := supabaseapi.SignIn(wait, func(u string) {
			a.supaStep(run, func() { supa.link = u })
			_ = mygo.Shell.OpenExternal(u)
		})
		stop()
		if err != nil {
			a.supabaseFailed(run, err)
			return
		}
		api.OnRefresh = keepManagement
		keepManagement(api.RefreshToken())
		a.supaStep(run, func() { supa.api, supa.step = api, stepFinding })
		found, err := provision.Find(ctx, api)
		if err != nil {
			a.supabaseFailed(run, err)
			return
		}
		// One Leaflet project is used at once. A project that is not one
		// yet, though named so, may be the person's own: they choose.
		if len(found.Projects) == 1 {
			a.connectProject(ctx, run, api, found.Projects[0])
			return
		}
		a.supaStep(run, func() {
			supa.found, supa.step = found, stepChoose
			if len(found.Orgs) > 0 {
				supa.org = found.Orgs[0].Slug
			}
		})
	}()
}

// chooseProject uses p, or creates a project in the organization chosen
// when p is nil.
func (a *App) chooseProject(p *supabaseapi.Project) {
	api, org := supa.api, supa.org
	ctx, cancel := context.WithCancel(context.Background())
	run := nextRun()
	supa.run, supa.cancel = run, cancel
	signIn.err = ""
	if p != nil {
		go a.connectProject(ctx, run, api, *p)
		return
	}
	supa.step = stepCreating
	go func() {
		created, err := provision.Create(ctx, api, org)
		if err != nil {
			a.supabaseFailed(run, err)
			return
		}
		a.connectProject(ctx, run, api, created)
	}()
}

// connectProject sets p up for this version of Leaflet, once it is up,
// makes it the project, and signs in to it as its owner.
func (a *App) connectProject(ctx context.Context, run int, api *supabaseapi.Client, p supabaseapi.Project) {
	if !p.Healthy() {
		a.supaStep(run, func() { supa.step = stepStarting })
		var err error
		if p, err = provision.Ready(ctx, api, p); err != nil {
			a.supabaseFailed(run, err)
			return
		}
	}
	a.supaStep(run, func() { supa.step, supa.done, supa.all = stepSetup, 0, 0 })
	deployed, _ := a.st.Value(deployedKey(p.Ref))
	hash, err := provision.Setup(ctx, api, p.Ref, deployed, func(done, all int) {
		a.supaStep(run, func() { supa.done, supa.all = done, all })
	})
	if err != nil {
		a.supabaseFailed(run, err)
		return
	}
	_ = a.st.SetValue(deployedKey(p.Ref), hash)
	publishable, secret, err := provision.Keys(ctx, api, p.Ref)
	if err != nil {
		a.supabaseFailed(run, err)
		return
	}
	a.supaStep(run, func() { supa.step = stepSigning })
	project := remote.Settings{URL: p.URL(), AnonKey: publishable, Ref: p.Ref, Name: p.Name}
	pending, err := a.signInAsOwner(ctx, api, p, project, secret)
	if err != nil {
		a.supabaseFailed(run, err)
		return
	}
	// The project and session become the device's only for the run still
	// under way: one cancelled, or started again since, leaves them as
	// they are.
	a.update(func() {
		if supa.run != run {
			go a.backend.Discard(pending)
			return
		}
		user, err := a.backend.Adopt(pending)
		if err != nil {
			supa = supabaseState{}
			signIn.err = err.Error()
			return
		}
		supa = supabaseState{}
		a.signedIn(user)
	})
}

// signInAsOwner signs in to p as the person signed in to Supabase, who
// owns it: the one member of its organization. A project whose owner is
// someone else, as one in an organization they were added to, is refused:
// being able to manage it does not make them its owner in Leaflet.
func (a *App) signInAsOwner(ctx context.Context, api *supabaseapi.Client, p supabaseapi.Project, project remote.Settings, secret string) (*remote.Pending, error) {
	email, name, err := provision.Owner(ctx, api, p.OrganizationSlug)
	if err != nil {
		return nil, err
	}
	owner, err := a.backend.ProjectOwner(ctx, project, secret)
	if err != nil {
		return nil, err
	}
	if owner != "" && !strings.EqualFold(owner, email) {
		return nil, errors.New(locales.SigninNotYourProject(p.Name, owner))
	}
	return a.backend.SignInAsOwner(ctx, project, secret, email, name)
}

// supabaseFailed shows err on the sign-in page, unless the sign-in run was
// cancelled.
func (a *App) supabaseFailed(run int, err error) {
	a.supaStep(run, func() {
		step := supa.step
		supa = supabaseState{}
		if step == stepCreating || step == stepStarting || step == stepSetup {
			signIn.err = locales.SigninSetupFailed(err.Error())
			return
		}
		signIn.err = err.Error()
	})
}

// cancelSupabase stops the sign-in under way.
func (a *App) cancelSupabase() {
	if supa.cancel != nil {
		supa.cancel()
	}
	supa = supabaseState{}
}

// forgetProject forgets the project chosen and the Supabase account's
// sign-in, on signing out: signing in again finds the project anew.
func (a *App) forgetProject() {
	if a.backend.Fixed() {
		return
	}
	_ = keychain.Delete(managementKey)
	_ = a.backend.Use(remote.Settings{})
}

// updateProject brings the project up to this version of Leaflet, when a
// newer one adds migrations or changes the functions, with the Supabase
// account's sign-in kept from signing in.
func (a *App) updateProject() {
	ref := a.backend.Project().Ref
	if ref == "" || !a.backend.SignedIn() {
		return
	}
	raw, err := keychain.Get(managementKey)
	if err != nil || len(raw) == 0 {
		return
	}
	api := supabaseapi.Resume(string(raw))
	api.OnRefresh = keepManagement
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	deployed, _ := a.st.Value(deployedKey(ref))
	hash, err := provision.Setup(ctx, api, ref, deployed, nil)
	if err != nil {
		if errors.Is(err, supabaseapi.ErrSignedOut) {
			_ = keychain.Delete(managementKey)
		}
		return
	}
	_ = a.st.SetValue(deployedKey(ref), hash)
}

// dashboardURL is the project's page in the Supabase dashboard.
func dashboardURL(ref string) string { return "https://supabase.com/dashboard/project/" + ref }
