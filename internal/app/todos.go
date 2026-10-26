package app

import (
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/editor"
	"leaflet/internal/store"
	"leaflet/internal/theme"
)

// todoRow is a row of the To-dos view: a section's header, or a to-do.
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
		case editor.Overdue(t.Due, now):
			s = "Overdue"
		case editor.DueLabel(t.Due, now) == "Today":
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

// todosView lists the open to-dos of every page that have a due date or
// an owner: a check finishes one, a click goes to it.
func (a *App) todosView(c *ui.Context) {
	pal := theme.Of(c)
	a.titleBar(c, func() {
		ui.Text(c, "To-dos").FontSize(13).FontWeight(600).TextColor(pal.Text).Padding(3, 6)
		ui.Spacer(c)
	})
	rows := todoRows(a.todoList(), time.Now())
	if len(rows) == 0 {
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Icon(c, theme.Icon("square-check")).FontSize(32).TextColor(pal.ChartMuted)
			ui.Text(c, "Nothing to do").FontSize(17).FontWeight(650).TextColor(pal.Text)
			ui.Text(c, "To-dos with a date, or someone @mentioned, show here from every page.").TextColor(pal.TextMuted)
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
	ui.Column(c).Grow(1).AlignItems(ui.Center).Children(func() {
		ui.List(c, &a.todoState, len(rows), func(i int) {
			r := rows[i]
			if r.header != "" {
				ui.Text(c, r.header).FontSize(12).FontWeight(650).TextColor(pal.TextMuted).Padding(16, 10, 6).
					Background(pal.Surface).FillWidth()
				return
			}
			a.todoRowView(c, r.todo, pal)
		}).Grow(1).Width(a.contentWidth(c)).Padding(8, 0, 40)
	})
}

func (a *App) todoRowView(c *ui.Context, t store.PageTodo, pal *theme.Palette) {
	row := ui.ButtonBase(c.Key("todo:"+t.PageID+"#"+t.BlockID)).Padding(8, 10).Gap(12).Radius(8).AlignItems(ui.Center).Justify(ui.Start).FillWidth()
	if row.Hovered() {
		row.Background(pal.Hover)
	}
	row.Children(func() {
		done := false
		if ui.Checkbox(c, &done, "").Label("Done").Changed() {
			a.finishTodo(t)
		}
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			text := t.Text
			if strings.TrimSpace(text) == "" {
				text = "To-do"
			}
			ui.Text(c, text).FontSize(14.5).TextColor(pal.Text).SingleLine()
			sub := t.PageTitle
			if t.Owner != "" {
				sub += " · " + t.Owner
			}
			ui.Text(c, sub).FontSize(12).TextColor(pal.TextMuted).SingleLine()
		})
		if !t.Due.IsZero() {
			now := time.Now()
			due := ui.Badge(c, editor.DueLabel(t.Due, now))
			if editor.Overdue(t.Due, now) {
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
	set := func(checked bool) {
		if ed := a.Editor(t.PageID); ed != nil {
			if n := ed.Content.Find(t.BlockID); n != nil && n.Bool("checked") != checked {
				ed.ToggleChecked(t.BlockID)
				a.savePage()
			}
			return
		}
		c := a.st.Content(t.PageID)
		if c == nil {
			return
		}
		if n := c.Find(t.BlockID); n != nil {
			n.Set("checked", checked)
			if _, _, err := a.st.SaveContent(t.PageID, c); err != nil {
				a.toast("Could not save: " + err.Error())
			}
		}
	}
	set(true)
	a.todosStale = true
	a.toastAction("Done: "+t.Text, "Undo", func() {
		set(false)
		a.todosStale = true
	})
}

// reminderHour is when a to-do due today is reminded of.
const reminderHour = 9

// dueReminders are the to-dos to remind of now: due today, from
// reminderHour, or overdue, and not reminded of for that date yet.
func dueReminders(todos []store.PageTodo, now time.Time, reminded func(t store.PageTodo) bool) []store.PageTodo {
	var out []store.PageTodo
	for _, t := range todos {
		if t.Due.IsZero() || reminded(t) {
			continue
		}
		today := editor.DueLabel(t.Due, now) == "Today"
		if editor.Overdue(t.Due, now) || today && now.Hour() >= reminderHour {
			out = append(out, t)
		}
	}
	return out
}

func reminderKey(t store.PageTodo) string { return "reminded:" + t.PageID + "#" + t.BlockID }

// remind shows a desktop notification for each to-do to remind of now,
// once for each of its due dates.
func (a *App) remind() {
	if !mygo.NotificationsSupported() {
		return
	}
	due := func(t store.PageTodo) string { return t.Due.Format("2006-01-02") }
	reminded := func(t store.PageTodo) bool {
		v, ok := a.st.Value(reminderKey(t))
		return ok && v == due(t)
	}
	for _, t := range dueReminders(a.todoList(), time.Now(), reminded) {
		title := "Due today"
		if editor.Overdue(t.Due, time.Now()) {
			title = "Overdue · " + editor.DueLabel(t.Due, time.Now())
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
