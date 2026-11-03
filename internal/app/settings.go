package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/icloud"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// settingsSections are Settings' sections, said in the language shown.
func settingsSections() []struct{ id, icon, label string } {
	return []struct{ id, icon, label string }{
		{"account", "user", locales.SettingsSectionAccount()},
		{"appearance", "palette", locales.SettingsSectionAppearance()},
		{"providers", "sparkles", locales.SettingsSectionProviders()},
		{"external", "bot", locales.SettingsSectionExternal()},
		{"speech", "mic", locales.SettingsSectionSpeech()},
		{"offline", "refresh-cw", locales.SettingsSectionOffline()},
	}
}

// settingsNav is MyGo's sidebar of the sections, the route's chosen.
func (a *App) settingsNav(c *ui.Context) ui.Element {
	pal := theme.Of(c)
	return ui.Sidebar(c.Key("settings-nav"), &a.settingsSection, func() {
		for _, s := range settingsSections() {
			ui.SidebarItem(c, s.id, theme.Icon(s.icon), s.label)
		}
	}).Width(settingsNavW).BorderWidth(0, 1, 0, 0).BorderColor(pal.Divider).Label(locales.SettingsTitle())
}

// The settings dialog's room: its section list, its padding beside a
// section, and its size, kept as the window allows.
const settingsNavW, settingsPadX = 200, 28

func settingsSize(c *ui.Context) (w, h float32) {
	ww, wh := c.Size()
	return max(min(880, ww-64), 320), max(min(640, wh*0.86), 240)
}

// settingsDialog is Settings over the window: the sections' list beside
// the section chosen, closed by Escape, a click outside or its button.
func (a *App) settingsDialog(c *ui.Context) {
	pal := theme.Of(c)
	w, h := settingsSize(c)
	dialog(c, &a.settingsOpen, w, func() {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			// Focused as it opens, its section chosen shows in the accent
			// and the arrows move through the sections.
			a.settingsNav(c).FillHeight().AutoFocus()
			ui.Column(c).Grow(1).Shrink(1).Children(func() {
				ui.Row(c).Padding(14, 14, 10, settingsPadX).Gap(12).AlignItems(ui.Center).
					BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
					ui.Text(c, locales.SettingsTitle()).FontSize(15).FontWeight(650).TextColor(pal.Text).Grow(1)
					tools(c, func() {
						if iconButton(c, "close", "x", locales.CommonClose()).Clicked() {
							a.settingsOpen = false
						}
					})
				})
				a.settingsBody(c)
			})
		})
	}).Height(h)
}

func (a *App) settingsBody(c *ui.Context) {
	ui.Scroll(c.Key("settings:" + a.settingsSection)).Grow(1).Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(24, settingsPadX, 40).Children(func() {
			ui.Column(c).Width(a.settingsWidth(c)).Gap(18).Children(func() {
				switch a.settingsSection {
				case "appearance":
					a.appearanceSettings(c)
				case "providers":
					a.modelsSettings(c)
				case "external":
					a.externalSettings(c)
				case "speech":
					a.speechSettings(c)
				case "offline":
					a.offlineSettings(c)
				default:
					a.accountSettings(c)
				}
			})
		})
	})
}

// settingsWidth is the width of a settings section: up to 640, within
// the dialog beside the section list, less its padding.
func (a *App) settingsWidth(c *ui.Context) float32 {
	w, _ := settingsSize(c)
	return max(min(w-settingsNavW-1-2*settingsPadX, 640), 200)
}

func heading(c *ui.Context, title, desc string) {
	pal := theme.Of(c)
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, title).FontSize(22).FontWeight(700).TextColor(pal.Text)
		if desc != "" {
			ui.Text(c, desc).FontSize(13.5).TextColor(pal.TextMuted)
		}
	})
}

// Account ---------------------------------------------------------------

type accountState struct {
	name string
	// nameEdited is set once the user changes the name field: until then
	// it shows the account's name as it is now, and nothing saves from it.
	// nameShown is set in a frame that showed the field, the section, and
	// shownBefore in the frame before.
	nameEdited  bool
	nameShown   bool
	shownBefore bool
	// devices are the devices of the account devicesOf names, once
	// loaded (devicesLoaded); loadingFor is the account whose devices are
	// loading, and devicesErr why they could not be, to try again.
	devices       []core.Device
	devicesOf     string
	devicesLoaded bool
	loadingFor    string
	devicesErr    string
	// deleteOpen shows the dialog deleting the account, confirm is what
	// is typed in it, and busy is set while the server deletes it.
	deleteOpen bool
	confirm    string
	busy       bool
	// signingOut is set while waiting changes go up before signing out;
	// unsynced counts those that could not, to ask about.
	signingOut bool
	unsynced   int
}

// Appearance ------------------------------------------------------------

func (a *App) appearanceSettings(c *ui.Context) {
	heading(c, locales.SettingsSectionAppearance(), locales.SettingsAppearanceDesc())
	themeIdx := map[string]int{"system": 0, "light": 1, "sepia": 2, "dark": 3}[a.settings.Theme]
	card(c, "appearance", func() {
		settingRow(c, "theme", locales.SettingsTheme(), "", true, func() {
			ui.Segmented(c.Key("theme"), &themeIdx, locales.SettingsSystem(), locales.SettingsThemeLight(), locales.SettingsThemeSepia(), locales.SettingsThemeDark())
			if want := []string{"system", "light", "sepia", "dark"}[themeIdx]; want != a.settings.Theme {
				a.settings.Theme = want
				a.saveSettings()
				a.applyTheme()
			}
		})
		settingRow(c, "language", locales.SettingsLanguage(), "", false, func() {
			tags := append([]string{""}, mygo.App.Languages()...)
			lang := a.settings.Language
			selectBox(c, "language", &lang, tags, func(tag string) string {
				if tag == "" {
					return locales.SettingsLanguageSystem()
				}
				return i18n.Autonym(tag)
			}, 180)
			if lang != a.settings.Language {
				a.settings.Language = lang
				a.saveSettings()
				mygo.App.SetLocale(lang)
			}
		})
		settingRow(c, "accent", locales.SettingsAccent(), "", false, func() { a.accentDots(c) })
		settingRow(c, "size", locales.SettingsTextSize(), "", false, func() {
			sizes := []float64{15, 17, 19, 21}
			chosen := 1
			for k, v := range sizes {
				if v == a.settings.TextSize {
					chosen = k
				}
			}
			if ui.Segmented(c.Key("size"), &chosen, locales.SettingsTextSizeSmall(), locales.SettingsTextSizeDefault(), locales.SettingsTextSizeLarge(), locales.SettingsTextSizeLarger()); sizes[chosen] != a.settings.TextSize {
				a.settings.TextSize = sizes[chosen]
				a.saveSettings()
			}
		})
	})
	label(c, locales.SettingsFonts())
	card(c, "fonts", func() {
		f := &a.settings.Fonts
		a.fontRow(c, "latin", locales.SettingsFontLatin(), &f.Latin, false, true)
		a.fontRow(c, "chinese", locales.SettingsFontChinese(), &f.Chinese, false, false)
		a.fontRow(c, "japanese", locales.SettingsFontJapanese(), &f.Japanese, false, false)
		a.fontRow(c, "code", locales.SettingsFontCode(), &f.Code, true, false)
	})
}

// fontFamilies are the names of the families text can use, all of them
// and the monospaced ones, read once.
var fontFamilies struct {
	once      sync.Once
	all, mono []string
}

// fontRow is a row choosing *family among the system's fonts, the
// monospaced ones for mono, or Leaflet's own for "".
func (a *App) fontRow(c *ui.Context, key, title string, family *string, mono, first bool) {
	fontFamilies.once.Do(func() {
		for _, f := range ui.FontFamilies() {
			fontFamilies.all = append(fontFamilies.all, f.Name)
			if f.Monospaced {
				fontFamilies.mono = append(fontFamilies.mono, f.Name)
			}
		}
	})
	names := fontFamilies.all
	if mono {
		names = fontFamilies.mono
	}
	settingRow(c, key, title, "", first, func() {
		def := locales.SettingsFontDefault()
		chosen := *family
		if chosen == "" {
			chosen = def
		}
		ui.Combobox(c.Key(key), &chosen, append([]string{def}, names...)).Width(220).Label(title)
		if chosen == def {
			chosen = ""
		}
		if chosen != *family {
			*family = chosen
			a.saveSettings()
			a.applyTheme()
		}
	})
}

// accentDots is a row of dots choosing the accent: the desktop's, where
// it has one, then Leaflet's. None chosen is the desktop's, or Blue.
func (a *App) accentDots(c *ui.Context) {
	pal := theme.Of(c)
	isDark := c.Theme().Dark
	def := "Blue"
	if c.Preferences().Accent.A > 0 {
		def = "system"
	}
	ui.Row(c.Key("accents")).Gap(8).Children(func() {
		dot := func(name, label string, color ui.Color) {
			d := ui.ButtonBase(c.Key(name)).Size(20, 20).Radius(999).Background(color).Label(label).Tooltip(label)
			chosen := a.settings.Accent == name || a.settings.Accent == "" && name == def
			if chosen {
				d.Border(2, pal.Text)
			} else if d.Hovered() {
				d.Border(2, pal.Border)
			}
			if d.Clicked() && !chosen {
				a.settings.Accent = name
				a.saveSettings()
				a.applyTheme()
			}
		}
		if def == "system" {
			system, _ := theme.Accent("system", isDark, c.Preferences().Accent)
			dot("system", locales.SettingsSystem(), system)
		}
		for _, acc := range theme.Accents {
			color := acc.Light
			if isDark {
				color = acc.Dark
			}
			dot(acc.Name, locales.SettingsAccentName(acc.Name), color)
		}
	})
}

func (a *App) accountSettings(c *ui.Context) {
	s := &a.account
	if !s.nameEdited {
		s.name = a.session.DisplayName
	}
	heading(c, locales.SettingsSectionAccount(), locales.SettingsAccountDesc())
	card(c, "profile", func() {
		settingRow(c, "name", locales.SettingsName(), locales.SettingsNameDesc(), true, func() {
			s.nameShown = true
			in := field(c, "name", &s.name, locales.SettingsYourName()).Width(220)
			if in.Changed() {
				s.nameEdited = true
			}
			// Return, or leaving the field, saves what was typed; the
			// field then shows the account's name again, as saved.
			if s.nameEdited && (in.Submitted() || !in.Focused()) {
				a.saveName(s.name)
				s.nameEdited = false
			}
		})
		// A build without a server says so once, in Sync and storage.
		if !a.hasServer() {
			return
		}
		title, desc := locales.SettingsSync(), locales.SettingsSyncDesc()
		project := a.backend.Project()
		if a.session.SignedIn {
			title, desc = locales.SettingsSignedIn(), a.session.Email
			if project.Ref != "" {
				desc = locales.SettingsSignedInProject(a.session.Email, project.Name)
			}
		}
		settingRow(c, "email", title, desc, false, func() {
			if a.session.SignedIn && project.Ref != "" {
				if iconButton(c, "dashboard", "external-link", locales.SettingsOpenSupabase()).Clicked() {
					_ = mygo.Shell.OpenExternal(dashboardURL(project.Ref))
				}
			}
			switch {
			case a.session.SignedIn:
				if button(c, "signout", "log-out", locales.CommonSignOut()).Disabled(s.signingOut).Clicked() {
					a.signOut()
				}
			case a.hasServer():
				a.signInButton(c)
			}
		})
	})
	if a.session.SignedIn {
		label(c, locales.SettingsDevices())
		// They load as the section opens, and for another account, as one
		// signed out elsewhere and then another signed in.
		if !s.shownBefore || s.devicesOf != a.session.UserID {
			a.loadDevices()
		}
		card(c, "devices", func() {
			switch {
			case s.devicesErr != "":
				settingRow(c, "failed", locales.SettingsDevicesFailed(s.devicesErr), "", true, func() {
					if button(c, "retry", "refresh-cw", locales.CommonRetry()).Clicked() {
						s.devicesErr = ""
						a.loadDevices()
					}
				})
				return
			case !s.devicesLoaded:
				settingRow(c, "loading", locales.SettingsDevicesLoading(), "", true, nil)
			case len(s.devices) == 0:
				settingRow(c, "none", locales.SettingsDevicesNone(), "", true, nil)
			}
			for i, d := range s.devices {
				desc := locales.SettingsDeviceLastActive(humanTime(d.LastActive))
				if d.This {
					desc = locales.SettingsDeviceThis()
				}
				if d.Revoked {
					desc = locales.SettingsDeviceSignedOut()
				}
				settingRow(c, "dev:"+d.ID, d.Name, desc, i == 0, func() {
					if !d.This && !d.Revoked && button(c, "revoke", "", locales.CommonSignOut()).Clicked() {
						a.revokeDevice(d.ID)
					}
				})
			}
		})
	}
	label(c, locales.SettingsYourData())
	card(c, "data", func() {
		settingRow(c, "export", locales.SettingsExportAll(), locales.SettingsExportAllDesc(), true, func() {
			if button(c, "export", "download", locales.SettingsExportButton()).Clicked() {
				a.exportAll()
			}
		})
		if a.session.SignedIn {
			settingRow(c, "delete", locales.SettingsDeleteAccount(), locales.SettingsDeleteAccountDesc(), false, func() {
				if danger(c, "delete", locales.SettingsDeleteButton()).Clicked() {
					s.deleteOpen, s.confirm = true, ""
				}
			})
		}
	})
	if s.deleteOpen {
		a.deleteAccountDialog(c)
	}
}

// deleteAccountDialog asks before deleting the account: the button works
// once DELETE is typed, and Return too then, and the dialog waits while
// the server deletes it.
func (a *App) deleteAccountDialog(c *ui.Context) {
	pal := theme.Of(c)
	s := &a.account
	dialog(c, &s.deleteOpen, 420, func() {
		dialogHeader(c, locales.SettingsDeleteAccountTitle(), locales.SettingsCannotUndo(), &s.deleteOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			ui.Text(c, locales.SettingsTypeDelete()).FontSize(13).TextColor(pal.TextMuted)
			submitted := field(c, "confirm", &s.confirm, "DELETE").Disabled(s.busy).AutoFocus().Submitted()
			ready := strings.TrimSpace(s.confirm) == "DELETE" && !s.busy
			ui.Row(c).Justify(ui.End).AlignItems(ui.Center).Gap(8).Children(func() {
				if s.busy {
					ui.Spinner(c).Size(14, 14)
				}
				if button(c, "cancel", "", locales.CommonCancel()).Disabled(s.busy).Clicked() {
					s.deleteOpen = false
				}
				label := locales.SettingsDeleteAccount()
				if s.busy {
					label = locales.SettingsDeletingAccount()
				}
				if danger(c, "go", label).Disabled(!ready).Clicked() || submitted && ready {
					a.deleteAccount()
				}
			})
		})
	})
}

// deleteAccount deletes the account on the server, then everything of it
// on this device.
func (a *App) deleteAccount() {
	s := &a.account
	s.busy = true
	c := a.core()
	var err error
	a.offMain(func() { err = c.DeleteAccount() }, func() {
		s.busy = false
		if err != nil {
			a.toast(err.Error())
			return
		}
		s.deleteOpen = false
		a.leave(a.st.Wipe, false)
		a.toast(locales.SettingsAccountDeleted())
	})
}

// signOutAlert asks before signing out removes changes not uploaded yet.
// The window shows it over whatever it shows, as the upload before it may
// finish after Settings has closed.
func (a *App) signOutAlert(c *ui.Context) {
	s := &a.account
	if s.unsynced == 0 {
		return
	}
	open := true
	if ui.AlertDialog(c, &open, locales.SettingsSignOutAnyway(), locales.SettingsSignOutUnsynced(s.unsynced),
		locales.CommonCancel(), locales.CommonSignOut()) == 1 {
		a.leave(a.st.ForgetAccount, false)
	}
	if !open {
		s.unsynced = 0
	}
}

// keepName saves the name typed in Account once a frame no longer shows
// its field, as Settings closed or went to another section, which the
// field leaving its focus never says then; and notes whether the frame
// showed it, for the section to know it opens. Call it after Settings
// builds.
func (a *App) keepName() {
	s := &a.account
	if s.nameEdited && !s.nameShown {
		a.saveName(s.name)
		s.nameEdited = false
	}
	s.shownBefore, s.nameShown = s.nameShown, false
}

// saveName keeps the name typed, here and then on the server.
func (a *App) saveName(name string) {
	c := a.core()
	changed, err := c.SetDisplayName(name)
	if err != nil {
		a.account.name = a.session.DisplayName
		a.toast(err.Error())
		return
	}
	if !changed {
		return
	}
	a.session = a.st.Session()
	go func() {
		if err := c.SendDisplayName(); err != nil {
			a.update(func() { a.toast(err.Error()) })
		}
	}()
}

// loadDevices loads the devices of the account signed in, off the main
// thread, unless they are loading; those of another account go first.
func (a *App) loadDevices() {
	s := &a.account
	user := a.session.UserID
	if s.devicesOf != user {
		s.devices, s.devicesOf, s.devicesLoaded, s.devicesErr = nil, user, false, ""
	}
	if s.loadingFor == user {
		return
	}
	s.loadingFor = user
	c := a.core()
	var devices []core.Device
	var err error
	a.offMain(func() { devices, err = c.Devices() }, func() {
		if s.loadingFor == user {
			s.loadingFor = ""
		}
		// Another account's, signed in meanwhile, load on their own.
		if s.devicesOf != user {
			return
		}
		if err != nil {
			s.devicesErr = err.Error()
			return
		}
		s.devices, s.devicesLoaded, s.devicesErr = devices, true, ""
	})
}

// revokeDevice signs out another device of the account.
func (a *App) revokeDevice(id string) {
	c := a.core()
	var err error
	a.offMain(func() { err = c.SignOutDevice(id) }, func() {
		if err != nil {
			a.toast(err.Error())
			return
		}
		a.loadDevices()
	})
}

func (a *App) exportAll() {
	// An export without the edits waiting would miss them; the save said
	// why.
	if !a.flush() {
		return
	}
	folders, files := slices.Clone(a.folders), slices.Clone(a.files)
	go func() {
		path, _ := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: locales.SettingsExportAll(), DefaultPath: locales.SettingsExportFileName()})
		if path == "" {
			return
		}
		missing, err := a.writeExport(path, folders, files)
		a.update(func() {
			switch {
			case err != nil:
				a.toast(locales.SettingsExportFailed(err.Error()))
			case missing > 0:
				a.toast(locales.SettingsExportedMissing(missing, filepath.Base(path)))
			default:
				a.toast(locales.SettingsExported(filepath.Base(path)))
			}
		})
	}()
}

// signOut uploads the changes waiting, then signs out and removes the
// account's pages from this device. Changes that could not go up are
// asked about first, and an open page that could not be saved stops it.
func (a *App) signOut() {
	if !a.flush() {
		return
	}
	a.account.signingOut = true
	go func() {
		_ = a.sync.SyncNow()
		a.update(func() {
			a.account.signingOut = false
			a.leave(a.st.ForgetSyncedAccount, true)
		})
	}()
}

// leave signs out and clears this device with clear, between sync passes.
// With synced, it does neither while a change waits to go up, or the open
// page cannot be saved, and asks about the changes instead; clear checks
// again as it removes, for a change that leaflet mcp stores meanwhile.
func (a *App) leave(clear func() error, synced bool) {
	if !a.flush() && synced {
		return
	}
	c := a.core()
	go func() {
		unsynced, err := c.Leave(clear, synced)
		a.update(func() {
			if unsynced > 0 {
				a.account.unsynced = unsynced
				return
			}
			if err != nil {
				a.toast(locales.SettingsRemovePagesFailed(err.Error()))
			}
			if _, err := a.st.LoadSession(nil); err != nil {
				a.toast(err.Error())
			}
			// Wiping the device removes the settings too: those shown, and
			// saved with the next change, are the ones a new device has.
			if _, kept := a.st.Value("settings"); !kept {
				was := a.settings
				a.settings = a.st.Settings()
				a.sidebarW, a.panelW = a.settings.SidebarWidth, a.settings.PanelWidth
				if a.settings.Theme != was.Theme || a.settings.Accent != was.Accent || a.settings.Fonts != was.Fonts {
					a.applyTheme()
				}
				// MyGo keeps the language it was set to apart.
				if a.settings.Language != was.Language {
					mygo.App.SetLocale(a.settings.Language)
				}
			}
			a.session = a.st.Session()
			s := &a.account
			s.devices, s.devicesOf, s.devicesLoaded, s.devicesErr, s.nameEdited = nil, "", false, "", false
		})
	}()
}

// Agent providers -------------------------------------------------------

type providerForm struct {
	open      bool
	id        string
	preset    string
	name      string
	baseURL   string
	apiKey    string
	model     string
	models    []string
	custom    bool
	tools     *bool
	fetching  bool
	fetchedAt string
	testing   bool
	result    string
	resultOK  bool
	saving    bool
	errorText string
	// confirmRemove asks before removing the provider; removing is set
	// while the server deletes it.
	confirmRemove, removing bool
	// A ChatGPT sign-in, before it is saved; stopSignIn stops the one
	// under way, as the dialog closes or another opens in its place.
	signingIn  bool
	stopSignIn context.CancelFunc
	chatgpt    *agents.ChatGPT
	email      string
	keyTimer   *time.Timer
	// reading is set while the key or sign-in kept here of the provider
	// edited is read (editProvider): saving waits, not to drop it.
	reading bool
	// gen tells this dialog from the ones opened before it: what a test,
	// sign-in or save started in one of those finds goes nowhere.
	gen int
}

// openForm opens the provider dialog on f, in place of the one open.
func (a *App) openForm(f providerForm) {
	a.form.cancelSignIn()
	a.formGen++
	f.gen = a.formGen
	a.form = f
}

// formIs reports whether the provider dialog is still the one opened as
// gen, for work started in it that finishes later.
func (a *App) formIs(gen int) bool { return a.form.open && a.form.gen == gen }

// cancelSignIn stops the dialog's ChatGPT sign-in under way, if one is:
// its page in the browser signs in to nothing then, and its port is free.
func (f *providerForm) cancelSignIn() {
	if f.stopSignIn != nil {
		f.stopSignIn()
		f.stopSignIn = nil
	}
}

func (a *App) modelsSettings(c *ui.Context) {
	pal := theme.Of(c)
	heading(c, locales.SettingsSectionProviders(), locales.SettingsProvidersDesc())
	card(c, "providers", func() {
		if len(a.providers) == 0 {
			settingRow(c, "none", locales.SettingsNoProviders(), locales.SettingsNoProvidersDesc(), true, nil)
		}
		for i, p := range a.providers {
			where := locales.SettingsProviderCloud()
			switch {
			case p.AuthKind == "account":
				where = locales.SettingsProviderPlan()
				if g := a.chatgptOf(p.ID); g != nil && g.Account().Email != "" {
					where = locales.SettingsProviderPlanEmail(g.Account().Email)
				}
			case p.IsLocal:
				where = locales.SettingsProviderLocal(p.BaseURL)
			}
			// Agents always use tools on a ChatGPT plan (hub.go), whatever
			// an older test left in supports_tools.
			if p.SupportsTools != nil && !*p.SupportsTools && p.AuthKind != "account" {
				where = locales.SettingsProviderChatOnly(where)
			}
			row := ui.ButtonBase(c.Key("p:" + p.ID)).Justify(ui.Start).Label(locales.SettingsProviderEdit(p.Name))
			if i > 0 {
				row.BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider)
			}
			if row.Hovered() {
				row.Background(pal.Hover)
			}
			row.Children(func() {
				settingRow(c, "row", p.Name+" · "+p.DefaultModel, where, true, func() {
					ui.Icon(c, theme.Icon("chevron-right")).FontSize(14).TextColor(pal.TextMuted)
				})
			})
			if row.Clicked() {
				a.editProvider(&p)
			}
		}
	})
	ui.Row(c).Gap(8).Children(func() {
		add := primary(c, "add", "plus", locales.SettingsAddProvider())
		add.Menu(func(m *ui.Menu) {
			// One ChatGPT sign-in is enough: its row edits it.
			signedIn := false
			for _, p := range a.providers {
				signedIn = signedIn || p.AuthKind == "account"
			}
			group := ""
			for _, p := range agents.Presets {
				if p.Auth == "account" && signedIn {
					continue
				}
				if p.Group != group {
					if group != "" {
						m.Separator()
					}
					group = p.Group
				}
				label := p.Name
				if p.Auth == "account" {
					label = locales.SettingsSignInWith(p.Name)
				}
				if m.Item(label).Chosen() {
					a.newProvider(p)
				}
			}
		})
	})
	if len(a.providers) > 0 {
		label(c, locales.SettingsDefaults())
		card(c, "defaults", func() {
			// The default picks a provider; its model is the one set on the
			// provider above, so the model is chosen in one place.
			var choices []string
			names := map[string]string{}
			for _, o := range a.hub.Options() {
				ch := store.ModelChoice{ProviderID: o.Provider.ID}.Encode()
				choices = append(choices, ch)
				names[ch] = o.Name()
			}
			cur := ""
			if def, ok := a.settings.Default(); ok {
				cur = store.ModelChoice{ProviderID: def.ProviderID}.Encode()
			}
			if !contains(choices, cur) && len(choices) > 0 {
				cur = choices[0]
			}
			was := cur
			if len(choices) > 1 {
				settingRow(c, "default", locales.SettingsDefaultAgent(), locales.SettingsDefaultAgentDesc(), true, func() {
					if selectBox(c, "default", &cur, choices, func(s string) string { return names[s] }, 240); cur != was {
						a.settings.DefaultModel = cur
						a.saveSettings()
					}
				})
			}
			r := a.settings.AgentsReadOtherPages
			settingRow(c, "cross", locales.SettingsReadOtherPages(), locales.SettingsReadOtherPagesDesc(), len(choices) < 2, func() {
				if ui.Switch(c.Key("cross"), &r).Changed() {
					a.settings.AgentsReadOtherPages = r
					a.saveSettings()
				}
			})
		})
	}
	if a.form.open {
		a.providerDialog(c)
		a.removeAlert(c)
	}
	if !a.form.open {
		a.form.cancelSignIn()
	}
}

func (a *App) newProvider(p agents.Preset) {
	a.openForm(providerForm{open: true, preset: p.ID, name: p.Name, baseURL: p.BaseURL, models: p.Suggested})
	if len(p.Suggested) > 0 {
		a.form.model = p.Suggested[0]
	}
	if p.Auth == "none" {
		// A server on this machine lists its models without a key.
		a.fetchModels()
	}
}

func (a *App) editProvider(p *store.Provider) {
	a.openForm(providerForm{open: true, id: p.ID, preset: p.Preset, name: p.Name, baseURL: p.BaseURL, model: p.DefaultModel,
		models: agents.Models(*p), tools: p.SupportsTools, reading: true})
	// Its key or sign-in kept here is read off the main thread, as the
	// keychain can take a while, or ask first; its models are listed
	// with it, and it saves only once it has come.
	id, gen := p.ID, a.form.gen
	local, account := p.IsLocal && p.AuthKind != "account", p.AuthKind == "account"
	var key string
	var g *agents.ChatGPT
	a.offMain(func() {
		if local {
			key = agents.LocalKey(id)
		}
		if account {
			g, _ = a.hub.ChatGPT(id)
		}
	}, func() {
		if !a.formIs(gen) {
			return
		}
		a.form.reading = false
		if a.form.apiKey == "" {
			a.form.apiKey = key
		}
		if g != nil && a.form.chatgpt == nil {
			a.form.chatgpt, a.form.email = g, g.Account().Email
		}
		a.fetchModels()
	})
}

// chatgptOf is the provider's ChatGPT sign-in, for what is drawn: nil
// until it has been read, once, off the main thread, as the keychain can
// take a while, or ask first.
func (a *App) chatgptOf(id string) *agents.ChatGPT {
	if g := a.hub.KnownChatGPT(id); g != nil {
		return g
	}
	if !a.chatgptRead[id] {
		if a.chatgptRead == nil {
			a.chatgptRead = map[string]bool{}
		}
		a.chatgptRead[id] = true
		a.offMain(func() { _, _ = a.hub.ChatGPT(id) }, func() {})
	}
	return nil
}

func (a *App) providerDialog(c *ui.Context) {
	preset := agents.PresetByID(a.form.preset)
	account := preset.Auth == "account"
	local := preset.Local
	here := a.hub.KeptHere(a.providerForm())
	title := locales.SettingsConnectProvider(preset.Name)
	if a.form.id != "" {
		title = locales.SettingsProviderEdit(a.form.name)
	}
	desc := locales.SettingsProviderDescCloud()
	switch {
	case account:
		desc = locales.SettingsProviderDescAccount()
	case local:
		desc = locales.SettingsProviderDescLocal()
	case here:
		desc = locales.SettingsProviderDescDevice()
	}
	if preset.ID == "claude" {
		desc = locales.SettingsProviderClaudeNote(desc)
	}
	dialog(c, &a.form.open, 500, func() {
		dialogHeader(c, title, desc, &a.form.open)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			if account {
				a.chatgptRow(c)
			} else {
				ui.Field(c, locales.SettingsName(), func() { field(c, "name", &a.form.name, preset.Name) })
				ui.Field(c, locales.SettingsBaseURL(), func() {
					ph := preset.Placeholder
					if ph == "" {
						ph = preset.BaseURL
					}
					// Another address has other models.
					if field(c, "url", &a.form.baseURL, ph).Changed() {
						a.fetchModelsSoon()
					}
				})
			}
			testButton := func() {
				label := locales.SettingsTest()
				if a.form.testing {
					label = locales.SettingsTesting()
				}
				if button(c, "test", "", label).MinWidth(82).Justify(ui.Center).Disabled(a.form.testing || (account && a.form.chatgpt == nil)).Clicked() {
					a.testProvider()
				}
			}
			if !account {
				ui.Field(c, locales.SettingsAPIKey(), func() {
					ph := locales.SettingsPasteKey()
					if a.form.id != "" && !local {
						ph = locales.SettingsKeySaved()
					}
					if local {
						ph = locales.SettingsOptional()
					}
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						var in ui.Element
						ui.Box(c).Grow(1).Children(func() { in = password(c, "key", &a.form.apiKey, ph) })
						if in.Changed() {
							a.fetchModelsSoon()
						}
						testButton()
					})
				})
			}
			if a.form.result != "" {
				kind := "danger"
				if a.form.resultOK {
					kind = "ok"
				}
				notice(c, kind, map[bool]string{true: "check", false: "circle-alert"}[a.form.resultOK], a.form.result, nil)
			}
			// What saving or removing found wrong shows below the model,
			// the last field.
			if !account || a.form.chatgpt != nil {
				ui.Field(c, locales.CommonModel(), func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						a.modelPicker(c)
						if account {
							testButton()
						}
					})
				}).Error(a.form.errorText)
			}
			// Nothing to save before signing in: an existing sign-in can
			// only be removed.
			signedOut := account && a.form.chatgpt == nil
			if signedOut && a.form.id == "" {
				return
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if a.form.id != "" && danger(c, "remove", locales.CommonRemove()).Disabled(a.form.removing).Clicked() {
					a.form.confirmRemove = true
				}
				ui.Spacer(c)
				if signedOut {
					return
				}
				// Only a provider kept on the server needs the account to
				// save it; one kept here saves signed out too.
				cannot := a.form.saving || a.form.reading || (!local && !here && !a.session.SignedIn) || (account && a.form.chatgpt == nil) || strings.TrimSpace(a.form.model) == ""
				if primary(c, "save", "", locales.CommonSave()).Disabled(cannot).Clicked() {
					a.saveProvider()
				}
			})
		})
	})
}

// chatgptRow signs in with ChatGPT, or shows who is signed in.
func (a *App) chatgptRow(c *ui.Context) {
	pal := theme.Of(c)
	if a.form.chatgpt != nil {
		ui.Row(c).Padding(10, 12).Gap(10).Radius(8).Background(pal.OkBg).Border(1, pal.OkBorder).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, theme.Icon("check")).FontSize(15).TextColor(pal.OkText)
			who := locales.SettingsSignedInAccount()
			if a.form.email != "" {
				who = locales.SettingsSignedInAs(a.form.email)
			}
			ui.Text(c, who).FontSize(13.5).TextColor(pal.OkText).Grow(1)
			if ghost(c, "again", "", locales.SettingsSwitchAccount()).Disabled(a.form.signingIn).Clicked() {
				a.chatgptSignIn()
			}
		})
		return
	}
	label := locales.SettingsChatGPTSignIn()
	if a.form.signingIn {
		label = locales.SettingsWaitingBrowser()
	}
	ui.Column(c).Gap(8).Children(func() {
		if primary(c, "signin", "user", label).Height(36).Disabled(a.form.signingIn).Clicked() {
			a.chatgptSignIn()
		}
		ui.Text(c, locales.SettingsChatGPTNote()).
			FontSize(12).TextColor(pal.TextMuted)
	})
}

func (a *App) chatgptSignIn() {
	a.form.signingIn, a.form.result = true, ""
	hub, had, gen := a.hub, a.form.chatgpt, a.form.gen
	ctx, cancel := context.WithCancel(context.Background())
	a.form.cancelSignIn()
	a.form.stopSignIn = cancel
	go func() {
		defer cancel()
		g, err := hub.SignInWithChatGPT(ctx, had, func(u string) { _ = mygo.Shell.OpenExternal(u) })
		a.update(func() {
			if !a.formIs(gen) {
				return
			}
			a.form.stopSignIn = nil
			a.form.signingIn = false
			if err != nil {
				a.form.result, a.form.resultOK = err.Error(), false
				return
			}
			a.form.chatgpt, a.form.email = g, g.Account().Email
			a.form.name = "ChatGPT"
			a.fetchModels()
		})
	}()
}

// modelPicker chooses a model from the provider's list, or takes any ID.
func (a *App) modelPicker(c *ui.Context) {
	const other = "\x00other"
	ui.Box(c).Grow(1).Children(func() {
		if a.form.custom || len(a.form.models) == 0 {
			ph := locales.SettingsModelID()
			if a.form.fetching {
				ph = locales.SettingsFetchingModels()
			}
			field(c, "model-id", &a.form.model, ph)
			return
		}
		options := append([]string{}, a.form.models...)
		if a.form.model != "" && !contains(options, a.form.model) {
			options = append([]string{a.form.model}, options...)
		}
		options = append(options, other)
		choice := a.form.model
		selectBox(c, "model", &choice, options, func(s string) string {
			if s == other {
				return locales.SettingsOtherModel()
			}
			if s == "" {
				return locales.SettingsChooseModel()
			}
			return s
		}, 0).FillWidth()
		if choice == other {
			a.form.custom, a.form.model = true, ""
		} else {
			a.form.model = choice
		}
	})
	if !a.form.custom && len(a.form.models) == 0 && !a.form.fetching {
		return
	}
	if a.form.custom && len(a.form.models) > 0 && ghost(c, "list", "", locales.SettingsFromList()).Clicked() {
		a.form.custom = false
		if len(a.form.models) > 0 {
			a.form.model = a.form.models[0]
		}
	}
}

// fetchModelsSoon lists the models once a pasted key or the address
// settles.
func (a *App) fetchModelsSoon() {
	if a.form.keyTimer != nil {
		a.form.keyTimer.Stop()
	}
	a.form.keyTimer = a.later(700*time.Millisecond, a.fetchModels)
}

// fetchModels lists the provider's models for the dropdown: from its
// server with the key typed here, from Leaflet's server for a saved cloud
// provider, or from the ChatGPT plan.
func (a *App) fetchModels() {
	preset := agents.PresetByID(a.form.preset)
	base, key, id, g := a.form.baseURL, strings.TrimSpace(a.form.apiKey), a.form.id, a.form.chatgpt
	switch {
	case preset.Auth == "account" && g == nil:
		return
	case preset.Auth == "key" && key == "" && (id == "" || !a.backend.SignedIn()):
		return
	}
	at, gen := base+"|"+key, a.form.gen
	a.form.fetching, a.form.fetchedAt = true, at
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var models []string
		var err error
		switch {
		case g != nil:
			models, err = g.ListModels(ctx)
		case key != "" || preset.Local:
			models, err = agents.NewClient(base, key).ListModels(ctx)
		default:
			var res map[string]any
			res, err = a.backend.Invoke(ctx, "providers", map[string]any{"action": "models", "provider_id": id})
			for _, m := range listAny(res["models"]) {
				models = append(models, fmt.Sprint(m))
			}
		}
		a.update(func() {
			if !a.formIs(gen) || a.form.fetchedAt != at {
				return // another dialog, or another fetch since
			}
			a.form.fetching = false
			if a.form.baseURL+"|"+strings.TrimSpace(a.form.apiKey) != at {
				return // the key or address changed since, not yet fetched for
			}
			if err != nil {
				if !preset.Local || a.form.id != "" {
					a.form.result, a.form.resultOK = locales.SettingsListModelsFailed(err.Error()), false
				}
				return
			}
			models = agents.ChatModels(models, preset.Suggested)
			if len(models) == 0 {
				return
			}
			a.form.models = models
			if a.form.model == "" || !contains(models, a.form.model) && !a.form.custom {
				a.form.model = models[0]
			}
			if a.form.result != "" && !a.form.resultOK {
				a.form.result = ""
			}
		})
	}()
}

func listAny(v any) []any {
	l, _ := v.([]any)
	return l
}

// providerForm is the provider of the dialog, to test or save.
func (a *App) providerForm() agents.Form {
	f := &a.form
	return agents.Form{
		ID: f.id, Preset: f.preset, Name: f.name, BaseURL: f.baseURL, APIKey: f.apiKey, Model: f.model,
		Models: f.models, SupportsTools: f.tools, ChatGPT: f.chatgpt,
	}
}

func (a *App) testProvider() {
	f := a.providerForm()
	a.form.testing, a.form.result = true, ""
	hub, gen := a.hub, a.form.gen
	go func() {
		res := hub.TestProvider(f)
		a.update(func() {
			if !a.formIs(gen) {
				return
			}
			a.form.testing = false
			a.form.result, a.form.resultOK = res.Message, res.OK
			if res.OK {
				t := res.SupportsTools
				a.form.tools = &t
				if len(res.Models) > 0 {
					a.form.models = res.Models
				}
			}
		})
	}()
}

// saveProvider saves the dialog's provider, off the main thread: to the
// keychain, or the server. The first is the default agent.
func (a *App) saveProvider() {
	f := a.providerForm()
	a.form.saving, a.form.errorText = true, ""
	hub, gen := a.hub, a.form.gen
	var p store.Provider
	var err error
	a.offMain(func() { p, err = hub.SaveProvider(f) }, func() {
		// Only the dialog it was saved from shows how it went, or closes:
		// one opened since is another provider's.
		if err != nil {
			if a.formIs(gen) {
				a.form.saving, a.form.errorText = false, err.Error()
			}
			return
		}
		if a.settings.DefaultModel == "" {
			a.settings.DefaultModel = store.ModelChoice{ProviderID: p.ID}.Encode()
			a.saveSettings()
		}
		if a.formIs(gen) {
			a.form.saving, a.form.open = false, false
		}
	})
}

// removeAlert asks before the provider of the dialog goes, with its key.
func (a *App) removeAlert(c *ui.Context) {
	if !a.form.confirmRemove {
		return
	}
	p, err := a.st.Provider(a.form.id)
	if err != nil {
		a.form.confirmRemove = false
		return
	}
	msg := locales.SettingsRemoveProviderCloud()
	switch {
	case p.AuthKind == "account":
		msg = locales.SettingsRemoveProviderAccount()
	case p.IsLocal:
		msg = locales.SettingsRemoveProviderLocal()
	}
	if ui.AlertDialog(c, &a.form.confirmRemove, locales.SettingsRemoveProviderTitle(p.Name), msg,
		locales.CommonCancel(), locales.CommonRemove()) == 1 {
		a.removeProvider(p.ID)
	}
}

// removeProvider removes a provider and its key, off the main thread.
// One kept on the server goes there first, and until the server has
// deleted it, it stays, and the dialog says why.
func (a *App) removeProvider(id string) {
	a.form.removing, a.form.errorText = true, ""
	hub, gen := a.hub, a.form.gen
	var err error
	a.offMain(func() { err = hub.RemoveProvider(id) }, func() {
		if a.formIs(gen) {
			a.form.removing = false
		}
		if err != nil {
			if a.formIs(gen) {
				a.form.errorText = err.Error()
			} else {
				a.toast(err.Error())
			}
			return
		}
		if ch, ok := a.settings.Default(); ok && ch.ProviderID == id {
			a.settings.DefaultModel = ""
			a.saveSettings()
		}
		if a.form.id == id {
			a.form.open = false
		}
	})
}

// Sync and storage ---------------------------------------------------

func (a *App) offlineSettings(c *ui.Context) {
	heading(c, locales.SettingsSectionOffline(), locales.SettingsOfflineDesc())
	if a.hasServer() || a.session.SignedIn || icloud.Available() || a.settings.ICloudSync {
		a.syncSettings(c)
	} else {
		card(c, "status", func() {
			settingRow(c, "local", locales.SettingsLocalOnly(), locales.SettingsLocalOnlyDesc(), true, nil)
		})
	}
	if len(a.conflicts) > 0 {
		label(c, locales.SettingsConflicts())
		card(c, "conflicts", func() {
			for i, cf := range a.conflicts {
				s := core.SidesOf(cf)
				// A block one side deleted and the other edited was kept,
				// and can be deleted again.
				deleted := s.Deleted != ""
				var desc string
				switch s.Deleted {
				case "here":
					desc = locales.SettingsConflictDeletedHere(s.Kept)
				case "there":
					desc = locales.SettingsConflictDeletedThere(s.Kept)
				default:
					desc = locales.SettingsConflictKeptOver(s.Kept, s.Other)
				}
				settingRow(c, cf.ID, a.pageTitleOf(cf.PageID), desc, i == 0, func() {
					ui.MenuButton(c.Key("act"), locales.SettingsResolve(), func(m *ui.Menu) {
						if deleted {
							if m.Item(locales.SettingsDeleteIt()).Chosen() {
								a.resolveConflict(cf.ID, core.DeleteBlock)
							}
							if m.Item(locales.SettingsKeepIt()).Chosen() {
								a.resolveConflict(cf.ID, core.KeepMerged)
							}
						}
						// The version the merge did not keep can come back,
						// whichever device's it is.
						if !deleted && !s.KeptHere && m.Item(locales.SettingsUseThisDevice()).Chosen() {
							a.resolveConflict(cf.ID, core.UseThisDevice)
						}
						if !deleted && s.KeptHere && m.Item(locales.SettingsUseOtherDevice()).Chosen() {
							a.resolveConflict(cf.ID, core.UseOtherDevice)
						}
						if !deleted && m.Item(locales.SettingsKeepNewer()).Chosen() {
							a.resolveConflict(cf.ID, core.KeepMerged)
						}
						if m.Item(locales.SettingsOpenPage()).Chosen() {
							a.openPage(cf.PageID)
						}
					})
				})
			}
		})
	}
	label(c, locales.SettingsStorage())
	card(c, "storage", func() {
		settingRow(c, "where", locales.SettingsDataFolder(), locales.SettingsDataFolderDesc(), true, func() {
			if button(c, "reveal", "folder-open", locales.SettingsShow()).Clicked() {
				mygo.Shell.ShowItemInFolder(a.st.Dir)
			}
		})
		settingRow(c, "images", locales.SettingsImageCache(), locales.SettingsImageCacheDesc(), false, func() {
			if button(c, "clear", "trash", locales.SettingsClear()).Clicked() {
				a.images.close()
				a.images = newImageCache(a.images.root)
				c.Toast(locales.SettingsImageCacheCleared())
			}
		})
	})
}

// syncSettings are the parts of Sync and storage about syncing: through
// the server, or through iCloud Drive.
func (a *App) syncSettings(c *ui.Context) {
	card(c, "status", func() {
		first := true
		if icloud.Available() || a.settings.ICloudSync {
			on := a.settings.ICloudSync
			desc := locales.SettingsICloudDesc()
			if on {
				desc = locales.SettingsICloudOnDesc()
			}
			settingRow(c, "icloud", locales.SettingsICloud(), desc, true, func() {
				// One way of syncing at a time: Supabase's, signed in.
				tip := ""
				if a.session.SignedIn {
					tip = locales.SettingsICloudSupabaseOn()
				}
				tipped(c, "icloud-tip", tip, func() {
					if ui.Switch(c.Key("icloud"), &on).Disabled(a.icloudBusy || a.session.SignedIn).Changed() {
						a.setICloud(on)
					}
				})
			})
			first = false
		}
		if !a.syncOn() {
			if a.hasServer() {
				settingRow(c, "sync", locales.SettingsNotSyncing(), locales.SettingsNotSyncingDesc(), first, func() {
					a.signInButton(c)
				})
			}
			return
		}
		status := locales.SettingsUpToDate()
		switch {
		case a.settings.WorkOffline:
			status = locales.SettingsOfflineByChoice()
		case a.offlineNow():
			status = locales.SettingsNoConnection()
		case a.syncing:
			status = locales.SettingsSyncing()
		case a.syncError != "":
			status = locales.SettingsSyncFailed(a.syncError)
		}
		title := locales.SettingsSynced()
		// As counted as the outbox changes: reading it here, at every
		// frame, read every change's whole payload.
		if n := a.pending; n > 0 {
			title = locales.SettingsChangesWaiting(n)
		}
		settingRow(c, "sync", title, status, first, func() {
			if errors.Is(a.syncErr, icloud.ErrNoAccess) && runtime.GOOS == "darwin" {
				if button(c, "privacy", "settings", locales.SettingsICloudOpenPrivacy()).Clicked() {
					openPrivacySettings()
				}
			}
			if button(c, "sync-now", "refresh-cw", locales.SettingsSyncNow()).Disabled(a.syncing).Clicked() {
				go func() { _ = a.sync.SyncNow() }()
			}
		})
		wo := a.settings.WorkOffline
		settingRow(c, "work-offline", locales.SettingsWorkOffline(), locales.SettingsWorkOfflineDesc(), false, func() {
			if ui.Switch(c.Key("wo"), &wo).Changed() {
				a.settings.WorkOffline = wo
				a.saveSettings()
				if !wo {
					a.sync.Kick()
				}
			}
		})
		all := a.settings.AllPagesOffline
		settingRow(c, "all", locales.SettingsAllPagesOffline(), locales.SettingsAllPagesOfflineDesc(), false, func() {
			if ui.Switch(c.Key("all"), &all).Changed() {
				a.core().SetAllPagesOffline(all)
			}
		})
		keep := a.settings.KeepAudioOnDevice
		settingRow(c, "audio", locales.SettingsKeepAudio(), "", false, func() {
			if selectBox(c, "audio", &keep, []string{"until_uploaded", "30_days", "always"}, func(s string) string {
				return map[string]string{"until_uploaded": locales.SettingsKeepAudioUntilUploaded(), "30_days": locales.SettingsKeepAudio30Days(), "always": locales.SettingsKeepAudioAlways()}[s]
			}, 170); keep != a.settings.KeepAudioOnDevice {
				a.settings.KeepAudioOnDevice = keep
				a.saveSettings()
			}
		})
	})
}

// resolveConflict settles conflict id as how says
// (core.Pages.ResolveConflict), saying why when it can't.
func (a *App) resolveConflict(id string, how core.Resolution) {
	if err := a.pageCore().ResolveConflict(id, how); err != nil {
		a.toast(locales.SettingsResolveFailed(err.Error()))
	}
}
