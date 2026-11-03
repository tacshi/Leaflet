package doc

import (
	"testing"
	"time"

	"leaflet/locales"
)

// The To-dos tab's sections come in order, overdue first, each by date,
// and those of no date as they came; a section with none is left out.
func TestTodoGroups(t *testing.T) {
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.Local)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.Local) }
	todos := []Todo{
		{BlockID: "later", Due: day(9)},
		{BlockID: "owner b", Owner: "Sam"},
		{BlockID: "soon", Due: day(4)},
		{BlockID: "this morning", Due: now.Add(-5 * time.Hour), Timed: true},
		{BlockID: "today", Due: day(3)},
		{BlockID: "owner a", Owner: "Ann"},
		{BlockID: "last week", Due: day(27).AddDate(0, -1, 0)},
	}
	groups := TodoGroups(todos, func(t Todo) Todo { return t }, now)
	var got [][]string
	var sections []TodoSection
	for _, g := range groups {
		sections = append(sections, g.Section)
		var ids []string
		for _, t := range g.Todos {
			ids = append(ids, t.BlockID)
		}
		got = append(got, ids)
	}
	want := [][]string{{"last week", "this morning"}, {"today"}, {"soon", "later"}, {"owner b", "owner a"}}
	if len(got) != len(want) {
		t.Fatalf("sections %v: %v", sections, got)
	}
	for i := range want {
		if sections[i] != TodoSection(i) || len(got[i]) != len(want[i]) {
			t.Fatalf("sections %v: %v, want %v", sections, got, want)
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("section %d: %v, want %v", i, got[i], want[i])
			}
		}
	}
	if g := TodoGroups(todos[:1], func(t Todo) Todo { return t }, now); len(g) != 1 || g[0].Section != TodoUpcoming {
		t.Errorf("one to-do: %+v", g)
	}
}

// A due date says when it is from now: Today, Tomorrow, Yesterday, or the
// day, with its year when not this one's, and its time when timed; a day
// is overdue once it has passed, and a time as soon as it has.
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

// The days are counted on the calendar: the day the clocks go forward is
// 23 hours long, and the day after it is still tomorrow.
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
