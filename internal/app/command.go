package app

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/cli"
	"leaflet/internal/theme"
	"leaflet/locales"
)

type commandStatus struct {
	supported        bool
	installed, other bool
	taken            string
	newTerminal      bool
}

type commandUI struct {
	status         commandStatus
	checked        time.Time
	checking       bool
	busy, removing bool
}

func commandName() string { return cli.CommandName(mygo.App.Name()) }

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
		case s.other:
			icon, text, color = "circle-alert", locales.ExternalCommandOther(), pal.WarnText
		case s.installed && s.newTerminal:
			icon, text, color = "check", locales.ExternalCommandOnPath(name), pal.OkText
		case s.installed:
			icon, text, color = "check", locales.ExternalCommandInstalled(name), pal.OkText
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
			case s.installed && !s.other:
				if button(c, "remove", "", locales.ExternalCommandRemove()).Clicked() {
					a.changeCommand(exe, true)
				}
			case s.taken != "":
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

var commandExts = []string{".com", ".exe", ".bat", ".cmd"}

func onPath(s commandStatus, dirs []string, name, exe, bin string, same func(d, bin string) bool, isFile func(string) bool) commandStatus {
	for _, d := range dirs {
		if same(d, bin) {
			break
		}
		for _, ext := range commandExts {
			f := filepath.Join(d, name+ext)
			switch {
			case !isFile(f):
				continue
			case isCopyBin(d, name, exe, isFile):
				s.other = true
			default:
				s.taken = f
			}
			return s
		}
	}
	return s
}

func isCopyBin(d, name, exe string, isFile func(string) bool) bool {
	return strings.EqualFold(filepath.Base(d), "bin") && isFile(filepath.Join(d, name+".exe")) &&
		isFile(filepath.Join(filepath.Dir(d), filepath.Base(exe)))
}

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
