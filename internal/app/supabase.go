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

//go:embed supabase.svg
var supabaseLogoSVG []byte

var supabaseLogo = sync.OnceValue(func() *ui.SVG { return ui.MustParseSVG(supabaseLogoSVG) })

func init() {
	if mygo.IsDev() {
		provision.ProjectName = provision.DevProjectName
	}
}

func (a *App) hasServer() bool { return a.backend.Configured() || !a.backend.Fixed() }

const (
	stepBrowser  = "browser"
	stepFinding  = "finding"
	stepChoose   = "choose"
	stepCreating = "creating"
	stepStarting = core.StepStarting
	stepSetup    = core.StepSetup
	stepSigning  = core.StepSigning
)

type supabaseState struct {
	run       int
	step      string
	api       *supabaseapi.Client
	found     provision.Found
	org       string
	done, all int
	cancel    context.CancelFunc
	link      string
}

func (a *App) nextRun() int {
	a.supaRuns++
	return a.supaRuns
}

func (a *App) supaStep(run int, fn func()) {
	a.update(func() {
		if a.supa.run == run {
			fn()
		}
	})
}

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

func (a *App) chooseProject(p *supabaseapi.Project) {
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

func (a *App) cancelSupabase() {
	if a.supa.cancel != nil {
		a.supa.cancel()
	}
	a.supa = supabaseState{}
}

func dashboardURL(ref string) string { return "https://supabase.com/dashboard/project/" + ref }
