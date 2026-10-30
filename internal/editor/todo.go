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

// dueHour is the time of day a due date takes as it is given one.
const dueHour = 9

// dueChip builds a to-do's due date after its text, which opens MyGo's
// calendar to change it, and a time of day; with none, it offers one
// while the to-do is under the pointer or has the caret. The popover
// changes a draft, saved as it closes, as MyGo's calendar chooses a day
// also as it turns the months.
func (e *Editor) dueChip(c *ui.Context, n *doc.Node, left, top float32, active bool, pal *theme.Palette) {
	due, timed, has := n.Due()
	open := e.dueOpen == n.ID
	if !has && (e.ReadOnly || !active && !open) {
		return
	}
	now := time.Now()
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
		open = !open
		if open {
			e.dueDraft, e.dueTimed, e.dueEdited = due, timed, false
			if !has {
				e.dueDraft = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
			}
		}
	}
	ui.Popover(c, chip, &open, func() {
		cal := ui.Calendar(c.Key("due-calendar"), &e.dueDraft)
		// A click on a day, the one chosen too, focuses the calendar.
		if cal.Changed() || cal.FocusWithin() {
			e.dueEdited = true
		}
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			if !e.dueTimed {
				if ui.Button(c, locales.EditorAddTime()).Grow(1).Clicked() {
					y, m, d := e.dueDraft.Date()
					e.dueDraft = time.Date(y, m, d, dueHour, 0, 0, 0, time.Local)
					e.dueTimed, e.dueEdited = true, true
				}
				return
			}
			if ui.TimeInput(c.Key("due-time"), &e.dueDraft).Label(locales.EditorDueTime()).Grow(1).Changed() {
				e.dueEdited = true
			}
			x := ui.Button(c.Key("remove-time"), "").Label(locales.EditorRemoveTime()).Tooltip(locales.EditorRemoveTime())
			x.Children(func() { ui.Icon(c, theme.Icon("x")).FontSize(13) })
			if x.Clicked() {
				e.dueTimed, e.dueEdited = false, true
			}
		})
		if has && ui.Button(c, locales.EditorRemoveDate()).FillWidth().Clicked() {
			e.SetDue(n.ID, time.Time{}, false)
			open, e.dueEdited = false, false
			e.Focus()
		}
	})
	if open {
		e.dueOpen = n.ID
	} else if e.dueOpen == n.ID {
		e.dueOpen = ""
		if e.dueEdited {
			e.dueEdited = false
			e.SetDue(n.ID, e.dueDraft, e.dueTimed)
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
