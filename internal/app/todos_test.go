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

func TestTodoRowsAndReminders(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.Local)
	day := func(d int) time.Time { return time.Date(2026, 10, 26+d, 0, 0, 0, 0, time.Local) }
	todo := func(id string, due time.Time) store.PageTodo {
		return store.PageTodo{Todo: doc.Todo{BlockID: id, Text: id, Due: due, Owner: "Sam"}, PageID: "p"}
	}
	todos := []store.PageTodo{todo("later", day(3)), todo("owned", time.Time{}), todo("today", day(0)), todo("late", day(-2)), todo("soon", day(1))}
	ids := func(rows []todoRow) string {
		var got []string
		for _, r := range rows {
			if r.day != nil {
				got = append(got, fmt.Sprintf("# %s %d", r.day.Key(), len(r.day.Todos)))
			} else {
				got = append(got, r.todo.BlockID)
			}
		}
		return fmt.Sprint(got)
	}
	unfolded := func(d todoDay) bool { return !d.Folded() }
	want := "[# 2026-10-24 1 # 2026-10-26 1 today # 2026-10-27 1 soon # 2026-10-29 1 later # no-date 1 owned]"
	if s := ids(todoRows(todos, now, unfolded)); s != want {
		t.Errorf("rows %s, want %s", s, want)
	}
	all := func(todoDay) bool { return true }
	if s, want := ids(todoRows(todos, now, all)), "[# 2026-10-24 1 late # 2026-10-26 1 today # 2026-10-27 1 soon # 2026-10-29 1 later # no-date 1 owned]"; s != want {
		t.Errorf("rows with every day open %s, want %s", s, want)
	}
	if s := ids(todoRows(todos[1:2], now, all)); s != "[owned]" {
		t.Errorf("rows of a to-do with no date %s", s)
	}
	if s := ids(todoRows(todos[:1], now, all)); s != "[# 2026-10-29 1 later]" {
		t.Errorf("rows of one day %s", s)
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
	for _, r := range todoRows(todos, at(10, 0), func(todoDay) bool { return true }) {
		if r.day != nil {
			got = append(got, todoDayTitle(*r.day, at(10, 0)))
		} else {
			got = append(got, r.todo.BlockID)
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

func TestTodoHeaderStaysOnAnOpaqueSidebar(t *testing.T) {
	a := templatesApp(t)
	n := &doc.Node{ID: "t1", Type: "todo_list"}
	n.SetDelta(doc.Delta{{Text: "Send the draft"}})
	n.Set("checked", false)
	n.SetDue(time.Now(), false)
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{n})}); err != nil {
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
	if a.todoState.Header == nil {
		t.Error("no header held on an opaque sidebar")
	}
	tt.SetVibrancy(true)
	tt.Frame()
	if a.todoState.Header != nil {
		t.Error("a header held over the window's vibrancy")
	}
	if !tt.HasText("Today") {
		t.Errorf("no section in %v", tt.Texts())
	}
}

func TestTodoClickScrollsItsPageToIt(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.TextSize = 16
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
	a.settings.SidebarOpen, a.settings.SidebarTab = true, tabTodos
	var ctx *ui.Context
	tt := ui.NewTester(func(c *ui.Context) {
		ctx = c
		a.view(c)
	}, 1300, 700)
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

func TestTodoDaysFoldAndUnfold(t *testing.T) {
	a := templatesApp(t)
	todo := func(id, text string, due time.Time) *doc.Node {
		n := &doc.Node{ID: id, Type: "todo_list"}
		n.SetDelta(doc.Delta{{Text: text}})
		n.Set("checked", false)
		n.SetDue(due, false)
		return n
	}
	now := time.Now()
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Yesterday's list", Content: doc.FromBlocks([]*doc.Node{
		todo("old", "Read for 40 minutes", now.AddDate(0, 0, -1)),
	})}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Today's list", Content: doc.FromBlocks([]*doc.Node{
		todo("new", "Listen for an hour", now),
	})}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Work", Content: doc.FromBlocks([]*doc.Node{
		todo("work", "Send the invoice", now),
	})}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.settings.SidebarOpen, a.settings.SidebarTab = true, tabTodos
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	for _, s := range []string{"Yesterday", "Today", "Listen for an hour", "Send the invoice", "Today's list", "Work"} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %v", s, tt.Texts())
		}
	}
	if tt.HasText("Read for 40 minutes") {
		t.Errorf("yesterday's to-do shows before its day is opened: %v", tt.Texts())
	}
	if err := tt.Click("Yesterday"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Read for 40 minutes") {
		t.Errorf("yesterday's to-do isn't shown once its day is opened: %v", tt.Texts())
	}
	if err := tt.Click("Today"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("Listen for an hour") || tt.HasText("Send the invoice") {
		t.Errorf("today's to-dos show with the day folded: %v", tt.Texts())
	}
	day := func(d time.Time) string { return d.Format(time.DateOnly) }
	saved := a.st.Settings().TodoDaysOpen
	if len(saved) != 2 || !saved[day(now.AddDate(0, 0, -1))] || saved[day(now)] {
		t.Errorf("days kept open or folded %v", saved)
	}
	if err := tt.Click("Today"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Listen for an hour") || !tt.HasText("Send the invoice") {
		t.Errorf("today's to-dos aren't shown once re-opened: %v", tt.Texts())
	}
	saved = a.st.Settings().TodoDaysOpen
	if _, ok := saved[day(now)]; ok {
		t.Errorf("re-opened default-open day still kept in %v", saved)
	}
}
