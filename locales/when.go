package locales

import (
	"time"

	"github.com/egoist/mygo/i18n"
)

func When(t, now time.Time) string { return In(i18n.Locale()).When(t, now) }

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

func Day(t, now time.Time) string { return In(i18n.Locale()).Day(t, now) }

func (msgs Messages) Day(t, now time.Time) string {
	if t, now = t.Local(), now.Local(); t.Year() == now.Year() {
		return msgs.PageTimeDate(t)
	}
	return msgs.PageTimeDateYear(t)
}

func (msgs Messages) minutesAgo(n int) string {
	_, lang, _ := Catalog.Message(msgs.locale, "pageTimeJustNow")
	return i18n.FormatRelativeTime(lang, -float64(n), i18n.RelativeMinute, i18n.RelativeTimeOptions{})
}
