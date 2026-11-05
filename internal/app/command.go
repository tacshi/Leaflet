package app

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/cli"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// The command line (internal/cli) in a terminal: Settings › External
// agents installs it, as a link to this app's executable on the Mac
// (command_darwin.go), and on Windows as the console program beside it,
// its folder on the user's Path (command_windows.go).

// commandStatus is how the command line stands on this computer.
type commandStatus struct {
	// supported is false where this copy has none to install: a build
	// without it, or a system Leaflet does not run on.
	supported bool
	// installed is the command running this copy; other, it running
	// another copy of Leaflet, which installing points here.
	installed, other bool
	// taken is another program's file where the command would go, which
	// is left as it is.
	taken string
	// newTerminal is set where terminals read where commands are as they
	// start, as Windows' read the Path: one open already finds none.
	newTerminal bool
}

// commandUI is the command line's row's state.
type commandUI struct {
	status   commandStatus
	checked  time.Time
	checking bool
	// busy is set while installing, or removing while removing.
	busy, removing bool
}

// commandName is the command line's name: leaflet, or leaflet-debug for
// Leaflet Debug.
func commandName() string { return cli.CommandName(mygo.App.Name()) }

// commandSettings is the command line's card: how it stands, and the
// button that installs, updates or removes it.
func (a *App) commandSettings(c *ui.Context, exe string, moved bool) {
	st := &a.external.command
	if !st.checking && !st.busy && time.Since(st.checked) > 3*time.Second {
		a.checkCommand(exe)
	}
	if !st.status.supported {
		return
	}
	pal := theme.Of(c)
	name := commandName()
	label(c, locales.ExternalCommandLine())
	card(c, "command", func() {
		s := st.status
		icon, text, color := "", locales.ExternalCommandDesc(), pal.TextMuted
		switch {
		case s.taken != "":
			icon, text, color = "circle-alert", locales.ExternalCommandTaken(s.taken), pal.WarnText
		case s.installed && s.newTerminal:
			icon, text, color = "check", locales.ExternalCommandOnPath(name), pal.OkText
		case s.installed:
			icon, text, color = "check", locales.ExternalCommandInstalled(name), pal.OkText
		case s.other:
			icon, text, color = "circle-alert", locales.ExternalCommandOther(), pal.WarnText
		}
		ui.Row(c).Padding(12, 16).Gap(16).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Gap(3).Children(func() {
				ui.Text(c, locales.ExternalCommandTitle(name)).FontSize(14).FontWeight(500).TextColor(pal.Text)
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					if icon != "" {
						ui.Icon(c, theme.Icon(icon)).FontSize(13).TextColor(color)
					}
					ui.Text(c, text).FontSize(12).TextColor(color).Wrap()
				})
			})
			switch {
			case st.busy:
				label := locales.ExternalCommandInstalling()
				if st.removing {
					label = locales.ExternalCommandRemoving()
				}
				ui.Button(c.Key("busy"), "").Label(label).Disabled(true).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c).Size(13, 13)
						ui.Text(c, label).SingleLine()
					})
				})
			case s.taken != "":
			case s.installed:
				if button(c, "remove", "", locales.ExternalCommandRemove()).Clicked() {
					a.changeCommand(exe, true)
				}
			default:
				label := locales.ExternalCommandInstall()
				if s.other {
					label = locales.ExternalUpdate()
				}
				if primary(c, "install", "", label).Disabled(moved).Clicked() {
					a.changeCommand(exe, false)
				}
			}
		})
	})
}

// checkCommand looks at how the command line stands, away from the
// window's thread.
func (a *App) checkCommand(exe string) {
	a.external.command.checking = true
	name := commandName()
	go func() {
		s := commandState(name, exe)
		a.update(func() {
			st := &a.external.command
			st.status, st.checked, st.checking = s, time.Now(), false
		})
	}()
}

// changeCommand installs the command line, or removes it, then looks at
// it again before the row stops being busy.
func (a *App) changeCommand(exe string, remove bool) {
	st := &a.external.command
	st.busy, st.removing = true, remove
	name := commandName()
	prompt := locales.ExternalCommandPrompt(name)
	go func() {
		var err error
		if remove {
			err = removeCommand(name, exe, prompt)
		} else {
			err = installCommand(name, exe, prompt)
		}
		s := commandState(name, exe)
		a.update(func() {
			st.busy, st.status, st.checked = false, s, time.Now()
			switch {
			case err != nil && remove:
				a.toast(locales.ExternalCommandRemoveFailed(name, err.Error()))
			case err != nil:
				a.toast(locales.ExternalCommandFailed(name, err.Error()))
			}
		})
	}()
}
