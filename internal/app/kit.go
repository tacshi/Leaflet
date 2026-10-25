package app

import (
	"github.com/egoist/mygo/ui"

	"leaflet/internal/theme"
)

// The app's widgets, built on MyGo's bases in Leaflet's look.

// iconButton is a square button showing an icon, named for assistive
// technology and its tooltip.
func iconButton(c *ui.Context, key, icon, label string) ui.Element {
	pal := theme.Of(c)
	b := ui.ButtonBase(c.Key(key)).Size(28, 28).Center().Radius(7).Label(label).Tooltip(label).TextColor(pal.Icon)
	if b.Pressed() {
		b.Background(pal.Divider)
	} else if b.Hovered() {
		b.Background(pal.Hover)
		b.TextColor(pal.Text)
	}
	b.Children(func() { ui.Icon(c, theme.Icon(icon)).FontSize(16) })
	return b
}

// button is a quiet bordered button, with an optional icon.
func button(c *ui.Context, key, icon, label string) ui.Element {
	pal := theme.Of(c)
	b := ui.ButtonBase(c.Key(key)).Height(30).Padding(0, 11).Gap(6).Radius(7).Border(1, pal.Border).
		Background(pal.Surface).TextColor(pal.Text).AlignItems(ui.Center)
	if b.Pressed() {
		b.Background(pal.Divider)
	} else if b.Hovered() {
		b.Background(pal.Hover.Over(pal.Surface))
	}
	b.Children(func() {
		if icon != "" {
			ui.Icon(c, theme.Icon(icon)).FontSize(14).TextColor(pal.Icon)
		}
		if label != "" {
			ui.Text(c, label).FontSize(13).FontWeight(500).SingleLine()
		}
	})
	return b
}

// primary is the accent button of a form or dialog.
func primary(c *ui.Context, key, icon, label string) ui.Element {
	pal := theme.Of(c)
	b := ui.ButtonBase(c.Key(key)).Height(30).Padding(0, 12).Gap(6).Radius(7).Background(pal.Accent).
		TextColor(pal.OnAccent).AlignItems(ui.Center)
	if b.IsDisabled() {
		b.Opacity(0.5)
	} else if b.Pressed() {
		b.Background(pal.Accent.Mix(pal.Text, 0.2))
	} else if b.Hovered() {
		b.Background(pal.Accent.Mix(pal.Text, 0.1))
	}
	b.Children(func() {
		if icon != "" {
			ui.Icon(c, theme.Icon(icon)).FontSize(14)
		}
		if label != "" {
			ui.Text(c, label).FontSize(13).FontWeight(600).SingleLine()
		}
	})
	return b
}

// ghost is a borderless text button.
func ghost(c *ui.Context, key, icon, label string) ui.Element {
	pal := theme.Of(c)
	b := ui.ButtonBase(c.Key(key)).Height(28).Padding(0, 8).Gap(6).Radius(7).TextColor(pal.TextSecondary).AlignItems(ui.Center)
	if b.Hovered() {
		b.Background(pal.Hover)
		b.TextColor(pal.Text)
	}
	b.Children(func() {
		if icon != "" {
			ui.Icon(c, theme.Icon(icon)).FontSize(14)
		}
		if label != "" {
			ui.Text(c, label).FontSize(13).SingleLine()
		}
	})
	return b
}

// danger is a button for what cannot be undone.
func danger(c *ui.Context, key, label string) ui.Element {
	pal := theme.Of(c)
	b := ui.ButtonBase(c.Key(key)).Height(30).Padding(0, 12).Radius(7).Border(1, pal.DangerBorder).
		Background(pal.DangerBg).TextColor(pal.DangerText).AlignItems(ui.Center)
	if b.Hovered() {
		b.Background(pal.DangerBorder)
	}
	b.Children(func() { ui.Text(c, label).FontSize(13).FontWeight(600) })
	return b
}

// field is a bordered text input.
func field(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return fieldBox(c, key, value, placeholder, false)
}

// growField is a field that takes the free width of the row it is in:
// Grow on what field returns sizes the input, not its box.
func growField(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return fieldBox(c, key, value, placeholder, true)
}

func fieldBox(c *ui.Context, key string, value *string, placeholder string, grow bool) ui.Element {
	pal := theme.Of(c)
	box := ui.Row(c.Key(key)).Height(34).Padding(0, 10).Radius(7).Border(1, pal.Border).Background(pal.Surface).AlignItems(ui.Center)
	if grow {
		box.Grow(1)
	}
	var in ui.Element
	box.Children(func() {
		in = ui.TextInputBase(c.Key("input"), value).Placeholder(placeholder).Grow(1).FontSize(14).TextColor(pal.Text)
	})
	if in.Focused() {
		box.Border(1.5, pal.Accent)
	}
	return in
}

// password is a bordered secret input.
func password(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return passwordBox(c, key, value, placeholder, false)
}

// growPassword is a password that takes the free width of its row, as
// growField is.
func growPassword(c *ui.Context, key string, value *string, placeholder string) ui.Element {
	return passwordBox(c, key, value, placeholder, true)
}

func passwordBox(c *ui.Context, key string, value *string, placeholder string, grow bool) ui.Element {
	pal := theme.Of(c)
	box := ui.Row(c.Key(key)).Height(34).Padding(0, 10).Radius(7).Border(1, pal.Border).Background(pal.Surface).AlignItems(ui.Center)
	if grow {
		box.Grow(1)
	}
	var in ui.Element
	box.Children(func() {
		in = ui.TextInputBase(c.Key("input"), value).Password().Placeholder(placeholder).Grow(1).FontSize(14).TextColor(pal.Text)
	})
	if in.Focused() {
		box.Border(1.5, pal.Accent)
	}
	return in
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

// toggle is a switch in Leaflet's colors.
func toggle(c *ui.Context, key string, on *bool) ui.Element {
	pal := theme.Of(c)
	sw := ui.SwitchBase(c.Key(key), on).Size(34, 20).Radius(999).Padding(2)
	track := pal.SwitchOff
	if *on {
		track = pal.Accent
	}
	sw.Background(track)
	x := float32(0)
	if *on {
		x = 14
	}
	knob := sw.Animate("knob", x, 120*1e6)
	sw.Children(func() {
		ui.Box(c).Size(16, 16).Radius(999).Background(ui.Hex("#FFFFFF")).Translate(knob, 0).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.25))
	})
	return sw
}

// avatar is a person's initial in a circle.
func avatar(c *ui.Context, initial string, size float32, agent bool) {
	pal := theme.Of(c)
	bg, fg := pal.AccentSoft, pal.AccentText
	if agent {
		bg, fg = pal.Accent, pal.OnAccent
	}
	ui.Box(c).Size(size, size).Radius(999).Background(bg).Center().Children(func() {
		if agent {
			ui.Icon(c, theme.Icon("sparkles")).FontSize(size * 0.55).TextColor(fg)
		} else {
			ui.Text(c, initial).FontSize(size * 0.48).FontWeight(650).TextColor(fg)
		}
	})
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
		if iconButton(c, "close", "x", "Close").Clicked() {
			*open = false
		}
	})
}

// segmented is a row of choices, one chosen.
func segmented(c *ui.Context, key string, chosen *int, labels ...string) ui.Element {
	pal := theme.Of(c)
	s := ui.SegmentedBase(c.Key(key), chosen, len(labels))
	s.Track.Padding(2).Radius(8).Background(pal.Ground).Border(1, pal.Divider).Children(func() {
		for i, l := range labels {
			seg := s.Segment(i).Height(26).Padding(0, 12).Radius(6).Center()
			if i == *chosen {
				seg.Background(pal.Surface).Shadow(0, 1, 2, 0, pal.Shadow).TextColor(pal.Text)
			} else {
				seg.TextColor(pal.TextMuted)
			}
			seg.Children(func() { ui.Text(c, l).FontSize(13).FontWeight(500) })
		}
	})
	return s.Track
}

// selectBox chooses one string of options, shown with display.
func selectBox(c *ui.Context, key string, value *string, options []string, display func(string) string, width float32) ui.Element {
	pal := theme.Of(c)
	if display == nil {
		display = func(s string) string { return s }
	}
	sel := ui.SelectBase(c.Key(key), value)
	sel.Trigger.Height(32).Width(width).Padding(0, 10).Gap(6).Radius(7).Border(1, pal.Border).Background(pal.Surface).AlignItems(ui.Center)
	sel.Trigger.Children(func() {
		ui.Text(c, display(*value)).FontSize(13).TextColor(pal.Text).Grow(1).SingleLine()
		ui.Icon(c, theme.Icon("chevron-down")).FontSize(13).TextColor(pal.TextMuted)
	})
	sel.Popup(func(panel ui.Element) {
		// The popup is at least as wide as the box, which may fill its row.
		panel.Margin(4, 0, 0, 0).Radius(9).Background(pal.Surface).Border(1, pal.Border).Shadow(0, 10, 28, 0, pal.Shadow).
			MinWidth(max(width, sel.Trigger.Bounds().W))
		// Long lists, as languages or a provider's models, scroll.
		ui.Scroll(c.Key("options")).MaxHeight(320).Padding(4).Children(func() {
			for _, o := range options {
				item := sel.Item(o).Height(30).Padding(0, 10).Radius(6).AlignItems(ui.Center)
				if item.Highlighted() {
					item.Background(pal.NavSelected)
				}
				item.Children(func() {
					ui.Text(c, display(o)).FontSize(13).TextColor(pal.Text).Grow(1).SingleLine()
					if o == *value {
						ui.Icon(c, theme.Icon("check")).FontSize(13).TextColor(pal.Accent)
					}
				})
			}
		})
	})
	return sel.Trigger
}
