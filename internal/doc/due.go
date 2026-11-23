package doc

import (
	"slices"
	"time"

	"leaflet/locales"
)

func Overdue(due time.Time, timed bool, now time.Time) bool {
	if timed {
		return due.Before(now)
	}
	return due.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local))
}

func DueLabel(m locales.Messages, due time.Time, timed bool, now time.Time) string {
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	var label string
	switch int(day(due).Sub(day(now)).Hours() / 24) {
	case 0:
		label = m.CommonToday()
	case 1:
		label = m.EditorTomorrow()
	case -1:
		label = m.EditorYesterday()
	default:
		if due.Year() == now.Year() {
			label = m.EditorDueDay(due)
		} else {
			label = m.EditorDueDate(due)
		}
	}
	if timed {
		return m.EditorDueAt(label, due)
	}
	return label
}

func DueToday(due, now time.Time) bool {
	y, m, d := due.In(time.Local).Date()
	ny, nm, nd := now.In(time.Local).Date()
	return y == ny && m == nm && d == nd
}

// TodoSection is where a day of to-dos falls from today.
type TodoSection int

const (
	TodoOverdue TodoSection = iota // a day before today
	TodoToday
	TodoUpcoming
	TodoNoDate
)

// TodoDay is the to-dos due on one day, from every page, or the to-dos with no date.
type TodoDay[T any] struct {
	// Day is the day's midnight in the local time zone, zero for the to-dos with no date.
	Day     time.Time
	Section TodoSection
	Todos   []T
}

// Key names the day to remember it by, as 2026-11-20, or "no-date".
func (d TodoDay[T]) Key() string {
	if d.Day.IsZero() {
		return "no-date"
	}
	return d.Day.Format(time.DateOnly)
}

// Folded tells whether the day shows only its header until it is opened: the days gone by do,
// so what was left undone on them doesn't push today down.
func (d TodoDay[T]) Folded() bool { return d.Section == TodoOverdue }

// TodoDays puts the to-dos in a group a day by their due dates in the local time zone, the days
// in order and the to-dos with no date last. A day's to-dos are in order of their times, and
// otherwise in the order given.
func TodoDays[T any](todos []T, todo func(T) Todo, now time.Time) []TodoDay[T] {
	by := map[string]*TodoDay[T]{}
	var days []*TodoDay[T]
	var undated []T
	for _, t := range todos {
		due := todo(t).Due
		if due.IsZero() {
			undated = append(undated, t)
			continue
		}
		day := localDay(due)
		key := day.Format(time.DateOnly)
		d := by[key]
		if d == nil {
			d = &TodoDay[T]{Day: day}
			by[key] = d
			days = append(days, d)
		}
		d.Todos = append(d.Todos, t)
	}
	slices.SortFunc(days, func(a, b *TodoDay[T]) int { return a.Day.Compare(b.Day) })
	today := localDay(now)
	out := make([]TodoDay[T], 0, len(days)+1)
	for _, d := range days {
		switch d.Day.Compare(today) {
		case -1:
			d.Section = TodoOverdue
		case 0:
			d.Section = TodoToday
		default:
			d.Section = TodoUpcoming
		}
		slices.SortStableFunc(d.Todos, func(a, b T) int { return todo(a).Due.Compare(todo(b).Due) })
		out = append(out, *d)
	}
	if len(undated) > 0 {
		out = append(out, TodoDay[T]{Section: TodoNoDate, Todos: undated})
	}
	return out
}

func localDay(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}
