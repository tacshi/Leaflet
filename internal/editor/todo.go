package editor

import (
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
)

func (e *Editor) SetDue(id string, t time.Time, timed bool) {
	n := e.node(id)
	if n == nil || n.Type != "todo_list" || e.ReadOnly {
		return
	}
	e.pushUndo(false)
	n.SetDue(t, timed)
	e.invalidate(id)
	e.changed()
}

func DueLabel(due time.Time, timed bool, now time.Time) string {
	return doc.DueLabel(locales.In(i18n.Locale()), due, timed, now)
}

func Overdue(due time.Time, timed bool, now time.Time) bool { return doc.Overdue(due, timed, now) }

func DueToday(due, now time.Time) bool { return doc.DueToday(due, now) }

var clock = time.Now

const dueHour = 9

type dueDraft struct {
	at            time.Time
	timed, edited bool
}

func (e *Editor) dueChip(c *ui.Context, n *doc.Node, left, top float32, active bool, pal *theme.Palette) {
	due, timed, has := n.Due()
	d := e.dues[n.ID]
	open := d != nil && e.dueOpen == n.ID
	if !has && (e.ReadOnly || !active && !open) {
		return
	}
	now := clock()
	label, fg, bg := locales.EditorAddDate(), pal.TextMuted, pal.Ground
	if has {
		label = DueLabel(due, timed, now)
		switch {
		case Overdue(due, timed, now):
			fg, bg = pal.DangerText, pal.DangerBg
		case DueToday(due, now):
			fg, bg = pal.AccentText, pal.AccentSoft
		}
	}
	chip := ui.ButtonBase(c.Key("due:"+n.ID)).Absolute().Left(left).Top(top).Height(20).Padding(0, 7).Gap(4).Radius(6).
		Background(bg).TextColor(fg).AlignItems(ui.Center).Label(locales.EditorDueDateLabel(label)).Cursor(ui.CursorPointer)
	chip.Draw(func(_ *ui.Painter, r ui.Rect) { e.chips[n.ID] = r })
	chip.Children(func() {
		ui.Icon(c, theme.Icon("calendar")).FontSize(11)
		ui.Text(c, label).FontSize(12).FontWeight(550)
	})
	if chip.Clicked() && !e.ReadOnly {
		if open = !open; open {
			if d != nil && d.edited {
				e.SetDue(n.ID, d.at, d.timed)
			}
			d = &dueDraft{at: due, timed: timed}
			if !has {
				d.at = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
			}
			if e.dues == nil {
				e.dues = map[string]*dueDraft{}
			}
			e.dues[n.ID] = d
		}
	}
	ui.Popover(c, chip, &open, func() {
		cal := ui.Calendar(c.Key("due-calendar"), &d.at).Browse()
		if cal.Changed() || cal.FocusWithin() {
			d.edited = true
		}
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			if !d.timed {
				if ui.Button(c.Key("add-time"), locales.EditorAddTime()).Grow(1).Clicked() {
					y, m, day := d.at.Date()
					d.at = time.Date(y, m, day, dueHour, 0, 0, 0, time.Local)
					d.timed, d.edited = true, true
				}
				return
			}
			if ui.TimeInput(c.Key("due-time"), &d.at).Label(locales.EditorDueTime()).Grow(1).Changed() {
				d.edited = true
			}
			x := ui.Button(c.Key("remove-time"), "").Label(locales.EditorRemoveTime()).Tooltip(locales.EditorRemoveTime())
			x.Children(func() { ui.Icon(c, theme.Icon("x")).FontSize(13) })
			if x.Clicked() {
				d.timed, d.edited = false, true
			}
		})
		if has && ui.Button(c.Key("remove-date"), locales.EditorRemoveDate()).FillWidth().Clicked() {
			e.SetDue(n.ID, time.Time{}, false)
			open, d.edited = false, false
			e.Focus()
		}
	})
	switch {
	case open:
		e.dueOpen = n.ID
	case d != nil:
		delete(e.dues, n.ID)
		if e.dueOpen == n.ID {
			e.dueOpen = ""
		}
		if d.edited {
			e.SetDue(n.ID, d.at, d.timed)
		}
	}
}

func (e *Editor) onChip(wx, wy float32) bool {
	for _, r := range e.chips {
		if wx >= r.X && wx < r.X+r.W && wy >= r.Y && wy < r.Y+r.H {
			return true
		}
	}
	return false
}
