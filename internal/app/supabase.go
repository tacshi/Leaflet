package app

import (
	"context"
	_ "embed"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/core"
	"leaflet/internal/provision"
	"leaflet/internal/supabaseapi"
	"leaflet/locales"
)

// Signing in with Supabase: the person's own Supabase account holds the
// project Leaflet syncs through, which this finds, or creates, and sets up.

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
	stepStarting = core.StepStarting
	stepSetup    = core.StepSetup
	stepSigning  = core.StepSigning
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

// nextRun starts a sign-in's run, on the main thread.
func (a *App) nextRun() int {
	a.supaRuns++
	return a.supaRuns
}

// supaStep runs fn on the main thread while run is the sign-in under way.
func (a *App) supaStep(run int, fn func()) {
	a.update(func() {
		if a.supa.run == run {
			fn()
		}
	})
}

// signInWithSupabase opens Supabase's sign-in in the browser, then finds
// the account's Leaflet project: one is used at once; otherwise the person
// chooses on the page.
func (a *App) signInWithSupabase() {
	ctx, cancel := context.WithCancel(context.Background())
	run := a.nextRun()
	a.supa = supabaseState{run: run, step: stepBrowser, cancel: cancel}
	a.signIn.err, a.signIn.info = "", ""
	cr := a.core()
	go func() {
		wait, stop := context.WithTimeout(ctx, 5*time.Minute)
		api, err := cr.SignInWithSupabase(wait, func(u string) {
			a.supaStep(run, func() { a.supa.link = u })
		})
		stop()
		if err != nil {
			a.supabaseFailed(run, err)
			return
		}
		a.supaStep(run, func() { a.supa.api, a.supa.step = api, stepFinding })
		found, err := provision.Find(ctx, api)
		if err != nil {
			a.supabaseFailed(run, err)
			return
		}
		// One Leaflet project is used at once. A project that is not one
		// yet, though named so, may be the person's own: they choose.
		if len(found.Projects) == 1 {
			a.connectProject(ctx, cr, run, api, found.Projects[0])
			return
		}
		a.supaStep(run, func() {
			a.supa.found, a.supa.step = found, stepChoose
			if len(found.Orgs) > 0 {
				a.supa.org = found.Orgs[0].Slug
			}
		})
	}()
}

// chooseProject uses p, or creates a project in the organization chosen
// when p is nil.
func (a *App) chooseProject(p *supabaseapi.Project) {
	// One setup at a time: a second click before the projects' list goes
	// does nothing.
	if a.supa.step != stepChoose {
		return
	}
	if a.supa.cancel != nil {
		a.supa.cancel()
	}
	api, org, cr := a.supa.api, a.supa.org, a.core()
	ctx, cancel := context.WithCancel(context.Background())
	run := a.nextRun()
	a.supa.run, a.supa.cancel = run, cancel
	a.signIn.err = ""
	if p != nil {
		// The list goes at once; the setup says its steps as they come.
		a.supa.step = stepSetup
		go a.connectProject(ctx, cr, run, api, *p)
		return
	}
	a.supa.step = stepCreating
	go func() {
		created, err := provision.Create(ctx, api, org)
		if err != nil {
			a.supabaseFailed(run, err)
			return
		}
		a.connectProject(ctx, cr, run, api, created)
	}()
}

// connectProject sets p up for this version of Leaflet, once it is up,
// makes it the project, and signs in to it as its owner, through the
// app's core cr.
func (a *App) connectProject(ctx context.Context, cr *core.Core, run int, api *supabaseapi.Client, p supabaseapi.Project) {
	pending, err := cr.ConnectProject(ctx, api, p, func(step string, done, all int) {
		a.supaStep(run, func() {
			a.supa.step = step
			if step == stepSetup {
				a.supa.done, a.supa.all = done, all
			}
		})
	})
	if err != nil {
		a.supabaseFailed(run, err)
		return
	}
	// The project and session become the device's only for the run still
	// under way: one cancelled, or started again since, leaves them as
	// they are.
	a.update(func() {
		if a.supa.run != run {
			go a.backend.Discard(pending)
			return
		}
		user, err := cr.AdoptProject(pending, api)
		if err != nil {
			a.supa = supabaseState{}
			a.signIn.err = err.Error()
			return
		}
		a.supa = supabaseState{}
		a.signedIn(user)
	})
}

// supabaseFailed shows err on the sign-in page, unless the sign-in run was
// cancelled.
func (a *App) supabaseFailed(run int, err error) {
	a.supaStep(run, func() {
		step := a.supa.step
		a.supa = supabaseState{}
		if step == stepCreating || step == stepStarting || step == stepSetup {
			a.signIn.err = locales.SigninSetupFailed(err.Error())
			return
		}
		a.signIn.err = err.Error()
	})
}

// cancelSupabase stops the sign-in under way.
func (a *App) cancelSupabase() {
	if a.supa.cancel != nil {
		a.supa.cancel()
	}
	a.supa = supabaseState{}
}

// dashboardURL is the project's page in the Supabase dashboard.
func dashboardURL(ref string) string { return "https://supabase.com/dashboard/project/" + ref }
