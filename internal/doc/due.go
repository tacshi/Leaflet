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

type TodoSection int

const (
	TodoOverdue TodoSection = iota
	TodoToday
	TodoUpcoming
	TodoNoDate
)

func (t Todo) Section(now time.Time) TodoSection {
	switch {
	case t.Due.IsZero():
		return TodoNoDate
	case Overdue(t.Due, t.Timed, now):
		return TodoOverdue
	case DueToday(t.Due, now):
		return TodoToday
	}
	return TodoUpcoming
}

type TodoGroup[T any] struct {
	Section TodoSection
	Todos   []T
}

func TodoGroups[T any](todos []T, todo func(T) Todo, now time.Time) []TodoGroup[T] {
	var by [TodoNoDate + 1][]T
	for _, t := range todos {
		s := todo(t).Section(now)
		by[s] = append(by[s], t)
	}
	var out []TodoGroup[T]
	for s, ts := range by {
		if len(ts) == 0 {
			continue
		}
		slices.SortStableFunc(ts, func(a, b T) int { return todo(a).Due.Compare(todo(b).Due) })
		out = append(out, TodoGroup[T]{Section: TodoSection(s), Todos: ts})
	}
	return out
}
