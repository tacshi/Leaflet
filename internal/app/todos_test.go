package app

import (
	"fmt"
	"slices"
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
	// One section alone has no header: the to-dos' dates say as much.
	if rows := todoRows(todos[:1], now); len(rows) != 1 || rows[0].header != "" {
		t.Errorf("one section's rows %+v", rows)
	}
	reminded := map[string]bool{"late": true}
	var due []string
	for _, r := range dueReminders(todos, now, func(t store.PageTodo) bool { return reminded[t.BlockID] }) {
		due = append(due, r.BlockID)
	}
	if fmt.Sprint(due) != "[today]" {
		t.Errorf("reminders %v", due)
	}
	// Before 9:00, today's waits, and one two days late is past being
	// reminded of: it is among the overdue.
	early := time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)
	if r := dueReminders(todos, early, func(store.PageTodo) bool { return false }); len(r) != 0 {
		t.Errorf("early reminders %+v", r)
	}
	// Yesterday's, from 9:00, is reminded of until 9:00 today.
	yesterday := []store.PageTodo{todo("yesterday", day(-1))}
	if r := dueReminders(yesterday, early, func(store.PageTodo) bool { return false }); len(r) != 1 {
		t.Errorf("reminders of yesterday's at 8:00 %+v", r)
	}
	if r := dueReminders(yesterday, now, func(store.PageTodo) bool { return false }); len(r) != 0 {
		t.Errorf("reminders of yesterday's at 10:00 %+v", r)
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

// A click on a reminder before the app has its window, as one that
// launched it, opens the to-do once it has; another kind of notification
// opens nothing.
func TestReminderClickBeforeTheWindow(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Trip", Content: doc.FromBlocks([]*doc.Node{textNode("Pack")})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	block := a.st.Content(p.ID).Children[0].ID
	a.notificationClicked("other:" + p.ID)
	a.notificationClicked(todoNotification + p.ID + "#" + block)
	if len(a.early) != 1 || a.page != nil && a.page.id == p.ID {
		t.Fatalf("%d waiting, page open %v", len(a.early), a.page != nil)
	}
	a.early[0]()
	if a.page == nil || a.page.id != p.ID || a.page.editor.Caret().ID != block {
		t.Errorf("the to-do's page isn't open at it")
	}
}

// The user is reminded of to-dos given to them, on any page, and of those
// given to no one on their own pages, not of another's nor of one on a
// page shared with them that names no one; nor again once reminded, nor
// of one overdue for days, as on a device new to the library.
func TestRemindersOfTheUsersOwnTodos(t *testing.T) {
	a := templatesApp(t)
	now := time.Now()
	now = time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.Local)
	todo := func(id, text string, due time.Time, owner string) *doc.Node {
		n := &doc.Node{ID: id, Type: "todo_list"}
		d := doc.Delta{{Text: text + " "}}
		if owner != "" {
			d = append(d, doc.Run{Text: owner, Attrs: doc.Attrs{Mention: &doc.Mention{Kind: "user", ID: owner, Name: owner}}})
		}
		n.SetDelta(d)
		n.Set("checked", false)
		n.SetDue(due, false)
		return n
	}
	me := a.session.UserID
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Mine", Content: doc.FromBlocks([]*doc.Node{
		todo("call", "Call the bank", now, ""),
		todo("ann", "Review", now, "ann"),
		todo("mine", "Send it", now, me),
		todo("old", "Renew", now.AddDate(0, 0, -3), ""),
	})}); err != nil {
		t.Fatal(err)
	}
	const theirs = "00000000-0000-0000-0000-0000000000bb"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.st.PutRemotePage(map[string]any{"id": theirs, "space_id": "00000000-0000-0000-0000-0000000000cc", "title": "Theirs",
		"sort_key": "V", "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(theirs, doc.FromBlocks([]*doc.Node{
		todo("shared", "Budget", now, ""),
		todo("given", "Draft the plan", now, me),
	})); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetMembers(theirs, []store.Member{{PageID: theirs, UserID: me, Role: "editor", Accepted: true}}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	ids := func() []string {
		var out []string
		for _, r := range a.reminders(now) {
			out = append(out, r.BlockID)
		}
		slices.Sort(out)
		return out
	}
	if got := fmt.Sprint(ids()); got != "[call given mine]" {
		t.Errorf("reminded of %s", got)
	}
	for _, r := range a.reminders(now) {
		if r.BlockID == "call" {
			if err := a.st.SetValue(reminderKey(r), dueValue(r)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := fmt.Sprint(ids()); got != "[given mine]" {
		t.Errorf("once reminded of the call, reminded of %s", got)
	}
}
