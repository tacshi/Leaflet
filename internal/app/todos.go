package app

import (
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// todoRow is a row of the To-dos tab: a section's header, or a to-do.
type todoRow struct {
	header string
	todo   store.PageTodo
}

// todoRows sorts open to-dos into sections: overdue, today's, the coming
// ones, each by date, and those with no date but an owner.
func todoRows(todos []store.PageTodo, now time.Time) []todoRow {
	sections := map[string][]store.PageTodo{}
	for _, t := range todos {
		s := "No date"
		switch {
		case t.Due.IsZero():
		case editor.Overdue(t.Due, t.Timed, now):
			s = "Overdue"
		case editor.DueToday(t.Due, now):
			s = "Today"
		default:
			s = "Upcoming"
		}
		sections[s] = append(sections[s], t)
	}
	var out []todoRow
	for _, s := range []string{"Overdue", "Today", "Upcoming", "No date"} {
		ts := sections[s]
		if len(ts) == 0 {
			continue
		}
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].Due.Before(ts[j].Due) })
		out = append(out, todoRow{header: s})
		for _, t := range ts {
			out = append(out, todoRow{todo: t})
		}
	}
	return out
}

// todoSection is the name a person reads of a section of the To-dos tab.
func todoSection(header string) string {
	switch header {
	case "Overdue":
		return locales.TodosSectionOverdue()
	case "Today":
		return locales.CommonToday()
	case "Upcoming":
		return locales.TodosSectionUpcoming()
	case "No date":
		return locales.TodosSectionNoDate()
	}
	return header
}

// todoList returns the open to-dos, read again after pages changed.
func (a *App) todoList() []store.PageTodo {
	if a.todosStale || a.todos == nil {
		a.todos, _ = a.st.Todos()
		if a.todos == nil {
			a.todos = []store.PageTodo{}
		}
		a.todosStale = false
	}
	return a.todos
}

// todosList is the sidebar's To-dos tab: the open to-dos of every page
// that have a due date or an owner. A check finishes one, a click goes to
// it.
func (a *App) todosList(c *ui.Context) {
	pal := theme.Of(c)
	rows := todoRows(a.todoList(), time.Now())
	if len(rows) == 0 {
		ui.Column(c).Grow(1).Padding(2, 18).Gap(4).Children(func() {
			ui.Text(c, locales.TodosEmptyTitle()).FontSize(13).FontWeight(600).TextColor(pal.Text)
			ui.Text(c, locales.TodosEmptyBody()).FontSize(13).TextColor(pal.TextMuted)
		})
		return
	}
	a.todoState.Header = func(i int) bool { return rows[i].header != "" }
	a.todoState.Key = func(i int) any {
		if rows[i].header != "" {
			return "section:" + rows[i].header
		}
		return rows[i].todo.PageID + "#" + rows[i].todo.BlockID
	}
	ui.List(c, &a.todoState, len(rows), func(i int) {
		r := rows[i]
		if r.header != "" {
			ui.Text(c, todoSection(r.header)).FontSize(11.5).FontWeight(650).TextColor(pal.TextMuted).LetterSpacing(0.2).
				Padding(10, 8, 4).FillWidth()
			return
		}
		a.todoRowView(c, r.todo, pal)
	}).Grow(1).Padding(0, 8, 12)
}

func (a *App) todoRowView(c *ui.Context, t store.PageTodo, pal *theme.Palette) {
	row := ui.ButtonBase(c.Key("todo:"+t.PageID+"#"+t.BlockID)).Padding(5, 8).Gap(8).Radius(6).AlignItems(ui.Start).Justify(ui.Start).FillWidth()
	if row.Hovered() {
		row.Background(pal.Hover)
	}
	row.Children(func() {
		// A page shared to view lists its to-dos, which only its editors
		// check off.
		done := false
		if ui.Checkbox(c, &done, "").Label(locales.CommonDone()).Margin(1, 0, 0).Disabled(!a.canEdit(t.PageID)).Changed() {
			a.finishTodo(t)
		}
		ui.Column(c).Grow(1).Shrink(1).Gap(1).Children(func() {
			text := t.Text
			if strings.TrimSpace(text) == "" {
				text = locales.TodosUntitled()
			}
			ui.Text(c, text).FontSize(13.5).TextColor(pal.Text).SingleLine().Tooltip(text)
			sub := t.PageTitle
			if t.Owner != "" {
				sub = locales.TodosPageAndOwner(sub, t.Owner)
			}
			ui.Text(c, sub).FontSize(11.5).TextColor(pal.TextMuted).SingleLine()
		})
		if !t.Due.IsZero() {
			now := time.Now()
			due := ui.Badge(c, editor.DueLabel(t.Due, t.Timed, now)).FontSize(11).Shrink(0)
			if editor.Overdue(t.Due, t.Timed, now) {
				due.Background(pal.DangerBg).TextColor(pal.DangerText)
			}
		}
	})
	if row.Clicked() {
		a.openBlock(t.PageID, t.BlockID)
	}
}

// finishTodo checks a to-do off, in its page, and offers to undo it.
func (a *App) finishTodo(t store.PageTodo) {
	if !a.canEdit(t.PageID) {
		return
	}
	set := func(checked bool) {
		if ed := a.Editor(t.PageID); ed != nil {
			if n := ed.Content.Find(t.BlockID); n != nil && n.Bool("checked") != checked {
				ed.ToggleChecked(t.BlockID)
				a.savePage()
			}
			return
		}
		err := a.saveStored(t.PageID, func(c *doc.Content) *doc.Content {
			if c == nil {
				return nil
			}
			n := c.Find(t.BlockID)
			if n == nil {
				return nil
			}
			n.Set("checked", checked)
			return c
		})
		if err != nil {
			a.toast(locales.SidebarCouldNotSave(err.Error()))
		}
	}
	set(true)
	a.todosStale = true
	a.toastAction(locales.TodosFinished(t.Text), locales.CommonUndo(), func() {
		set(false)
		a.todosStale = true
	})
}

// reminderHour is when a to-do due today is reminded of, without a time
// of day of its own.
const reminderHour = 9

// dueReminders are the to-dos to remind of now: from their time of day,
// or else reminderHour of their day, on, and not reminded of for that
// date yet.
func dueReminders(todos []store.PageTodo, now time.Time, reminded func(t store.PageTodo) bool) []store.PageTodo {
	var out []store.PageTodo
	for _, t := range todos {
		if t.Due.IsZero() || reminded(t) {
			continue
		}
		at := t.Due
		if !t.Timed {
			at = time.Date(at.Year(), at.Month(), at.Day(), reminderHour, 0, 0, 0, time.Local)
		}
		if !now.Before(at) {
			out = append(out, t)
		}
	}
	return out
}

func reminderKey(t store.PageTodo) string { return "reminded:" + t.PageID + "#" + t.BlockID }

// remind shows a desktop notification for each to-do to remind of now,
// once for each of its due dates and times.
func (a *App) remind() {
	if !mygo.NotificationsSupported() {
		return
	}
	due := func(t store.PageTodo) string {
		if t.Timed {
			return t.Due.Format(doc.DueTimeLayout)
		}
		return t.Due.Format(doc.DueLayout)
	}
	reminded := func(t store.PageTodo) bool {
		v, ok := a.st.Value(reminderKey(t))
		return ok && v == due(t)
	}
	for _, t := range dueReminders(a.todoList(), time.Now(), reminded) {
		now := time.Now()
		title := locales.TodosDueToday()
		// A to-do due at a time today is reminded of at it, as due.
		if editor.Overdue(t.Due, t.Timed, now) && (!t.Timed || !editor.DueToday(t.Due, now)) {
			title = locales.TodosOverdueOn(editor.DueLabel(t.Due, t.Timed, now))
		}
		n := mygo.NewNotification(mygo.NotificationOptions{ID: todoNotification + t.PageID + "#" + t.BlockID,
			Title: title, Subtitle: t.PageTitle, Body: t.Text, Group: "todos"})
		if err := n.Show(); err == nil {
			_ = a.st.SetValue(reminderKey(t), due(t))
		}
	}
}

// todoNotification starts the ID of a to-do's reminder, which a click on
// it, also after the app quit, opens the to-do by.
const todoNotification = "todo:"

// startReminders checks for to-dos to remind of each minute, and opens a
// to-do when its reminder is clicked.
func (a *App) startReminders() {
	mygo.App.OnNotificationClick(func(id string) {
		target, ok := strings.CutPrefix(id, todoNotification)
		if !ok {
			return
		}
		page, block, _ := strings.Cut(target, "#")
		a.update(func() { a.openBlock(page, block) })
	})
	var tick func()
	tick = func() {
		a.update(a.remind)
		time.AfterFunc(time.Minute, tick)
	}
	time.AfterFunc(5*time.Second, tick)
}
