package editor

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func fixClock(t *testing.T) time.Time {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	was := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = was })
	return at
}

func TestDueDate(t *testing.T) {
	now := fixClock(t)
	e, tt, _ := setup(t)
	tt.Type("[] Send the draft")
	n := e.Content.Children[0]
	tt.Frame()
	if err := tt.Click("Due date: Add date"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if e.dueOpen != n.ID {
		t.Fatal("the calendar did not open")
	}
	tomorrow := now.AddDate(0, 0, 1)
	e.SetDue(n.ID, tomorrow, false)
	tt.Frame()
	if _, ok := tt.Find("Due date: Tomorrow"); !ok {
		t.Errorf("no Tomorrow chip in %v", tt.Texts())
	}
	if p := e.Caret(); p.ID != n.ID || p.Off != len("Send the draft") {
		t.Errorf("caret %v", p)
	}
	e.Undo()
	if _, _, ok := e.Content.Children[0].Due(); ok {
		t.Error("undo kept the date")
	}
}

func TestDuePopover(t *testing.T) {
	now := fixClock(t)
	e, tt, _ := setup(t)
	tt.Type("[] Send the draft")
	n := e.Content.Children[0]
	tt.Frame()
	click := func(s string) {
		t.Helper()
		if err := tt.Click(s); err != nil {
			t.Fatalf("%v in %v", err, tt.Texts())
		}
		tt.Frame()
	}
	escape := func() {
		t.Helper()
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if e.dueOpen != "" {
			t.Fatal("the popover is still open")
		}
	}
	click("Due date: Add date")
	escape()
	if _, _, ok := n.Due(); ok {
		t.Fatal("an untouched popover added a date")
	}
	click("Due date: Add date")
	click("Next month")
	if e.dueOpen != n.ID {
		t.Fatal("the next month closed the popover")
	}
	if !DueToday(e.dues[n.ID].at, now) {
		t.Fatalf("the next month chose %v", e.dues[n.ID].at)
	}
	want := time.Date(now.Year(), now.Month()+1, 15, 0, 0, 0, 0, time.Local)
	click(want.Format("January 2, 2006"))
	if _, _, ok := n.Due(); ok {
		t.Fatal("the date was saved before the popover closed")
	}
	click("Add time")
	escape()
	if due, timed, ok := n.Due(); !ok || !timed || !DueToday(due, want) || due.Hour() != dueHour || due.Minute() != 0 {
		t.Fatalf("due %v, timed %v", due, timed)
	}
	e.Undo()
	if _, _, ok := e.Content.Children[0].Due(); ok {
		t.Fatal("undo kept the date")
	}
	n = e.Content.Children[0]
	e.Focus()
	tt.Frame()
	click("Due date: Add date")
	click(now.Format("January 2, 2006"))
	escape()
	if due, timed, ok := n.Due(); !ok || timed || !DueToday(due, now) {
		t.Fatalf("due %v, timed %v, ok %v", due, timed, ok)
	}
}

func TestDueChipAfterPlaceholder(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("[] ")
	n := e.Content.Children[0]
	tt.Frame()
	empty, ok := e.chips[n.ID]
	if !ok {
		t.Fatalf("no date offered in %v", tt.Texts())
	}
	tt.Type(placeholder(n, true))
	tt.Frame()
	typed := e.chips[n.ID]
	if d := empty.X - typed.X; d < -1 || d > 1 {
		t.Errorf("the date is at %v on the empty to-do, %v after its placeholder's text", empty.X, typed.X)
	}
}

func TestDueChipStaysPut(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("Intro")
	tt.Key(0, ui.KeyEnter)
	tt.Type("[] ")
	n := e.Content.Children[1]
	tt.Frame()
	focused, ok := e.chips[n.ID]
	if !ok {
		t.Fatalf("no date offered in %v", tt.Texts())
	}
	e.PlaceCaret(e.Content.Children[0].ID, 0)
	tt.Move(focused.X+focused.W/2, focused.Y+focused.H/2)
	tt.Frame()
	if hovered, ok := e.chips[n.ID]; !ok || hovered.X != focused.X {
		t.Errorf("the date is at %v under the pointer, %v with the caret", hovered.X, focused.X)
	}
}

func TestDuePopoversKeepTheirDrafts(t *testing.T) {
	now := fixClock(t)
	e, tt, _ := setup(t)
	tt.Type("[] Above")
	tt.Key(0, ui.KeyEnter)
	tt.Type("Below")
	above, below := e.Content.Children[0], e.Content.Children[1]
	e.SetDue(above.ID, now.AddDate(0, 0, 1), false)
	tt.Frame()
	click := func(s string) {
		t.Helper()
		if err := tt.Click(s); err != nil {
			t.Fatalf("%v in %v", err, tt.Texts())
		}
		tt.Frame()
	}
	click("Due date: Add date")
	if e.dueOpen != below.ID {
		t.Fatal("the calendar did not open")
	}
	day := time.Date(now.Year(), now.Month(), 15, 0, 0, 0, 0, time.Local)
	click(day.Format("January 2, 2006"))
	click("Due date: Tomorrow")
	tt.Frame()
	if due, _, ok := below.Due(); !ok || !DueToday(due, day) {
		t.Errorf("below is due %v, %v; want %v", due, ok, day)
	}
	if e.dueOpen != above.ID || e.dues[above.ID] == nil {
		t.Errorf("open is %q, drafts %v", e.dueOpen, e.dues)
	}
	if due, _, _ := above.Due(); !DueToday(due, now.AddDate(0, 0, 1)) {
		t.Errorf("above is due %v", due)
	}
}
