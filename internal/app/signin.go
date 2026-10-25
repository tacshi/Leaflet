package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/theme"
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
}

var signIn signInState

func (a *App) signInView(c *ui.Context) {
	pal := theme.Of(c)
	s := &signIn
	a.titleBar(c, func() { ui.Spacer(c) })
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(64, 24).Children(func() {
			ui.Column(c).Width(380).Gap(16).Children(func() {
				ui.Column(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(44, 44).Radius(12).Background(pal.Accent).Center().Children(func() {
						ui.Icon(c, theme.Icon("file-text")).FontSize(22).TextColor(pal.OnAccent)
					})
					ui.Text(c, "Sign in to Leaflet").FontSize(22).FontWeight(700).TextColor(pal.Text).Margin(10, 0, 0)
					ui.Text(c, "Sync your pages between devices, share them, and use cloud models.").FontSize(13.5).TextColor(pal.TextMuted).TextAlign(ui.Center)
				})
				if !a.backend.Configured() {
					notice(c, "info", "info", "This build has no server. Set SUPABASE_URL and SUPABASE_ANON_KEY to sign in.", nil)
					return
				}
				segmented(c, "mode", &s.mode, "Password", "Create account", "Email code")
				switch s.mode {
				case 0:
					field(c, "email", &s.email, "Email").AutoFocus()
					if password(c, "password", &s.password, "Password").Submitted() {
						a.doSignIn()
					}
					if primary(c, "go", "", "Sign in").Disabled(s.busy).Clicked() {
						a.doSignIn()
					}
				case 1:
					field(c, "name", &s.name, "Your name").AutoFocus()
					field(c, "email", &s.email, "Email")
					if password(c, "password", &s.password, "Password (at least 8 characters)").Submitted() {
						a.doSignUp()
					}
					if primary(c, "go", "", "Create account").Disabled(s.busy).Clicked() {
						a.doSignUp()
					}
				case 2:
					field(c, "email", &s.email, "Email").AutoFocus()
					if s.sent {
						if field(c, "code", &s.code, "Code from the email").Submitted() {
							a.doVerify()
						}
						if primary(c, "verify", "", "Sign in").Disabled(s.busy).Clicked() {
							a.doVerify()
						}
					} else if primary(c, "send", "", "Email me a code").Disabled(s.busy).Clicked() {
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
				if ghost(c, "local", "laptop", "Continue on this device").Clicked() {
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
				s.info = "Check your email to confirm the account, then sign in."
				return
			}
			a.signedIn(user)
		})
	}()
}

func (a *App) doSignIn() {
	email, pw := strings.TrimSpace(signIn.email), signIn.password
	if email == "" || pw == "" {
		signIn.err = "Enter your email and password."
		return
	}
	a.runAuth(func() (*store.User, error) { return a.backend.SignIn(email, pw) })
}

func (a *App) doSignUp() {
	email, pw, name := strings.TrimSpace(signIn.email), signIn.password, strings.TrimSpace(signIn.name)
	if len(pw) < 8 {
		signIn.err = "Use a password of at least 8 characters."
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
			signIn.info = "We emailed you a sign-in link and code."
		})
	}()
}

func (a *App) doVerify() {
	email, code := strings.TrimSpace(signIn.email), signIn.code
	a.runAuth(func() (*store.User, error) { return a.backend.VerifyCode(email, code) })
}

// signedIn sets the session up after signing in and starts syncing.
func (a *App) signedIn(user *store.User) {
	a.flush()
	if _, err := a.st.LoadSession(user); err != nil {
		a.toast(err.Error())
		return
	}
	a.session = a.st.Session()
	signIn = signInState{}
	a.toast("Signed in as " + user.Email)
	a.back()
	if a.route.Kind == "signin" {
		a.show(Route{Kind: "home"})
	}
	a.sync.Kick()
}
