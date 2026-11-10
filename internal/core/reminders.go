package core

import (
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

const ReminderHour = 9

const ReminderWindow = 24 * time.Hour

func ReminderAt(t store.PageTodo) time.Time {
	if t.Timed {
		return t.Due
	}
	return time.Date(t.Due.Year(), t.Due.Month(), t.Due.Day(), ReminderHour, 0, 0, 0, time.Local)
}

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
