package app

import (
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

func remoteQuery(order string) remote.Query { return remote.Query{Order: order} }

type signInState struct {
	// email, code and sent are the email-code form of a server the build
	// or environment fixes, such as the local stack.
	email string
	code  string
	sent  bool
	busy  bool
	err   string
	info  string
	// asking is the account signed in as while signInAlert asks about
	// the previous account's changes that signing in removes, lost.
	asking *store.User
	lost   int
}

var signIn signInState

func (a *App) signInView(c *ui.Context) {
	pal := theme.Of(c)
	s := &signIn
	a.titleBar(c, func() { ui.Spacer(c) })
	a.signInAlert(c)
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(64, 24).Children(func() {
			ui.Column(c).Width(380).Gap(16).Children(func() {
				ui.Column(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Image(c, supabaseLogo()).Size(42, 44)
					ui.Text(c, locales.SigninTitle()).FontSize(22).FontWeight(700).TextColor(pal.Text).Margin(10, 0, 0)
					ui.Text(c, locales.SigninSubtitle()).FontSize(13.5).TextColor(pal.TextMuted).TextAlign(ui.Center)
				})
				if !a.hasServer() {
					notice(c, "info", "info", locales.SigninNoServer(), nil)
					return
				}
				if a.backend.Fixed() {
					a.emailCodeForm(c)
				} else {
					a.supabaseSignIn(c)
				}
				if s.err != "" {
					notice(c, "danger", "circle-alert", s.err, nil)
				}
				if s.info != "" {
					notice(c, "ok", "check", s.info, nil)
				}
				ui.Divider(c)
				if ghost(c, "local", "laptop", locales.SigninContinueLocal()).Clicked() {
					a.cancelSupabase()
					a.back()
					if a.route.Kind == "signin" {
						a.show(Route{Kind: "home"})
					}
				}
			})
		})
	})
}

func (a *App) runAuth(fn func() (*store.User, error)) {
	s := &signIn
	s.busy, s.err, s.info = true, "", ""
	go func() {
		user, err := fn()
		a.update(func() {
			s.busy = false
			if err != nil {
				s.err = err.Error()
				return
			}
			a.signedIn(user)
		})
	}()
}

// supabaseSignIn is signing in with Supabase: one button, then the steps
// of finding, creating and setting up the project.
func (a *App) supabaseSignIn(c *ui.Context) {
	pal := theme.Of(c)
	// A step under way can be cancelled, to start again: the browser's
	// wait, the one most often left, holds the sign-in's port till then.
	busy := func(text string) {
		ui.Row(c.Key("busy")).Gap(10).AlignItems(ui.Center).Justify(ui.Center).Padding(8, 0).Children(func() {
			ui.Spinner(c).Size(16, 16)
			ui.Text(c, text).FontSize(13.5).TextColor(pal.TextMuted)
		})
		ui.Row(c.Key("cancel-row")).Justify(ui.Center).Children(func() {
			if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
				a.cancelSupabase()
			}
		})
	}
	switch supa.step {
	case "":
		// One way of syncing at a time: iCloud's, while it is on.
		if tip := a.supabaseTip(); tip != "" {
			ui.Column(c.Key("supabase-tip")).FillWidth().Tooltip(tip).Children(func() {
				primary(c, "supabase", "", locales.SigninContinueSupabase()).FillWidth().Disabled(true)
			})
		} else if primary(c, "supabase", "", locales.SigninContinueSupabase()).FillWidth().Clicked() {
			a.signInWithSupabase()
		}
		ui.Row(c).Gap(6).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.SigninNoSupabase()).FontSize(13).TextColor(pal.TextMuted)
			ui.Link(c, locales.SigninCreateSupabase(), supabaseSignUp).FontSize(13)
		})
	case stepBrowser:
		busy(locales.SigninWaitingBrowser())
		if supa.link != "" {
			ui.Row(c).Gap(6).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.SigninNewOrg()).FontSize(13).TextColor(pal.TextMuted)
				ui.Link(c, locales.SigninReopenBrowser(), supa.link).FontSize(13)
			})
		}
	case stepFinding:
		busy(locales.SigninFindingProject())
	case stepChoose:
		a.projectChoice(c)
	case stepCreating:
		busy(locales.SigninCreatingProject())
	case stepStarting:
		busy(locales.SigninStartingProject())
	case stepSetup:
		if supa.all > 0 {
			busy(locales.SigninSettingUpSteps(supa.done, supa.all))
		} else {
			busy(locales.SigninSettingUp())
		}
	case stepSigning:
		busy(locales.SigninSigningIn())
	}
}

// supabaseSignUp is where people without a Supabase account make one.
const supabaseSignUp = "https://supabase.com/dashboard/sign-up"

// projectChoice lists the account's Leaflet projects to use, those
// Leaflet made and did not finish, and a new project to create.
func (a *App) projectChoice(c *ui.Context) {
	f := &supa.found
	label(c, locales.SigninChooseProject())
	card(c, "projects", func() {
		row := 0
		for _, p := range f.Projects {
			settingRow(c, "p-"+p.Ref, p.Name, p.Ref+" · "+p.Region, row == 0, func() {
				if button(c, "use", "", locales.SigninUseProject()).Clicked() {
					a.chooseProject(&p)
				}
			})
			row++
		}
		for _, p := range f.Unfinished {
			desc := locales.SigninProjectNotSetUp()
			if p.Status == "INACTIVE" {
				desc = locales.SigninProjectPaused()
			}
			settingRow(c, "u-"+p.Ref, p.Name, desc, row == 0, func() {
				if button(c, "setup", "", locales.SigninSetUpProject()).Clicked() {
					a.chooseProject(&p)
				}
			})
			row++
		}
		desc := locales.SigninNewProjectDesc()
		settingRow(c, "new", locales.SigninNewProject(), desc, row == 0, func() {
			if len(f.Orgs) > 1 {
				slugs := make([]string, len(f.Orgs))
				for i, o := range f.Orgs {
					slugs[i] = o.Slug
				}
				selectBox(c, "org", &supa.org, slugs, func(slug string) string {
					for _, o := range f.Orgs {
						if o.Slug == slug {
							return o.Name
						}
					}
					return slug
				}, 160)
			}
			if primary(c, "create", "", locales.SigninCreateProject()).Disabled(supa.org == "").Clicked() {
				a.chooseProject(nil)
			}
		})
	})
}

// emailCodeForm signs in to a server the build or environment fixes,
// such as the local stack, with a code sent by email.
func (a *App) emailCodeForm(c *ui.Context) {
	s := &signIn
	field(c, "email", &s.email, locales.CommonEmail()).AutoFocus()
	if s.sent {
		if field(c, "code", &s.code, locales.SigninCode()).Submitted() {
			a.doVerify()
		}
		if primary(c, "verify", "", locales.CommonSignIn()).Disabled(s.busy).Clicked() {
			a.doVerify()
		}
	} else if primary(c, "send", "", locales.SigninSendCode()).Disabled(s.busy).Clicked() {
		a.doSendCode()
	}
}

func (a *App) doSendCode() {
	email := strings.TrimSpace(signIn.email)
	signIn.busy, signIn.err = true, ""
	go func() {
		err := a.backend.SendCode(email)
		a.update(func() {
			signIn.busy = false
			if err != nil {
				signIn.err = err.Error()
				return
			}
			signIn.sent = true
			signIn.info = locales.SigninCodeSent()
		})
	}()
}

func (a *App) doVerify() {
	email, code := strings.TrimSpace(signIn.email), signIn.code
	a.runAuth(func() (*store.User, error) { return a.backend.VerifyCode(email, code) })
}

// signedIn sets the session up after signing in and starts syncing. As
// someone else than the account whose pages this device keeps, whose
// changes not uploaded yet would go, it asks first; an open page that
// could not be saved stops it, as it stops signing out.
func (a *App) signedIn(user *store.User) {
	if !a.flush() && a.replacesAccount(user) {
		// The save said why; the new account signs out again.
		go a.backend.SignOut()
		return
	}
	if n := a.lostBySigningIn(user); n > 0 {
		signIn.asking, signIn.lost = user, n
		return
	}
	a.useAccount(user, false)
}

// replacesAccount reports whether signing in as user removes the pages of
// another account this device keeps.
func (a *App) replacesAccount(user *store.User) bool {
	prev := a.st.CachedAccount()
	return prev != "" && prev != user.ID
}

// lostBySigningIn counts the changes signing in as user removes: those of
// another account this device keeps the pages of, which never went up.
func (a *App) lostBySigningIn(user *store.User) int {
	if !a.replacesAccount(user) {
		return 0
	}
	return a.st.PendingCount()
}

// useAccount makes user the session: the previous account's pages go, if
// it was another's, once the open page's edits are saved; with their
// changes not uploaded yet only when drop says the user agreed.
func (a *App) useAccount(user *store.User, drop bool) {
	if a.replacesAccount(user) && !a.flush() {
		go a.backend.SignOut()
		return
	}
	if drop && a.replacesAccount(user) {
		if err := a.st.ForgetAccount(); err != nil {
			a.toast(err.Error())
			return
		}
	}
	// One way of syncing at a time: the server's, now. Settings say so,
	// so iCloud sync shows off once signed out again.
	if a.sync.ICloudOn() || a.settings.ICloudSync {
		a.sync.SetICloud(false)
		a.watchICloud(false)
		a.settings.ICloudSync = false
		a.saveSettings()
	}
	if _, err := a.st.LoadSession(user); err != nil {
		// A change made since it counted, by leaflet mcp: it asks again.
		if errors.Is(err, store.ErrUnsynced) {
			signIn.asking, signIn.lost = user, a.st.PendingCount()
			return
		}
		a.toast(err.Error())
		return
	}
	a.session = a.st.Session()
	signIn = signInState{}
	// The name field shows the account's name now, not one typed before.
	account.nameEdited = false
	a.toast(locales.SettingsSignedInAs(user.Email))
	a.back()
	if a.route.Kind == "signin" {
		a.show(Route{Kind: "home"})
	}
	a.sync.Kick()
}

// signInAlert asks before signing in as another account removes the
// previous one's changes not uploaded yet; not signing in signs the new
// account out again.
func (a *App) signInAlert(c *ui.Context) {
	s := &signIn
	user := s.asking
	if user == nil {
		return
	}
	open := true
	if ui.AlertDialog(c, &open, locales.SigninAnyway(), locales.SigninRemovesUnsynced(s.lost, user.Email),
		locales.CommonCancel(), locales.CommonSignIn()) == 1 {
		s.asking = nil
		a.useAccount(user, true)
		return
	}
	if !open {
		s.asking, s.lost = nil, 0
		go a.backend.SignOut()
	}
}
