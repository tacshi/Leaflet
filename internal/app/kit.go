package app

import (
	"github.com/egoist/mygo/ui"

	"leaflet/internal/theme"
	"leaflet/locales"
)

// The app's widgets: MyGo's, with what Leaflet adds to them.

// tools is MyGo's Toolbar of icon buttons: they take no face until
// hovered. It keeps its size, so that none goes into its overflow menu.
func tools(c *ui.Context, fn func()) ui.Element {
	return ui.Toolbar(c, fn).Padding(0).Gap(2).Shrink(0)
}

// iconButton is MyGo's Button showing an icon, named for assistive
// technology and its tooltip; build it in tools.
func iconButton(c *ui.Context, key, icon, label string) ui.Element {
	b := ui.Button(c.Key(key), "").Label(label).Tooltip(label).Padding(6)
	b.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(16) })
	return b
}

// withIcon gives a MyGo button an icon before its label.
func withIcon(c *ui.Context, b ui.Element, icon, label string) ui.Element {
	b.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if icon != "" {
				ui.Icon(c, theme.Icon(icon)).FontSize(14)
			}
			if label != "" {
				ui.Text(c, label).SingleLine()
			}
		})
	})
	return b.Label(label)
}

// button is MyGo's Button, with an optional icon.
func button(c *ui.Context, key, icon, label string) ui.Element {
	return withIcon(c, ui.Button(c.Key(key), ""), icon, label)
}

// primary is MyGo's PrimaryButton, the main action of a form or dialog.
func primary(c *ui.Context, key, icon, label string) ui.Element {
	return withIcon(c, ui.PrimaryButton(c.Key(key), ""), icon, label)
}

// ghost is MyGo's Button for a lesser action, as button is.
func ghost(c *ui.Context, key, icon, label string) ui.Element {
	return button(c, key, icon, label)
}

// danger is MyGo's Button, in the danger color, for what cannot be
// undone.
func danger(c *ui.Context, key, label string) ui.Element {
	pal := theme.Of(c)
	return ui.Button(c.Key(key), label).TextColor(pal.DangerText)
}

// field is MyGo's text input.
func field(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return fieldBox(c, key, value, placeholder, false)
}

// growField is a field that takes the free width of the row it is in.
func growField(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return fieldBox(c, key, value, placeholder, true)
}

func fieldBox(c *ui.Context, key string, value *string, placeholder string, grow bool) ui.Element {
	in := ui.TextInput(c.Key(key), value).Placeholder(placeholder)
	if grow {
		in.Grow(1)
	}
	return in
}

// password is MyGo's text input for a secret.
func password(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return passwordBox(c, key, value, placeholder, false)
}

// growPassword is a password that takes the free width of its row, as
// growField is.
func growPassword(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return passwordBox(c, key, value, placeholder, true)
}

func passwordBox(c *ui.Context, key string, value *string, placeholder string, grow bool) ui.Element {
	return fieldBox(c, key, value, placeholder, grow).Password()
}

// label is a small heading over a group of settings or fields.
func label(c *ui.Context, text string) {
	pal := theme.Of(c)
	ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(pal.TextMuted)
}

// notice is a tinted box of information: info, warn, ok or danger.
func notice(c *ui.Context, kind, icon, text string, children func()) {
	pal := theme.Of(c)
	bg, border, fg := pal.AccentSoft, pal.AccentSoftBorder, pal.AccentText
	switch kind {
	case "warn":
		bg, border, fg = pal.WarnBg, pal.WarnBorder, pal.WarnText
	case "ok":
		bg, border, fg = pal.OkBg, pal.OkBorder, pal.OkText
	case "danger":
		bg, border, fg = pal.DangerBg, pal.DangerBorder, pal.DangerText
	}
	ui.Row(c).Padding(10, 12).Gap(10).Radius(8).Background(bg).Border(1, border).AlignItems(ui.Start).Children(func() {
		if icon != "" {
			ui.Icon(c, theme.Icon(icon)).FontSize(15).TextColor(fg).Margin(1, 0, 0, 0)
		}
		ui.Column(c).Grow(1).Gap(8).Children(func() {
			ui.Text(c, text).FontSize(13).TextColor(fg)
			if children != nil {
				children()
			}
		})
	})
}

// card is a bordered panel of settings.
func card(c *ui.Context, key string, fn func()) ui.Element {
	pal := theme.Of(c)
	return ui.Column(c.Key(key)).Radius(10).Border(1, pal.Divider).Background(pal.Surface).Children(fn)
}

// settingRow is a row of a card: a title and description beside a control.
func settingRow(c *ui.Context, key, title, desc string, first bool, control func()) {
	pal := theme.Of(c)
	row := ui.Row(c.Key(key)).Padding(12, 16).Gap(16).AlignItems(ui.Center).FillWidth()
	if !first {
		row.BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(14).FontWeight(500).TextColor(pal.Text)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(pal.TextMuted)
			}
		})
		if control != nil {
			control()
		}
	})
}

// avatar is MyGo's avatar of a person named name, size high, or an
// agent's sparkles in the accent's circle.
func avatar(c *ui.Context, name string, size float32, agent bool) {
	if agent {
		pal := theme.Of(c)
		ui.Box(c).Size(size, size).Radius(999).Background(pal.Accent).Center().Children(func() {
			ui.Icon(c, theme.Icon("sparkles")).FontSize(size * 0.55).TextColor(pal.OnAccent)
		})
		return
	}
	// MyGo's avatar is as high as 2.25 times the theme's font size, which
	// it takes as it is created.
	saved := c.Theme()
	t := *saved
	t.FontSize = size / 2.25
	c.SetTheme(&t)
	ui.Avatar(c, name, nil)
	c.SetTheme(saved)
}

// dialog is a modal panel of Leaflet's look, closed by Escape or a click
// outside.
func dialog(c *ui.Context, open *bool, width float32, fn func()) ui.Element {
	pal := theme.Of(c)
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(10, 12, 16, 0.32))
		panel.Width(width).MaxHeightPercent(86).Radius(12).Background(pal.Surface).Border(1, pal.Border).
			Shadow(0, 24, 60, 0, pal.Shadow).Clip()
		fn()
	})
}

// dialogHeader is a dialog's title row with a close button.
func dialogHeader(c *ui.Context, title, subtitle string, open *bool) {
	pal := theme.Of(c)
	ui.Row(c).Padding(16, 16, 12, 20).Gap(12).AlignItems(ui.Start).Children(func() {
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			ui.Text(c, title).FontSize(16).FontWeight(650).TextColor(pal.Text)
			if subtitle != "" {
				ui.Text(c, subtitle).FontSize(13).TextColor(pal.TextMuted)
			}
		})
		tools(c, func() {
			if iconButton(c, "close", "x", locales.CommonClose()).Clicked() {
				*open = false
			}
		})
	})
}

// selectBox is MyGo's Select choosing one of options into *value, each
// shown by display (nil shows it as it is): the Select chooses among
// their names, which go back to the option.
func selectBox(c *ui.Context, key string, value *string, options []string, display func(string) string, width float32) ui.Element {
	if display == nil {
		display = func(s string) string { return s }
	}
	names := make([]string, len(options))
	for i, o := range options {
		names[i] = display(o)
	}
	name := display(*value)
	sel := ui.Select(c.Key(key), &name, names).Width(width)
	for i, n := range names {
		if n == name && options[i] != *value {
			*value = options[i]
		}
	}
	return sel
}

// accentBadge is MyGo's badge in the accent's colors, for what a page is:
// a template, or shared.
func accentBadge(c *ui.Context, text string) ui.Element {
	pal := theme.Of(c)
	return ui.Badge(c, text).Background(pal.AccentSoft).TextColor(pal.AccentText)
}
