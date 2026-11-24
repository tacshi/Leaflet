package mobile

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

func todoPage(t *testing.T, c *Core, title string, due map[string]time.Time) store.Page {
	t.Helper()
	var blocks []*doc.Node
	for id, at := range due {
		n := &doc.Node{ID: id, Type: "todo_list"}
		n.SetDelta(doc.Delta{{Text: "Do " + id}})
		n.Set("checked", false)
		n.SetDue(at, true)
		blocks = append(blocks, n)
	}
	p, err := c.core.Store.CreatePage(store.NewPageOptions{Title: title, Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func reminders(t *testing.T, c *Core, now time.Time, limit int) []reminder {
	t.Helper()
	_, offset := now.Zone()
	raw, err := c.Reminders(now.UnixMilli(), limit, now.Location().String(), offset)
	if err != nil {
		t.Fatal(err)
	}
	var out []reminder
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRemindersDueThenComing(t *testing.T) {
	c, _ := open(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)
	p := todoPage(t, c, "Plan", map[string]time.Time{
		"due":   now.Add(-time.Hour),
		"old":   now.Add(-3 * 24 * time.Hour),
		"later": now.Add(48 * time.Hour),
		"soon":  now.Add(time.Hour),
	})
	got := reminders(t, c, now, 10)
	var ids []string
	for _, r := range got {
		ids = append(ids, r.BlockID)
	}
	if want := []string{"due", "soon", "later"}; len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("reminders %v, want %v", ids, want)
	}
	if got[0].At != 0 {
		t.Errorf("a to-do due shows at %d, not at once", got[0].At)
	}
	if want := now.Add(time.Hour).UnixMilli(); got[1].At != want {
		t.Errorf("at %d, want %d", got[1].At, want)
	}
	if r := got[1]; r.ID != core.ReminderID(p.ID, "soon") || r.Subtitle != "Plan" || r.Body != "Do soon" || r.Title == "" {
		t.Errorf("reminder %+v", r)
	}
}

func TestRemindedOnceForEachDate(t *testing.T) {
	c, _ := open(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)
	todoPage(t, c, "Plan", map[string]time.Time{"soon": now.Add(time.Hour)})
	first := reminders(t, c, now, 10)
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RemindedOf(string(raw)); err != nil {
		t.Fatal(err)
	}
	if got := reminders(t, c, now, 10); len(got) != 1 {
		t.Errorf("to come, %d reminders", len(got))
	}
	if got := reminders(t, c, now.Add(2*time.Hour), 10); len(got) != 0 {
		t.Errorf("reminded twice: %+v", got)
	}
	first[0].Due = "2026-03-09"
	raw, _ = json.Marshal(first)
	if err := c.RemindedOf(string(raw)); err != nil {
		t.Fatal(err)
	}
	if got := reminders(t, c, now.Add(2*time.Hour), 10); len(got) != 1 {
		t.Errorf("another date, %d reminders", len(got))
	}
}

func TestRemindersStopAtTheLimit(t *testing.T) {
	c, _ := open(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)
	todoPage(t, c, "Plan", map[string]time.Time{
		"a": now.Add(3 * time.Hour), "b": now.Add(time.Hour), "c": now.Add(2 * time.Hour),
	})
	got := reminders(t, c, now, 2)
	if len(got) != 2 || got[0].BlockID != "b" || got[1].BlockID != "c" {
		t.Errorf("reminders %+v", got)
	}
}

func TestRemindersLeaveOthersToDos(t *testing.T) {
	c, _ := open(t)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)
	n := &doc.Node{ID: "t", Type: "todo_list"}
	n.SetDelta(doc.Delta{{Text: "Ann", Attrs: doc.Attrs{Mention: &doc.Mention{Kind: "user", ID: "ann", Name: "Ann"}}}})
	n.Set("checked", false)
	n.SetDue(now.Add(time.Hour), true)
	if _, err := c.core.Store.CreatePage(store.NewPageOptions{Title: "Plan", Content: doc.FromBlocks([]*doc.Node{n})}); err != nil {
		t.Fatal(err)
	}
	if got := reminders(t, c, now, 10); len(got) != 0 {
		t.Errorf("reminded of another's: %+v", got)
	}
}

func dueOn(id, text, due string) *doc.Node {
	n := &doc.Node{ID: id, Type: "todo_list"}
	n.SetDelta(doc.Delta{{Text: text}})
	n.Set("checked", false)
	n.Set("due", due)
	return n
}

// openIn opens a core in loc, the zone the device was in as the app opened.
func openIn(t *testing.T, loc *time.Location, todos ...*doc.Node) *Core {
	t.Helper()
	old := time.Local
	setLocal(loc)
	t.Cleanup(func() { setLocal(old) })
	c, _ := open(t)
	if _, err := c.core.Store.CreatePage(store.NewPageOptions{Title: "Trip", Content: doc.FromBlocks(todos)}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRemindersComeAtTheirTimeInTheZoneTheDeviceIsIn(t *testing.T) {
	c := openIn(t, time.FixedZone("New York", -5*3600),
		dueOn("pack", "Pack", "2026-12-02"), dueOn("call", "Call home", "2026-12-02T15:00"))
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	got := reminders(t, c, time.Date(2026, 12, 1, 12, 0, 0, 0, berlin), 10)
	want := map[string]time.Time{
		"pack": time.Date(2026, 12, 2, core.ReminderHour, 0, 0, 0, berlin),
		"call": time.Date(2026, 12, 2, 15, 0, 0, 0, berlin),
	}
	if len(got) != len(want) {
		t.Fatalf("reminders %+v", got)
	}
	for _, r := range got {
		if at := time.UnixMilli(r.At); !at.Equal(want[r.BlockID]) {
			t.Errorf("%s reminds at %s, want %s", r.BlockID, at.In(berlin), want[r.BlockID])
		}
	}
	got = reminders(t, c, time.Date(2026, 12, 2, 10, 0, 0, 0, berlin), 10)
	if len(got) != 2 || got[0].BlockID != "pack" || got[0].At != 0 || got[1].BlockID != "call" {
		t.Errorf("at 10:00 in Berlin, reminders %+v: want Pack's due, and the call's to come", got)
	}
}

func TestTodosShowTheirDuesInTheZoneTheDeviceIsIn(t *testing.T) {
	c := openIn(t, time.FixedZone("Tokyo", 9*3600),
		dueOn("pack", "Pack", "2026-12-02"), dueOn("call", "Call home", "2026-12-02T15:00"))
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.todosAt(time.Date(2026, 12, 2, 8, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatal(err)
	}
	var groups []todoGroup
	if err := json.Unmarshal([]byte(raw), &groups); err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{
		"pack": time.Date(2026, 12, 2, 0, 0, 0, 0, ny),
		"call": time.Date(2026, 12, 2, 15, 0, 0, 0, ny),
	}
	if len(groups) != 1 || groups[0].Section != "today" || groups[0].Count != len(want) || len(groups[0].Pages) != 1 {
		t.Fatalf("at 8:00 in New York, to-dos %+v: want both today", groups)
	}
	for _, td := range groups[0].Pages[0].Todos {
		if due := time.UnixMilli(td.Due); !due.Equal(want[td.BlockID]) {
			t.Errorf("%s is due %s, want %s", td.BlockID, due.In(ny), want[td.BlockID])
		}
	}
}

func TestAReminderTitleTellsTheDayOnTheDevicesClock(t *testing.T) {
	c := openIn(t, time.FixedZone("Tokyo", 9*3600), dueOn("call", "Call home", "2026-12-02T15:00"))
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	got := reminders(t, c, time.Date(2026, 12, 2, 16, 0, 0, 0, ny), 10)
	if want := locales.In(i18n.Locale()).TodosDueToday(); len(got) != 1 || got[0].Title != want {
		t.Errorf("an hour after a call due at 15:00 today, reminders %+v: want one titled %q", got, want)
	}
}
