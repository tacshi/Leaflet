package editor

import (
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// SetDue gives a to-do a due date, with its time of day when timed, or
// takes it away for a zero time, as a step of undo.
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

// DueLabel says when a due date is, from now: Today, Tomorrow, Yesterday,
// or the day, with its year when not this one's, and its time when timed.
func DueLabel(due time.Time, timed bool, now time.Time) string {
	// The days between are counted on the calendar, in UTC, where each is
	// 24 hours long: a day that changes the clocks is 23 or 25.
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	var label string
	switch int(day(due).Sub(day(now)).Hours() / 24) {
	case 0:
		label = locales.CommonToday()
	case 1:
		label = locales.EditorTomorrow()
	case -1:
		label = locales.EditorYesterday()
	default:
		if due.Year() == now.Year() {
			label = locales.EditorDueDay(due)
		} else {
			label = locales.EditorDueDate(due)
		}
	}
	if timed {
		return locales.EditorDueAt(label, due)
	}
	return label
}

// Overdue reports whether a due date has passed: its time when timed, or
// else its day, once now is a day after it.
func Overdue(due time.Time, timed bool, now time.Time) bool {
	if timed {
		return due.Before(now)
	}
	return due.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local))
}

// DueToday reports whether a due date is on now's day.
func DueToday(due, now time.Time) bool {
	y, m, d := due.In(time.Local).Date()
	ny, nm, nd := now.In(time.Local).Date()
	return y == ny && m == nm && d == nd
}

// clock is the time now, which a to-do's date is shown from.
var clock = time.Now

// dueHour is the time of day a due date takes as it is given one.
const dueHour = 9

// A dueDraft is the date in a to-do's calendar, with its time of day when
// timed, which the to-do takes as the calendar closes when edited. Each
// to-do has its own: a click on another's date, opening its calendar,
// closes this one, which still saves what it was given.
type dueDraft struct {
	at            time.Time
	timed, edited bool
}

// dueChip builds a to-do's due date after its text, which opens MyGo's
// calendar to change it, and a time of day; with none, it offers one
// while the to-do is under the pointer or has the caret. The popover
// changes a draft, saved as it closes, as MyGo's calendar chooses a day
// also as it turns the months.
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
				// Closed by another's calendar and opened again before it
				// built, it saves what it was given first.
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
		// Its buttons and Page Up and Down only show other months, so
		// looking at one leaves the date as it was. A click on a day, the
		// one chosen too, focuses the calendar.
		cal := ui.Calendar(c.Key("due-calendar"), &d.at).Browse()
		if cal.Changed() || cal.FocusWithin() {
			d.edited = true
		}
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			if !d.timed {
				if ui.Button(c, locales.EditorAddTime()).Grow(1).Clicked() {
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
		if has && ui.Button(c, locales.EditorRemoveDate()).FillWidth().Clicked() {
			e.SetDue(n.ID, time.Time{}, false)
			open, d.edited = false, false
			e.Focus()
		}
	})
	switch {
	case open:
		e.dueOpen = n.ID
	case d != nil:
		// Closed, by the user or by another to-do's calendar opening.
		delete(e.dues, n.ID)
		if e.dueOpen == n.ID {
			e.dueOpen = ""
		}
		if d.edited {
			e.SetDue(n.ID, d.at, d.timed)
		}
	}
}

// onChip reports whether a point in the window is on a to-do's date,
// whose chip takes the press.
func (e *Editor) onChip(wx, wy float32) bool {
	for _, r := range e.chips {
		if wx >= r.X && wx < r.X+r.W && wy >= r.Y && wy < r.Y+r.H {
			return true
		}
	}
	return false
}
