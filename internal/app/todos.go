package app

import (
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
// ones, each by date, and those with no date but an owner. A section
// alone has no header, which would only say what the to-dos' dates do.
func todoRows(todos []store.PageTodo, now time.Time) []todoRow {
	groups := doc.TodoGroups(todos, func(t store.PageTodo) doc.Todo { return t.Todo }, now)
	var out []todoRow
	for _, g := range groups {
		if len(groups) > 1 {
			out = append(out, todoRow{header: todoHeaders[g.Section]})
		}
		for _, t := range g.Todos {
			out = append(out, todoRow{todo: t})
		}
	}
	return out
}

// todoHeaders are the To-dos tab's sections, as todoSection names them.
var todoHeaders = [...]string{doc.TodoOverdue: "Overdue", doc.TodoToday: "Today", doc.TodoUpcoming: "Upcoming", doc.TodoNoDate: "No date"}

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
			ui.Text(c, locales.TodosEmptyBody()).FontSize(13).TextColor(pal.TextMuted).Pretty()
		})
		return
	}
	// A header held at the top needs a fill to cover the rows under it, and
	// a see-through sidebar has none to match: there it scrolls with them.
	opaque := opaqueSidebar(c, pal)
	a.todoState.Header = nil
	if opaque {
		a.todoState.Header = func(i int) bool { return rows[i].header != "" }
	}
	a.todoState.Key = func(i int) any {
		if rows[i].header != "" {
			return "section:" + rows[i].header
		}
		return rows[i].todo.PageID + "#" + rows[i].todo.BlockID
	}
	ui.List(c, &a.todoState, len(rows), func(i int) {
		r := rows[i]
		if r.header != "" {
			h := ui.Text(c, todoSection(r.header)).FontSize(11.5).FontWeight(650).TextColor(pal.TextMuted).LetterSpacing(0.2).
				Padding(6, 8, 4).FillWidth().Radius(6)
			if opaque {
				h.Background(pal.Sidebar)
			}
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
		if ed := a.openEditor(t.PageID); ed != nil {
			if n := ed.Content.Find(t.BlockID); n != nil && n.Bool("checked") != checked {
				ed.ToggleChecked(t.BlockID)
				a.savePage()
			}
			return
		}
		err := a.pageCore().EditPage(t.PageID, func(c *doc.Content) bool {
			n := c.Find(t.BlockID)
			if n == nil {
				return false
			}
			n.Set("checked", checked)
			return true
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

// reminderWindow is how long after its time a to-do is still reminded
// of. One later than that, as on a device that was off, or one new to
// the library, is among the To-dos tab's Overdue, not one notification of
// many at once.
const reminderWindow = 24 * time.Hour

// dueReminders are the to-dos to remind of now: from their time of day,
// or else reminderHour of their day, on, for reminderWindow, and not
// reminded of for that date yet.
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
		if !now.Before(at) && now.Sub(at) < reminderWindow {
			out = append(out, t)
		}
	}
	return out
}

// remindsOf reports whether the user is reminded of a to-do: one given to
// them, or one given to no one on a page of their own. Another's to-do is
// that person's to be reminded of, on their devices, as is one on a page
// shared with the user that names no one.
func (a *App) remindsOf(t store.PageTodo) bool {
	if t.Owner != "" {
		return t.OwnerID != "" && t.OwnerID == a.session.UserID
	}
	return a.ownsPage(t.PageID)
}

// reminders are the to-dos to remind the user of now, each once for each
// of its due dates and times.
func (a *App) reminders(now time.Time) []store.PageTodo {
	var todos []store.PageTodo
	for _, t := range a.todoList() {
		if a.remindsOf(t) {
			todos = append(todos, t)
		}
	}
	return dueReminders(todos, now, func(t store.PageTodo) bool {
		v, ok := a.st.Value(reminderKey(t))
		return ok && v == dueValue(t)
	})
}

// dueValue is a to-do's due date and time as its reminder is kept by.
func dueValue(t store.PageTodo) string {
	if t.Timed {
		return t.Due.Format(doc.DueTimeLayout)
	}
	return t.Due.Format(doc.DueLayout)
}

func reminderKey(t store.PageTodo) string { return "reminded:" + t.PageID + "#" + t.BlockID }

// remind shows a desktop notification for each to-do to remind of now.
func (a *App) remind() {
	if !mygo.NotificationsSupported() {
		return
	}
	now := time.Now()
	for _, t := range a.reminders(now) {
		title := locales.TodosDueToday()
		// A to-do due at a time today is reminded of at it, as due.
		if editor.Overdue(t.Due, t.Timed, now) && (!t.Timed || !editor.DueToday(t.Due, now)) {
			title = locales.TodosOverdueOn(editor.DueLabel(t.Due, t.Timed, now))
		}
		n := mygo.NewNotification(mygo.NotificationOptions{ID: todoNotification + t.PageID + "#" + t.BlockID,
			Title: title, Subtitle: t.PageTitle, Body: t.Text, Group: "todos"})
		if err := n.Show(); err == nil {
			_ = a.st.SetValue(reminderKey(t), dueValue(t))
		}
	}
}

// todoNotification starts the ID of a to-do's reminder, which a click on
// it, also after the app quit, opens the to-do by.
const todoNotification = "todo:"

// notificationClicked opens the to-do of a reminder clicked, showing the
// window, which may have been closed to the menu bar. Run has it hear the
// clicks before the app runs, for one that launched the app, whose to-do
// opens once the window has been made.
func (a *App) notificationClicked(id string) {
	target, ok := strings.CutPrefix(id, todoNotification)
	if !ok {
		return
	}
	page, block, _ := strings.Cut(target, "#")
	open := func() {
		a.showWindow()
		a.openBlock(page, block)
	}
	a.winMu.Lock()
	if a.win == nil {
		// Not started yet: start runs it with the window made.
		a.early = append(a.early, open)
		a.winMu.Unlock()
		return
	}
	a.winMu.Unlock()
	a.update(open)
}

// startReminders checks for to-dos to remind of each minute.
func (a *App) startReminders() {
	var tick func()
	tick = func() {
		a.update(a.remind)
		time.AfterFunc(time.Minute, tick)
	}
	time.AfterFunc(5*time.Second, tick)
}
