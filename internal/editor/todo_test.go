package editor

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestDueDate(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("[] Send the draft")
	n := e.Content.Children[0]
	tt.Frame()
	// The caret is in the to-do, so it offers a date.
	if err := tt.Click("Due date: Add date"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if e.dueOpen != n.ID {
		t.Fatal("the calendar did not open")
	}
	tomorrow := time.Now().AddDate(0, 0, 1)
	e.SetDue(n.ID, tomorrow, false)
	tt.Frame()
	if _, ok := tt.Find("Due date: Tomorrow"); !ok {
		t.Errorf("no Tomorrow chip in %v", tt.Texts())
	}
	// The press on the chip went to it, not the text: the caret stays.
	if p := e.Caret(); p.ID != n.ID || p.Off != len("Send the draft") {
		t.Errorf("caret %v", p)
	}
	e.Undo()
	if _, _, ok := e.Content.Children[0].Due(); ok {
		t.Error("undo kept the date")
	}
}

// The popover changes a draft, which the to-do takes as it closes:
// turning the months keeps it open, and adds no date by itself.
func TestDuePopover(t *testing.T) {
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
	// Opened and closed untouched, it adds no date.
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
	if _, _, ok := n.Due(); ok {
		t.Fatal("the date was saved before the popover closed")
	}
	click("Add time")
	escape()
	now := time.Now()
	want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 1, 0)
	if due, timed, ok := n.Due(); !ok || !timed || !DueToday(due, want) || due.Hour() != dueHour || due.Minute() != 0 {
		t.Fatalf("due %v, timed %v", due, timed)
	}
	// It was one step of undo.
	e.Undo()
	if _, _, ok := e.Content.Children[0].Due(); ok {
		t.Fatal("undo kept the date")
	}
	// Today, the day the calendar starts on, is chosen by a click too.
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

func TestDueLabel(t *testing.T) {
	now := time.Date(2026, 10, 26, 20, 0, 0, 0, time.Local)
	for due, want := range map[time.Time]string{
		time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local): "Today",
		time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local): "Tomorrow",
		time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local): "Yesterday",
		time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local):  "Nov 3",
		time.Date(2027, 1, 3, 0, 0, 0, 0, time.Local):   "Jan 3, 2027",
	} {
		if got := DueLabel(due, false, now); got != want {
			t.Errorf("%v: %q, want %q", due, got, want)
		}
	}
	if !Overdue(time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local), false, now) || Overdue(time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local), false, now) {
		t.Error("overdue")
	}
	// With a time of day, it shows, and the to-do is overdue once it passed.
	if got := DueLabel(time.Date(2026, 10, 27, 15, 0, 0, 0, time.Local), true, now); got != "Tomorrow 3:00 PM" {
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
	local := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = local })
	for _, tc := range []struct {
		now, due time.Time
		want     string
	}{
		{time.Date(2026, 3, 8, 10, 0, 0, 0, ny), time.Date(2026, 3, 9, 0, 0, 0, 0, ny), "Tomorrow"},
		{time.Date(2026, 3, 9, 10, 0, 0, 0, ny), time.Date(2026, 3, 8, 0, 0, 0, 0, ny), "Yesterday"},
		{time.Date(2026, 3, 8, 23, 0, 0, 0, ny), time.Date(2026, 3, 8, 0, 0, 0, 0, ny), "Today"},
		{time.Date(2026, 11, 1, 10, 0, 0, 0, ny), time.Date(2026, 11, 2, 0, 0, 0, 0, ny), "Tomorrow"},
	} {
		if got := DueLabel(tc.due, false, tc.now); got != tc.want {
			t.Errorf("%v from %v: %q, want %q", tc.due, tc.now, got, tc.want)
		}
	}
}

// An empty to-do offers its date after its placeholder, where it would
// be with that text typed, not over it.
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

// Under the pointer without the caret, an empty to-do offers its date
// where it does with the caret, off where a click starts typing.
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
