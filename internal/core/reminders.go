package core

import (
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// ReminderHour is when a to-do due today is reminded of, without a time
// of day of its own.
const ReminderHour = 9

// ReminderWindow is how long after its time a to-do is still reminded
// of. One later than that, as on a device that was off, or one new to
// the library, is among the To-dos tab's Overdue, not one notification of
// many at once.
const ReminderWindow = 24 * time.Hour

// ReminderAt is when a to-do with a due date is reminded of: at its time
// of day, or else at ReminderHour of its day.
func ReminderAt(t store.PageTodo) time.Time {
	if t.Timed {
		return t.Due
	}
	return time.Date(t.Due.Year(), t.Due.Month(), t.Due.Day(), ReminderHour, 0, 0, 0, time.Local)
}

// DueReminders are the to-dos to remind of now: from ReminderAt on, for
// ReminderWindow, and not reminded of for that date yet.
func DueReminders(todos []store.PageTodo, now time.Time, reminded func(t store.PageTodo) bool) []store.PageTodo {
	var out []store.PageTodo
	for _, t := range todos {
		if t.Due.IsZero() || reminded(t) {
			continue
		}
		at := ReminderAt(t)
		if !now.Before(at) && now.Sub(at) < ReminderWindow {
			out = append(out, t)
		}
	}
	return out
}

// RemindsOf reports whether the user is reminded of a to-do: one given to
// them, or one given to no one on a page of their own. Another's to-do is
// that person's to be reminded of, on their devices, as is one on a page
// shared with the user that names no one.
func RemindsOf(t store.PageTodo, userID string, ownsPage bool) bool {
	if t.Owner != "" {
		return t.OwnerID != "" && t.OwnerID == userID
	}
	return ownsPage
}

// ReminderValue is a to-do's due date and time as its reminder is kept by.
func ReminderValue(t store.PageTodo) string {
	if t.Timed {
		return t.Due.Format(doc.DueTimeLayout)
	}
	return t.Due.Format(doc.DueLayout)
}

// ReminderKey is the value that keeps which date a to-do was last reminded
// of for.
func ReminderKey(pageID, blockID string) string { return "reminded:" + pageID + "#" + blockID }

// ReminderID names a to-do's reminder, on the desktop as on a phone: a
// click on it opens the to-do by it, also after the app quit.
func ReminderID(pageID, blockID string) string { return "todo:" + pageID + "#" + blockID }

// ReminderTitle says what a reminder shown at now says of a to-do, in the
// language of m.
func ReminderTitle(m locales.Messages, t store.PageTodo, now time.Time) string {
	// A to-do due at a time today is reminded of at it, as due.
	if doc.Overdue(t.Due, t.Timed, now) && (!t.Timed || !doc.DueToday(t.Due, now)) {
		return m.TodosOverdueOn(doc.DueLabel(m, t.Due, t.Timed, now))
	}
	return m.TodosDueToday()
}
