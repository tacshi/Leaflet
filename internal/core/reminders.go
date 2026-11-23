package core

import (
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

const ReminderHour = 9

const ReminderWindow = 24 * time.Hour

// ReminderAt is when t's reminder shows in loc, the zone the device is in: at the time it is
// due, or at 9:00 of a day due with no time. A due is a time on the clock wherever the device
// is, so it is taken in loc rather than in the zone it was read in.
func ReminderAt(t store.PageTodo, loc *time.Location) time.Time {
	due := t.Due
	if !t.Timed {
		due = time.Date(due.Year(), due.Month(), due.Day(), ReminderHour, 0, 0, 0, due.Location())
	}
	return OnClock(due, loc)
}

// OnClock is the time in loc with t's date and time of day, as t's own zone has them.
func OnClock(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
}

// DueReminders are those of todos, not reminded of yet, whose time in now's zone has come by
// now, within ReminderWindow.
func DueReminders(todos []store.PageTodo, now time.Time, reminded func(t store.PageTodo) bool) []store.PageTodo {
	var out []store.PageTodo
	for _, t := range todos {
		if t.Due.IsZero() || reminded(t) {
			continue
		}
		at := ReminderAt(t, now.Location())
		if !now.Before(at) && now.Sub(at) < ReminderWindow {
			out = append(out, t)
		}
	}
	return out
}

func RemindsOf(t store.PageTodo, userID string, ownsPage bool) bool {
	if t.Owner != "" {
		return t.OwnerID != "" && t.OwnerID == userID
	}
	return ownsPage
}

func ReminderValue(t store.PageTodo) string {
	if t.Timed {
		return t.Due.Format(doc.DueTimeLayout)
	}
	return t.Due.Format(doc.DueLayout)
}

func ReminderKey(pageID, blockID string) string { return "reminded:" + pageID + "#" + blockID }

func ReminderID(pageID, blockID string) string { return "todo:" + pageID + "#" + blockID }

func ReminderTitle(m locales.Messages, t store.PageTodo, now time.Time) string {
	if doc.Overdue(t.Due, t.Timed, now) && (!t.Timed || !doc.DueToday(t.Due, now)) {
		return m.TodosOverdueOn(doc.DueLabel(m, t.Due, t.Timed, now))
	}
	return m.TodosDueToday()
}
