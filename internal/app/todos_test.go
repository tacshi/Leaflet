package app

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

func TestTodoDaysAndReminders(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.Local)
	day := func(d int) time.Time { return time.Date(2026, 10, 26+d, 0, 0, 0, 0, time.Local) }
	todo := func(id string, due time.Time) store.PageTodo {
		return store.PageTodo{Todo: doc.Todo{BlockID: id, Text: id, Due: due, Owner: "Sam"}, PageID: "p"}
	}
	todos := []store.PageTodo{todo("later", day(3)), todo("owned", time.Time{}), todo("today", day(0)), todo("late", day(-2)), todo("soon", day(1))}
	var got []string
	for _, d := range todoDays(todos, now) {
		got = append(got, fmt.Sprintf("%s %s %d", d.Key(), todoDayTitle(d, now), len(d.Todos)))
	}
	if want := "[2026-10-24 Oct 24 1 2026-10-26 Today 1 2026-10-27 Tomorrow 1 2026-10-29 Oct 29 1 no-date No date 1]"; fmt.Sprint(got) != want {
		t.Errorf("days %v, want %s", got, want)
	}
	days := todoDays(todos, now)
	if d := todoDayAt(days, "2026-10-27", now); len(d.Todos) != 1 || d.Todos[0].BlockID != "soon" {
		t.Errorf("tomorrow %+v", d)
	}
	if d := todoDayAt(days, "2026-10-25", now); len(d.Todos) != 0 || d.Section != doc.TodoOverdue || todoDayTitle(d, now) != "Yesterday" {
		t.Errorf("a day with nothing left on it %+v", d)
	}
	if d := todoDayAt(days, "2026-10-30", now); d.Section != doc.TodoUpcoming {
		t.Errorf("a day to come with nothing on it %+v", d)
	}
	if d := todoDayAt(nil, noDateKey, now); d.Section != doc.TodoNoDate || todoDayTitle(d, now) != "No date" {
		t.Errorf("no date with nothing on it %+v", d)
	}
	reminded := map[string]bool{"late": true}
	var due []string
	for _, r := range core.DueReminders(todos, now, func(t store.PageTodo) bool { return reminded[t.BlockID] }) {
		due = append(due, r.BlockID)
	}
	if fmt.Sprint(due) != "[today]" {
		t.Errorf("reminders %v", due)
	}
	early := time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)
	if r := core.DueReminders(todos, early, func(store.PageTodo) bool { return false }); len(r) != 0 {
		t.Errorf("early reminders %+v", r)
	}
	yesterday := []store.PageTodo{todo("yesterday", day(-1))}
	if r := core.DueReminders(yesterday, early, func(store.PageTodo) bool { return false }); len(r) != 1 {
		t.Errorf("reminders of yesterday's at 8:00 %+v", r)
	}
	if r := core.DueReminders(yesterday, now, func(store.PageTodo) bool { return false }); len(r) != 0 {
		t.Errorf("reminders of yesterday's at 10:00 %+v", r)
	}
}

func TestTimedTodos(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, time.Local) }
	todo := func(id string, due time.Time) store.PageTodo {
		return store.PageTodo{Todo: doc.Todo{BlockID: id, Text: id, Due: due, Timed: true}, PageID: "p"}
	}
	todos := []store.PageTodo{todo("meet", at(15, 0)), todo("call", at(8, 30))}
	var got []string
	for _, d := range todoDays(todos, at(10, 0)) {
		got = append(got, todoDayTitle(d, at(10, 0)))
		for _, t := range d.Todos {
			got = append(got, t.BlockID)
		}
	}
	if want := "[Today call meet]"; fmt.Sprint(got) != want {
		t.Errorf("rows %v, want %s", got, want)
	}
	none := func(store.PageTodo) bool { return false }
	if r := core.DueReminders(todos, at(10, 0), none); len(r) != 1 || r[0].BlockID != "call" {
		t.Errorf("reminders at 10:00 %+v", r)
	}
	if r := core.DueReminders(todos, at(15, 0), none); len(r) != 2 {
		t.Errorf("reminders at 15:00 %+v", r)
	}
}

func todoNode(id, text string, due time.Time) *doc.Node {
	n := &doc.Node{ID: id, Type: "todo_list"}
	n.SetDelta(doc.Delta{{Text: text}})
	n.Set("checked", false)
	if !due.IsZero() {
		n.SetDue(due, false)
	}
	return n
}

// windowApp is an app to build its whole window in a test.
func windowApp(t *testing.T) *App {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
	a.settings.SidebarOpen, a.settings.SidebarTab = true, tabTodos
	return a
}

func TestTodosView(t *testing.T) {
	a := windowApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{todoNode("t1", "Send the draft", time.Now())})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.settings.SidebarTab = tabPages
	tt := ui.NewTester(a.view, 1300, 700)
	tt.Frame()
	if err := tt.Click("To-dos"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Today") || tt.HasText("Send the draft") {
		t.Errorf("the tab lists more than the days: %v", tt.Texts())
	}
	if err := tt.Click("Today"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.route != (Route{Kind: "day", ID: time.Now().Format(time.DateOnly)}) {
		t.Fatalf("route %+v", a.route)
	}
	for _, s := range []string{"Send the draft", "PRD", "1 to-do left", locales.TodosDayDate(time.Now())} {
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
	if !tt.HasText("Nothing to do") || !tt.HasText("All done") || tt.HasText("1 to-do left") {
		t.Errorf("still listed: %v", tt.Texts())
	}
}

func TestTodoClickScrollsItsPageToIt(t *testing.T) {
	a := windowApp(t)
	var blocks []*doc.Node
	for i := range 80 {
		blocks = append(blocks, textNode(fmt.Sprintf("Line %d.", i)))
	}
	todo := &doc.Node{ID: "t1", Type: "todo_list"}
	todo.SetDelta(doc.Delta{{Text: "Join the team call"}})
	todo.Set("checked", false)
	todo.SetDue(time.Now(), false)
	blocks = append(blocks, todo)
	for i := range 20 {
		blocks = append(blocks, textNode(fmt.Sprintf("After it %d.", i)))
	}
	interview, err := a.st.CreatePage(store.NewPageOptions{Title: "Interview", Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	notes, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{textNode("Hello")})})
	a.reloadAll()
	a.openPage(notes.ID)
	var ctx *ui.Context
	tt := ui.NewTester(func(c *ui.Context) {
		ctx = c
		a.view(c)
	}, 1300, 700)
	tt.Frame()
	if err := tt.Click("Today"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Join the team call"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		tt.Frame()
	}
	if a.page == nil || a.page.id != interview.ID || a.page.editor.Caret().ID != "t1" {
		t.Fatal("the to-do's page isn't open at it")
	}
	top, ok := a.page.editor.BlockTop("t1")
	view := a.page.view.Bounds(ctx)
	if !ok || view.H == 0 || top-view.Y < 0 || top-view.Y > 2*scrollMargin {
		t.Errorf("the to-do is %v below the top of the page (shown %v)", top-view.Y, ok)
	}
}

func TestTodosOfAPageToView(t *testing.T) {
	a := windowApp(t)
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
	a.openDay(time.Now().Format(time.DateOnly))
	tt := ui.NewTester(a.view, 1300, 700)
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

func TestReminderClickBeforeTheWindow(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Trip", Content: doc.FromBlocks([]*doc.Node{textNode("Pack")})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	block := a.st.Content(p.ID).Children[0].ID
	a.notificationClicked("other:" + p.ID)
	a.notificationClicked(core.ReminderID(p.ID, block))
	if len(a.early) != 1 || a.page != nil && a.page.id == p.ID {
		t.Fatalf("%d waiting, page open %v", len(a.early), a.page != nil)
	}
	a.early[0]()
	if a.page == nil || a.page.id != p.ID || a.page.editor.Caret().ID != block {
		t.Errorf("the to-do's page isn't open at it")
	}
}

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
			if err := a.st.SetValue(core.ReminderKey(r.PageID, r.BlockID), core.ReminderValue(r)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := fmt.Sprint(ids()); got != "[given mine]" {
		t.Errorf("once reminded of the call, reminded of %s", got)
	}
}

func TestTodoDayView(t *testing.T) {
	a := windowApp(t)
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Yesterday's list", Content: doc.FromBlocks([]*doc.Node{
		todoNode("old", "Read for 40 minutes", yesterday),
	})}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Today's list", Content: doc.FromBlocks([]*doc.Node{
		todoNode("new", "Listen for an hour", now),
		todoNode("retell", "Retell for 30 minutes", now),
	})}); err != nil {
		t.Fatal(err)
	}
	work, err := a.st.CreatePage(store.NewPageOptions{Title: "Work", Content: doc.FromBlocks([]*doc.Node{
		todoNode("work", "Send the invoice", now),
		todoNode("trip", "Plan the trip", now.AddDate(0, 0, 1)),
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	tt := ui.NewTester(a.view, 1300, 700)
	tt.Frame()
	for _, s := range []string{"Yesterday", "Today", "Tomorrow"} {
		if !tt.HasText(s) {
			t.Errorf("no day %q in %v", s, tt.Texts())
		}
	}
	for _, s := range []string{"Read for 40 minutes", "Listen for an hour", "Send the invoice", "Plan the trip"} {
		if tt.HasText(s) {
			t.Errorf("to-do %q listed in the tab: %v", s, tt.Texts())
		}
	}
	if err := tt.Click("Today"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, s := range []string{"Today's list", "Listen for an hour", "Retell for 30 minutes", "Work", "Send the invoice", "3 to-dos left"} {
		if !tt.HasText(s) {
			t.Errorf("no %q on today's page in %v", s, tt.Texts())
		}
	}
	for _, s := range []string{"Read for 40 minutes", "Plan the trip", "Yesterday's list"} {
		if tt.HasText(s) {
			t.Errorf("%q, not due today, on today's page: %v", s, tt.Texts())
		}
	}
	if a.todoRow != 1 {
		t.Errorf("row %d chosen, want today's", a.todoRow)
	}
	if err := tt.Click("Yesterday"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Read for 40 minutes") || tt.HasText("Listen for an hour") {
		t.Errorf("yesterday's page: %v", tt.Texts())
	}
	a.openDay(now.Format(time.DateOnly))
	tt.Frame()
	if err := tt.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.page == nil || a.page.id != work.ID {
		t.Fatalf("a click on a page's name didn't open it: %+v", a.route)
	}
	if a.todoRow != -1 {
		t.Errorf("a day still chosen, row %d, with a page open", a.todoRow)
	}
}

func TestTodoDayRoutes(t *testing.T) {
	for _, r := range []Route{{Kind: "day", ID: "2026-11-23"}, {Kind: "day", ID: noDateKey}, {Kind: "page", ID: "p"}} {
		if got := routeAt(r.path()); got != r {
			t.Errorf("route %+v comes back as %+v", r, got)
		}
	}
	a := templatesApp(t)
	a.route = Route{Kind: "day", ID: noDateKey}
	if a.routeTitle() != "No date" || a.gone(a.route) {
		t.Errorf("title %q, gone %v", a.routeTitle(), a.gone(a.route))
	}
}
