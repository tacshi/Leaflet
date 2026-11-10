package app

import (
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

type todoRow struct {
	header string
	todo   store.PageTodo
}

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

var todoHeaders = [...]string{doc.TodoOverdue: "Overdue", doc.TodoToday: "Today", doc.TodoUpcoming: "Upcoming", doc.TodoNoDate: "No date"}

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

func (a *App) todoList() []store.PageTodo {
	if a.todosStale || a.todos == nil {
		reread(&a.todos, a.st.Todos)
		if a.todos == nil {
			a.todos = []store.PageTodo{}
		}
		a.todosStale = false
	}
	return a.todos
}

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

func (a *App) remindsOf(t store.PageTodo) bool {
	return core.RemindsOf(t, a.session.UserID, a.ownsPage(t.PageID))
}

func (a *App) reminders(now time.Time) []store.PageTodo {
	var todos []store.PageTodo
	for _, t := range a.todoList() {
		if a.remindsOf(t) {
			todos = append(todos, t)
		}
	}
	return core.DueReminders(todos, now, func(t store.PageTodo) bool {
		v, ok := a.st.Value(core.ReminderKey(t.PageID, t.BlockID))
		return ok && v == core.ReminderValue(t)
	})
}

func (a *App) remind() {
	if !mygo.NotificationsSupported() {
		return
	}
	now := time.Now()
	for _, t := range a.reminders(now) {
		n := mygo.NewNotification(mygo.NotificationOptions{ID: core.ReminderID(t.PageID, t.BlockID),
			Title: core.ReminderTitle(locales.In(i18n.Locale()), t, now), Subtitle: t.PageTitle, Body: t.Text, Group: "todos"})
		if err := n.Show(); err == nil {
			_ = a.st.SetValue(core.ReminderKey(t.PageID, t.BlockID), core.ReminderValue(t))
		}
	}
}

func (a *App) notificationClicked(id string) {
	target, ok := strings.CutPrefix(id, "todo:")
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
		a.early = append(a.early, open)
		a.winMu.Unlock()
		return
	}
	a.winMu.Unlock()
	a.update(open)
}

func (a *App) startReminders() {
	var tick func()
	tick = func() {
		a.update(a.remind)
		time.AfterFunc(time.Minute, tick)
	}
	time.AfterFunc(5*time.Second, tick)
}
