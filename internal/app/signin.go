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
	mode     int // 0 sign in, 1 create account, 2 email code
	email    string
	password string
	name     string
	code     string
	sent     bool
	busy     bool
	err      string
	info     string
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
					ui.Box(c).Size(44, 44).Radius(12).Background(pal.Accent).Center().Children(func() {
						ui.Icon(c, theme.Icon("file-text")).FontSize(22).TextColor(pal.OnAccent)
					})
					ui.Text(c, locales.SigninTitle()).FontSize(22).FontWeight(700).TextColor(pal.Text).Margin(10, 0, 0)
					ui.Text(c, locales.SigninSubtitle()).FontSize(13.5).TextColor(pal.TextMuted).TextAlign(ui.Center)
				})
				if !a.backend.Configured() {
					notice(c, "info", "info", locales.SigninNoServer(), nil)
					return
				}
				ui.Segmented(c.Key("mode"), &s.mode, locales.SigninPassword(), locales.SigninCreateAccount(), locales.SigninEmailCode())
				switch s.mode {
				case 0:
					field(c, "email", &s.email, locales.CommonEmail()).AutoFocus()
					if password(c, "password", &s.password, locales.SigninPassword()).Submitted() {
						a.doSignIn()
					}
					if primary(c, "go", "", locales.CommonSignIn()).Disabled(s.busy).Clicked() {
						a.doSignIn()
					}
				case 1:
					field(c, "name", &s.name, locales.SettingsYourName()).AutoFocus()
					field(c, "email", &s.email, locales.CommonEmail())
					if password(c, "password", &s.password, locales.SigninPasswordNew()).Submitted() {
						a.doSignUp()
					}
					if primary(c, "go", "", locales.SigninCreateAccount()).Disabled(s.busy).Clicked() {
						a.doSignUp()
					}
				case 2:
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
				if s.err != "" {
					notice(c, "danger", "circle-alert", s.err, nil)
				}
				if s.info != "" {
					notice(c, "ok", "check", s.info, nil)
				}
				ui.Divider(c)
				if ghost(c, "local", "laptop", locales.SigninContinueLocal()).Clicked() {
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
			if user == nil {
				s.info = locales.SigninConfirmEmail()
				return
			}
			a.signedIn(user)
		})
	}()
}

func (a *App) doSignIn() {
	email, pw := strings.TrimSpace(signIn.email), signIn.password
	if email == "" || pw == "" {
		signIn.err = locales.SigninEnterEmailPassword()
		return
	}
	a.runAuth(func() (*store.User, error) { return a.backend.SignIn(email, pw) })
}

func (a *App) doSignUp() {
	email, pw, name := strings.TrimSpace(signIn.email), signIn.password, strings.TrimSpace(signIn.name)
	if len(pw) < 8 {
		signIn.err = locales.SigninPasswordTooShort()
		return
	}
	a.runAuth(func() (*store.User, error) { return a.backend.SignUp(email, pw, name) })
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
