package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

func TestTodoRowsAndReminders(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.Local)
	day := func(d int) time.Time { return time.Date(2026, 10, 26+d, 0, 0, 0, 0, time.Local) }
	todo := func(id string, due time.Time) store.PageTodo {
		return store.PageTodo{Todo: doc.Todo{BlockID: id, Text: id, Due: due, Owner: "Sam"}, PageID: "p"}
	}
	todos := []store.PageTodo{todo("later", day(3)), todo("owned", time.Time{}), todo("today", day(0)), todo("late", day(-2)), todo("soon", day(1))}
	var got []string
	for _, r := range todoRows(todos, now) {
		if r.header != "" {
			got = append(got, "# "+r.header)
		} else {
			got = append(got, r.todo.BlockID)
		}
	}
	want := "[# Overdue late # Today today # Upcoming soon later # No date owned]"
	if s := fmt.Sprint(got); s != want {
		t.Errorf("rows %s, want %s", s, want)
	}
	reminded := map[string]bool{"late": true}
	var due []string
	for _, r := range dueReminders(todos, now, func(t store.PageTodo) bool { return reminded[t.BlockID] }) {
		due = append(due, r.BlockID)
	}
	if fmt.Sprint(due) != "[today]" {
		t.Errorf("reminders %v", due)
	}
	// Before 9:00, today's waits.
	early := time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)
	if r := dueReminders(todos, early, func(store.PageTodo) bool { return false }); len(r) != 1 || r[0].BlockID != "late" {
		t.Errorf("early reminders %+v", r)
	}
}

// A to-do with a time of day is overdue, and reminded of, from its time.
func TestTimedTodos(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, time.Local) }
	todo := func(id string, due time.Time) store.PageTodo {
		return store.PageTodo{Todo: doc.Todo{BlockID: id, Text: id, Due: due, Timed: true}, PageID: "p"}
	}
	todos := []store.PageTodo{todo("meet", at(15, 0)), todo("call", at(8, 30))}
	var got []string
	for _, r := range todoRows(todos, at(10, 0)) {
		got = append(got, r.header+r.todo.BlockID)
	}
	if want := "[Overdue call Today meet]"; fmt.Sprint(got) != want {
		t.Errorf("rows %v, want %s", got, want)
	}
	none := func(store.PageTodo) bool { return false }
	if r := dueReminders(todos, at(10, 0), none); len(r) != 1 || r[0].BlockID != "call" {
		t.Errorf("reminders at 10:00 %+v", r)
	}
	if r := dueReminders(todos, at(15, 0), none); len(r) != 2 {
		t.Errorf("reminders at 15:00 %+v", r)
	}
}

// The To-dos tab lists to-dos from every page; checking one off checks
// it in its page.
func TestTodosView(t *testing.T) {
	a := templatesApp(t)
	n := &doc.Node{ID: "t1", Type: "todo_list"}
	n.SetDelta(doc.Delta{{Text: "Send the draft"}})
	n.Set("checked", false)
	n.SetDue(time.Now(), false)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{n})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.settings.SidebarOpen = true
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	if err := tt.Click("To-dos"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, s := range []string{"Today", "Send the draft", "PRD"} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %v", s, tt.Texts())
		}
	}
	if err := tt.Click("Done"); err != nil {
		t.Fatal(err)
	}
	if !a.st.Content(p.ID).Find("t1").Bool("checked") {
		t.Error("the to-do was not checked off in its page")
	}
	tt.Frame()
	if !tt.HasText("Nothing to do") {
		t.Errorf("still listed: %v", tt.Texts())
	}
}

// A to-do of a page shared with the user to view is listed, but only its
// editors check it off.
func TestTodosOfAPageToView(t *testing.T) {
	a := templatesApp(t)
	const id = "00000000-0000-0000-0000-0000000000bb"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.st.PutRemotePage(map[string]any{"id": id, "space_id": "00000000-0000-0000-0000-0000000000cc", "title": "Theirs",
		"sort_key": "V", "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	n := &doc.Node{ID: "t1", Type: "todo_list"}
	n.SetDelta(doc.Delta{{Text: "Review the budget"}})
	n.Set("checked", false)
	n.SetDue(time.Now(), false)
	if _, _, err := a.st.SaveContent(id, doc.FromBlocks([]*doc.Node{n})); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetMembers(id, []store.Member{{PageID: id, UserID: a.session.UserID, Role: "viewer", Accepted: true}}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.settings.SidebarOpen, a.settings.SidebarTab = true, tabTodos
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	if !tt.HasText("Review the budget") {
		t.Fatalf("not listed: %v", tt.Texts())
	}
	if err := tt.Click(locales.CommonDone()); err != nil {
		t.Fatal(err)
	}
	a.finishTodo(a.todoList()[0])
	if a.st.Content(id).Find("t1").Bool("checked") {
		t.Error("a to-do of a page to view was checked off")
	}
}
