package editor

import (
	"testing"
	"time"
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
	e.SetDue(n.ID, tomorrow)
	tt.Frame()
	if _, ok := tt.Find("Due date: Tomorrow"); !ok {
		t.Errorf("no Tomorrow chip in %v", tt.Texts())
	}
	// The press on the chip went to it, not the text: the caret stays.
	if p := e.Caret(); p.ID != n.ID || p.Off != len("Send the draft") {
		t.Errorf("caret %v", p)
	}
	e.Undo()
	if _, ok := e.Content.Children[0].Due(); ok {
		t.Error("undo kept the date")
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
		if got := DueLabel(due, now); got != want {
			t.Errorf("%v: %q, want %q", due, got, want)
		}
	}
	if !Overdue(time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local), now) || Overdue(time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local), now) {
		t.Error("overdue")
	}
}
