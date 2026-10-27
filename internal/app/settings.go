package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
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
		{"offline", "cloud-download", locales.SettingsSectionOffline()},
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
	name       string
	nameLoaded bool
	devices    []map[string]any
	loaded     bool
	deleteOpen bool
	confirm    string
	busy       bool
	// signingOut is set while waiting changes go up before signing out;
	// unsynced counts those that could not, to ask about.
	signingOut bool
	unsynced   int
}

var account accountState

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
}

// accentDots is a row of dots choosing the accent: the desktop's, where
// it has one, then Leaflet's.
func (a *App) accentDots(c *ui.Context) {
	pal := theme.Of(c)
	isDark := c.Theme().Dark
	ui.Row(c.Key("accents")).Gap(8).Children(func() {
		dot := func(name, label string, color ui.Color) {
			d := ui.ButtonBase(c.Key(name)).Size(20, 20).Radius(999).Background(color).Label(label).Tooltip(label)
			chosen := a.settings.Accent == name || a.settings.Accent == "" && name == theme.Accents[0].Name
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
		if system, ok := theme.Accent("system", isDark, c.Preferences().Accent); ok {
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
	pal := theme.Of(c)
	s := &account
	if !s.nameLoaded {
		s.name, s.nameLoaded = a.session.DisplayName, true
	}
	heading(c, locales.SettingsSectionAccount(), locales.SettingsAccountDesc())
	card(c, "profile", func() {
		settingRow(c, "name", locales.SettingsName(), locales.SettingsNameDesc(), true, func() {
			in := field(c, "name", &s.name, locales.SettingsYourName()).Width(220)
			if in.Submitted() || (in.Changed() && false) {
				a.saveName(s.name)
			}
			if !in.Focused() && s.name != a.session.DisplayName && strings.TrimSpace(s.name) != "" {
				a.saveName(s.name)
			}
		})
		// A build without a server says so once, in Offline and storage.
		if !a.backend.Configured() {
			return
		}
		title, desc := locales.SettingsSync(), locales.SettingsSyncDesc()
		if a.session.SignedIn {
			title, desc = locales.SettingsSignedIn(), a.session.Email
		}
		settingRow(c, "email", title, desc, false, func() {
			switch {
			case a.session.SignedIn:
				if button(c, "signout", "log-out", locales.CommonSignOut()).Disabled(s.signingOut).Clicked() {
					a.signOut()
				}
			case a.backend.Configured():
				if primary(c, "signin", "", locales.CommonSignIn()).Clicked() {
					a.navigate(Route{Kind: "signin"})
				}
			}
		})
	})
	if a.session.SignedIn {
		label(c, locales.SettingsDevices())
		if !s.loaded {
			s.loaded = true
			go a.loadDevices()
		}
		card(c, "devices", func() {
			if len(s.devices) == 0 {
				settingRow(c, "none", locales.SettingsDevicesLoading(), "", true, nil)
			}
			me := a.st.DeviceID()
			for i, d := range s.devices {
				id := store.S(d, "id")
				desc := locales.SettingsDeviceLastActive(humanTime(store.Time(d["last_active_at"])))
				if id == me {
					desc = locales.SettingsDeviceThis()
				}
				if d["revoked_at"] != nil {
					desc = locales.SettingsDeviceSignedOut()
				}
				settingRow(c, "dev:"+id, store.S(d, "name"), desc, i == 0, func() {
					if id != me && d["revoked_at"] == nil && button(c, "revoke", "", locales.CommonSignOut()).Clicked() {
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer cancel()
							if _, err := a.backend.Upsert(ctx, "devices", map[string]any{"id": id, "user_id": a.backend.UserID(), "revoked_at": time.Now().UTC().Format(time.RFC3339)}, ""); err != nil {
								a.update(func() { a.toast(locales.SettingsDeviceSignOutFailed(err.Error())) })
								return
							}
							a.loadDevices()
						}()
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
	if s.unsynced > 0 {
		open := true
		dialog(c, &open, 420, func() {
			dialogHeader(c, locales.SettingsSignOutAnyway(), locales.SettingsSignOutUnsynced(s.unsynced), &open)
			ui.Row(c).Padding(0, 20, 20).Justify(ui.End).Gap(8).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					open = false
				}
				if danger(c, "go", locales.CommonSignOut()).Clicked() {
					open = false
					a.leave(a.st.ForgetAccount)
				}
			})
		})
		if !open {
			s.unsynced = 0
		}
	}
	if s.deleteOpen {
		dialog(c, &s.deleteOpen, 420, func() {
			dialogHeader(c, locales.SettingsDeleteAccountTitle(), locales.SettingsCannotUndo(), &s.deleteOpen)
			ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
				ui.Text(c, locales.SettingsTypeDelete()).FontSize(13).TextColor(pal.TextMuted)
				field(c, "confirm", &s.confirm, "DELETE").AutoFocus()
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
						s.deleteOpen = false
					}
					if danger(c, "go", locales.SettingsDeleteAccount()).Clicked() && s.confirm == "DELETE" && !s.busy {
						s.busy = true
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
							defer cancel()
							_, err := a.backend.Invoke(ctx, "account", map[string]any{"action": "delete"})
							a.update(func() {
								s.busy = false
								if err != nil {
									a.toast(locales.SettingsDeleteAccountFailed(err.Error()))
									return
								}
								s.deleteOpen = false
								a.leave(a.st.Wipe)
								a.toast(locales.SettingsAccountDeleted())
							})
						}()
					}
				})
			})
		})
	}
}

func (a *App) saveName(name string) {
	name = strings.TrimSpace(name)
	if name == "" || name == a.session.DisplayName {
		return
	}
	if err := a.st.SetDisplayName(name); err != nil {
		account.name = a.session.DisplayName
		a.toast(locales.SettingsSaveFailed(err.Error()))
		return
	}
	a.session = a.st.Session()
	if a.backend.SignedIn() {
		go func() {
			err := a.backend.UpdateUser(map[string]any{"display_name": name})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, uerr := a.backend.Upsert(ctx, "profiles", map[string]any{"id": a.backend.UserID(), "display_name": name}, "")
			if err == nil {
				err = uerr
			}
			if err != nil {
				a.update(func() { a.toast(locales.SettingsNameServerFailed(err.Error())) })
			}
		}()
	}
}

func (a *App) loadDevices() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rows, err := a.backend.Select(ctx, "devices", remoteQuery("last_active_at.desc"))
	if err != nil {
		return
	}
	a.update(func() { account.devices = rows })
}

func (a *App) exportAll() {
	a.flush()
	go func() {
		path, _ := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: locales.SettingsExportAll(), DefaultPath: locales.SettingsExportFileName()})
		if path == "" {
			return
		}
		err := a.writeExport(path)
		a.update(func() {
			if err != nil {
				a.toast(locales.SettingsExportFailed(err.Error()))
			} else {
				a.toast(locales.SettingsExported(filepath.Base(path)))
			}
		})
	}()
}

// signOut uploads the changes waiting, then signs out and removes the
// account's pages from this device. Changes that could not go up are
// asked about first.
func (a *App) signOut() {
	a.flush()
	account.signingOut = true
	go func() {
		_ = a.sync.SyncNow()
		n := a.st.PendingCount()
		a.update(func() {
			account.signingOut = false
			if n > 0 {
				account.unsynced = n
				return
			}
			a.leave(a.st.ForgetAccount)
		})
	}()
}

// leave signs out and clears this device with clear, between sync passes.
func (a *App) leave(clear func() error) {
	a.flush()
	go func() {
		var err error
		a.sync.Hold(func() {
			a.backend.SignOut()
			err = clear()
		})
		a.update(func() {
			if err != nil {
				a.toast(locales.SettingsRemovePagesFailed(err.Error()))
			}
			if _, err := a.st.LoadSession(nil); err != nil {
				a.toast(err.Error())
			}
			a.session = a.st.Session()
			account.loaded, account.devices, account.nameLoaded = false, nil, false
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
	// A ChatGPT sign-in, before it is saved.
	signingIn bool
	chatgpt   *agents.ChatGPT
	email     string
	keyTimer  *time.Timer
}

var form providerForm

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
				if g, err := a.hub.ChatGPT(p.ID); err == nil && g.Account().Email != "" {
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
			pp := p
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
				a.editProvider(&pp)
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
			var choices []string
			names := map[string]string{}
			for _, o := range a.hub.Options() {
				for _, m := range agents.Models(o.Provider) {
					ch := store.ModelChoice{ProviderID: o.Provider.ID, Model: m}.Encode()
					choices = append(choices, ch)
					names[ch] = o.Name() + " · " + m
				}
			}
			cur := a.settings.DefaultModel
			if cur == "" && len(choices) > 0 {
				cur = choices[0]
			}
			settingRow(c, "default", locales.SettingsDefaultAgent(), locales.SettingsDefaultAgentDesc(), true, func() {
				if selectBox(c, "default", &cur, choices, func(s string) string { return names[s] }, 240); cur != a.settings.DefaultModel {
					a.settings.DefaultModel = cur
					a.saveSettings()
				}
			})
			v := a.settings.LocalWhenOffline
			settingRow(c, "offline", locales.SettingsLocalWhenOffline(), locales.SettingsLocalWhenOfflineDesc(), false, func() {
				if ui.Switch(c.Key("offline"), &v).Changed() {
					a.settings.LocalWhenOffline = v
					a.saveSettings()
				}
			})
			r := a.settings.AgentsReadOtherPages
			settingRow(c, "cross", locales.SettingsReadOtherPages(), locales.SettingsReadOtherPagesDesc(), false, func() {
				if ui.Switch(c.Key("cross"), &r).Changed() {
					a.settings.AgentsReadOtherPages = r
					a.saveSettings()
				}
			})
		})
	}
	if form.open {
		a.providerDialog(c)
	}
}

func (a *App) newProvider(p agents.Preset) {
	form = providerForm{open: true, preset: p.ID, name: p.Name, baseURL: p.BaseURL, models: p.Suggested}
	if len(p.Suggested) > 0 {
		form.model = p.Suggested[0]
	}
	if p.Auth == "none" {
		// A server on this machine lists its models without a key.
		a.fetchModels()
	}
}

func (a *App) editProvider(p *store.Provider) {
	form = providerForm{open: true, id: p.ID, preset: p.Preset, name: p.Name, baseURL: p.BaseURL, model: p.DefaultModel,
		models: agents.Models(*p), tools: p.SupportsTools}
	if p.IsLocal && p.AuthKind != "account" {
		form.apiKey = agents.LocalKey(p.ID)
	}
	if p.AuthKind == "account" {
		if g, err := a.hub.ChatGPT(p.ID); err == nil {
			form.chatgpt, form.email = g, g.Account().Email
		}
	}
	a.fetchModels()
}

func (a *App) providerDialog(c *ui.Context) {
	pal := theme.Of(c)
	preset := agents.PresetByID(form.preset)
	account := preset.Auth == "account"
	local := preset.Local
	title := locales.SettingsConnectProvider(preset.Name)
	if form.id != "" {
		title = locales.SettingsProviderEdit(form.name)
	}
	desc := locales.SettingsProviderDescCloud()
	switch {
	case account:
		desc = locales.SettingsProviderDescAccount()
	case local:
		desc = locales.SettingsProviderDescLocal()
	case !a.backend.Configured():
		desc = locales.SettingsProviderDescNoServer()
	case !a.session.SignedIn:
		desc = locales.SettingsProviderDescSignIn()
	}
	if preset.ID == "claude" {
		desc = locales.SettingsProviderClaudeNote(desc)
	}
	dialog(c, &form.open, 500, func() {
		dialogHeader(c, title, desc, &form.open)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			fieldRow := func(name string, fn func()) {
				ui.Column(c).Gap(5).Children(func() {
					label(c, name)
					fn()
				})
			}
			if account {
				a.chatgptRow(c)
			} else {
				fieldRow(locales.SettingsName(), func() { field(c, "name", &form.name, preset.Name) })
				fieldRow(locales.SettingsBaseURL(), func() {
					ph := preset.Placeholder
					if ph == "" {
						ph = preset.BaseURL
					}
					field(c, "url", &form.baseURL, ph)
				})
			}
			testButton := func() {
				label := locales.SettingsTest()
				if form.testing {
					label = locales.SettingsTesting()
				}
				if button(c, "test", "", label).MinWidth(82).Justify(ui.Center).Disabled(form.testing || (account && form.chatgpt == nil)).Clicked() {
					a.testProvider()
				}
			}
			if !account {
				fieldRow(locales.SettingsAPIKey(), func() {
					ph := locales.SettingsPasteKey()
					if form.id != "" && !local {
						ph = locales.SettingsKeySaved()
					}
					if local {
						ph = locales.SettingsOptional()
					}
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						var in ui.Element
						ui.Box(c).Grow(1).Children(func() { in = password(c, "key", &form.apiKey, ph) })
						if in.Changed() {
							a.fetchModelsSoon()
						}
						testButton()
					})
				})
			}
			if form.result != "" {
				kind := "danger"
				if form.resultOK {
					kind = "ok"
				}
				notice(c, kind, map[bool]string{true: "check", false: "circle-alert"}[form.resultOK], form.result, nil)
			}
			if !account || form.chatgpt != nil {
				fieldRow(locales.CommonModel(), func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						a.modelPicker(c)
						if account {
							testButton()
						}
					})
				})
			}
			if form.errorText != "" {
				ui.Text(c, form.errorText).FontSize(12.5).TextColor(pal.DangerText)
			}
			// Nothing to save before signing in: an existing sign-in can
			// only be removed.
			signedOut := account && form.chatgpt == nil
			if signedOut && form.id == "" {
				return
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if form.id != "" && danger(c, "remove", locales.CommonRemove()).Clicked() {
					a.removeProvider(form.id)
				}
				ui.Spacer(c)
				if signedOut {
					return
				}
				cannot := form.saving || (!local && !a.session.SignedIn) || (account && form.chatgpt == nil) || strings.TrimSpace(form.model) == ""
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
	if form.chatgpt != nil {
		ui.Row(c).Padding(10, 12).Gap(10).Radius(8).Background(pal.OkBg).Border(1, pal.OkBorder).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, theme.Icon("check")).FontSize(15).TextColor(pal.OkText)
			who := locales.SettingsSignedInAccount()
			if form.email != "" {
				who = locales.SettingsSignedInAs(form.email)
			}
			ui.Text(c, who).FontSize(13.5).TextColor(pal.OkText).Grow(1)
			if ghost(c, "again", "", locales.SettingsSwitchAccount()).Clicked() {
				a.chatgptSignIn()
			}
		})
		return
	}
	label := locales.SettingsChatGPTSignIn()
	if form.signingIn {
		label = locales.SettingsWaitingBrowser()
	}
	ui.Column(c).Gap(8).Children(func() {
		if primary(c, "signin", "user", label).Height(36).Disabled(form.signingIn).Clicked() {
			a.chatgptSignIn()
		}
		ui.Text(c, locales.SettingsChatGPTNote()).
			FontSize(12).TextColor(pal.TextMuted)
	})
}

func (a *App) chatgptSignIn() {
	form.signingIn, form.result = true, ""
	host := a.hub.HostID()
	clientID := ""
	if form.chatgpt != nil {
		clientID = form.chatgpt.Account().ClientID
	} else if id, ok := a.st.Value("chatgpt.client"); ok {
		clientID = id
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		g, err := agents.SignInChatGPT(ctx, host, clientID, func(u string) { _ = mygo.Shell.OpenExternal(u) })
		a.update(func() {
			form.signingIn = false
			if err != nil {
				form.result, form.resultOK = err.Error(), false
				return
			}
			_ = a.st.SetValue("chatgpt.client", g.Account().ClientID)
			form.chatgpt, form.email = g, g.Account().Email
			form.name = "ChatGPT"
			a.fetchModels()
		})
	}()
}

// modelPicker chooses a model from the provider's list, or takes any ID.
func (a *App) modelPicker(c *ui.Context) {
	const other = "\x00other"
	ui.Box(c).Grow(1).Children(func() {
		if form.custom || len(form.models) == 0 {
			ph := locales.SettingsModelID()
			if form.fetching {
				ph = locales.SettingsFetchingModels()
			}
			field(c, "model-id", &form.model, ph)
			return
		}
		options := append([]string{}, form.models...)
		if form.model != "" && !contains(options, form.model) {
			options = append([]string{form.model}, options...)
		}
		options = append(options, other)
		choice := form.model
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
			form.custom, form.model = true, ""
		} else {
			form.model = choice
		}
	})
	if !form.custom && len(form.models) == 0 && !form.fetching {
		return
	}
	if form.custom && len(form.models) > 0 && ghost(c, "list", "", locales.SettingsFromList()).Clicked() {
		form.custom = false
		if len(form.models) > 0 {
			form.model = form.models[0]
		}
	}
}

// fetchModelsSoon lists the models once a pasted key settles.
func (a *App) fetchModelsSoon() {
	if form.keyTimer != nil {
		form.keyTimer.Stop()
	}
	form.keyTimer = time.AfterFunc(700*time.Millisecond, func() { a.update(a.fetchModels) })
}

// fetchModels lists the provider's models for the dropdown: from its
// server with the key typed here, from Leaflet's server for a saved cloud
// provider, or from the ChatGPT plan.
func (a *App) fetchModels() {
	preset := agents.PresetByID(form.preset)
	base, key, id, g := form.baseURL, strings.TrimSpace(form.apiKey), form.id, form.chatgpt
	switch {
	case preset.Auth == "account" && g == nil:
		return
	case preset.Auth == "key" && key == "" && (id == "" || !a.backend.SignedIn()):
		return
	}
	at := base + "|" + key
	form.fetching, form.fetchedAt = true, at
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
			if form.fetchedAt != at {
				return // the key or address changed since
			}
			form.fetching = false
			if err != nil {
				if !preset.Local || form.id != "" {
					form.result, form.resultOK = locales.SettingsListModelsFailed(err.Error()), false
				}
				return
			}
			models = agents.ChatModels(models, preset.Suggested)
			if len(models) == 0 {
				return
			}
			form.models = models
			if form.model == "" || !contains(models, form.model) && !form.custom {
				form.model = models[0]
			}
			if form.result != "" && !form.resultOK {
				form.result = ""
			}
		})
	}()
}

func listAny(v any) []any {
	l, _ := v.([]any)
	return l
}

func (a *App) testProvider() {
	preset := agents.PresetByID(form.preset)
	base, key, id, model, g := form.baseURL, strings.TrimSpace(form.apiKey), form.id, form.model, form.chatgpt
	form.testing, form.result = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		var res agents.TestResult
		switch {
		case g != nil:
			if model == "" {
				res = agents.TestResult{Message: locales.SettingsChooseModelToTest()}
			} else {
				res = agents.TestModel(ctx, g, model, nil)
			}
		case preset.Local || key != "":
			// A key typed here is tried from this device, before it goes
			// to the server.
			res = agents.NewClient(base, key).Test(ctx, model)
		case id != "":
			r, err := a.backend.Invoke(ctx, "providers", map[string]any{"action": "test", "provider_id": id, "model": model})
			if err != nil {
				res = agents.TestResult{Message: err.Error()}
			} else {
				res.OK, _ = r["ok"].(bool)
				res.SupportsTools, _ = r["supports_tools"].(bool)
				res.Message, _ = r["message"].(string)
			}
		default:
			res = agents.TestResult{Message: locales.SettingsPasteKeyToTest()}
		}
		a.update(func() {
			form.testing = false
			form.result, form.resultOK = res.Message, res.OK
			if res.OK {
				t := res.SupportsTools
				form.tools = &t
				if ms := agents.ChatModels(res.Models, preset.Suggested); len(ms) > 0 {
					form.models = ms
				}
			}
		})
	}()
}

func (a *App) saveProvider() {
	preset := agents.PresetByID(form.preset)
	if preset.Auth != "account" && strings.TrimSpace(form.baseURL) == "" || strings.TrimSpace(form.model) == "" {
		form.errorText = locales.SettingsNeedURLAndModel()
		return
	}
	if preset.Local {
		p := store.Provider{ID: form.id, Preset: form.preset, Name: strings.TrimSpace(form.name), BaseURL: agents.NormalizeBaseURL(form.baseURL),
			AuthKind: "none", DefaultModel: form.model, IsLocal: true, SupportsTools: form.tools, Models: form.models}
		if p.ID == "" {
			p.ID = doc.NewRowID()
		}
		if p.Name == "" {
			p.Name = preset.Name
		}
		if form.apiKey != "" {
			p.AuthKind = "api_key"
		}
		if preset.Auth == "account" {
			p.AuthKind, p.BaseURL = "account", preset.BaseURL
			if err := agents.UseChatGPT(p.ID, form.chatgpt); err != nil {
				form.errorText = locales.SettingsKeepSignInFailed(err.Error())
				return
			}
		}
		if old, err := a.st.Provider(p.ID); err == nil {
			p.CreatedAt = old.CreatedAt
		}
		if err := a.st.PutProvider(p); err != nil {
			form.errorText = err.Error()
			return
		}
		if preset.Auth != "account" {
			if err := agents.SetLocalKey(p.ID, form.apiKey); err != nil {
				form.errorText = locales.SettingsSaveKeyFailed(err.Error())
				return
			}
		}
		if a.settings.DefaultModel == "" {
			a.settings.DefaultModel = store.ModelChoice{ProviderID: p.ID, Model: p.DefaultModel}.Encode()
			a.saveSettings()
		}
		form.open = false
		return
	}
	body := map[string]any{"preset": form.preset, "name": strings.TrimSpace(form.name), "base_url": agents.NormalizeBaseURL(form.baseURL),
		"auth_kind": "api_key", "default_model": form.model}
	if form.id != "" {
		body["id"] = form.id
	}
	if form.apiKey != "" {
		body["api_key"] = form.apiKey
	}
	form.saving = true
	tools := form.tools
	models := form.models
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := a.backend.Invoke(ctx, "providers", map[string]any{"action": "save", "provider": body})
		a.update(func() {
			form.saving = false
			if err != nil {
				form.errorText = err.Error()
				return
			}
			raw, _ := json.Marshal(res["provider"])
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				form.errorText = err.Error()
				return
			}
			p := store.ProviderFromRemote(row)
			if tools != nil {
				// What the dialog knows, from the stored value or a test
				// run in it, over what the server last heard.
				p.SupportsTools = tools
			}
			if len(p.Models) == 0 {
				p.Models = models
			}
			if err := a.st.PutProvider(p); err != nil {
				form.errorText = err.Error()
				return
			}
			if a.settings.DefaultModel == "" {
				a.settings.DefaultModel = store.ModelChoice{ProviderID: p.ID, Model: p.DefaultModel}.Encode()
				a.saveSettings()
			}
			form.open = false
		})
	}()
}

func (a *App) removeProvider(id string) {
	p, err := a.st.Provider(id)
	if err != nil {
		return
	}
	if !p.IsLocal && a.backend.SignedIn() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := a.backend.Invoke(ctx, "providers", map[string]any{"action": "delete", "id": id}); err != nil {
				a.update(func() { a.toast(locales.SettingsDeleteFailed(err.Error())) })
			}
		}()
	}
	if p.AuthKind == "account" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			agents.ForgetChatGPT(ctx, id)
		}()
	}
	if err := agents.SetLocalKey(id, ""); err != nil {
		a.toast(locales.SettingsDeleteKeyFailed(err.Error()))
	}
	if err := a.st.DeleteProvider(id); err != nil {
		a.toast(locales.SettingsDeleteFailed(err.Error()))
		return
	}
	if ch, ok := a.settings.Default(); ok && ch.ProviderID == id {
		a.settings.DefaultModel = ""
		a.saveSettings()
	}
	form.open = false
}

// Offline and storage ---------------------------------------------------

type offlineState struct {
	queue  []store.OutboxEntry
	loaded time.Time
}

var offline offlineState

func (a *App) offlineSettings(c *ui.Context) {
	heading(c, locales.SettingsSectionOffline(), locales.SettingsOfflineDesc())
	if a.backend.Configured() {
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
				cf := cf
				kept, other := cf.LocalNode, cf.RemoteNode
				if cf.Kept == "remote" {
					kept, other = cf.RemoteNode, cf.LocalNode
				}
				// A block one side deleted and the other edited was kept,
				// and can be deleted again.
				deleted := other == nil
				desc := ""
				switch {
				case cf.LocalNode == nil:
					desc = locales.SettingsConflictDeletedHere(short(kept.Delta().Plain(), 40))
				case cf.RemoteNode == nil:
					desc = locales.SettingsConflictDeletedThere(short(kept.Delta().Plain(), 40))
				default:
					desc = locales.SettingsConflictKeptOver(short(kept.Delta().Plain(), 40), short(other.Delta().Plain(), 40))
				}
				settingRow(c, cf.ID, a.pageTitleOf(cf.PageID), desc, i == 0, func() {
					ui.MenuButton(c.Key("act"), locales.SettingsResolve(), func(m *ui.Menu) {
						if deleted {
							if m.Item(locales.SettingsDeleteIt()).Chosen() {
								a.resolveConflict(cf, nil)
							}
							if m.Item(locales.SettingsKeepIt()).Chosen() {
								if err := a.st.ResolveConflict(cf.ID); err != nil {
									a.toast(locales.SettingsResolveFailed(err.Error()))
								}
							}
						}
						if !deleted && m.Item(locales.SettingsUseThisDevice()).Chosen() {
							a.resolveConflict(cf, cf.LocalNode)
						}
						if !deleted && m.Item(locales.SettingsKeepNewer()).Chosen() {
							if err := a.st.ResolveConflict(cf.ID); err != nil {
								a.toast(locales.SettingsResolveFailed(err.Error()))
							}
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
				a.images = newImageCache(a.images.dir)
				_ = os.RemoveAll(a.images.dir)
				c.Toast(locales.SettingsImageCacheCleared())
			}
		})
	})
}

// syncSettings are the parts of Offline and storage about the server.
func (a *App) syncSettings(c *ui.Context) {
	if time.Since(offline.loaded) > 2*time.Second {
		offline.queue, _ = a.st.Pending()
		offline.loaded = time.Now()
		c.After(2 * time.Second)
	}
	card(c, "status", func() {
		if !a.session.SignedIn {
			settingRow(c, "sync", locales.SettingsNotSyncing(), locales.SettingsNotSyncingDesc(), true, func() {
				if primary(c, "signin", "", locales.CommonSignIn()).Clicked() {
					a.navigate(Route{Kind: "signin"})
				}
			})
			return
		}
		status := locales.SettingsUpToDate()
		switch {
		case a.settings.WorkOffline:
			status = locales.SettingsOfflineByChoice()
		case !a.online:
			status = locales.SettingsNoConnection()
		case a.syncing:
			status = locales.SettingsSyncing()
		case a.syncError != "":
			status = locales.SettingsSyncFailed(a.syncError)
		}
		title := locales.SettingsSynced()
		if n := len(offline.queue); n > 0 {
			title = locales.SettingsChangesWaiting(n)
		}
		settingRow(c, "sync", title, status, true, func() {
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
				a.settings.AllPagesOffline = all
				a.saveSettings()
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

// resolveConflict puts a version of a block back, or deletes the block
// for a nil node.
func (a *App) resolveConflict(cf store.ConflictRow, node *doc.Node) {
	apply := func(content *doc.Content) {
		if node == nil {
			content.Remove(cf.BlockID)
			if len(content.Children) == 0 {
				content.Children = doc.Empty().Children
			}
			return
		}
		if content.Find(cf.BlockID) != nil {
			_ = content.Replace(cf.BlockID, node.Clone())
		} else {
			_ = content.Insert(doc.Position{At: "end"}, node.Clone())
		}
	}
	if ed := a.Editor(cf.PageID); ed != nil {
		ed.Edit(apply)
		a.savePage()
	} else if c := a.st.Content(cf.PageID); c != nil {
		if err := a.st.AddHistory(cf.PageID, c, "conflict", "", locales.SettingsHistoryBeforeResolving()); err != nil {
			a.toast(locales.SettingsResolveFailed(err.Error()))
			return
		}
		apply(c)
		if _, _, err := a.st.SaveContent(cf.PageID, c); err != nil {
			a.toast(locales.SettingsResolveFailed(err.Error()))
			return
		}
	}
	if err := a.st.ResolveConflict(cf.ID); err != nil {
		a.toast(locales.SettingsResolveFailed(err.Error()))
	}
}
