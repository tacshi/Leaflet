package core

import (
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

func TestRemindersReadDuesInTheZoneOfNow(t *testing.T) {
	read := time.FixedZone("New York", -5*3600)
	berlin := time.FixedZone("Berlin", 3600)
	todo := func(id string, due time.Time, timed bool) store.PageTodo {
		return store.PageTodo{Todo: doc.Todo{BlockID: id, Text: id, Due: due, Timed: timed}, PageID: "p"}
	}
	pack := todo("pack", time.Date(2026, 12, 2, 0, 0, 0, 0, read), false)
	call := todo("call", time.Date(2026, 12, 2, 15, 0, 0, 0, read), true)
	if at, want := ReminderAt(pack, berlin), time.Date(2026, 12, 2, ReminderHour, 0, 0, 0, berlin); !at.Equal(want) {
		t.Errorf("a day's reminder at %s, want %s", at, want)
	}
	if at, want := ReminderAt(call, berlin), time.Date(2026, 12, 2, 15, 0, 0, 0, berlin); !at.Equal(want) {
		t.Errorf("a time's reminder at %s, want %s", at, want)
	}
	none := func(store.PageTodo) bool { return false }
	due := DueReminders([]store.PageTodo{pack, call}, time.Date(2026, 12, 2, 10, 0, 0, 0, berlin), none)
	if len(due) != 1 || due[0].BlockID != "pack" {
		t.Errorf("at 10:00 in Berlin, due %+v: want only pack's, whose 9:00 has come there", due)
	}
}
