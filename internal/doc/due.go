package doc

import (
	"slices"
	"time"
)

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

// TodoSection is a group of the To-dos tab, in the order the tab shows
// them: overdue, today's, the coming ones, and those with no date but an
// owner.
type TodoSection int

const (
	TodoOverdue TodoSection = iota
	TodoToday
	TodoUpcoming
	TodoNoDate
)

// Section is the group of the To-dos tab a to-do is in at now.
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

// TodoGroup is a section of the To-dos tab and its to-dos.
type TodoGroup[T any] struct {
	Section TodoSection
	Todos   []T
}

// TodoGroups sorts to-dos, each read by todo, into the To-dos tab's
// sections at now: in their order, each by date, the earliest first, and
// those of a date in the order given. Sections with none are left out.
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
