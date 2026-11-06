package locales

import (
	"time"

	"github.com/egoist/mygo/i18n"
)

// When says when t was, at now, in the language the app shows, as
// bylines, comments and settings say it.
func When(t, now time.Time) string { return In(i18n.Locale()).When(t, now) }

// When says when t was, at now, in the language of msgs, on the desktop
// and the phone alike: just now, minutes ago, today or yesterday at a
// time, or the date, with its year when not now's. Today and yesterday
// are days of the calendar, here. A zero time says nothing.
func (msgs Messages) When(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	t, now = t.Local(), now.Local()
	y, m, day := now.Date()
	today := time.Date(y, m, day, 0, 0, 0, 0, time.Local)
	switch {
	case d < time.Minute:
		return msgs.PageTimeJustNow()
	case d < time.Hour:
		return msgs.minutesAgo(int(d.Minutes()))
	case !t.Before(today):
		return msgs.PageTimeTodayAt(t)
	case !t.Before(today.AddDate(0, 0, -1)):
		return msgs.PageTimeYesterdayAt(t)
	}
	return msgs.Day(t, now)
}

// Day says the day of t, at now, in the language the app shows, with its
// year when not now's.
func Day(t, now time.Time) string { return In(i18n.Locale()).Day(t, now) }

// Day says the day of t, at now, in the language of msgs, with its year
// when not now's, here.
func (msgs Messages) Day(t, now time.Time) string {
	if t, now = t.Local(), now.Local(); t.Year() == now.Year() {
		return msgs.PageTimeDate(t)
	}
	return msgs.PageTimeDateYear(t)
}

// minutesAgo says n minutes ago in the language the catalog takes for msgs,
// so that it never differs from the words around it. Any message tells that
// language; TestMinutesAgoInEachLanguage fails if this one goes.
func (msgs Messages) minutesAgo(n int) string {
	_, lang, _ := Catalog.Message(msgs.locale, "pageTimeJustNow")
	return i18n.FormatRelativeTime(lang, -float64(n), i18n.RelativeMinute, i18n.RelativeTimeOptions{})
}
