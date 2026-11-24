package doc

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"leaflet/locales"
)

func TestTodoDays(t *testing.T) {
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.Local)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.Local) }
	todos := []Todo{
		{BlockID: "later", Due: day(9)},
		{BlockID: "owner b", Owner: "Sam"},
		{BlockID: "soon", Due: day(4)},
		{BlockID: "tonight", Due: day(3).Add(20 * time.Hour), Timed: true},
		{BlockID: "this morning", Due: now.Add(-5 * time.Hour), Timed: true},
		{BlockID: "today", Due: day(3)},
		{BlockID: "owner a", Owner: "Ann"},
		{BlockID: "last week", Due: day(27).AddDate(0, -1, 0)},
		{BlockID: "also soon", Due: day(4)},
		{BlockID: "last week too", Due: day(27).AddDate(0, -1, 0).Add(9 * time.Hour), Timed: true},
	}
	var got []string
	for _, d := range TodoDays(todos, func(t Todo) Todo { return t }, now) {
		var ids []string
		for _, t := range d.Todos {
			ids = append(ids, t.BlockID)
		}
		got = append(got, fmt.Sprintf("%s %d %v", d.Key(), d.Section, ids))
	}
	want := []string{
		"2026-10-27 0 [last week last week too]",
		"2026-11-03 1 [today this morning tonight]",
		"2026-11-04 2 [soon also soon]",
		"2026-11-09 2 [later]",
		TodoNoDateKey + " 3 [owner b owner a]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("days\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if d := TodoDays(todos[1:2], func(t Todo) Todo { return t }, now); len(d) != 1 || d[0].Section != TodoNoDate || !d[0].Day.IsZero() {
		t.Errorf("one to-do with no date: %+v", d)
	}
	if d := TodoDays([]Todo{}, func(t Todo) Todo { return t }, now); len(d) != 0 {
		t.Errorf("no to-dos: %+v", d)
	}
}

func TestTodoDayAt(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.Local)
	todos := []Todo{{BlockID: "soon", Due: time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local)}, {BlockID: "someday"}}
	days := TodoDays(todos, func(t Todo) Todo { return t }, now)
	if d := TodoDayAt(days, "2026-10-27", now); len(d.Todos) != 1 || d.Todos[0].BlockID != "soon" || d.Section != TodoUpcoming {
		t.Errorf("tomorrow %+v", d)
	}
	if d := TodoDayAt(days, "2026-10-25", now); len(d.Todos) != 0 || d.Section != TodoOverdue || d.Key() != "2026-10-25" {
		t.Errorf("a day with nothing left on it %+v", d)
	}
	if d := TodoDayAt(days, "2026-10-26", now); len(d.Todos) != 0 || d.Section != TodoToday {
		t.Errorf("today with nothing on it %+v", d)
	}
	if d := TodoDayAt(days, "2026-10-30", now); len(d.Todos) != 0 || d.Section != TodoUpcoming {
		t.Errorf("a day to come with nothing on it %+v", d)
	}
	if d := TodoDayAt(days, TodoNoDateKey, now); len(d.Todos) != 1 || d.Section != TodoNoDate {
		t.Errorf("no date %+v", d)
	}
	if d := TodoDayAt[Todo](nil, TodoNoDateKey, now); len(d.Todos) != 0 || d.Section != TodoNoDate || d.Key() != TodoNoDateKey {
		t.Errorf("no date with nothing on it %+v", d)
	}
}

func TestTodoDayByPage(t *testing.T) {
	type pageTodo struct{ page, text string }
	d := TodoDay[pageTodo]{Todos: []pageTodo{{"list", "Listen"}, {"work", "Invoice"}, {"list", "Retell"}}}
	var got []string
	for _, todos := range d.ByPage(func(t pageTodo) string { return t.page }) {
		got = append(got, fmt.Sprint(todos))
	}
	if want := "[[{list Listen} {list Retell}] [{work Invoice}]]"; fmt.Sprint(got) != want {
		t.Errorf("by page %v, want %s", got, want)
	}
	if got := (TodoDay[pageTodo]{}).ByPage(func(t pageTodo) string { return t.page }); len(got) != 0 {
		t.Errorf("no to-dos: %v", got)
	}
}

func TestTodoDaysOverAChangeOfClocks(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	local := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = local })
	todos := []Todo{
		{BlockID: "before", Due: time.Date(2026, 11, 1, 0, 30, 0, 0, ny), Timed: true},
		{BlockID: "after", Due: time.Date(2026, 11, 1, 23, 30, 0, 0, ny), Timed: true},
		{BlockID: "next", Due: time.Date(2026, 11, 2, 0, 0, 0, 0, ny)},
	}
	days := TodoDays(todos, func(t Todo) Todo { return t }, time.Date(2026, 11, 1, 12, 0, 0, 0, ny))
	if len(days) != 2 || days[0].Key() != "2026-11-01" || len(days[0].Todos) != 2 || days[0].Section != TodoToday || days[1].Section != TodoUpcoming {
		t.Errorf("over the end of summer time: %+v", days)
	}
}

func TestDueLabel(t *testing.T) {
	en := locales.In("en")
	now := time.Date(2026, 10, 26, 20, 0, 0, 0, time.Local)
	for due, want := range map[time.Time]string{
		time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local): "Today",
		time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local): "Tomorrow",
		time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local): "Yesterday",
		time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local):  "Nov 3",
		time.Date(2027, 1, 3, 0, 0, 0, 0, time.Local):   "Jan 3, 2027",
	} {
		if got := DueLabel(en, due, false, now); got != want {
			t.Errorf("%v: %q, want %q", due, got, want)
		}
	}
	if !Overdue(time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local), false, now) || Overdue(time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local), false, now) {
		t.Error("overdue")
	}
	if got := DueLabel(en, time.Date(2026, 10, 27, 15, 0, 0, 0, time.Local), true, now); got != "Tomorrow 3:00 PM" {
		t.Errorf("timed label %q", got)
	}
	if !Overdue(time.Date(2026, 10, 26, 19, 30, 0, 0, time.Local), true, now) || Overdue(time.Date(2026, 10, 26, 20, 30, 0, 0, time.Local), true, now) {
		t.Error("timed overdue")
	}
}

func TestDueLabelOverAChangeOfClocks(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	for _, tc := range []struct {
		now, due time.Time
		want     string
	}{
		{time.Date(2026, 3, 8, 10, 0, 0, 0, ny), time.Date(2026, 3, 9, 0, 0, 0, 0, ny), "Tomorrow"},
		{time.Date(2026, 3, 9, 10, 0, 0, 0, ny), time.Date(2026, 3, 8, 0, 0, 0, 0, ny), "Yesterday"},
		{time.Date(2026, 3, 8, 23, 0, 0, 0, ny), time.Date(2026, 3, 8, 0, 0, 0, 0, ny), "Today"},
		{time.Date(2026, 11, 1, 10, 0, 0, 0, ny), time.Date(2026, 11, 2, 0, 0, 0, 0, ny), "Tomorrow"},
	} {
		if got := DueLabel(locales.In("en"), tc.due, false, tc.now); got != tc.want {
			t.Errorf("%v from %v: %q, want %q", tc.due, tc.now, got, tc.want)
		}
	}
}
