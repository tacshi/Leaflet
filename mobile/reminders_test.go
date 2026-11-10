package mobile

import (
	"encoding/json"
	"testing"
	"time"

	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/store"
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
	raw, err := c.Reminders(now.UnixMilli(), limit)
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
