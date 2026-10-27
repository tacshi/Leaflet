package editor

import (
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// SetDue gives a to-do a due date, or takes it away for a zero time, as a
// step of undo.
func (e *Editor) SetDue(id string, t time.Time) {
	n := e.node(id)
	if n == nil || n.Type != "todo_list" || e.ReadOnly {
		return
	}
	e.pushUndo(false)
	n.SetDue(t)
	e.invalidate(id)
	e.changed()
}

// DueLabel says when a due date is, from now: Today, Tomorrow, Yesterday,
// or the day, with its year when not this one's.
func DueLabel(due, now time.Time) string {
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local) }
	switch int(day(due).Sub(day(now)).Hours() / 24) {
	case 0:
		return locales.CommonToday()
	case 1:
		return locales.EditorTomorrow()
	case -1:
		return locales.EditorYesterday()
	}
	if due.Year() == now.Year() {
		return locales.EditorDueDay(due)
	}
	return locales.EditorDueDate(due)
}

// Overdue reports whether a due date is a day before now's.
func Overdue(due, now time.Time) bool {
	return due.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local))
}

// dueChip builds a to-do's due date after its text, which opens MyGo's
// calendar to change it; with none, it offers one while the to-do is
// under the pointer or has the caret.
func (e *Editor) dueChip(c *ui.Context, n *doc.Node, left, top float32, active bool, pal *theme.Palette) {
	due, has := n.Due()
	open := e.dueOpen == n.ID
	if !has && (e.ReadOnly || !active && !open) {
		return
	}
	now := time.Now()
	label, fg, bg := locales.EditorAddDate(), pal.TextMuted, pal.Ground
	if has {
		label = DueLabel(due, now)
		switch {
		case Overdue(due, now):
			fg, bg = pal.DangerText, pal.DangerBg
		case label == locales.CommonToday():
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
	}
	ui.Popover(c, chip, &open, func() {
		date := due
		if !has {
			date = now
		}
		if ui.Calendar(c, &date).Changed() {
			e.SetDue(n.ID, date)
			open = false
			e.Focus()
		}
		if has && ui.Button(c, locales.EditorRemoveDate()).Clicked() {
			e.SetDue(n.ID, time.Time{})
			open = false
			e.Focus()
		}
	})
	if open {
		e.dueOpen = n.ID
	} else if e.dueOpen == n.ID {
		e.dueOpen = ""
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
