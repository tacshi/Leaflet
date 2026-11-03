package locales

import (
	"testing"
	"time"
)

// Today and yesterday are days of the calendar, not 24 and 48 hours; a
// time under an hour ago says how long ago, and a zero time nothing.
func TestWhenByTheCalendar(t *testing.T) {
	now := time.Date(2026, 3, 10, 9, 0, 0, 0, time.Local)
	msgs := In("en")
	at := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, time.Local)
	}
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{now.Add(-20 * time.Second), msgs.PageTimeJustNow()},
		{now.Add(time.Hour), msgs.PageTimeJustNow()},
		{now.Add(-5 * time.Minute), msgs.PageTimeMinutesAgo(5)},
		{at(3, 10, 1, 0), msgs.PageTimeTodayAt(at(3, 10, 1, 0))},
		{at(3, 9, 23, 0), msgs.PageTimeYesterdayAt(at(3, 9, 23, 0))},
		{at(3, 9, 0, 30), msgs.PageTimeYesterdayAt(at(3, 9, 0, 30))},
		{at(3, 8, 23, 0), msgs.PageTimeDate(at(3, 8, 23, 0))},
		{time.Date(2025, 12, 31, 12, 0, 0, 0, time.Local), msgs.PageTimeDateYear(time.Date(2025, 12, 31, 12, 0, 0, 0, time.Local))},
		{time.Time{}, ""},
	} {
		if got := msgs.When(c.at.UTC(), now); got != c.want {
			t.Errorf("%v: %q, want %q", c.at, got, c.want)
		}
	}
	if got := In("ja").When(now.Add(-20*time.Second), now); got != In("ja").PageTimeJustNow() || got == msgs.PageTimeJustNow() {
		t.Errorf("in Japanese: %q", got)
	}
}
