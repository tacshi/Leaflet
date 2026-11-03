package doc

import (
	"testing"
	"time"
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
