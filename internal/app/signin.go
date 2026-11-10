package app

import (
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

type signInState struct {
	email  string
	code   string
	sent   bool
	busy   bool
	err    string
	info   string
	asking *store.User
	lost   int
}

func (a *App) signInView(c *ui.Context) {
	pal := theme.Of(c)
	s := &a.signIn
	a.titleBar(c, func() { ui.Spacer(c) })
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
	s := &a.signIn
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

func (a *App) supabaseSignIn(c *ui.Context) {
	pal := theme.Of(c)
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
	switch a.supa.step {
	case "":
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
		if a.supa.link != "" {
			ui.Row(c).Gap(6).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.SigninNewOrg()).FontSize(13).TextColor(pal.TextMuted)
				ui.Link(c, locales.SigninReopenBrowser(), a.supa.link).FontSize(13)
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
		if a.supa.all > 0 {
			busy(locales.SigninSettingUpSteps(a.supa.done, a.supa.all))
		} else {
			busy(locales.SigninSettingUp())
		}
	case stepSigning:
		busy(locales.SigninSigningIn())
	}
}

const supabaseSignUp = "https://supabase.com/dashboard/sign-up"

func (a *App) projectChoice(c *ui.Context) {
	f := &a.supa.found
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
				selectBox(c, "org", &a.supa.org, slugs, func(slug string) string {
					for _, o := range f.Orgs {
						if o.Slug == slug {
							return o.Name
						}
					}
					return slug
				}, 160)
			}
			if primary(c, "create", "", locales.SigninCreateProject()).Disabled(a.supa.org == "").Clicked() {
				a.chooseProject(nil)
			}
		})
	})
}

func (a *App) emailCodeForm(c *ui.Context) {
	pal := theme.Of(c)
	s := &a.signIn
	email := field(c, "email", &s.email, locales.CommonEmail()).Disabled(s.sent)
	if !s.sent {
		email.AutoFocus()
		if primary(c, "send", "", locales.SigninSendCode()).Disabled(s.busy).Clicked() {
			a.doSendCode()
		}
		return
	}
	if field(c, "code", &s.code, locales.SigninCode()).AutoFocus().Submitted() && !s.busy {
		a.doVerify()
	}
	if primary(c, "verify", "", locales.CommonSignIn()).Disabled(s.busy).Clicked() {
		a.doVerify()
	}
	ui.Row(c).Gap(6).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		if ghost(c, "resend", "", locales.SigninResendCode()).Disabled(s.busy).Clicked() {
			a.doSendCode()
		}
		ui.Text(c, "·").FontSize(13).TextColor(pal.TextMuted)
		if ghost(c, "change", "", locales.SigninChangeEmail()).Disabled(s.busy).Clicked() {
			s.sent, s.code, s.err, s.info = false, "", "", ""
		}
	})
}

func (a *App) doSendCode() {
	s := &a.signIn
	email := strings.TrimSpace(s.email)
	s.busy, s.err, s.info = true, "", ""
	var err error
	a.offMain(func() { err = a.backend.SendCode(email) }, func() {
		s.busy = false
		if err != nil {
			s.err = err.Error()
			return
		}
		s.sent, s.code = true, ""
		s.info = locales.SigninCodeSent()
	})
}

func (a *App) doVerify() {
	email, code := strings.TrimSpace(a.signIn.email), a.signIn.code
	a.runAuth(func() (*store.User, error) { return a.backend.VerifyCode(email, code) })
}

func (a *App) signedIn(user *store.User) {
	if !a.flush() && a.replacesAccount(user) {
		go a.backend.SignOut()
		return
	}
	if n := a.lostBySigningIn(user); n > 0 {
		a.signIn.asking, a.signIn.lost = user, n
		return
	}
	a.useAccount(user, false)
}

func (a *App) replacesAccount(user *store.User) bool { return a.core().ReplacesAccount(user) }

func (a *App) lostBySigningIn(user *store.User) int { return a.core().LostBySigningIn(user) }

func (a *App) useAccount(user *store.User, drop bool) {
	replaces := a.replacesAccount(user)
	if replaces && !a.flush() {
		go a.backend.SignOut()
		return
	}
	if err := a.core().UseAccount(user, drop); err != nil {
		if errors.Is(err, store.ErrUnsynced) {
			a.signIn.asking, a.signIn.lost = user, a.st.PendingCount()
			return
		}
		go a.backend.SignOut()
		a.toast(err.Error())
		return
	}
	a.session = a.st.Session()
	if replaces {
		delete(a.drafts, agents.WorkspaceID)
	}
	a.signIn = signInState{}
	a.account.nameEdited = false
	a.toast(locales.SettingsSignedInAs(user.Email))
	if a.route.Kind == "signin" {
		a.back()
		if a.route.Kind == "signin" {
			a.show(Route{Kind: "home"})
		}
	}
	a.sync.Kick()
}

func (a *App) signInAlert(c *ui.Context) {
	s := &a.signIn
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
